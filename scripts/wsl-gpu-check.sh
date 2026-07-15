#!/usr/bin/env bash
# Run INSIDE the Ubuntu-24.04 WSL2 distro (as any user):
#   wsl -d Ubuntu-24.04 -- bash /mnt/c/Projects/Arrmada/scripts/wsl-gpu-check.sh
# Verifies AMD VAAPI hardware encode is reachable before migrating the app onto WSL2 Docker.
set -u
echo "== kernel =="; uname -r
echo "== /dev/dxg (GPU paravirt) =="; ls -la /dev/dxg 2>&1
echo "== /dev/dri (render node — MUST exist for VAAPI) =="; ls -la /dev/dri 2>&1
echo "== dxg adapter-enumeration errors (should be empty) =="; dmesg 2>/dev/null | grep -iE 'dxgkio_query_adapter_info' | tail -3
if [ -e /dev/dri/renderD128 ]; then
  echo "== vainfo (looking for VAEntrypointEncSlice on HEVC/H264) =="
  LIBVA_DRIVER_NAME=d3d12 vainfo --display drm --device /dev/dri/renderD128 2>&1 | grep -iE 'VAProfile.*(HEVC|H264).*Enc|driver version' 
  echo "== live hevc_vaapi test encode =="
  ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=s=1280x720:d=2:r=24 \
    -vaapi_device /dev/dri/renderD128 -vf 'format=nv12,hwupload' -c:v hevc_vaapi -qp 24 -f null - \
    && echo "  >>> HARDWARE ENCODE WORKS <<<" || echo "  >>> hevc_vaapi FAILED <<<"
else
  echo ">>> /dev/dri/renderD128 absent — GPU-PV not enumerating the adapter; hardware encode is NOT available yet. <<<"
fi
