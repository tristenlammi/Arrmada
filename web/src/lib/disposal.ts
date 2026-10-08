import { useEffect, useState } from "react";
import { api, type RecycleMode } from "./api";

// Where deleted files go doesn't change while a page is open (it's an env setting plus two
// guard rails), so every dialog on the page shares one fetch.
let cached: Promise<RecycleMode> | null = null;

export function loadRecycleMode(): Promise<RecycleMode> {
  if (!cached) {
    cached = api.recycleMode().catch((e) => {
      cached = null; // a failed fetch shouldn't stick for the rest of the visit
      throw e;
    });
  }
  return cached;
}

// useRecycleMode returns the recycle mode, or null while it loads (or if it couldn't be read).
export function useRecycleMode(): RecycleMode | null {
  const [mode, setMode] = useState<RecycleMode | null>(null);
  useEffect(() => {
    let live = true;
    loadRecycleMode().then((m) => { if (live) setMode(m); }).catch(() => {});
    return () => { live = false; };
  }, []);
  return mode;
}

// fmtBytes is the same compact size the Settings page shows (MB / GB / TB).
export function fmtBytes(b: number): string {
  if (!b || b <= 0) return "0 MB";
  const tb = b / 1024 ** 4;
  if (tb >= 1) return `${tb.toFixed(2)} TB`;
  const gb = b / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${(b / 1024 ** 2).toFixed(0)} MB`;
}

// disposalLine says honestly what deleting `bytes` will do: move it to the bin (and for how
// long it can come back), or erase it because the bin is switched off. Dialogs use this
// instead of hard-coding "recycle bin", which was untrue whenever the bin was off.
export function disposalLine(bytes: number, mode: RecycleMode | null): string {
  const size = fmtBytes(bytes);
  if (!mode) return `Deletes ${size} — checking whether the recycle bin is on…`;
  if (!mode.enabled) return `Permanently deletes ${size} — the recycle bin is switched off`;
  const keep = mode.retention_days > 0
    ? `for ${mode.retention_days} day${mode.retention_days === 1 ? "" : "s"}`
    : "until you empty it";
  return `Moves ${size} to the recycle bin — restorable from Settings → System → Recycle bin ${keep}`;
}
