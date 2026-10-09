import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { useTabParam } from "../lib/useTabParam";
import { rovingTarget } from "../ui/Tabs";
import {
  api,
  type Evaluation,
  type FitCounts,
  type FormatInfo,
  type IdealFile,
  type Movie,
  type MusicPreset,
  type ProfileMoveCounts,
  type QualityProfileInfo,
  type ReleaseList,
  type Series,
  type StoredProfile,
  type TargetPref,
} from "../lib/api";
import { useQuery } from "../lib/query";
import { ErrorState, Skeleton, StaleBanner } from "../ui";

const NO_PROFILES: QualityProfileInfo[] = [];
const NO_FORMATS: FormatInfo[] = [];
const NO_LADDER: string[] = [];
const NO_PRESETS: MusicPreset[] = [];

// Quality profiles. A video profile is built around its TARGET FILE — the codec, HDR, audio
// and bitrate window you want — which decides what's grabbed, how releases rank, and how the
// library's files are judged. Sources, rules and upgrades sit underneath it; raw scores and
// custom formats live in Advanced. Books and music have their own, simpler builders.

const MEDIA_TABS = [
  { key: "movie", label: "Movies" },
  { key: "series", label: "Series" },
  { key: "book", label: "Books" },
  { key: "music", label: "Music" },
] as const;
type Media = (typeof MEDIA_TABS)[number]["key"];
const MEDIA_KEYS: readonly Media[] = MEDIA_TABS.map((t) => t.key);

const RESOLUTIONS = [
  { v: "2160p", l: "4K" },
  { v: "1080p", l: "1080p" },
  { v: "720p", l: "720p" },
  { v: "576p", l: "576p" },
  { v: "480p", l: "480p" },
];
const RES_RANK: Record<string, number> = { "2160p": 5, "1080p": 4, "720p": 3, "576p": 2, "480p": 1 };

const SOURCES = [
  { v: "", l: "Any source" },
  { v: "HDTV", l: "HDTV+" },
  { v: "DVD", l: "DVD+" },
  // One WEB tier: the server treats WEB-DL and WEBRip alike for these gates (ranking still
  // prefers WEB-DL), so a stored "WEBRip" shows as this option too — see sourceValue.
  { v: "WEB-DL", l: "WEB+ (WEB-DL or WEBRip)" },
  { v: "BluRay", l: "BluRay+" },
  { v: "Remux", l: "Remux only" },
];

const MAX_SOURCES = [
  { v: "", l: "No upper limit" },
  { v: "Remux", l: "up to Remux" },
  { v: "BluRay", l: "up to BluRay (no Remux)" },
  { v: "WEB-DL", l: "up to WEB" },
  { v: "DVD", l: "up to DVD" },
];

const CONDITION_TYPES = [
  { v: "dynamic_range", l: "Dynamic range (DV/HDR10…)" },
  { v: "audio", l: "Audio (Atmos/TrueHD…)" },
  { v: "codec", l: "Codec (x265/x264…)" },
  { v: "source", l: "Source (BluRay/WEB-DL…)" },
  { v: "resolution", l: "Resolution" },
  { v: "edition", l: "Edition (Director's Cut…)" },
  { v: "release_group", l: "Release group" },
];

// Common junk file-types / sources worth one-click rejecting.
// (Cams, telesyncs and screeners have their own rule — see PreReleaseRule.)
const REJECT_TYPES = ["XviD", "AVI", "WMV", "3D"];
// Executable/script extensions — pre-rejected on new profiles for safety.
const EXECUTABLE_TYPES = ["exe", "bat", "cmd", "scr", "msi", "com", "vbs", "ps1"];

// Book file formats, grouped by edition — the score-able formats in a book profile.
const BOOK_FORMATS: { group: string; formats: string[] }[] = [
  { group: "Ebook", formats: ["EPUB", "AZW3", "MOBI", "AZW", "PDF", "CBZ", "CBR", "FB2"] },
  { group: "Audiobook", formats: ["M4B", "MP3", "M4A", "FLAC", "AAC", "OGG", "OPUS"] },
];

// How much better a release must be before it's worth replacing a same-resolution file.
// Expressed as a percentage, not Mbps: "2 Mbps better" more than doubles a 480p file and
// is noise on a 2160p one. 20% is the server's floor (quality.MinUpgradePercent), so the
// options start above it and the UI never promises something the server overrides.
const UPGRADE_STEPS = [
  { percent: 0, label: "Off", detail: "Size is ignored. A file is only replaced by a better resolution or a format you want." },
  { percent: 25, label: "Noticeably better", detail: "A 2.0 GB episode is replaced at about 2.5 GB. Swaps a thin, heavily-compressed encode for a normal one." },
  { percent: 50, label: "Clearly better", detail: "A 2.0 GB episode is replaced at about 3.0 GB. The new file has to be visibly heavier." },
  { percent: 100, label: "Much better", detail: "A 2.0 GB episode is replaced at about 4.0 GB. Only a dramatic jump, like a compact web rip giving way to a near-source encode." },
];

// How strongly smaller files win among equals (StoredProfile.small_bias).
const SIZE_LEANS = [
  { v: 0, l: "Off" },
  { v: 0.15, l: "Slightly" },
  { v: 4, l: "Strongly" },
];

// --- The target file -----------------------------------------------------------------

type Row = "codec" | "hdr" | "audio";
const TARGET_ROWS: { row: Row; label: string; hint: string; options: { k: string; l: string }[] }[] = [
  {
    row: "codec", label: "Video codec",
    hint: "A file is one of these.",
    options: [{ k: "hevc", l: "HEVC (H.265)" }, { k: "av1", l: "AV1" }, { k: "h264", l: "H.264" }],
  },
  {
    row: "hdr", label: "HDR",
    hint: "HDR10+ counts as HDR10 too. A Dolby Vision file counts as the format under it unless Dolby Vision has a setting.",
    options: [{ k: "HDR10+", l: "HDR10+" }, { k: "HDR10", l: "HDR10" }, { k: "DV", l: "Dolby Vision" }, { k: "HLG", l: "HLG" }, { k: "SDR", l: "SDR (no HDR)" }],
  },
  {
    row: "audio", label: "Audio",
    hint: "Features a file has or doesn't.",
    options: [{ k: "atmos", l: "Dolby Atmos" }, { k: "lossless", l: "Lossless (TrueHD, DTS-HD MA, FLAC)" }],
  },
];

// Each state's look and meaning — shown in the key above the rows and on hover. Only Must
// and Avoid decide whether a library file fits; Prefer just ranks releases.
const PREF_TONE: Record<string, { bg: string; fg: string; label: string; means: string }> = {
  must: { bg: "var(--accent)", fg: "var(--accent-ink)", label: "Must", means: "Never grabbed without it, and a file without it doesn't fit. Two in a row means either will do." },
  want: { bg: "var(--good)", fg: "#fff", label: "Prefer", means: "Picked over releases without it. A file without it still fits." },
  "": { bg: "var(--line)", fg: "var(--ink)", label: "—", means: "No opinion either way." },
  avoid: { bg: "var(--reject)", fg: "#fff", label: "Avoid", means: "Grabbed only when nothing else is available, and a file with it doesn't fit." },
};

// The bitrate windows are per resolution, with 576p and 480p together as "SD" (how files
// are labelled once analysed).
const WINDOW_KEYS = [
  { key: "2160p", label: "4K", from: ["2160p"] },
  { key: "1080p", label: "1080p", from: ["1080p"] },
  { key: "720p", label: "720p", from: ["720p"] },
  { key: "SD", label: "SD", from: ["576p", "480p"] },
];

function setPref(ideal: IdealFile, row: Row, key: string, pref: TargetPref): IdealFile {
  const m = { ...(ideal[row] ?? {}) };
  if (pref) m[key] = pref;
  else delete m[key];
  return { ...ideal, [row]: m };
}

function windowKeys(allowed: string[]) {
  return WINDOW_KEYS.filter((w) => allowed.length === 0 || w.from.some((f) => allowed.includes(f)));
}

// --- Starting points ------------------------------------------------------------------

function emptyProfile(media: string): StoredProfile {
  return {
    id: 0,
    media_type: media,
    name: "",
    base: "",
    allowed_resolutions: [],
    min_source: "",
    max_source: "",
    bitrate_cap_mbps: 0,
    small_bias: 0,
    min_format_score: 0,
    format_scores: {},
    required_formats: [],
    custom_formats: [],
    keywords: [],
    rejected: [...EXECUTABLE_TYPES], // reject executables by default (malware safety)
    min_seeders: 0,
    stall_minutes: 0,
    upgrades_enabled: true,
    upgrade_min_percent: 0,
    ideal: media === "movie" || media === "series" ? {} : undefined,
  };
}

// Templates for a new video profile. Windows follow the bitrates that look like the source
// on a big screen in HEVC: 4K 15–35 Mb/s, 1080p 5–15, 720p 3–8. TV is encoded leaner than
// film, and series grabs are held to these windows too, so a series profile starts lower —
// otherwise a good x265 WEB episode sits under the floor and only wins when nothing else does.
const TV_WINDOWS = { "2160p": { min: 10, max: 30 }, "1080p": { min: 3, max: 12 }, "720p": { min: 1.5, max: 6 } };
const VIDEO_TEMPLATES: { key: string; name: string; desc: string; make: (media: string) => StoredProfile }[] = [
  {
    key: "4k", name: "4K HDR collection", desc: "4K first, 1080p if that's all there is. HEVC or AV1, HDR10+ preferred, Atmos wanted.",
    make: (m) => ({
      ...emptyProfile(m), allowed_resolutions: ["2160p", "1080p"],
      ideal: {
        codec: { hevc: "want", av1: "want" }, hdr: { "HDR10+": "want" }, audio: { atmos: "want" },
        bitrate: m === "series"
          ? { "2160p": TV_WINDOWS["2160p"], "1080p": TV_WINDOWS["1080p"] }
          : { "2160p": { min: 15, max: 35 }, "1080p": { min: 5, max: 15 } },
      },
    }),
  },
  {
    key: "1080", name: "1080p efficient", desc: "1080p in HEVC or AV1, 720p as a fallback. Good quality without remux-sized files.",
    make: (m) => ({
      ...emptyProfile(m), allowed_resolutions: ["1080p", "720p"],
      ideal: {
        codec: { hevc: "want", av1: "want" },
        bitrate: m === "series"
          ? { "1080p": TV_WINDOWS["1080p"], "720p": TV_WINDOWS["720p"] }
          : { "1080p": { min: 5, max: 15 }, "720p": { min: 3, max: 8 } },
      },
    }),
  },
  {
    key: "compact", name: "Compact", desc: "The smallest watchable files — for big TV libraries or limited space.",
    make: (m) => ({
      ...emptyProfile(m), allowed_resolutions: ["1080p", "720p"], small_bias: 4,
      ideal: { codec: { hevc: "want", av1: "want" }, bitrate: { "1080p": { min: 3, max: 8 }, "720p": { min: 2, max: 5 } } },
    }),
  },
  { key: "blank", name: "Start from scratch", desc: "An empty profile — set everything yourself.", make: (m) => emptyProfile(m) },
];

const NO_COUNTS: FitCounts = { titles: 0, files: 0, fits: 0, over: 0, under: 0, mismatch: 0 };

// --- Shared look ---------------------------------------------------------------------

const panelStyle = { background: "var(--panel)", border: "1px solid var(--line)" };
const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" };
const primaryStyle = { background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" };

// =====================================================================================
// List
// =====================================================================================

export function Quality() {
  // ?media= keeps the media you were looking at across a reload, Back and a bookmark.
  const [media, setMedia] = useTabParam(MEDIA_KEYS, "movie", "media");
  // One cache entry per media tab, so switching back to a tab (or Back from an
  // editor) shows its profiles at once while they revalidate.
  const listQ = useQuery(`quality:${media}`, () => api.qualityProfiles(media), { staleMs: 0 });
  const profiles = listQ.data?.profiles ?? NO_PROFILES;
  const formats = listQ.data?.formats ?? NO_FORMATS;
  const ladder = listQ.data?.music_ladder ?? NO_LADDER;
  const musicPresets = listQ.data?.music_presets ?? NO_PRESETS;
  const loadError = listQ.error?.message ?? null;
  const [fits, setFits] = useState<Record<string, FitCounts>>({});
  const [editing, setEditing] = useState<StoredProfile | null>(null);
  const [picking, setPicking] = useState(false);
  // A failed action on a card (the list's own load errors are loadError).
  const [error, setError] = useState<string | null>(null);
  // "Moved 12 films to …" after a delete. It lives here, not on the card, because the
  // card is gone once the list refreshes.
  const [notice, setNotice] = useState<string | null>(null);
  const isVideo = media === "movie" || media === "series";

  const loadFits = useCallback(() => {
    if (media === "movie" || media === "series") {
      api.libraryFitProfiles(media).then((r) => setFits(r.profiles ?? {})).catch(() => setFits({}));
    } else {
      setFits({});
    }
  }, [media]);
  useEffect(() => { loadFits(); }, [loadFits]);

  const refetchList = listQ.refetch;
  const refresh = useCallback(() => {
    refetchList().then(() => setError(null));
    loadFits();
  }, [refetchList, loadFits]);
  useEffect(() => setNotice(null), [media]);

  const openNew = () => (isVideo ? setPicking(true) : setEditing(emptyProfile(media)));
  const editRef = async (info: QualityProfileInfo) => setEditing(await api.qualityProfile(info.key));
  const duplicate = async (info: QualityProfileInfo) => {
    const sp = await api.qualityProfile(info.key);
    setEditing({ ...sp, id: 0, name: `${sp.name} (copy)` });
  };

  if (editing) {
    const done = { onCancel: () => setEditing(null), onSaved: () => { setEditing(null); refresh(); } };
    if (editing.media_type === "book") return <BookBuilder initial={editing} {...done} />;
    if (editing.media_type === "music") return <MusicBuilder initial={editing} ladder={ladder} presets={musicPresets} {...done} />;
    return <VideoBuilder formats={formats} initial={editing} {...done} />;
  }

  return (
    <>
      <PageHeader title="Quality profiles" />
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
          <MediaSwitch value={media} onChange={setMedia} />
          <button onClick={openNew} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={primaryStyle}>+ New profile</button>
        </div>

        <p className="mb-4 text-[12.5px] text-ink-dim">
          {media === "book"
            ? "A book profile picks which formats to grab (EPUB, M4B…) and can boost releases by keyword — e.g. GraphicAudio +100 to prefer full-cast dramatizations. Higher score wins."
            : media === "music"
              ? "A music profile is a ladder of audio qualities. Arrmada grabs the best tier available on your ladder. Albums you already have aren't replaced when a better tier turns up — there's no music upgrade sweep yet."
              : "A profile describes the file you want — codec, HDR, audio and a bitrate window. Arrmada grabs to it, ranks releases by it, and shows which files in your library don't fit."}
        </p>

        {error && <div className="mb-3 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}
        {listQ.data && loadError && <StaleBanner message={loadError} onRetry={refresh} />}
        {notice && (
          <div className="mb-3 flex items-center justify-between gap-3 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--accent-line)", color: "var(--ink)" }}>
            <span>{notice}</span>
            <button onClick={() => setNotice(null)} aria-label="Dismiss" className="text-ink-dim">✕</button>
          </div>
        )}

        {!listQ.data ? (
          loadError ? <ErrorState what="quality profiles" message={loadError} onRetry={refresh} busy={listQ.loading} /> : <Skeleton variant="list" count={3} />
        ) : profiles.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No quality profiles yet. Add one to tell Arrmada what to grab.
          </div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {profiles.map((p) => (
              <ProfileCard key={p.key} info={p} media={media} counts={isVideo ? (fits[p.key] ?? NO_COUNTS) : undefined}
                others={profiles.filter((o) => o.key !== p.key)}
                onEdit={() => editRef(p)} onDuplicate={() => duplicate(p)} onChange={refresh}
                onDeleted={(msg) => { setNotice(msg); refresh(); }} onError={setError} />
            ))}
          </div>
        )}
      </div>
      {picking && (
        <TemplatePicker onClose={() => setPicking(false)} onPick={(sp) => { setPicking(false); setEditing(sp); }} media={media} />
      )}
    </>
  );
}

// MediaSwitch is a segmented control rather than the underline tab bar, but it behaves
// like tabs for the keyboard and screen readers: arrows, Home and End move and select.
function MediaSwitch({ value, onChange }: { value: Media; onChange: (v: Media) => void }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKeyDown = (e: React.KeyboardEvent, i: number) => {
    const to = rovingTarget(e.key, i, MEDIA_TABS.length);
    if (to === null) return;
    e.preventDefault();
    refs.current[to]?.focus();
    onChange(MEDIA_TABS[to].key);
  };
  return (
    <div role="tablist" aria-label="Profiles for" className="inline-flex rounded-lg p-1" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      {MEDIA_TABS.map((t, i) => {
        const active = t.key === value;
        return (
          <button key={t.key} ref={(el) => { refs.current[i] = el; }} type="button" role="tab" aria-selected={active} tabIndex={active ? 0 : -1} onClick={() => onChange(t.key)} onKeyDown={(e) => onKeyDown(e, i)} className="rounded-md px-3 py-1.5 text-[12px] font-semibold transition-colors" style={{ background: active ? "var(--accent)" : "transparent", color: active ? "var(--accent-ink)" : "var(--ink-dim)" }}>
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

// movedSummary reads a delete's counts back as "12 films and 1 request". Extra versions
// are the same films again, so they aren't counted twice.
function movedSummary(m: ProfileMoveCounts): string {
  const n = (count: number, one: string, many: string) => (count > 0 ? [`${count} ${count === 1 ? one : many}`] : []);
  const parts = [
    ...n(m.movies, "film", "films"),
    ...n(m.series, "show", "shows"),
    ...n(m.books, "book", "books"),
    ...n(m.artists, "artist", "artists"),
    ...n(m.requests, "request", "requests"),
    ...n(m.grabs, "download", "downloads"),
  ];
  if (parts.length <= 1) return parts[0] ?? "";
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

function ProfileCard({ info, media, counts, others, onEdit, onDuplicate, onChange, onDeleted, onError }: {
  info: QualityProfileInfo; media: string; counts?: FitCounts; others: QualityProfileInfo[];
  onEdit: () => void; onDuplicate: () => void; onChange: () => void;
  onDeleted: (message: string) => void; onError: (message: string) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  // Where this profile's titles go: the default, or the first other profile when this is
  // the default.
  const fallback = (others.find((o) => o.is_default) ?? others[0])?.key ?? "";
  const [moveTo, setMoveTo] = useState(fallback);
  const target = others.find((o) => o.key === moveTo) ?? others.find((o) => o.key === fallback);
  const del = async () => {
    if (!target) return;
    try {
      const r = await api.deleteQualityProfile(Number(info.key.replace("custom:", "")), target.key);
      const what = movedSummary(r.moved);
      onDeleted(what ? `Deleted ${info.name}. Moved ${what} to ${target.name}.` : `Deleted ${info.name}.`);
    } catch (e) {
      setConfirming(false);
      onError((e as Error).message);
    }
  };
  const makeDefault = async () => {
    await api.setDefaultProfile(media, info.key);
    onChange();
  };
  const titles = counts?.titles ?? 0;
  const noun = media === "series" ? (titles === 1 ? "show" : "shows") : titles === 1 ? "film" : "films";
  // Books and music have no fit counts, so their titles go unnumbered.
  const what = counts ? `${titles} ${noun}` : media === "book" ? "its books" : "its artists";
  return (
    <div className="rounded-xl p-3.5" style={{ ...panelStyle, border: `1px solid ${info.is_default ? "var(--accent)" : "var(--line)"}` }}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5">
        {/* A floor on the title's width: on a phone the buttons wrap below it rather than over it. */}
        <div className="min-w-[200px] flex-1">
          <div className="flex items-center gap-2">
            <span className="text-[13.5px] font-semibold">{info.name}</span>
            {info.is_default && <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Default</span>}
          </div>
          <div className="mt-1 truncate text-[11.5px] text-ink-dim" title={info.summary}>{info.summary || "Any quality"}</div>
        </div>
        <div className="flex flex-none flex-wrap items-center gap-2">
          {!info.is_default && (
            <button onClick={makeDefault} title="Use this profile by default when adding" className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Make default</button>
          )}
          <button onClick={onEdit} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Edit</button>
          <button onClick={onDuplicate} title="Start a new profile from this one" className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Duplicate</button>
          {confirming && target ? (
            <>
              {/* Every title on a profile needs somewhere real to go, so the delete asks where. */}
              <label className="flex items-center gap-1.5 text-[11.5px] text-ink-dim">
                Move {what} to
                <select value={target.key} onChange={(e) => setMoveTo(e.target.value)} className="rounded-lg px-2 py-1.5 text-[11.5px]" style={fieldStyle}>
                  {others.map((o) => <option key={o.key} value={o.key}>{o.name}</option>)}
                </select>
              </label>
              <button onClick={del} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>
                {counts && titles === 0 ? "Delete" : `Delete and move ${what} to ${target.name}`}
              </button>
              <button onClick={() => setConfirming(false)} aria-label="Keep the profile" className="rounded-lg px-2.5 py-1.5 text-[11.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>✕</button>
            </>
          ) : (
            <button onClick={() => setConfirming(true)} disabled={others.length === 0}
              title={others.length === 0 ? "Create another profile first" : undefined}
              className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold disabled:cursor-not-allowed disabled:opacity-40" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Delete</button>
          )}
        </div>
      </div>
      {counts && (
        <div className="mt-2.5 flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t pt-2.5" style={{ borderColor: "var(--line-soft)" }}>
          <span className="font-mono text-[10.5px] text-ink-faint">Used by {titles} {noun}</span>
          {counts.files > 0 && <div className="min-w-[180px] flex-1"><FitBar counts={counts} /></div>}
        </div>
      )}
    </div>
  );
}

// FitBar shows how a set of files fits its target: green fits, red over the ceiling,
// orange under the floor, yellow the bitrate's fine but something else isn't.
function FitBar({ counts }: { counts: FitCounts }) {
  if (counts.files === 0) return <span className="text-[11px] text-ink-faint">No analysed files to judge yet</span>;
  const segs: [number, string][] = [[counts.fits, "var(--good)"], [counts.over, "var(--reject)"], [counts.under, "var(--under)"], [counts.mismatch, "var(--mismatch)"]];
  return (
    <div>
      <div className="flex h-[7px] overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
        {segs.filter(([n]) => n > 0).map(([n, c], i) => <span key={i} style={{ flex: n, background: c }} />)}
      </div>
      <div className="mt-1 font-mono text-[10.5px] text-ink-faint">
        <span style={{ color: "var(--good)" }}>{counts.fits} fit</span>
        {counts.over > 0 && <> · <span style={{ color: "var(--reject)" }}>{counts.over} over</span></>}
        {counts.under > 0 && <> · <span style={{ color: "var(--under)" }}>{counts.under} under</span></>}
        {counts.mismatch > 0 && <> · <span style={{ color: "var(--mismatch)" }}>{counts.mismatch} don't fit</span></>}
        {" "}of {counts.files} file{counts.files === 1 ? "" : "s"}
      </div>
    </div>
  );
}

function TemplatePicker({ media, onPick, onClose }: { media: string; onPick: (sp: StoredProfile) => void; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div role="dialog" aria-modal="true" aria-labelledby="tpl-title" className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-[8vh] w-full max-w-[640px] rounded-xl p-5" style={{ ...panelStyle, boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <h3 id="tpl-title" className="text-[15px] font-bold">New {media === "series" ? "series" : "movie"} profile</h3>
        <p className="mt-1 text-[12px] text-ink-dim">Pick a starting point — everything can be changed after.</p>
        <div className="mt-4 grid grid-cols-1 gap-2.5 sm:grid-cols-2">
          {VIDEO_TEMPLATES.map((t) => (
            <button key={t.key} onClick={() => onPick(t.make(media))} className="rounded-xl p-3.5 text-left transition-colors hover:border-[var(--accent)]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
              <div className="text-[13px] font-semibold">{t.name}</div>
              <div className="mt-1 text-[11.5px] leading-[1.45] text-ink-dim">{t.desc}</div>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

// =====================================================================================
// Video builder
// =====================================================================================

type DecisionView = { winner: Evaluation | null; why?: string[]; chosen_over?: string; eligible: Evaluation[]; rejected: Evaluation[] };

function withTarget(sp: StoredProfile): StoredProfile {
  // An empty target (not a missing one) is how the builder says "no target": the server
  // reads a missing one from the scores instead.
  return { ...sp, ideal: sp.ideal ?? {}, required_formats: sp.required_formats ?? [], keywords: sp.keywords ?? [], rejected: sp.rejected ?? [], custom_formats: sp.custom_formats ?? [] };
}

function useUnsaved(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);
}

function VideoBuilder({ formats, initial, onCancel, onSaved }: { formats: FormatInfo[]; initial: StoredProfile; onCancel: () => void; onSaved: () => void }) {
  const start = useMemo(() => withTarget(initial), [initial]);
  const [sp, setSp] = useState<StoredProfile>(start);
  const [decision, setDecision] = useState<DecisionView | null>(null);
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const startJSON = useRef(JSON.stringify(start));
  const dirty = JSON.stringify(sp) !== startJSON.current;
  useUnsaved(dirty);

  // Live preview over the sample releases, debounced.
  useEffect(() => {
    let alive = true;
    const t = window.setTimeout(() => {
      api.qualityPreviewSpec(sp).then((p) => {
        if (!alive) return;
        // Go marshals empty slices as null — coalesce so .length/.map are safe.
        setDecision({ ...p.decision, eligible: p.decision.eligible ?? [], rejected: p.decision.rejected ?? [] });
      }).catch(() => {});
    }, 250);
    return () => { alive = false; window.clearTimeout(t); };
  }, [sp]);

  const patch = (p: Partial<StoredProfile>) => setSp((s) => ({ ...s, ...p }));
  const ideal = sp.ideal ?? {};
  // Target edits apply to the latest state, so two quick changes can't overwrite each other.
  const updateIdeal = (fn: (i: IdealFile) => IdealFile) => setSp((s) => ({ ...s, ideal: fn(s.ideal ?? {}) }));

  const leave = () => {
    if (dirty && !window.confirm("Discard your changes to this profile?")) return;
    onCancel();
  };
  const save = async () => {
    if (!sp.name.trim()) { setError("Give your profile a name."); return; }
    for (const [key, w] of Object.entries(ideal.bitrate ?? {})) {
      if (w.min > 0 && w.max > 0 && w.min > w.max) { setError(`The ${key === "2160p" ? "4K" : key} bitrate floor is above its ceiling.`); return; }
    }
    setSaving(true);
    setError(null);
    try {
      if (sp.id > 0) await api.updateQualityProfile(sp.id, sp);
      else await api.createQualityProfile(sp);
      startJSON.current = JSON.stringify(sp);
      onSaved();
    } catch (e) {
      setError((e as Error).message);
      setSaving(false);
    }
  };

  const winner = decision?.winner ?? null;
  const total = (decision?.eligible.length ?? 0) + (decision?.rejected.length ?? 0);
  const otherFormats = formats.filter((f) => !f.target);

  return (
    <>
      <PageHeader title={sp.id > 0 ? "Edit profile" : "New profile"} tail={sp.media_type === "series" ? "Series" : "Movies"} />
      <div className="mx-auto grid w-full max-w-[1240px] grid-cols-1 gap-7 px-4 pb-4 pt-6 sm:px-6 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
        <section className="min-w-0">
          <div className="mb-4">
            <button onClick={leave} className="text-[12px] text-ink-dim hover:text-[var(--ink)]">← Back</button>
          </div>
          <label htmlFor="qp-name" className="mb-1.5 block font-mono text-[10px] font-bold uppercase tracking-wide text-accent">Name</label>
          <input id="qp-name" value={sp.name} onChange={(e) => patch({ name: e.target.value })} placeholder="My 4K collection" className="w-full rounded-lg px-3 py-2 text-[13px]" style={fieldStyle} />

          <SectionLabel>1 · The file you want</SectionLabel>
          <TargetEditor sp={sp} ideal={ideal} onIdeal={updateIdeal} onResolutions={(r) => patch({ allowed_resolutions: r })} />

          <div className="mt-6 flex flex-col gap-2.5">
            <Collapsible n={2} title="Sources" summary={sourceSummary(sp)}>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <label className="block">
                  <span className="mb-1 block text-[10.5px] text-ink-faint">Minimum</span>
                  <select value={sourceValue(sp.min_source)} onChange={(e) => patch({ min_source: e.target.value })} className="w-full rounded-lg px-3 py-2 text-[12.5px]" style={fieldStyle}>
                    {SOURCES.map((s) => <option key={s.v} value={s.v}>{s.l}</option>)}
                  </select>
                </label>
                <label className="block">
                  <span className="mb-1 block text-[10.5px] text-ink-faint">Maximum</span>
                  <select value={sourceValue(sp.max_source)} onChange={(e) => patch({ max_source: e.target.value })} className="w-full rounded-lg px-3 py-2 text-[12.5px]" style={fieldStyle}>
                    {MAX_SOURCES.map((s) => <option key={s.v} value={s.v}>{s.l}</option>)}
                  </select>
                </label>
              </div>
              <p className="mt-2 text-[10.5px] text-ink-faint">Within these sources and your target, Arrmada picks the highest-bitrate release.</p>
            </Collapsible>

            <Collapsible n={3} title="Rules" summary={rulesSummary(sp)}>
              <div className="mb-1.5 text-[12px] font-semibold">Reject</div>
              <PreReleaseRule allowed={!!sp.allow_prerelease} onChange={(allowed) => patch({ allow_prerelease: allowed })} />
              <RejectEditor rejected={sp.rejected ?? []} onChange={(r) => patch({ rejected: r })} />
              <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2">
                <NumberField label="Minimum seeders" hint="Skip releases with fewer" value={sp.min_seeders} onChange={(v) => patch({ min_seeders: v })} />
                <StallField value={sp.stall_minutes} onChange={(v) => patch({ stall_minutes: v })} />
              </div>
            </Collapsible>

            <Collapsible n={4} title="Upgrades" summary={upgradesSummary(sp)}>
              <UpgradesEditor sp={sp} patch={patch} />
            </Collapsible>

            <Collapsible title="Advanced" summary={advancedSummary(sp, otherFormats)}>
              <AdvancedPanel sp={sp} patch={patch} otherFormats={otherFormats} />
            </Collapsible>
          </div>
        </section>

        <aside className="min-w-0">
          <div className="flex flex-col gap-4 lg:sticky lg:top-[72px]">
            <div>
              <div className="mb-1 flex items-center gap-2 text-[15px] font-bold">
                <span className="inline-block h-2 w-2 rounded-full" style={{ background: "var(--accent)" }} />
                What you'll get
              </div>
              <p className="mb-3 mt-1 text-[11.5px] text-ink-faint">Live on a sample set of real-world releases — change anything and it re-decides.</p>
              {!winner ? (
                <div className="rounded-xl p-7 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Nothing in the sample matches this profile. Loosen a Must or widen a bitrate window.</div>
              ) : (
                <Hero winner={winner} why={decision?.why ?? []} chosenOver={decision?.chosen_over} open={open} onToggle={() => setOpen((o) => !o)} eligible={decision?.eligible.slice(1) ?? []} rejected={decision?.rejected ?? []} total={total} />
              )}
            </div>
            <LibraryFitPanel sp={sp} />
            <TestPanel sp={sp} />
          </div>
        </aside>
      </div>
      <SaveBar dirty={dirty} saving={saving} error={error} onSave={save} onCancel={leave}
        mobileNote={winner ? `Would grab ${winner.candidate.release.resolution} ${winner.candidate.release.source}` : undefined} />
    </>
  );
}

// sourceValue maps a stored source onto the option that shows it: WEB-DL and WEBRip are
// one WEB tier, so an older profile saved with "WEBRip" reads as WEB+ / up to WEB.
function sourceValue(v: string): string {
  return v === "WEBRip" ? "WEB-DL" : v;
}

// sourceName is a source as the summary says it, with the two WEB labels as one.
function sourceName(v: string): string {
  return sourceValue(v) === "WEB-DL" ? "WEB" : v;
}

function sourceSummary(sp: StoredProfile): string {
  const min = SOURCES.find((s) => s.v === sourceValue(sp.min_source))?.l ?? "Any source";
  if (!sp.max_source) return sp.min_source ? min : "Any source";
  return `${sp.min_source ? sourceName(sp.min_source) : "Any"} to ${sourceName(sp.max_source)}`;
}

function rulesSummary(sp: StoredProfile): string {
  const rej = (sp.rejected ?? []).filter((r) => !EXECUTABLE_TYPES.some((t) => t.toLowerCase() === r.toLowerCase()));
  const parts: string[] = [];
  if (!sp.allow_prerelease) parts.push("no cams or screeners");
  if (rej.length) parts.push(`rejects ${rej.slice(0, 3).join(", ")}${rej.length > 3 ? "…" : ""}`);
  if (EXECUTABLE_TYPES.every((t) => (sp.rejected ?? []).some((r) => r.toLowerCase() === t))) parts.push("no executables");
  if (sp.min_seeders > 0) parts.push(`${sp.min_seeders}+ seeders`);
  parts.push(`stall: ${stallLabel(sp.stall_minutes)}`);
  return parts.join(" · ") || "None";
}

function upgradesSummary(sp: StoredProfile): string {
  if (!sp.upgrades_enabled) return "Off";
  const parts = ["On"];
  // Upgrading only stops at the target where a resolution's window has a floor.
  if (Object.values(sp.ideal?.bitrate ?? {}).some((w) => w.min > 0)) parts.push("stops at the target");
  const step = UPGRADE_STEPS.find((s) => s.percent === sp.upgrade_min_percent);
  if (step && step.percent > 0) parts.push(`also +${step.percent}% bitrate`);
  return parts.join(" · ");
}

function advancedSummary(sp: StoredProfile, other: FormatInfo[]): string {
  const parts: string[] = [];
  const scored = other.filter((f) => (sp.format_scores[f.name] ?? 0) !== 0 || (sp.required_formats ?? []).includes(f.name)).length;
  if (scored) parts.push(`${scored} other format${scored === 1 ? "" : "s"}`);
  if ((sp.custom_formats ?? []).length) parts.push(`${sp.custom_formats!.length} custom`);
  if ((sp.keywords ?? []).length) parts.push(`${sp.keywords!.length} keyword${sp.keywords!.length === 1 ? "" : "s"}`);
  if (sp.small_bias > 0) parts.push("prefers smaller files");
  return parts.join(" · ") || "Custom formats, keywords, raw scores";
}

// TargetEditor sets the file the profile aims for: its resolutions, a state for each codec,
// HDR and audio option, and a bitrate window per resolution.
function TargetEditor({ sp, ideal, onIdeal, onResolutions }: { sp: StoredProfile; ideal: IdealFile; onIdeal: (fn: (i: IdealFile) => IdealFile) => void; onResolutions: (r: string[]) => void }) {
  const allowed = sp.allowed_resolutions;
  const best = allowed.reduce((b, r) => (RES_RANK[r] > (RES_RANK[b] ?? 0) ? r : b), "");
  const toggleRes = (v: string) => onResolutions(allowed.includes(v) ? allowed.filter((r) => r !== v) : [...allowed, v]);
  const setWindow = (key: string, part: "min" | "max", v: number) => onIdeal((cur) => {
    const b = { ...(cur.bitrate ?? {}) };
    const w = { min: b[key]?.min ?? 0, max: b[key]?.max ?? 0, [part]: Math.max(0, v || 0) };
    if (!w.min && !w.max) delete b[key];
    else b[key] = w;
    return { ...cur, bitrate: b };
  });
  return (
    <div className="rounded-xl p-4" style={panelStyle}>
      <p className="mb-3.5 text-[11px] leading-[1.5] text-ink-faint">
        One description drives everything: what's grabbed, how releases rank, and which files in your library are flagged as not fitting.
      </p>

      <div className="mb-1 text-[12px] font-semibold">Resolution</div>
      <div className="flex flex-wrap gap-2">
        {RESOLUTIONS.map((r) => {
          const active = allowed.includes(r.v);
          return (
            <button key={r.v} onClick={() => toggleRes(r.v)} className="inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "var(--panel)", color: active ? "var(--accent)" : "var(--ink-dim)" }}>
              {r.l}
              {active && <span className="font-mono text-[9px] font-bold uppercase opacity-80">{r.v === best ? "goal" : "fallback"}</span>}
            </button>
          );
        })}
      </div>
      <p className="mb-4 mt-1.5 text-[10.5px] text-ink-faint">
        {allowed.length === 0 ? "Any resolution. Pick some to limit what's grabbed." : "Only these are grabbed. The highest is the goal; the rest are fallbacks while it isn't available."}
      </p>

      <PrefKey />

      {TARGET_ROWS.map((row) => (
        <div key={row.row} className="mb-4">
          <div className="flex items-baseline justify-between gap-2">
            <span className="text-[12px] font-semibold">{row.label}</span>
          </div>
          <div className="mb-1.5 text-[10.5px] text-ink-faint">{row.hint}</div>
          <div className="flex flex-col gap-1.5">
            {row.options.map((o) => (
              <div key={o.k} className="flex flex-wrap items-center justify-between gap-2 rounded-lg py-1 pl-2.5 pr-1" style={{ background: "var(--panel-2)" }}>
                <span className="text-[12.5px]">{o.l}</span>
                <PrefPicker value={(ideal[row.row]?.[o.k] ?? "") as TargetPref} onChange={(p) => onIdeal((cur) => setPref(cur, row.row, o.k, p))} label={o.l} />
              </div>
            ))}
          </div>
        </div>
      ))}

      <div className="text-[12px] font-semibold">Bitrate window</div>
      <div className="mb-2 text-[10.5px] text-ink-faint">
        The whole file's average, as the library shows it. Above the ceiling isn't grabbed; under the floor is only grabbed when nothing better exists.
      </div>
      <div className="flex flex-col gap-1.5">
        {windowKeys(allowed).map((w) => (
          <div key={w.key} className="flex items-center gap-2 text-[12px]">
            <span className="w-[52px] font-mono text-ink-dim">{w.label}</span>
            <NumIn value={ideal.bitrate?.[w.key]?.min} onSet={(v) => setWindow(w.key, "min", v)} label={`${w.label} floor in Mb/s`} />
            <span className="text-ink-faint">to</span>
            <NumIn value={ideal.bitrate?.[w.key]?.max} onSet={(v) => setWindow(w.key, "max", v)} label={`${w.label} ceiling in Mb/s`} />
            <span className="text-[11px] text-ink-faint">Mb/s</span>
          </div>
        ))}
      </div>

    </div>
  );
}

function PrefPicker({ value, onChange, label }: { value: TargetPref; onChange: (p: TargetPref) => void; label: string }) {
  const opts: TargetPref[] = ["avoid", "", "want", "must"];
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      {opts.map((o) => {
        const on = value === o;
        const t = PREF_TONE[o];
        return (
          <button key={o || "none"} role="radio" aria-checked={on} onClick={() => onChange(o)} className="rounded-md px-2 py-1 text-[10.5px] font-semibold"
            title={`${t.label === "—" ? "No opinion" : t.label}: ${t.means}`}
            aria-label={`${t.label === "—" ? "No opinion" : t.label} — ${t.means}`}
            style={{ background: on ? t.bg : "transparent", color: on ? t.fg : "var(--ink-faint)", minWidth: 38 }}>
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

// PrefKey explains the buttons, in their own colours, before the first row uses them.
function PrefKey() {
  const order: TargetPref[] = ["must", "want", "", "avoid"];
  return (
    <div className="mb-4 rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
      <div className="mb-2 text-[11px] font-semibold text-ink-dim">What the buttons mean</div>
      <div className="flex flex-col gap-1.5">
        {order.map((o) => {
          const t = PREF_TONE[o];
          return (
            <div key={o || "none"} className="flex items-start gap-2.5 text-[11px] leading-[1.45]">
              <span className="w-[46px] flex-none rounded-md py-0.5 text-center text-[10.5px] font-semibold" style={{ background: t.bg, color: t.fg }}>{t.label}</span>
              <span className="text-ink-dim">{t.means}</span>
            </div>
          );
        })}
      </div>
      <div className="mt-2 text-[10.5px] text-ink-faint">Only Must and Avoid flag a file in your library as not fitting. Hover any button for its meaning.</div>
    </div>
  );
}

function NumIn({ value, onSet, label }: { value?: number; onSet: (v: number) => void; label: string }) {
  return (
    <input type="number" min={0} step="any" aria-label={label} value={value || ""} placeholder="any" onChange={(e) => onSet(Number(e.target.value))}
      className="w-[64px] rounded-lg px-2 py-1 text-right font-mono text-[12.5px]" style={fieldStyle} />
  );
}

function Collapsible({ n, title, summary, children }: { n?: number; title: string; summary: string; children: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-xl" style={panelStyle}>
      <button onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex w-full items-center gap-3 px-4 py-3 text-left">
        <span className="min-w-0 flex-1">
          <span className="text-[13px] font-semibold">{n ? `${n} · ` : ""}{title}</span>
          <span className="ml-2 text-[11.5px] text-ink-faint">{summary}</span>
        </span>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true" style={{ transform: open ? "rotate(180deg)" : "none", transition: "transform .2s", color: "var(--ink-faint)" }}><path d="M6 9l6 6 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" /></svg>
      </button>
      {open && <div className="border-t px-4 pb-4 pt-3.5" style={{ borderColor: "var(--line-soft)" }}>{children}</div>}
    </div>
  );
}

function UpgradesEditor({ sp, patch }: { sp: StoredProfile; patch: (p: Partial<StoredProfile>) => void }) {
  return (
    <div>
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-[12.5px] font-semibold">Automatically upgrade</div>
          <div className="text-[10.5px] leading-[1.45] text-ink-faint">
            After a file is imported, keep watching for a better release and replace it. Stops once the file is the target — the goal resolution,
            everything you prefer, inside a bitrate window that has a floor.
          </div>
        </div>
        <Switch on={sp.upgrades_enabled} onChange={(v) => patch({ upgrades_enabled: v })} label="Automatically upgrade" />
      </div>
      {sp.upgrades_enabled && (
        <div className="mt-3.5 border-t pt-3" style={{ borderColor: "var(--line)" }}>
          <div className="text-[12px] font-semibold">Also replace a same-quality file when it's bigger</div>
          <div className="mt-0.5 text-[10.5px] text-ink-faint">Two releases can both be 1080p and still look different. Pick how much better one has to be before it's worth re-downloading.</div>
          <div className="mt-2.5 flex flex-col gap-1.5">
            {UPGRADE_STEPS.map((step) => {
              const on = sp.upgrade_min_percent === step.percent;
              return (
                <button key={step.percent} onClick={() => patch({ upgrade_min_percent: step.percent })} className="flex items-start gap-2.5 rounded-lg px-3 py-2 text-left"
                  style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel-2)" }}>
                  <span className="mt-[3px] inline-block h-3 w-3 flex-none rounded-full" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent)" : "transparent" }} />
                  <span className="min-w-0">
                    <span className="flex items-baseline gap-1.5">
                      <span className="text-[11.5px] font-semibold" style={{ color: on ? "var(--accent)" : "var(--ink)" }}>{step.label}</span>
                      {step.percent > 0 && <span className="font-mono text-[10px] text-ink-faint">+{step.percent}% bitrate</span>}
                    </span>
                    <span className="mt-0.5 block text-[10.5px] leading-[1.45] text-ink-faint">{step.detail}</span>
                  </span>
                </button>
              );
            })}
          </div>
          <div className="mt-2 text-[10.5px] text-ink-faint">Never above a bitrate ceiling. Comparisons account for codec, so a smaller HEVC file isn't treated as worse than a bloated H.264 one.</div>
        </div>
      )}
    </div>
  );
}

function Switch({ on, onChange, label }: { on: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <button role="switch" aria-checked={on} aria-label={label} onClick={() => onChange(!on)} className="relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors"
      style={{ background: on ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)" }}>
      <span className="inline-block h-4 w-4 transform rounded-full bg-white transition-transform" style={{ transform: on ? "translateX(22px)" : "translateX(3px)" }} />
    </button>
  );
}

function AdvancedPanel({ sp, patch, otherFormats }: { sp: StoredProfile; patch: (p: Partial<StoredProfile>) => void; otherFormats: FormatInfo[] }) {
  const [scores, setScores] = useState(false);
  const [cfName, setCfName] = useState("");
  const [cfType, setCfType] = useState("release_group");
  const [cfValue, setCfValue] = useState("");
  const [cfScore, setCfScore] = useState(50);

  const setScore = (name: string, score: number) => {
    const next = { ...sp.format_scores };
    if (score === 0) delete next[name];
    else next[name] = score;
    patch({ format_scores: next });
  };
  const setFormat = (name: string, score: number, required: boolean) => {
    const next = { ...sp.format_scores };
    if (score === 0) delete next[name];
    else next[name] = score;
    const cur = sp.required_formats ?? [];
    patch({ format_scores: next, required_formats: required ? [...cur.filter((n) => n !== name), name] : cur.filter((n) => n !== name) });
  };
  const addCustom = () => {
    if (!cfName.trim() || !cfValue.trim()) return;
    const cf = { name: cfName.trim(), conditions: [{ type: cfType, value: cfValue.trim() }] };
    patch({ custom_formats: [...(sp.custom_formats ?? []), cf], format_scores: { ...sp.format_scores, [cf.name]: cfScore } });
    setCfName("");
    setCfValue("");
  };
  const removeCustom = (name: string) => {
    const next = { ...sp.format_scores };
    delete next[name];
    patch({ custom_formats: (sp.custom_formats ?? []).filter((c) => c.name !== name), format_scores: next });
  };

  return (
    <div className="flex flex-col gap-5">
      <div>
        <div className="mb-1 flex items-center justify-between gap-2">
          <span className="text-[12px] font-semibold">Other formats</span>
          <label className="flex items-center gap-1.5 text-[10.5px] text-ink-faint">
            <input type="checkbox" checked={scores} onChange={(e) => setScores(e.target.checked)} /> show raw scores
          </label>
        </div>
        <div className="mb-2 text-[10.5px] text-ink-faint">Formats the target doesn't cover, scored directly.</div>
        <div className="flex flex-col gap-2">
          {otherFormats.map((f) => (
            <FormatToggle key={f.name} format={f} score={sp.format_scores[f.name] ?? 0} required={(sp.required_formats ?? []).includes(f.name)} advanced={scores} onChange={(s, req) => setFormat(f.name, s, req)} />
          ))}
        </div>
      </div>

      <div>
        <div className="text-[12px] font-semibold">Custom formats</div>
        <p className="mb-2 text-[10.5px] text-ink-faint">Match anything the parser reads — a favourite release group, an edition, a source — and score it.</p>
        {(sp.custom_formats ?? []).map((c) => (
          <div key={c.name} className="mb-2 flex items-center gap-2 rounded-lg p-2 text-[11.5px]" style={{ background: "var(--panel-2)" }}>
            <span className="font-semibold">{c.name}</span>
            <span className="font-mono text-[10.5px] text-ink-faint">{c.conditions[0]?.type} = {c.conditions[0]?.value}</span>
            <input type="number" aria-label={`${c.name} score`} value={sp.format_scores[c.name] ?? 0} onChange={(e) => setScore(c.name, Number(e.target.value))}
              className="ml-auto w-[64px] rounded-lg px-2 py-0.5 text-right font-mono text-[11.5px]" style={{ ...fieldStyle, color: (sp.format_scores[c.name] ?? 0) >= 0 ? "var(--good)" : "var(--reject)" }} />
            <button onClick={() => removeCustom(c.name)} aria-label={`Remove ${c.name}`} className="text-ink-faint hover:text-[var(--reject)]">✕</button>
          </div>
        ))}
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <input value={cfName} onChange={(e) => setCfName(e.target.value)} placeholder="Format name" className="rounded-lg px-2.5 py-1.5 text-[12px]" style={fieldStyle} />
          <select value={cfType} onChange={(e) => setCfType(e.target.value)} className="rounded-lg px-2 py-1.5 text-[12px]" style={fieldStyle}>
            {CONDITION_TYPES.map((t) => <option key={t.v} value={t.v}>{t.l}</option>)}
          </select>
          <input value={cfValue} onChange={(e) => setCfValue(e.target.value)} placeholder="FraMeSToR" className="rounded-lg px-2.5 py-1.5 text-[12px]" style={fieldStyle} />
          <div className="flex items-center gap-2">
            <input type="number" aria-label="Score" value={cfScore} onChange={(e) => setCfScore(Number(e.target.value))} className="w-full rounded-lg px-2 py-1.5 text-right font-mono text-[12px]" style={fieldStyle} />
            <button onClick={addCustom} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>Add</button>
          </div>
        </div>
      </div>

      <div>
        <div className="text-[12px] font-semibold">Name contains</div>
        <p className="mb-2 text-[10.5px] text-ink-faint">Points for any release whose name contains a word — IMAX, Criterion, PROPER. Negative to push one down.</p>
        <KeywordEditor keywords={sp.keywords ?? []} onChange={(kw) => patch({ keywords: kw })} />
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label className="block rounded-xl p-3" style={panelStyle}>
          <span className="text-[12px] font-semibold">Prefer smaller files</span>
          <span className="mb-2 block text-[10.5px] text-ink-faint">Off: the biggest of equals wins. Strongly: the smallest watchable.</span>
          <select value={SIZE_LEANS.some((s) => s.v === sp.small_bias) ? sp.small_bias : 0.15} onChange={(e) => patch({ small_bias: Number(e.target.value) })} className="w-full rounded-lg px-2.5 py-1.5 text-[12.5px]" style={fieldStyle}>
            {SIZE_LEANS.map((s) => <option key={s.v} value={s.v}>{s.l}</option>)}
          </select>
        </label>
        <NumberField label="Minimum total score" hint="Refuse releases scoring below this" value={sp.min_format_score} onChange={(v) => patch({ min_format_score: v })} allowNegative />
      </div>
    </div>
  );
}

// LibraryFitPanel judges the library against the target as edited: the titles using this
// profile, or — for a new profile — every title, as if they all used it.
function LibraryFitPanel({ sp }: { sp: StoredProfile }) {
  const [res, setRes] = useState<{ scope: "profile" | "library"; counts: FitCounts } | null>(null);
  const navigate = useNavigate();
  const key = JSON.stringify({ i: sp.ideal, r: sp.allowed_resolutions, id: sp.id, m: sp.media_type });
  useEffect(() => {
    let alive = true;
    const t = window.setTimeout(() => { api.libraryFitPreview(sp).then((r) => alive && setRes(r)).catch(() => {}); }, 400);
    return () => { alive = false; window.clearTimeout(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  const series = sp.media_type === "series";
  const misfits = res ? res.counts.over + res.counts.under + res.counts.mismatch : 0;
  const show = () => {
    // Open the library table filtered to what doesn't fit (the tables remember their view).
    try {
      if (series) localStorage.setItem("series.view", "table");
      else { localStorage.setItem("movies.view", "table"); localStorage.setItem("movies.table.misfits", "misfits"); }
    } catch { /* storage blocked */ }
    navigate(series ? "/series" : "/movies");
  };
  return (
    <div className="rounded-xl p-4" style={panelStyle}>
      <div className="mb-0.5 text-[13px] font-semibold">In your library</div>
      <div className="mb-2.5 text-[11px] text-ink-faint">
        {!res ? "Checking…"
          : res.scope === "library"
            ? `Every ${series ? "show" : "film"} judged as if it used this profile (${res.counts.titles} ${series ? "shows" : "films"})`
            : `${res.counts.titles} ${series ? (res.counts.titles === 1 ? "show uses" : "shows use") : res.counts.titles === 1 ? "film uses" : "films use"} this profile — as edited`}
      </div>
      {res && <FitBar counts={res.counts} />}
      {res && misfits > 0 && (
        <button onClick={show} className="mt-2.5 text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>
          {series ? "Open the shows table →" : `Show the films that don't fit →`}
        </button>
      )}
    </div>
  );
}

// TestPanel runs the profile, as edited, against a real title's indexer results.
function TestPanel({ sp }: { sp: StoredProfile }) {
  const series = sp.media_type === "series";
  const [movies, setMovies] = useState<Movie[] | null>(null);
  const [shows, setShows] = useState<Series[] | null>(null);
  const [q, setQ] = useState("");
  const [pick, setPick] = useState<{ id: number; title: string } | null>(null);
  const [season, setSeason] = useState(1);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ReleaseList | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showAll, setShowAll] = useState(false);

  const loadTitles = () => {
    if (series && shows === null) api.series().then((r) => setShows(r.series ?? [])).catch(() => setShows([]));
    if (!series && movies === null) api.movies().then((r) => setMovies(r.movies ?? [])).catch(() => setMovies([]));
  };
  const matches = useMemo(() => {
    const n = q.trim().toLowerCase();
    if (!n || pick) return [];
    const list: { id: number; title: string; year?: number }[] = series ? (shows ?? []) : (movies ?? []);
    return list.filter((t) => t.title.toLowerCase().includes(n)).slice(0, 6);
  }, [q, pick, series, movies, shows]);

  const run = async () => {
    if (!pick) return;
    setBusy(true);
    setErr(null);
    setResult(null);
    setShowAll(false);
    try {
      setResult(await api.qualityTest(series ? { profile: sp, series_id: pick.id, season } : { profile: sp, movie_id: pick.id }));
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const releases = result?.releases ?? [];
  const winner = releases.find((r) => r.recommended);
  const eligible = releases.filter((r) => r.eligible && !r.recommended);
  const skipped = releases.filter((r) => !r.eligible);

  return (
    <div className="rounded-xl p-4" style={panelStyle}>
      <div className="mb-0.5 text-[13px] font-semibold">Test on a real title</div>
      <div className="mb-2.5 text-[11px] text-ink-faint">Searches your indexers and shows what this profile, as edited, would pick.</div>
      <div className="relative">
        <div className="flex gap-2">
          <input value={pick ? pick.title : q} onFocus={loadTitles} onChange={(e) => { setPick(null); setQ(e.target.value); setResult(null); }}
            placeholder={series ? "A show in your library" : "A film in your library"} aria-label="Title to test" className="min-w-0 flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={fieldStyle} />
          {series && (
            <input type="number" min={1} value={season} onChange={(e) => setSeason(Math.max(1, Number(e.target.value) || 1))} aria-label="Season"
              className="w-[64px] rounded-lg px-2 py-1.5 text-right font-mono text-[12.5px]" style={fieldStyle} title="Season" />
          )}
          <button onClick={run} disabled={!pick || busy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
            {busy ? "Searching…" : "Search"}
          </button>
        </div>
        {matches.length > 0 && (
          <div className="absolute left-0 right-0 z-10 mt-1 overflow-hidden rounded-lg" style={{ ...panelStyle, boxShadow: "var(--shadow)" }}>
            {matches.map((t) => (
              <button key={t.id} onClick={() => { setPick({ id: t.id, title: t.title }); setQ(""); }} className="block w-full px-3 py-1.5 text-left text-[12px] hover:bg-[var(--panel-2)]">
                {t.title} <span className="text-ink-faint">{t.year || ""}</span>
              </button>
            ))}
          </div>
        )}
      </div>
      {err && <div className="mt-2.5 text-[11.5px]" style={{ color: "var(--reject)" }}>{err}</div>}
      {result && (
        <div className="mt-3">
          {winner ? (
            <div className="rounded-lg p-3" style={{ background: "var(--accent-soft)", border: "1px solid var(--accent-line)" }}>
              <div className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-accent">Would grab</div>
              <div className="mt-1 text-[13px] font-semibold">{winner.summary}</div>
              <div className="mt-0.5 font-mono text-[10.5px] text-ink-dim">
                {winner.size_gb.toFixed(1)} GB{winner.bitrate_mbps ? ` · ${winner.bitrate_mbps.toFixed(1)} Mb/s` : ""} · ▲ {winner.seeders}
              </div>
              <div className="mt-1 truncate font-mono text-[10px] text-ink-faint" title={winner.title}>{winner.title}</div>
            </div>
          ) : (
            <div className="rounded-lg p-3 text-[12px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
              {releases.length === 0 ? "Your indexers found no releases for this." : "Nothing found fits this profile."}
            </div>
          )}
          {(result.why ?? []).length > 0 && winner && (
            <ul className="mt-2 flex flex-col gap-0.5 text-[11.5px] text-ink-dim">
              {(result.why ?? []).map((w) => <li key={w}>✓ {w}</li>)}
            </ul>
          )}
          {releases.length > 0 && (
            <button onClick={() => setShowAll((s) => !s)} className="mt-2 text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>
              {showAll ? "Hide the others" : `${eligible.length} other eligible · ${skipped.length} skipped`}
            </button>
          )}
          {showAll && (
            <div className="mt-2 max-h-[280px] overflow-y-auto rounded-lg thin-scroll" style={{ border: "1px solid var(--line)" }}>
              {[...eligible, ...skipped].map((r) => (
                <div key={r.title} className="flex items-center gap-2 px-3 py-1.5 text-[11.5px]" style={{ borderBottom: "1px solid var(--line-soft)", opacity: r.eligible ? 1 : 0.6 }}>
                  <span className="min-w-0 flex-1 truncate" title={r.title}>{r.summary}</span>
                  <span className="font-mono text-[10.5px] text-ink-faint">{r.size_gb.toFixed(1)} GB</span>
                  {!r.eligible && <span className="max-w-[45%] truncate text-right text-[10.5px]" style={{ color: "var(--reject)" }} title={r.reject_reason}>{r.reject_reason}</span>}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// SaveBar stays at the bottom of the page while editing.
function SaveBar({ dirty, saving, error, onSave, onCancel, mobileNote }: { dirty: boolean; saving: boolean; error: string | null; onSave: () => void; onCancel: () => void; mobileNote?: string }) {
  return (
    <div className="sticky bottom-0 z-20 mt-2" style={{ background: "var(--bg)", borderTop: "1px solid var(--line)" }}>
      <div className="mx-auto flex w-full max-w-[1240px] flex-wrap items-center gap-3 px-4 py-3 sm:px-6">
        <div className="min-w-0 flex-1 text-[12px]">
          {error ? <span style={{ color: "var(--reject)" }}>{error}</span>
            : dirty ? <span className="text-ink-dim">Unsaved changes</span>
              : <span className="text-ink-faint">No changes</span>}
          {mobileNote && <span className="ml-2 font-mono text-[10.5px] text-ink-faint lg:hidden">· {mobileNote}</span>}
        </div>
        <button onClick={onCancel} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
        <button onClick={onSave} disabled={saving} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={primaryStyle}>{saving ? "Saving…" : "Save profile"}</button>
      </div>
    </div>
  );
}

// A profile's stall timeout: 0 follows the global default (Settings → Downloads), -1 is
// off, anything else is minutes. 0 used to mean off, which left dead torrents at 0% forever.
function stallLabel(minutes: number): string {
  if (minutes < 0) return "off";
  if (minutes === 0) return "default";
  const h = minutes / 60;
  return Number.isInteger(h) ? `${h}h` : `${Math.round(h * 10) / 10}h`;
}

function StallField({ value, onChange }: { value: number; onChange: (v: number) => void }) {
  // The global default is only shown, never edited, here — it lives in Settings. If it
  // can't be read (a manager without access to Settings) the shipped default is shown.
  const [globalMin, setGlobalMin] = useState<number | null>(null);
  useEffect(() => {
    api.settings().then((s) => setGlobalMin(s.downloads_stall_minutes ?? 360)).catch(() => setGlobalMin(360));
  }, []);
  const mode = value < 0 ? "off" : value === 0 ? "default" : "custom";
  const def = globalMin == null ? "6h" : globalMin === 0 ? "never" : stallLabel(globalMin);
  return (
    <label className="block rounded-xl p-3" style={panelStyle}>
      <span className="block text-[12px] font-semibold">Stall timeout</span>
      <span className="mb-2 block text-[10.5px] text-ink-faint">Try another release after this long without progress</span>
      <div className="flex gap-2">
        <select
          value={mode}
          onChange={(e) => onChange(e.target.value === "off" ? -1 : e.target.value === "default" ? 0 : value > 0 ? value : 360)}
          className="flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]"
          style={fieldStyle}
        >
          <option value="default">Use default ({def})</option>
          <option value="off">Off</option>
          <option value="custom">Custom</option>
        </select>
        {mode === "custom" && (
          <input
            type="number"
            min={0.5}
            max={168}
            step={0.5}
            aria-label="Hours"
            value={Math.round((value / 60) * 10) / 10}
            onChange={(e) => onChange(Math.min(10080, Math.max(30, Math.round((Number(e.target.value) || 0) * 60))))}
            className="w-20 rounded-lg px-2.5 py-1.5 text-[13px]"
            style={fieldStyle}
          />
        )}
        {mode === "custom" && <span className="self-center text-[11px] text-ink-faint">h</span>}
      </div>
    </label>
  );
}

function NumberField({ label, hint, value, onChange, allowNegative }: { label: string; hint: string; value: number; onChange: (v: number) => void; allowNegative?: boolean }) {
  return (
    <label className="block rounded-xl p-3" style={panelStyle}>
      <span className="block text-[12px] font-semibold">{label}</span>
      <span className="mb-2 block text-[10.5px] text-ink-faint">{hint}</span>
      <input type="number" min={allowNegative ? undefined : 0} value={value} onChange={(e) => onChange(allowNegative ? Number(e.target.value) : Math.max(0, Number(e.target.value)))} className="w-full rounded-lg px-2.5 py-1.5 text-[13px]" style={fieldStyle} />
    </label>
  );
}

// =====================================================================================
// Music builder
// =====================================================================================

// A music profile is a ladder: each tier on it can be grabbed and the highest available wins.
// There is no music upgrade sweep yet, so the profile only decides the first grab. Scores follow the ladder's rank, the same spacing
// the presets use, and anything off the ladder scores nothing — which the engine refuses.
function MusicBuilder({ initial, ladder, presets, onCancel, onSaved }: { initial: StoredProfile; ladder: string[]; presets: MusicPreset[]; onCancel: () => void; onSaved: () => void }) {
  const start = useMemo(() => ({ ...initial, format_scores: initial.format_scores ?? {}, rejected: initial.rejected ?? [] }), [initial]);
  const [sp, setSp] = useState<StoredProfile>(start);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const startJSON = useRef(JSON.stringify(start));
  const dirty = JSON.stringify(sp) !== startJSON.current;
  useUnsaved(dirty);
  const patch = (p: Partial<StoredProfile>) => setSp((s) => ({ ...s, ...p }));

  // Each tier's score: the full-ladder preset's value, else spaced by rank.
  const canonical = useMemo(() => {
    const full = presets.reduce<Record<string, number>>((best, p) => (Object.keys(p.format_scores).length > Object.keys(best).length ? p.format_scores : best), {});
    const out: Record<string, number> = {};
    ladder.forEach((t, i) => { out[t] = full[t] ?? (ladder.length - i) * 10; });
    return out;
  }, [ladder, presets]);

  const on = (tier: string) => (sp.format_scores[tier] ?? 0) > 0;
  const toggle = (tier: string) => {
    const next = { ...sp.format_scores };
    if (on(tier)) delete next[tier];
    else next[tier] = canonical[tier];
    patch({ format_scores: next, min_format_score: 1 });
  };
  const applyPreset = (p: MusicPreset) => patch({ format_scores: { ...p.format_scores }, min_format_score: p.min_format_score, upgrades_enabled: p.upgrades_enabled, name: sp.name || p.name });
  const top = ladder.find((t) => on(t));
  const kept = ladder.filter((t) => on(t)).length;

  const leave = () => {
    if (dirty && !window.confirm("Discard your changes to this profile?")) return;
    onCancel();
  };
  const save = async () => {
    if (!sp.name.trim()) { setError("Give the profile a name."); return; }
    if (kept === 0) { setError("Keep at least one quality tier on the ladder."); return; }
    setSaving(true);
    setError(null);
    try {
      if (sp.id > 0) await api.updateQualityProfile(sp.id, sp);
      else await api.createQualityProfile(sp);
      onSaved();
    } catch (e) { setError((e as Error).message); setSaving(false); }
  };

  return (
    <>
      <PageHeader title={sp.id > 0 ? "Edit music profile" : "New music profile"} tail="Music" />
      <div className="mx-auto w-full max-w-[720px] px-4 pb-4 pt-6 sm:px-6">
        <div className="mb-4"><button onClick={leave} className="text-[12px] text-ink-dim hover:text-[var(--ink)]">← Back</button></div>
        <label htmlFor="mp-name" className="mb-1.5 block font-mono text-[10px] font-bold uppercase tracking-wide text-accent">Name</label>
        <input id="mp-name" value={sp.name} onChange={(e) => patch({ name: e.target.value })} placeholder="Lossless, or the best MP3" className="w-full rounded-lg px-3 py-2 text-[13px]" style={fieldStyle} />

        {presets.length > 0 && (
          <>
            <SectionLabel>Start from</SectionLabel>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {presets.map((p) => (
                <button key={p.name} onClick={() => applyPreset(p)} className="rounded-xl p-3 text-left transition-colors hover:border-[var(--accent)]" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
                  <div className="text-[12.5px] font-semibold">{p.name}</div>
                  <div className="mt-0.5 text-[11px] leading-[1.45] text-ink-faint">{p.description}</div>
                </button>
              ))}
            </div>
          </>
        )}

        <SectionLabel>Quality ladder</SectionLabel>
        <p className="-mt-1 mb-2 text-[10.5px] text-ink-faint">
          Best first. Every tier you keep can be grabbed and the highest available wins. Tiers you leave off are never grabbed.
        </p>
        <div className="overflow-hidden rounded-xl" style={panelStyle}>
          {ladder.map((tier, i) => {
            const active = on(tier);
            const lossless = ["FLAC-24", "FLAC", "ALAC", "WAV"].includes(tier);
            return (
              <button key={tier} onClick={() => toggle(tier)} role="checkbox" aria-checked={active} className="flex w-full items-center gap-3 px-3.5 py-2 text-left"
                style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)", background: active ? "var(--accent-soft)" : "transparent" }}>
                <span className="grid h-4 w-4 flex-none place-items-center rounded" style={{ background: active ? "var(--accent)" : "transparent", border: `1px solid ${active ? "var(--accent)" : "var(--line)"}` }}>
                  {active && <svg width="10" height="10" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M4 12l5 5L20 6" stroke="var(--accent-ink)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>}
                </span>
                <span className="w-[80px] font-mono text-[12.5px] font-semibold" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>{tier}</span>
                <span className="text-[10.5px] text-ink-faint">{lossless ? "lossless" : "lossy"}</span>
                {active && tier === top && <span className="ml-auto rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Top</span>}
              </button>
            );
          })}
        </div>

        <SectionLabel>Upgrades</SectionLabel>
        {/* No music upgrade sweep exists, so a switch here would promise something nothing does.
            upgrades_enabled is left as saved, so the switch can come back with the sweep. */}
        <div className="rounded-xl p-4 text-[11.5px] text-ink-faint" style={panelStyle}>
          Automatic album upgrades aren't built yet. An album is grabbed once, at the best tier available on your ladder.
        </div>

        <SectionLabel>Reject</SectionLabel>
        <RejectEditor rejected={sp.rejected ?? []} onChange={(r) => patch({ rejected: r })} />
      </div>
      <SaveBar dirty={dirty} saving={saving} error={error} onSave={save} onCancel={leave} />
    </>
  );
}

// =====================================================================================
// Book builder
// =====================================================================================

// BookBuilder edits a book quality profile: which formats to grab (per edition) and keyword
// boosts (e.g. GraphicAudio +100), plus hard-reject terms. No resolution/bitrate.
function BookBuilder({ initial, onCancel, onSaved }: { initial: StoredProfile; onCancel: () => void; onSaved: () => void }) {
  const start = useMemo(() => ({ ...initial, format_scores: initial.format_scores ?? {}, keywords: initial.keywords ?? [], rejected: initial.rejected ?? [] }), [initial]);
  const [sp, setSp] = useState<StoredProfile>(start);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const startJSON = useRef(JSON.stringify(start));
  const dirty = JSON.stringify(sp) !== startJSON.current;
  useUnsaved(dirty);
  const patch = (p: Partial<StoredProfile>) => setSp((s) => ({ ...s, ...p }));
  const setFormatScore = (name: string, score: number) => {
    const next = { ...sp.format_scores };
    if (score <= 0) delete next[name];
    else next[name] = score;
    patch({ format_scores: next });
  };
  const leave = () => {
    if (dirty && !window.confirm("Discard your changes to this profile?")) return;
    onCancel();
  };
  const save = async () => {
    if (!sp.name.trim()) { setError("Give the profile a name."); return; }
    setSaving(true);
    setError(null);
    try {
      if (sp.id > 0) await api.updateQualityProfile(sp.id, sp);
      else await api.createQualityProfile(sp);
      onSaved();
    } catch (e) { setError((e as Error).message); setSaving(false); }
  };

  return (
    <>
      <PageHeader title={sp.id > 0 ? "Edit book profile" : "New book profile"} tail="Books" />
      <div className="mx-auto w-full max-w-[720px] px-4 pb-4 pt-6 sm:px-6">
        <div className="mb-4"><button onClick={leave} className="text-[12px] text-ink-dim hover:text-[var(--ink)]">← Back</button></div>
        <label htmlFor="bp-name" className="mb-1.5 block font-mono text-[10px] font-bold uppercase tracking-wide text-accent">Name</label>
        <input id="bp-name" value={sp.name} onChange={(e) => patch({ name: e.target.value })} placeholder="Audiobooks — GraphicAudio first" className="mb-1 w-full rounded-lg px-3 py-2 text-[13px]" style={fieldStyle} />

        {BOOK_FORMATS.map((grp) => (
          <div key={grp.group} className="mb-2">
            <SectionLabel>{grp.group} formats</SectionLabel>
            <p className="-mt-1 mb-2 text-[10.5px] text-ink-faint">Score a format above 0 to grab it (higher = preferred); 0 skips it. The {grp.group.toLowerCase()} edition is fetched only if at least one of its formats is scored.</p>
            <div className="flex flex-col gap-1.5">
              {grp.formats.map((f) => {
                const v = sp.format_scores?.[f] ?? 0;
                return (
                  <div key={f} className="flex items-center gap-3 rounded-lg px-2.5 py-1.5" style={panelStyle}>
                    <span className="w-[56px] font-mono text-[12px] font-semibold">{f}</span>
                    <input type="range" min={0} max={100} step={5} value={v} aria-label={`${f} score`} onChange={(e) => setFormatScore(f, Number(e.target.value))} className="flex-1 accent-[var(--accent)]" />
                    <input type="number" min={0} value={v} aria-label={`${f} score`} onChange={(e) => setFormatScore(f, Math.max(0, Number(e.target.value)))} className="w-[60px] rounded-lg px-2 py-1 text-right font-mono text-[12px]" style={fieldStyle} />
                  </div>
                );
              })}
            </div>
          </div>
        ))}

        <SectionLabel>Name contains</SectionLabel>
        <p className="-mt-1 mb-2 text-[10.5px] text-ink-faint">Add points to any release whose name contains a term — GraphicAudio +100, Dramatized +80, Unabridged +20. Combined with the format score; highest total wins.</p>
        <KeywordEditor keywords={sp.keywords ?? []} onChange={(kw) => patch({ keywords: kw })} />

        <SectionLabel>Reject</SectionLabel>
        <RejectEditor rejected={sp.rejected ?? []} onChange={(r) => patch({ rejected: r })} />
      </div>
      <SaveBar dirty={dirty} saving={saving} error={error} onSave={save} onCancel={leave} />
    </>
  );
}

// =====================================================================================
// Shared editors
// =====================================================================================

function KeywordEditor({ keywords, onChange }: { keywords: { term: string; score: number }[]; onChange: (kw: { term: string; score: number }[]) => void }) {
  const [term, setTerm] = useState("");
  const [score, setScore] = useState(25);
  const add = () => {
    if (!term.trim()) return;
    onChange([...keywords, { term: term.trim(), score }]);
    setTerm("");
  };
  return (
    <div>
      {keywords.length > 0 && (
        <div className="mb-2 flex flex-col gap-1.5">
          {keywords.map((k, i) => (
            <div key={i} className="flex items-center gap-2 rounded-lg px-2.5 py-1.5 text-[12px]" style={panelStyle}>
              <span className="font-semibold">{k.term}</span>
              <span className="ml-auto font-mono" style={{ color: k.score >= 0 ? "var(--good)" : "var(--reject)" }}>{k.score > 0 ? `+${k.score}` : k.score}</span>
              <button onClick={() => onChange(keywords.filter((_, j) => j !== i))} aria-label={`Remove ${k.term}`} className="text-ink-faint hover:text-[var(--reject)]">✕</button>
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-2">
        <input value={term} onChange={(e) => setTerm(e.target.value)} onKeyDown={(e) => e.key === "Enter" && add()} placeholder="IMAX" aria-label="Word" className="min-w-0 flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={fieldStyle} />
        <input type="number" value={score} aria-label="Points" onChange={(e) => setScore(Number(e.target.value))} className="w-[70px] rounded-lg px-2 py-1.5 text-right font-mono text-[12px]" style={fieldStyle} />
        <button onClick={add} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>Add</button>
      </div>
    </div>
  );
}

// PreReleaseRule is the switch for cams, telesyncs, telecines, screeners and workprints —
// refused unless a profile opts in. It's a rule over the parsed source, so it catches every
// spelling (HDCAM, CAMRip, HDTS, TELECINE, DVDSCR…), not just one word.
function PreReleaseRule({ allowed, onChange }: { allowed: boolean; onChange: (allowed: boolean) => void }) {
  const on = !allowed;
  return (
    <button onClick={() => onChange(on)} role="checkbox" aria-checked={on} className="mb-2 flex w-full items-center gap-2.5 rounded-lg p-2.5 text-left"
      style={{ border: `1px solid ${on ? "var(--reject)" : "var(--line)"}`, background: on ? "var(--reject-soft)" : "var(--panel)" }}>
      <span className="grid h-4 w-4 flex-none place-items-center rounded" style={{ background: on ? "var(--reject)" : "transparent", border: `1px solid ${on ? "var(--reject)" : "var(--line)"}` }}>
        {on && <svg width="10" height="10" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M4 12l5 5L20 6" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>}
      </span>
      <span className="min-w-0 flex-1">
        <span className="text-[12.5px] font-semibold" style={{ color: on ? "var(--reject)" : "var(--ink)" }}>Reject cams, telesyncs and screeners</span>
        <span className="block text-[10.5px] text-ink-faint">CAM, HDCAM, TS, HDTS, telecine, DVDSCR, workprint, R5 — however they're spelled. On by default.</span>
      </span>
    </button>
  );
}

function RejectEditor({ rejected, onChange }: { rejected: string[]; onChange: (r: string[]) => void }) {
  const [term, setTerm] = useState("");
  const has = (t: string) => rejected.some((r) => r.toLowerCase() === t.toLowerCase());
  const toggle = (t: string) => (has(t) ? onChange(rejected.filter((r) => r.toLowerCase() !== t.toLowerCase())) : onChange([...rejected, t]));
  const addCustom = () => {
    if (!term.trim() || has(term.trim())) { setTerm(""); return; }
    onChange([...rejected, term.trim()]);
    setTerm("");
  };
  const hidden = [...REJECT_TYPES, ...EXECUTABLE_TYPES].map((t) => t.toLowerCase());
  const custom = rejected.filter((r) => !hidden.includes(r.toLowerCase()));
  const execOn = EXECUTABLE_TYPES.every((t) => has(t));
  const toggleExec = () =>
    execOn
      ? onChange(rejected.filter((r) => !EXECUTABLE_TYPES.some((t) => t.toLowerCase() === r.toLowerCase())))
      : onChange([...rejected.filter((r) => !EXECUTABLE_TYPES.some((t) => t.toLowerCase() === r.toLowerCase())), ...EXECUTABLE_TYPES]);
  return (
    <div>
      <button onClick={toggleExec} role="checkbox" aria-checked={execOn} className="mb-2 flex w-full items-center gap-2.5 rounded-lg p-2.5 text-left" style={{ border: `1px solid ${execOn ? "var(--reject)" : "var(--line)"}`, background: execOn ? "var(--reject-soft)" : "var(--panel)" }}>
        <span className="grid h-4 w-4 flex-none place-items-center rounded" style={{ background: execOn ? "var(--reject)" : "transparent", border: `1px solid ${execOn ? "var(--reject)" : "var(--line)"}` }}>
          {execOn && <svg width="10" height="10" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M4 12l5 5L20 6" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>}
        </span>
        <span className="min-w-0 flex-1">
          <span className="text-[12.5px] font-semibold" style={{ color: execOn ? "var(--reject)" : "var(--ink)" }}>Reject executables and scripts</span>
          <span className="block text-[10.5px] text-ink-faint">.exe .bat .cmd .scr .msi … — malware safety, on by default</span>
        </span>
      </button>
      <div className="mb-2 flex flex-wrap gap-1.5">
        {REJECT_TYPES.map((t) => (
          <button key={t} onClick={() => toggle(t)} aria-pressed={has(t)} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: `1px solid ${has(t) ? "var(--reject)" : "var(--line)"}`, background: has(t) ? "var(--reject-soft)" : "var(--panel)", color: has(t) ? "var(--reject)" : "var(--ink-dim)" }}>{t}</button>
        ))}
      </div>
      {custom.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {custom.map((r) => (
            <span key={r} className="flex items-center gap-1.5 rounded-lg px-2 py-1 text-[11.5px]" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>{r}<button onClick={() => toggle(r)} aria-label={`Stop rejecting ${r}`}>✕</button></span>
          ))}
        </div>
      )}
      <div className="flex items-center gap-2">
        <input value={term} onChange={(e) => setTerm(e.target.value)} onKeyDown={(e) => e.key === "Enter" && addCustom()} placeholder="Telesync" aria-label="Reject any release containing" className="min-w-0 flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={fieldStyle} />
        <button onClick={addCustom} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Reject</button>
      </div>
      <p className="mt-1.5 text-[10.5px] text-ink-faint">Any release whose name contains one of these is skipped entirely.</p>
    </div>
  );
}

function FormatToggle({ format, score, required, advanced, onChange }: { format: FormatInfo; score: number; required: boolean; advanced: boolean; onChange: (s: number, required: boolean) => void }) {
  const state = required ? "require" : score > 0 ? "prefer" : score < 0 ? "avoid" : "ignore";
  const opts: { key: string; label: string; val: number; req: boolean; tone: string }[] = [
    { key: "avoid", label: "Avoid", val: -50, req: false, tone: "var(--reject)" },
    { key: "ignore", label: "—", val: 0, req: false, tone: "var(--ink-faint)" },
    { key: "prefer", label: "Prefer", val: 50, req: false, tone: "var(--good)" },
    { key: "require", label: "Must", val: 50, req: true, tone: "var(--accent)" },
  ];
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg p-2.5" style={{ background: "var(--panel-2)" }}>
      <div className="min-w-0 flex-1">
        <div className="text-[12.5px] font-semibold">{format.name}</div>
        <div className="truncate text-[11px] text-ink-faint" title={format.description}>{format.description}</div>
      </div>
      {advanced ? (
        <div className="flex items-center gap-2">
          <label className="flex items-center gap-1 text-[10.5px] text-ink-faint" title="Reject any release without this format">
            <input type="checkbox" checked={required} onChange={(e) => onChange(score, e.target.checked)} /> must
          </label>
          <input type="number" value={score} aria-label={`${format.name} score`} onChange={(e) => onChange(Number(e.target.value), required)} className="w-[72px] rounded-lg px-2 py-1 text-right font-mono text-[12px]" style={fieldStyle} />
        </div>
      ) : (
        <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
          {opts.map((o) => (
            <button key={o.key} onClick={() => onChange(o.val, o.req)} aria-pressed={state === o.key} className="rounded-md px-2.5 py-1 text-[11px] font-semibold" style={{ background: state === o.key ? o.tone : "transparent", color: state === o.key ? "#fff" : "var(--ink-faint)" }}>{o.label}</button>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Preview
// ---------------------------------------------------------------------------

function Hero({ winner, why, chosenOver, open, onToggle, eligible, rejected, total }: { winner: Evaluation; why: string[]; chosenOver?: string; open: boolean; onToggle: () => void; eligible: Evaluation[]; rejected: Evaluation[]; total: number }) {
  const r = winner.candidate.release;
  return (
    <>
      <div className="rounded-2xl p-5" style={{ background: "linear-gradient(180deg, var(--accent-soft), var(--panel) 62%)", border: "1px solid var(--accent)", boxShadow: "var(--shadow)" }}>
        <div className="mb-2.5 font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-accent">★ Arrmada would grab this</div>
        <div className="mb-3 text-[22px] font-bold tracking-tight">{r.resolution} {r.source}</div>
        <div className="mb-4 flex flex-wrap gap-1.5">
          {(r.hdr ?? []).map((h) => <Chip key={h} accent>{h}</Chip>)}
          {(r.audio ?? []).map((a) => <Chip key={a} accent>{a}</Chip>)}
          {r.codec && <Chip>{r.codec}</Chip>}
          <Chip>{winner.candidate.size_gb.toFixed(1)} GB</Chip>
          <Chip>▲ {winner.candidate.seeders}</Chip>
        </div>
        <div className="rounded-xl p-3.5" style={{ background: "color-mix(in srgb, var(--bg) 34%, transparent)", border: "1px solid var(--line-soft)" }}>
          <div className="mb-2.5 font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Why this one</div>
          <div className="flex flex-col gap-2">
            {why.map((w) => (
              <div key={w} className="flex items-start gap-2 text-[13px]">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true" style={{ color: "var(--accent)", flex: "none", marginTop: 2 }}><path d="M4 12l5 5L20 6" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>
                <span>{w}</span>
              </div>
            ))}
          </div>
        </div>
        {chosenOver && <div className="mt-3.5 border-t border-dashed pt-3.5 text-[12.5px] text-ink-dim" style={{ borderColor: "var(--line)" }}>{chosenOver}</div>}
        <div className="mt-3 truncate font-mono text-[10.5px] text-ink-faint" title={winner.candidate.name}>{winner.candidate.name}</div>
      </div>
      <button onClick={onToggle} aria-expanded={open} className="mt-3 flex w-full items-center justify-center gap-2 rounded-xl p-2.5 text-[12.5px] font-semibold text-ink-dim transition-colors hover:text-ink" style={panelStyle}>
        {open ? "Hide" : `Compare all ${total}`} sample releases
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true" style={{ transform: open ? "rotate(180deg)" : "none", transition: "transform .2s" }}><path d="M6 9l6 6 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" fill="none" /></svg>
      </button>
      {open && (
        <div className="mt-2.5 overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--line-soft)" }}>
          {eligible.length > 0 && <CmpLabel>Also eligible</CmpLabel>}
          {eligible.map((e, i) => <CmpRow key={`e${i}`} ev={e} />)}
          {rejected.length > 0 && <CmpLabel>Skipped</CmpLabel>}
          {rejected.map((e, i) => <CmpRow key={`r${i}`} ev={e} skip />)}
        </div>
      )}
    </>
  );
}

function Chip({ children, accent }: { children: React.ReactNode; accent?: boolean }) {
  return <span className="rounded-md px-1.5 py-1 font-mono text-[10.5px]" style={{ background: accent ? "var(--accent-soft)" : "var(--panel-2)", border: `1px solid ${accent ? "var(--accent-line)" : "var(--line-soft)"}`, color: accent ? "var(--accent)" : "var(--ink-dim)" }}>{children}</span>;
}

function CmpLabel({ children }: { children: React.ReactNode }) {
  return <div className="px-3.5 pb-1 pt-2.5 font-mono text-[9px] font-bold uppercase tracking-[0.09em] text-ink-faint" style={{ background: "var(--panel)" }}>{children}</div>;
}

function CmpRow({ ev, skip }: { ev: Evaluation; skip?: boolean }) {
  const r = ev.candidate.release;
  const note = skip ? ev.reject_reason
    : ev.avoided_formats?.length ? `ranked down — ${ev.avoided_formats.join(", ")}`
      : ev.matched?.length ? ev.matched.join(", ") : "eligible";
  return (
    <div className="flex items-center gap-3 px-3.5 py-2.5" style={{ background: "var(--panel)", opacity: skip ? 0.6 : 1 }}>
      <span className="min-w-[108px] text-[12.5px] font-semibold">{r.resolution} {r.source}</span>
      <span className="min-w-[50px] font-mono text-[11.5px] text-ink-dim">{ev.candidate.size_gb.toFixed(1)} GB</span>
      <span className="ml-auto text-right text-[12px]" style={{ color: skip ? "var(--reject)" : "var(--ink-faint)" }}>{note}</span>
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-3 mt-6 flex items-center gap-2.5">
      <span className="font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-accent">{children}</span>
      <span className="h-px flex-1" style={{ background: "var(--line-soft)" }} />
    </div>
  );
}
