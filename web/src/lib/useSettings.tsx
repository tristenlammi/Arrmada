import { createContext, useContext, useEffect, useRef, useState } from "react";
import { api, type AppSettings } from "./api";
import { useMe } from "./me";

// One copy of /api/v1/settings shared by every Settings section, so a value edited in one
// section and saved from another's Save button behaves exactly as it did when Settings was
// a single page of tabs: Save sends every key that differs from what the server last said.

interface SettingsDraft {
  s: AppSettings | null; // what's on screen, edits included
  error: string | null;
  flash: { text: string; good: boolean } | null;
  patch: (p: Partial<AppSettings>) => void;
  save: () => Promise<void>;
  syncRegion: (region: string) => void;
}

const SettingsContext = createContext<SettingsDraft | null>(null);

export function SettingsProvider({ children }: { children: React.ReactNode }) {
  const { setBooksEnabled, setMusicEnabled } = useMe();
  const [s, setS] = useState<AppSettings | null>(null);
  // What the server last told us. Save sends only the keys that differ from it, so a
  // value saved elsewhere on the page (the Discovery region) or by another tab isn't
  // overwritten by a stale copy, and nothing read-only is ever echoed back.
  const [loaded, setLoaded] = useState<AppSettings | null>(null);
  const [flash, setFlash] = useState<{ text: string; good: boolean } | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.settings().then((x) => { setS(x); setLoaded(x); }).catch((e: Error) => setError(e.message));
  }, []);

  const patch = (p: Partial<AppSettings>) => setS((x) => (x ? { ...x, ...p } : x));

  // Only the newest message's timer may clear it; otherwise an earlier save's timer
  // wipes "Saved ✓" a moment after it appears.
  const flashTimer = useRef<number | undefined>(undefined);
  const showFlash = (text: string, good: boolean) => {
    setFlash({ text, good });
    window.clearTimeout(flashTimer.current);
    flashTimer.current = window.setTimeout(() => setFlash(null), 2000);
  };
  useEffect(() => () => window.clearTimeout(flashTimer.current), []);

  const save = async () => {
    if (!s || !loaded) return;
    setError(null);
    const diff: Record<string, unknown> = {};
    for (const k of Object.keys(s) as (keyof AppSettings)[]) {
      if (s[k] !== loaded[k]) diff[k] = s[k];
    }
    if (Object.keys(diff).length === 0) {
      showFlash("Nothing to change", false);
      return;
    }
    try {
      const sent = s;
      const next = await api.updateSettings(diff as Partial<AppSettings>);
      // Take the server's copy, except for fields edited while the request was in
      // flight: those stay as typed and show up as unsaved changes for the next Save.
      setS((cur) => {
        if (!cur) return next;
        const merged = { ...next };
        for (const k of Object.keys(cur) as (keyof AppSettings)[]) {
          if (cur[k] !== sent[k]) (merged as Record<string, unknown>)[k] = cur[k];
        }
        return merged;
      });
      setLoaded(next);
      setBooksEnabled(next.books_enabled); // reflect module on/off in nav + Discover live
      setMusicEnabled(next.music_enabled);
      showFlash("Saved ✓", true);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  // The Discovery region has its own Save button; keep both copies in step with it so
  // the page's snapshot never disagrees with what was just stored.
  const syncRegion = (region: string) => {
    setS((x) => (x ? { ...x, tmdb_region: region } : x));
    setLoaded((x) => (x ? { ...x, tmdb_region: region } : x));
  };

  return <SettingsContext.Provider value={{ s, error, flash, patch, save, syncRegion }}>{children}</SettingsContext.Provider>;
}

export function useSettingsDraft(): SettingsDraft {
  const ctx = useContext(SettingsContext);
  if (!ctx) throw new Error("useSettingsDraft must be used inside <SettingsProvider>");
  return ctx;
}

// useLoadedSettings is useSettingsDraft for a section that only renders once the settings have
// loaded (the hub shows "Loading…" until then), so s is never null inside it.
export function useLoadedSettings(): SettingsDraft & { s: AppSettings } {
  const ctx = useSettingsDraft();
  if (!ctx.s) throw new Error("settings used before they loaded");
  return ctx as SettingsDraft & { s: AppSettings };
}

// SaveBar saves every unsaved edit on the page — not just the section it sits in — exactly
// as the single Save button did before Settings was split into sections.
export function SaveBar() {
  const { save, flash } = useSettingsDraft();
  return (
    <div className="flex items-center gap-3">
      <button onClick={save} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Save settings</button>
      {flash && <span className="text-[12px]" style={{ color: flash.good ? "var(--good)" : "var(--ink-faint)" }}>{flash.text}</span>}
    </div>
  );
}
