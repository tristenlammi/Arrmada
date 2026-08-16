#!/usr/bin/env bash
# av1-ladder.sh — measure what an AV1 re-encode actually costs you, on your own
# content and your own hardware, before changing what Arrmada does to a library.
#
# "Is AV1 as good as HEVC at a smaller size" has no general answer: it depends on the
# source's grain and motion, on the encoder (SVT-AV1 and a GPU's AV1 block are not the
# same encoder), and on the quality target. So this measures rather than argues.
#
# It samples four ~20s windows from the middle of the file — the same sampling Arrmada's
# own quality gate uses — encodes each rung of a CRF ladder, and scores every rung with
# VMAF and SSIM against the source. Sampling is what makes this take minutes instead of
# a night; a full 4K AV1 encode at preset 4 can run longer than the film.
#
# Usage:
#   ./av1-ladder.sh /path/to/source.mkv            # default ladder
#   CRFS="20 24 28" ./av1-ladder.sh source.mkv     # pick your own rungs
#   PRESET=4 ./av1-ladder.sh source.mkv            # slower/better SVT-AV1 preset
#   COMPARE_HEVC=1 ./av1-ladder.sh source.mkv      # also encode x265 for a side-by-side
#
# Read the output like this:
#   VMAF >= 95   visually transparent for practical purposes — no visible loss
#   VMAF 93-95   very good; differences visible only in a still A/B on a big panel
#   VMAF < 93    you will see it on grain, gradients and dark scenes
# Pick the highest CRF (smallest file) that still clears your VMAF bar.

set -euo pipefail

SRC="${1:?usage: av1-ladder.sh <source-file>}"
[ -r "$SRC" ] || { echo "cannot read $SRC" >&2; exit 1; }

CRFS="${CRFS:-22 26 30 34}"
PRESET="${PRESET:-5}"
SAMPLE_SECS="${SAMPLE_SECS:-20}"
COMPARE_HEVC="${COMPARE_HEVC:-0}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FFMPEG="${FFMPEG:-ffmpeg}"
FFPROBE="${FFPROBE:-ffprobe}"

probe() { "$FFPROBE" -v error -select_streams v:0 -show_entries "$1" -of default=nw=1:nk=1 "$SRC" | head -1; }

DURATION="$(probe format=duration 2>/dev/null || true)"
[ -n "$DURATION" ] || DURATION="$("$FFPROBE" -v error -show_entries format=duration -of default=nw=1:nk=1 "$SRC")"
CODEC="$(probe stream=codec_name)"
WIDTH="$(probe stream=width)"
HEIGHT="$(probe stream=height)"
PIXFMT="$(probe stream=pix_fmt)"
SRC_BYTES="$(stat -c %s "$SRC" 2>/dev/null || stat -f %z "$SRC")"

# 10-bit output even from an 8-bit source. AV1 costs nothing for the extra depth and it
# is the single biggest defence against the banding that makes a re-encode "look bad" on
# skies, smoke and dark gradients — the artefact people notice first.
case "$PIXFMT" in *10le|*10be|*12le|*12be) DEPTH="yuv420p10le" ;; *) DEPTH="yuv420p10le" ;; esac

echo "source : $(basename "$SRC")"
echo "video  : $CODEC ${WIDTH}x${HEIGHT} $PIXFMT · $(awk -v b="$SRC_BYTES" 'BEGIN{printf "%.2f GB", b/1073741824}')"
echo "encode : SVT-AV1 preset $PRESET → $DEPTH"
echo

# Four windows inside the middle ~80%, so credits and fade-ins don't flatter the score.
OFFSETS=()
for frac in 0.15 0.38 0.61 0.84; do
  OFFSETS+=("$(awk -v d="$DURATION" -v f="$frac" 'BEGIN{printf "%d", d*f}')")
done

# Cut the reference samples once and reuse them for every rung, so each rung is scored
# against identical pixels and the comparison is apples to apples.
echo "cutting reference samples…"
REFS=()
for i in "${!OFFSETS[@]}"; do
  ref="$WORK/ref-$i.mkv"
  "$FFMPEG" -v error -y -ss "${OFFSETS[$i]}" -i "$SRC" -t "$SAMPLE_SECS" \
    -map 0:v:0 -c:v ffv1 -an -sn "$ref"          # lossless: the yardstick must not itself lose anything
  REFS+=("$ref")
done

# score <encoded-dir-prefix> → prints "VMAF SSIM" averaged over the samples
score() {
  local prefix="$1" vsum=0 ssum=0 n=0
  for i in "${!REFS[@]}"; do
    local log="$WORK/vmaf-$i.json"
    "$FFMPEG" -v error -i "${prefix}-$i.mkv" -i "${REFS[$i]}" \
      -lavfi "[0:v]setpts=PTS-STARTPTS[d];[1:v]setpts=PTS-STARTPTS[r];[d][r]libvmaf=log_fmt=json:log_path=$log:feature=name=float_ssim" \
      -f null - 2>/dev/null
    local v s
    v="$(grep -o '"vmaf"[^}]*"mean":[0-9.]*' "$log" | grep -o '[0-9.]*$' | head -1)"
    s="$(grep -o '"float_ssim"[^}]*"mean":[0-9.]*' "$log" | grep -o '[0-9.]*$' | head -1)"
    [ -n "$v" ] || continue
    vsum="$(awk -v a="$vsum" -v b="$v" 'BEGIN{print a+b}')"
    ssum="$(awk -v a="$ssum" -v b="${s:-0}" 'BEGIN{print a+b}')"
    n=$((n+1))
  done
  awk -v v="$vsum" -v s="$ssum" -v n="$n" 'BEGIN{if(n)printf "%.2f %.4f", v/n, s/n; else print "n/a n/a"}'
}

# bytes across all samples of one rung — the size proxy, since the samples are a fixed
# slice of the runtime the ratio scales to the whole file.
rung_bytes() {
  local prefix="$1" total=0
  for i in "${!REFS[@]}"; do
    total=$((total + $(stat -c %s "${prefix}-$i.mkv" 2>/dev/null || stat -f %z "${prefix}-$i.mkv")))
  done
  echo "$total"
}

REF_BYTES="$(
  total=0
  for i in "${!OFFSETS[@]}"; do
    cut="$WORK/srccut-$i.mkv"
    "$FFMPEG" -v error -y -ss "${OFFSETS[$i]}" -i "$SRC" -t "$SAMPLE_SECS" -map 0:v:0 -c:v copy -an -sn "$cut" 2>/dev/null || true
    [ -f "$cut" ] && total=$((total + $(stat -c %s "$cut" 2>/dev/null || stat -f %z "$cut")))
  done
  echo "$total"
)"

printf '%-10s %-8s %-9s %-9s %s\n' ENCODER CRF VMAF SSIM "SIZE vs SOURCE"
printf '%.0s─' {1..58}; echo

for crf in $CRFS; do
  prefix="$WORK/av1-$crf"
  for i in "${!REFS[@]}"; do
    "$FFMPEG" -v error -y -i "${REFS[$i]}" \
      -c:v libsvtav1 -preset "$PRESET" -crf "$crf" \
      -svtav1-params "tune=0" -pix_fmt "$DEPTH" -an -sn "${prefix}-$i.mkv"
  done
  read -r vmaf ssim <<<"$(score "$prefix")"
  bytes="$(rung_bytes "$prefix")"
  pct="$(awk -v a="$bytes" -v b="$REF_BYTES" 'BEGIN{if(b)printf "%.0f%%", 100*a/b; else print "?"}')"
  printf '%-10s %-8s %-9s %-9s %s\n' "SVT-AV1" "$crf" "$vmaf" "$ssim" "$pct"
done

if [ "$COMPARE_HEVC" = "1" ]; then
  for crf in $CRFS; do
    prefix="$WORK/hevc-$crf"
    for i in "${!REFS[@]}"; do
      "$FFMPEG" -v error -y -i "${REFS[$i]}" \
        -c:v libx265 -preset slow -crf "$crf" \
        -x265-params "aq-mode=3:psy-rd=2.0:psy-rdoq=1.0:no-sao=1:bframes=8:rc-lookahead=40" \
        -pix_fmt "$DEPTH" -an -sn "${prefix}-$i.mkv"
    done
    read -r vmaf ssim <<<"$(score "$prefix")"
    bytes="$(rung_bytes "$prefix")"
    pct="$(awk -v a="$bytes" -v b="$REF_BYTES" 'BEGIN{if(b)printf "%.0f%%", 100*a/b; else print "?"}')"
    printf '%-10s %-8s %-9s %-9s %s\n' "x265" "$crf" "$vmaf" "$ssim" "$pct"
  done
fi

echo
echo "Pick the highest CRF still clearing your VMAF bar (95 ≈ visually transparent),"
echo "then set that as the AV1 target. Audio is never touched by any of this — Atmos,"
echo "TrueHD and DTS are stream-copied regardless of the video codec."
