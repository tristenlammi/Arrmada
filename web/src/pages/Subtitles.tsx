import { useCallback, useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type SubtitleSettings, type MovieSubStatus, type SeriesSubStatus } from "../lib/api";

type Tab = "movies" | "series";

// Common subtitle languages offered as toggle chips (ISO 639-1). The backend accepts any.
const LANGS: { code: string; name: string }[] = [
  { code: "en", name: "English" }, { code: "es", name: "Spanish" }, { code: "fr", name: "French" },
  { code: "de", name: "German" }, { code: "it", name: "Italian" }, { code: "pt", name: "Portuguese" },
  { code: "nl", name: "Dutch" }, { code: "sv", name: "Swedish" }, { code: "pl", name: "Polish" },
  { code: "ru", name: "Russian" }, { code: "tr", name: "Turkish" }, { code: "ar", name: "Arabic" },
  { code: "hi", name: "Hindi" }, { code: "ja", name: "Japanese" }, { code: "ko", name: "Korean" },
  { code: "zh", name: "Chinese" },
];
const langName = (code: string) => LANGS.find((l) => l.code === code)?.name ?? code.toUpperCase();

export function Subtitles() {
  const [tab, setTab] = useState<Tab>("movies");
  const [settings, setSettings] = useState<SubtitleSettings | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  const loadSettings = useCallback(() => api.subtitleSettings().then(setSettings).catch(() => {}), []);
  useEffect(() => { loadSettings(); }, [loadSettings]);

  const patchSettings = async (body: { movies_auto?: boolean; series_auto?: boolean; languages?: string[] }) => {
    try { setSettings(await api.updateSubtitleSettings(body)); } catch (e) { flash((e as Error).message); }
  };

  return (
    <>
      <PageHeader title="Subtitles" crumb="Services / Subtitles" />
      <div className="mx-auto w-full max-w-[1200px] px-4 py-5 sm:px-6">
        {/* Discover-style tabs */}
        <div className="mb-5 flex gap-1 border-b" style={{ borderColor: "var(--line)" }}>
          {(["movies", "series"] as Tab[]).map((t) => {
            const active = tab === t;
            return (
              <button key={t} onClick={() => setTab(t)} className="relative px-4 py-2.5 text-[13.5px] font-semibold capitalize transition-colors" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>
                {t}
                {active && <span className="absolute inset-x-2 -bottom-px h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
              </button>
            );
          })}
        </div>

        {!settings ? (
          <p className="text-[12.5px] text-ink-dim">Loading…</p>
        ) : (
          <Dashboard
            key={tab}
            tab={tab}
            settings={settings}
            onToggleAuto={(v) => patchSettings(tab === "movies" ? { movies_auto: v } : { series_auto: v })}
            onLanguages={(langs) => patchSettings({ languages: langs })}
            flash={flash}
          />
        )}
      </div>
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

function Dashboard({ tab, settings, onToggleAuto, onLanguages, flash }: {
  tab: Tab; settings: SubtitleSettings;
  onToggleAuto: (v: boolean) => void; onLanguages: (langs: string[]) => void; flash: (m: string) => void;
}) {
  const auto = tab === "movies" ? settings.movies_auto : settings.series_auto;
  const [editLangs, setEditLangs] = useState(false);

  const toggleLang = (code: string) => {
    const has = settings.languages.includes(code);
    const next = has ? settings.languages.filter((l) => l !== code) : [...settings.languages, code];
    if (next.length === 0) { flash("Keep at least one language."); return; }
    onLanguages(next);
  };

  return (
    <div className="flex flex-col gap-5">
      {/* Provider status */}
      {!settings.provider_ready ? (
        <Banner tone="avoid">Subtitle provider not configured. Set <code>ARRMADA_OPENSUBTITLES_API_KEY</code> (and a username + password to download) to search and grab subtitles.</Banner>
      ) : !settings.can_download ? (
        <Banner tone="avoid">Search is ready, but downloading needs a free OpenSubtitles account — set <code>ARRMADA_OPENSUBTITLES_USERNAME</code> and <code>ARRMADA_OPENSUBTITLES_PASSWORD</code>.</Banner>
      ) : null}

      {/* Controls */}
      <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="text-[13.5px] font-bold">Automatic subtitle grabbing</div>
            <div className="mt-0.5 text-[11.5px] text-ink-faint">When on, Arrmada periodically fetches missing subtitles for your {tab} and saves them as sidecar SRT files next to each video.</div>
          </div>
          <button role="switch" aria-checked={auto} onClick={() => onToggleAuto(!auto)} className="inline-flex items-center gap-2 text-[12.5px] font-semibold">
            <span className="relative inline-block h-[22px] w-[38px] rounded-full transition-colors" style={{ background: auto ? "var(--accent)" : "var(--line)" }}>
              <span className="absolute top-[3px] h-[16px] w-[16px] rounded-full bg-white transition-all" style={{ left: auto ? "19px" : "3px" }} />
            </span>
            <span style={{ color: auto ? "var(--ink)" : "var(--ink-dim)" }}>{auto ? "On" : "Off"}</span>
          </button>
        </div>

        <div className="mt-4 border-t pt-3" style={{ borderColor: "var(--line-soft)" }}>
          <div className="flex items-center justify-between">
            <span className="font-mono text-[10px] uppercase tracking-wide text-ink-faint">Wanted languages</span>
            <button onClick={() => setEditLangs((v) => !v)} className="text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>{editLangs ? "Done" : "Edit"}</button>
          </div>
          {editLangs ? (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {LANGS.map((l) => {
                const on = settings.languages.includes(l.code);
                return (
                  <button key={l.code} onClick={() => toggleLang(l.code)} className="rounded-full px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel-2)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>
                    {l.name}
                  </button>
                );
              })}
            </div>
          ) : (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {settings.languages.map((c) => <span key={c} className="rounded-full px-2.5 py-1 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{langName(c)}</span>)}
            </div>
          )}
        </div>
      </div>

      {tab === "movies" ? <MoviesList canDownload={settings.can_download} flash={flash} /> : <SeriesList canDownload={settings.can_download} flash={flash} />}
    </div>
  );
}

function MoviesList({ canDownload, flash }: { canDownload: boolean; flash: (m: string) => void }) {
  const [items, setItems] = useState<MovieSubStatus[] | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const load = useCallback(() => api.subtitleMovies().then(setItems).catch(() => setItems([])), []);
  useEffect(() => { load(); }, [load]);

  const grab = async (m: MovieSubStatus) => {
    setBusy(m.id);
    try { await api.searchMovieSubs(m.id); flash(`Searching subtitles for “${m.title}”…`); setTimeout(load, 6000); }
    catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  if (items === null) return <ListSkeleton />;
  if (items.length === 0) return <Empty>No downloaded movies yet — grab some in the Movies library first.</Empty>;
  const missing = items.filter((m) => (m.missing?.length ?? 0) > 0).length;

  return (
    <div>
      <Stats have={items.length - missing} total={items.length} noun="movies" />
      <div className="flex flex-col gap-1.5">
        {items.map((m) => (
          <Row key={m.id} poster={m.poster_url} title={m.title} year={m.year}
            present={m.present} missing={m.missing}
            action={<GrabButton disabled={!canDownload || (m.missing?.length ?? 0) === 0} busy={busy === m.id} onClick={() => grab(m)} />} />
        ))}
      </div>
    </div>
  );
}

function SeriesList({ canDownload, flash }: { canDownload: boolean; flash: (m: string) => void }) {
  const [items, setItems] = useState<SeriesSubStatus[] | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const load = useCallback(() => api.subtitleSeries().then(setItems).catch(() => setItems([])), []);
  useEffect(() => { load(); }, [load]);

  const grab = async (s: SeriesSubStatus) => {
    setBusy(s.id);
    try { await api.searchSeriesSubs(s.id); flash(`Searching subtitles for “${s.title}”…`); setTimeout(load, 8000); }
    catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  if (items === null) return <ListSkeleton />;
  if (items.length === 0) return <Empty>No downloaded episodes yet — grab some in the Series library first.</Empty>;
  const complete = items.filter((s) => s.missing_subs === 0).length;

  return (
    <div>
      <Stats have={complete} total={items.length} noun="series" />
      <div className="flex flex-col gap-1.5">
        {items.map((s) => (
          <Row key={s.id} poster={s.poster_url} title={s.title} year={s.year}
            meta={<span className="text-[11.5px]" style={{ color: s.missing_subs === 0 ? "var(--good)" : "var(--avoid)" }}>{s.complete}/{s.episodes} episodes subtitled{s.missing_subs > 0 ? ` · ${s.missing_subs} missing` : ""}</span>}
            action={<GrabButton disabled={!canDownload || s.missing_subs === 0} busy={busy === s.id} onClick={() => grab(s)} />} />
        ))}
      </div>
    </div>
  );
}

function Row({ poster, title, year, present, missing, meta, action }: {
  poster?: string; title: string; year: number; present?: string[]; missing?: string[]; meta?: React.ReactNode; action: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-3 rounded-xl p-2.5" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="h-[54px] w-[36px] flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
        {poster && <img src={poster} alt="" className="h-full w-full object-cover" loading="lazy" />}
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-semibold">{title} {year > 0 && <span className="font-normal text-ink-faint">({year})</span>}</div>
        <div className="mt-1 flex flex-wrap items-center gap-1.5">
          {meta}
          {present?.map((l) => <Chip key={l} tone="good">{langName(l)}</Chip>)}
          {missing?.map((l) => <Chip key={l} tone="avoid">{langName(l)}</Chip>)}
        </div>
      </div>
      <div className="flex-none">{action}</div>
    </div>
  );
}

function Chip({ children, tone }: { children: React.ReactNode; tone: "good" | "avoid" }) {
  const c = tone === "good" ? { bg: "var(--good-soft, rgba(90,140,90,.16))", fg: "var(--good)" } : { bg: "var(--avoid-soft)", fg: "var(--avoid)" };
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: c.bg, color: c.fg }}>{tone === "good" ? "✓ " : ""}{children}</span>;
}

function GrabButton({ disabled, busy, onClick }: { disabled: boolean; busy: boolean; onClick: () => void }) {
  return (
    <button onClick={onClick} disabled={disabled || busy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
      {busy ? "Searching…" : "Search subtitles"}
    </button>
  );
}

function Stats({ have, total, noun }: { have: number; total: number; noun: string }) {
  return <div className="mb-2.5 text-[12px] text-ink-dim"><b style={{ color: "var(--ink)" }}>{have}</b> of {total} {noun} fully subtitled</div>;
}

function Banner({ children, tone }: { children: React.ReactNode; tone: "avoid" }) {
  return <div className="rounded-lg p-3 text-[12px]" style={{ border: `1px solid var(--${tone})`, background: `var(--${tone}-soft)`, color: "var(--ink-dim)" }}>{children}</div>;
}

function Empty({ children }: { children: React.ReactNode }) {
  return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{children}</div>;
}

function ListSkeleton() {
  return <div className="flex flex-col gap-1.5">{Array.from({ length: 6 }).map((_, i) => <div key={i} className="h-[74px] rounded-xl" style={{ background: "var(--panel-2)" }} />)}</div>;
}
