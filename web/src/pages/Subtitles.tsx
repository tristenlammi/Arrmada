import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { PageHeader } from "../components/PageHeader";
import { RescanButton, scanTitle } from "../components/RescanButton";
import { api, type SubtitleSettings, type SubFileEntry, type SubSeriesGroup, type SubtitleJob, type SubtitleCoverage, type SubLangStatus, type WhisperStatus } from "../lib/api";

type Tab = "overview" | "queue" | "library" | "logs" | "settings";
const ACTIVE = new Set(["queued", "running"]);

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
const SOURCE_LABEL: Record<string, string> = { extract: "extract", ocr: "OCR", download: "download", ai: "AI" };

const card = "rounded-xl p-4";
const cardStyle = { border: "1px solid var(--line)", background: "var(--panel)" } as const;
const lbl = "font-mono text-[9.5px] font-bold uppercase tracking-[0.11em] text-ink-faint";

export function Subtitles() {
  const [tab, setTab] = useState<Tab>("overview");
  const [settings, setSettings] = useState<SubtitleSettings | null>(null);
  const [jobs, setJobs] = useState<SubtitleJob[]>([]);
  const [toast, setToast] = useState<string | null>(null);
  const flash = useCallback((m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); }, []);

  const loadSettings = useCallback(() => api.subtitleSettings().then(setSettings).catch(() => {}), []);
  useEffect(() => { loadSettings(); }, [loadSettings]);

  const anyActive = jobs.some((j) => ACTIVE.has(j.state));
  useEffect(() => {
    let alive = true;
    const tick = () => api.subtitleJobs().then((j) => { if (alive) setJobs(j); }).catch(() => {});
    tick();
    const t = setInterval(tick, anyActive ? 1500 : 4000);
    return () => { alive = false; clearInterval(t); };
  }, [anyActive]);

  const patchSettings = async (body: { movies_auto?: boolean; series_auto?: boolean; languages?: string[] }) => {
    try { setSettings(await api.updateSubtitleSettings(body)); } catch (e) { flash((e as Error).message); }
  };
  const ensureAll = async () => {
    try {
      const [m, tv] = await Promise.all([api.subtitleSweep("movies"), api.subtitleSweep("tv")]);
      const n = m.queued + tv.queued;
      flash(n ? `Queued ${n} file${n === 1 ? "" : "s"} missing subtitles…` : "Everything already has its subtitles. 🎉");
      setTab("queue");
      api.subtitleJobs().then(setJobs);
    } catch (e) { flash((e as Error).message); }
  };

  const activeCount = jobs.filter((j) => ACTIVE.has(j.state)).length;
  const provider = settings?.provider_ready ? (settings.can_download ? "OpenSubtitles ready" : "OpenSubtitles: search only") : "AI + embedded only";
  const providerOK = !!settings?.can_download;
  const TABS: { key: Tab; label: string; n?: string }[] = [
    { key: "overview", label: "Overview" },
    { key: "queue", label: "Queue", n: activeCount ? `${activeCount} active` : undefined },
    { key: "library", label: "Library" },
    { key: "logs", label: "Logs" },
    { key: "settings", label: "Settings" },
  ];

  return (
    <>
      <PageHeader title="Subtitles" crumb="Library / Subtitles" />
      <div className="mx-auto w-full max-w-[1240px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <p className="max-w-[64ch] text-[12.5px] text-ink-dim">One external <code>.srt</code> per language, next to every video. Arrmada uses the best source it can — an embedded track, a download, or (soon) AI transcription — and keeps your kept languages while stripping the rest. Pick languages in <b>Settings</b>.</p>
          <div className="flex items-center gap-2">
            <span className="inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-[12px] font-semibold" style={{ border: `1px solid ${providerOK ? "var(--good)" : "var(--line)"}`, background: providerOK ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--panel-2)" }}>
              <span className="h-2 w-2 rounded-full" style={{ background: providerOK ? "var(--good)" : "var(--ink-faint)" }} />
              {provider}
            </span>
            <button onClick={() => setTab("settings")} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Settings</button>
            <button onClick={ensureAll} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Ensure all</button>
          </div>
        </div>

        <div className="mb-5 flex gap-1 border-b" style={{ borderColor: "var(--line)" }}>
          {TABS.map((t) => {
            const active = tab === t.key;
            return (
              <button key={t.key} onClick={() => setTab(t.key)} className="relative px-4 py-2.5 text-[13.5px] font-semibold transition-colors" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>
                {t.label}{t.n && <span className="ml-1.5 font-mono text-[10px] text-ink-faint">{t.n}</span>}
                {active && <span className="absolute inset-x-2 -bottom-px h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
              </button>
            );
          })}
        </div>

        {tab === "overview" && <Overview jobs={jobs} settings={settings} flash={flash} />}
        {tab === "queue" && <Queue jobs={jobs} onChange={() => api.subtitleJobs().then(setJobs).catch(() => {})} flash={flash} />}
        {tab === "library" && <Library flash={flash} onQueued={() => api.subtitleJobs().then(setJobs)} />}
        {tab === "logs" && <LogsConsole />}
        {tab === "settings" && settings && <SettingsTab settings={settings} onPatch={patchSettings} flash={flash} />}
      </div>
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

/* ============================= OVERVIEW ============================= */
function Overview({ jobs, settings, flash }: { jobs: SubtitleJob[]; settings: SubtitleSettings | null; flash: (m: string) => void }) {
  // The totals come from the server's last library pass. This used to fetch the flat
  // list of every file — a directory listing per file, 25,000 of them — every time the
  // tab opened.
  const [cov, setCov] = useState<SubtitleCoverage | null>(null);
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => api.subtitleCoverage().then(setCov).catch(() => {}), []);
  useEffect(() => { load(); }, [load]);
  // While a pass is running, keep asking until it lands.
  useEffect(() => {
    if (!cov?.scanning) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [cov?.scanning, load]);
  const rescan = async () => {
    setBusy(true);
    try { const r = await api.subtitleRescan(); flash(r.started ? "Rescanning the library…" : "A scan is already running."); await load(); }
    catch (e) { flash(e instanceof Error ? e.message : "Couldn't start a scan"); }
    finally { setBusy(false); }
  };
  const active = sortActive(jobs);
  const scanned = !!cov && cov.scanned_at > 0;
  const pct = scanned && cov.files ? Math.round((cov.covered / cov.files) * 100) : 0;

  return (
    <div className="flex flex-col gap-3.5">
      <div className="grid gap-3.5" style={{ gridTemplateColumns: "1.1fr 1fr 1fr" }}>
        <div className={card} style={cardStyle}>
          <div className="flex items-center justify-between">
            <div className={lbl}>Coverage</div>
            <RescanButton busy={busy || !!cov?.scanning} onClick={rescan} title={scanTitle(cov)} />
          </div>
          <div className="mt-2 text-[30px] font-extrabold tracking-tight">{scanned ? `${pct}%` : "…"}</div>
          <div className="mt-3 border-t pt-3 text-[12px] text-ink-dim" style={{ borderColor: "var(--line-soft)" }}>
            {scanned
              ? <><b style={{ color: "var(--good)" }}>{cov.covered.toLocaleString()}</b> fully subtitled · <b style={{ color: cov.missing ? "var(--avoid)" : "var(--ink)" }}>{cov.missing.toLocaleString()}</b> missing a language · {cov.files.toLocaleString()} files</>
              : "Scanning your library — the first pass after startup takes a few minutes."}
          </div>
        </div>
        <div className={card} style={cardStyle}>
          <div className={lbl}>Kept languages</div>
          <div className="mt-2.5 flex flex-wrap gap-1.5">
            {(settings?.languages ?? []).map((c) => <span key={c} className="rounded-full px-2.5 py-1 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{langName(c)}</span>)}
          </div>
          <div className="mt-3 text-[11.5px] text-ink-faint">These languages are kept as external <code>.srt</code>; everything else is stripped from the video (once stripping ships).</div>
        </div>
        <div className={card} style={cardStyle}>
          <div className={lbl}>Sources &amp; automation</div>
          <div className="mt-2 flex flex-col gap-1.5 text-[12px] text-ink-dim">
            <Row2 label="Embedded extract" on />
            <Row2 label="OpenSubtitles download" on={!!settings?.can_download} />
            <Row2 label="Image-sub OCR" on={false} soon />
            <Row2 label="AI transcription" on={!!settings?.ai_ready} soon={!settings?.ai_ready} />
          </div>
          <div className="mt-2.5 border-t pt-2.5 text-[11.5px] text-ink-faint" style={{ borderColor: "var(--line-soft)" }}>
            Auto: movies {settings?.movies_auto ? "on" : "off"} · series {settings?.series_auto ? "on" : "off"}
          </div>
        </div>
      </div>

      {active.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="text-[14px] font-bold">Working now</div>
          <div className="mt-2 flex flex-col gap-1.5">{active.map((j) => <ActiveRow key={j.id} j={j} />)}</div>
        </div>
      )}
    </div>
  );
}
function Row2({ label, on, soon }: { label: string; on: boolean; soon?: boolean }) {
  return (
    <div className="flex items-center justify-between">
      <span>{label}</span>
      <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: on ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--panel-2)", color: on ? "var(--good)" : "var(--ink-faint)" }}>{soon ? "soon" : on ? "on" : "off"}</span>
    </div>
  );
}

/* ============================= QUEUE ============================= */
function Queue({ jobs, onChange, flash }: { jobs: SubtitleJob[]; onChange: () => void; flash: (m: string) => void }) {
  // The API lists newest-first (right for "Recent"), which put the running job — the
  // oldest active one — at the bottom. Show it the way the worker sees it: what's running
  // on top, then what's next, in the order it will run.
  const active = sortActive(jobs);
  const done = jobs.filter((j) => !ACTIVE.has(j.state));
  const running = active.filter((j) => j.state === "running").length;
  const queued = active.length - running;
  const [busy, setBusy] = useState(false);
  const cancel = async (j: SubtitleJob) => {
    setBusy(true);
    try { await api.subtitleCancelJob(j.id); onChange(); }
    catch (e) { flash(e instanceof Error ? e.message : "Couldn't stop that job"); }
    finally { setBusy(false); }
  };
  const clear = async () => {
    setBusy(true);
    try { const r = await api.subtitleClearQueue(); flash(`Removed ${r.cleared} queued job${r.cleared === 1 ? "" : "s"}.`); onChange(); }
    catch (e) { flash(e instanceof Error ? e.message : "Couldn't clear the queue"); }
    finally { setBusy(false); }
  };
  return (
    <div className="flex flex-col gap-3.5">
      <div className={card} style={cardStyle}>
        <div className="flex items-center gap-2">
          <div className="text-[14px] font-bold">Active <span className="font-mono text-[11px] text-ink-faint">{running} running · {queued} queued</span></div>
          <div className="flex-1" />
          {queued > 0 && (
            <button type="button" onClick={clear} disabled={busy} className="rounded-md border px-2 py-1 text-[11px] font-semibold hover:bg-[var(--panel-2)] disabled:opacity-50" style={{ borderColor: "var(--line)", color: "var(--ink-dim)" }}>
              Clear queue
            </button>
          )}
        </div>
        {active.length === 0 ? <div className="mt-2 text-[12px] text-ink-dim">Nothing in the queue right now.</div> : <div className="mt-2 flex flex-col gap-1.5">{active.map((j) => <ActiveRow key={j.id} j={j} onCancel={() => cancel(j)} busy={busy} />)}</div>}
      </div>
      {done.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="text-[14px] font-bold">Recent</div>
          <div className="mt-2 flex flex-col gap-1.5">
            {done.slice(0, 30).map((j) => (
              <div key={j.id} className="flex items-center gap-2.5 text-[12px]">
                <StateBadge state={j.state} />
                <span className="flex-1 truncate font-semibold">{j.title}{j.redo && <span className="ml-1.5 rounded px-1 py-0.5 font-mono text-[9px] font-bold uppercase text-ink-faint" style={{ border: "1px solid var(--line)" }} title="Replacing the subtitles already there">redo</span>}</span>
                <span className="text-[11.5px] text-ink-faint">{j.note}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
// sortActive lists the running job first, then the queue in the order the worker will
// take it (oldest first). The API's newest-first order put the running job at the bottom.
function sortActive(jobs: SubtitleJob[]): SubtitleJob[] {
  return jobs
    .filter((j) => ACTIVE.has(j.state))
    .sort((a, b) => (a.state === b.state ? a.at - b.at || a.id - b.id : a.state === "running" ? -1 : 1));
}

function ActiveRow({ j, onCancel, busy }: { j: SubtitleJob; onCancel?: () => void; busy?: boolean }) {
  const running = j.state === "running";
  const stopping = running && j.note === "stopping…";
  const pct = running && j.progress ? Math.min(100, Math.max(0, j.progress)) : 0;
  // Elapsed since the worker picked it up, and a straight-line ETA once there's enough
  // progress to extrapolate from. The list is polled every 1.5s, so this stays live.
  const elapsed = running && j.started_at ? Date.now() / 1000 - j.started_at : 0;
  const eta = elapsed > 0 && pct >= 5 ? (elapsed * (100 - pct)) / pct : 0;
  return (
    <div className="flex flex-col gap-1 text-[12px]">
      <div className="flex items-center gap-2.5">
        <StateBadge state={j.state} />
        <span className="flex-1 truncate font-semibold">{j.title}{j.redo && <span className="ml-1.5 rounded px-1 py-0.5 font-mono text-[9px] font-bold uppercase text-ink-faint" style={{ border: "1px solid var(--line)" }} title="Replacing the subtitles already there">redo</span>}</span>
        {stopping ? <span className="font-mono text-[10.5px] text-ink-faint">stopping…</span>
          : running && j.stage && <span className="truncate font-mono text-[10.5px] text-ink-faint">{j.stage}</span>}
        {running && elapsed > 0 && <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtElapsed(elapsed)}{eta > 0 ? ` · ~${fmtElapsed(eta)} left` : ""}</span>}
        {running && pct > 0 && <span className="w-[38px] flex-none text-right font-mono text-[10.5px] text-ink-dim">{pct}%</span>}
        {running && pct === 0 && <span className="h-3 w-3 flex-none animate-spin rounded-full" style={{ border: "2px solid var(--line)", borderTopColor: "var(--accent)" }} />}
        {/* Stop kills the running ffmpeg/whisper; Remove just drops a queued job. */}
        {onCancel && (
          <button type="button" onClick={onCancel} disabled={busy || stopping} title={running ? "Stop this job" : "Remove from the queue"}
            className="flex-none rounded-md border px-1.5 py-0.5 text-[10.5px] font-semibold hover:bg-[var(--panel-2)] disabled:opacity-50"
            style={{ borderColor: "var(--line)", color: running ? "var(--reject)" : "var(--ink-dim)" }}>
            {running ? "Stop" : "Remove"}
          </button>
        )}
      </div>
      {/* whisper reports progress in 5% steps; anything else finishes before a bar would help. */}
      {running && pct > 0 && (
        <div className="h-1 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
          <div className="h-full rounded-full" style={{ width: `${pct}%`, background: "var(--accent)", transition: "width 600ms linear" }} />
        </div>
      )}
    </div>
  );
}
function fmtElapsed(sec: number): string {
  sec = Math.max(0, Math.round(sec));
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
  return h ? `${h}h ${String(m).padStart(2, "0")}m` : m ? `${m}m ${String(s).padStart(2, "0")}s` : `${s}s`;
}
function StateBadge({ state }: { state: string }) {
  const s = state === "done" ? { bg: "var(--good-soft, rgba(127,176,105,.16))", fg: "var(--good)" }
    : state === "failed" ? { bg: "var(--reject-soft)", fg: "var(--reject)" }
    : state === "running" ? { bg: "var(--accent-soft)", fg: "var(--accent)" }
    : state === "cancelled" ? { bg: "var(--panel-2)", fg: "var(--ink-dim)" }
    : { bg: "var(--panel-2)", fg: "var(--ink-faint)" };
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: s.bg, color: s.fg }}>{state}</span>;
}

/* ============================= LIBRARY ============================= */
type SortKey = "title" | "missing" | "embedded";
function rowKey(f: SubFileEntry): string {
  return f.kind === "episode" ? `e:${f.series_id}:${f.season}:${f.episode}` : `m:${f.movie_id}`;
}
function embeddedKinds(f: SubFileEntry): { txt: boolean; pgs: boolean; vob: boolean } {
  let txt = false, pgs = false, vob = false;
  for (const t of f.embedded ?? []) {
    if (t.text) txt = true;
    else if (t.codec === "hdmv_pgs_subtitle") pgs = true;
    else if (t.codec === "dvd_subtitle") vob = true;
  }
  return { txt, pgs, vob };
}
type Filter = "missing" | "pgs" | "text" | "external";

function Library({ flash, onQueued }: { flash: (m: string) => void; onQueued: () => void }) {
  const [media, setMedia] = useState<"movies" | "tv">("movies");
  const [items, setItems] = useState<SubFileEntry[] | null>(null);
  const [scanning, setScanning] = useState(false); // no pass has completed yet (just after startup)
  const [refreshKey, setRefreshKey] = useState(0);
  const [rescanBusy, setRescanBusy] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [queued, setQueued] = useState<Set<string>>(new Set());
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "missing", dir: "desc" });
  const [filters, setFilters] = useState<Set<Filter>>(new Set());

  useEffect(() => {
    setItems(null); setQueued(new Set()); setFilters(new Set());
    // TV is rendered per show by TVLibrary, which fetches its own rolled-up list.
    if (media === "tv") return;
    api.subtitleLibrary(media).then((r) => { setItems(r.items); setScanning(!!r.scanning); }).catch(() => setItems([]));
  }, [media, refreshKey]);

  // Start a fresh pass and hold the spinner until it lands, then reload. Bounded so a
  // stalled pass can't pin the button.
  const rescan = async () => {
    setRescanBusy(true);
    try {
      const r = await api.subtitleRescan();
      flash(r.started ? "Rescanning the library…" : "A scan is already running — waiting for it.");
      for (let i = 0; i < 300; i++) {
        await new Promise((res) => setTimeout(res, 2000));
        const c = await api.subtitleCoverage().catch(() => null);
        if (c && !c.scanning) break;
      }
      setRefreshKey((k) => k + 1);
      flash("Library rescanned.");
    } catch (e) { flash((e as Error).message); } finally { setRescanBusy(false); }
  };

  const ensure = async (f: SubFileEntry, redo = false) => {
    if (redo && !confirmRedo(f.title)) return;
    const key = rowKey(f);
    setBusy(key);
    try {
      if (f.kind === "episode") await api.subtitleQueueEpisode(f.series_id!, f.season!, f.episode!, redo);
      else await api.subtitleQueueMovie(f.movie_id!, redo);
      setQueued((q) => new Set(q).add(key));
      flash(redo ? `Redoing “${f.title}”` : `Queued “${f.title}”`);
      onQueued();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  const toggleF = (f: Filter) => setFilters((s) => { const n = new Set(s); n.has(f) ? n.delete(f) : n.add(f); return n; });
  const setSortKey = (key: SortKey) => setSort((s) => s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: key === "title" ? "asc" : "desc" });

  const counts = useMemo(() => {
    const c = { missing: 0, pgs: 0, text: 0, external: 0 };
    for (const f of items ?? []) {
      if (f.missing > 0) c.missing++;
      const k = embeddedKinds(f);
      if (k.pgs || k.vob) c.pgs++;
      if (k.txt) c.text++;
      if ((f.external ?? []).length > 0) c.external++;
    }
    return c;
  }, [items]);

  const view = useMemo(() => {
    let list = (items ?? []).slice();
    if (filters.has("missing")) list = list.filter((f) => f.missing > 0);
    if (filters.has("pgs")) list = list.filter((f) => { const k = embeddedKinds(f); return k.pgs || k.vob; });
    if (filters.has("text")) list = list.filter((f) => embeddedKinds(f).txt);
    if (filters.has("external")) list = list.filter((f) => (f.external ?? []).length > 0);
    const dir = sort.dir === "asc" ? 1 : -1;
    const val = (f: SubFileEntry): number | string => {
      switch (sort.key) {
        case "title": return f.title.toLowerCase();
        case "missing": return f.missing;
        case "embedded": return (f.embedded ?? []).length;
      }
    };
    return list.sort((a, b) => {
      const va = val(a), vb = val(b);
      return typeof va === "string" || typeof vb === "string" ? String(va).localeCompare(String(vb)) * dir : (va - vb) * dir;
    });
  }, [items, filters, sort]);

  const noun = media === "tv" ? "episodes" : "movies";
  const filtering = filters.size > 0;
  const HEADERS: { label: string; key?: SortKey }[] = [
    { label: "Title", key: "title" }, { label: "Audio" }, { label: "Embedded", key: "embedded" },
    { label: "Coverage" }, { label: "Health" }, { label: "" },
  ];

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <div className="inline-flex w-fit rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          {(["movies", "tv"] as const).map((m) => (
            <button key={m} onClick={() => setMedia(m)} className="rounded-md px-3.5 py-1.5 text-[12px] font-semibold" style={{ background: media === m ? "var(--accent)" : "transparent", color: media === m ? "var(--accent-ink)" : "var(--ink-faint)" }}>
              {m === "movies" ? "Movies" : "TV Shows"}{items && media === m && m === "movies" ? <span className="ml-1.5 font-mono text-[10px] opacity-70">{items.length.toLocaleString()}</span> : null}
            </button>
          ))}
        </div>
        {/* The lists come from the server's last pass; this asks for a new one. */}
        <RescanButton busy={rescanBusy} onClick={rescan} title="Rescan the library for new files and subtitles" />
      </div>

      {media === "tv" ? (
        <TVLibrary key={refreshKey} flash={flash} onQueued={onQueued} />
      ) : items === null || (scanning && items.length === 0) ? (
        <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Scanning your {noun}…{items !== null && " The first pass after startup takes a few minutes."}</div>
      ) : items.length === 0 ? (
        <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>No downloaded {noun} yet.</div>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-1.5">
            <Pill active={filters.has("missing")} disabled={!counts.missing} onClick={() => toggleF("missing")}>Missing <span className="opacity-60">{counts.missing}</span></Pill>
            <Pill active={filters.has("pgs")} disabled={!counts.pgs} onClick={() => toggleF("pgs")}>PGS/VobSub <span className="opacity-60">{counts.pgs}</span></Pill>
            <Pill active={filters.has("text")} disabled={!counts.text} onClick={() => toggleF("text")}>Embedded text <span className="opacity-60">{counts.text}</span></Pill>
            <Pill active={filters.has("external")} disabled={!counts.external} onClick={() => toggleF("external")}>Has external <span className="opacity-60">{counts.external}</span></Pill>
            {filtering && <button onClick={() => setFilters(new Set())} className="ml-1 text-[10.5px] text-ink-faint underline hover:text-[var(--ink)]">clear</button>}
          </div>
          <p className="text-[11px] text-ink-faint">
            {filtering ? <><b style={{ color: "var(--ink)" }}>{view.length.toLocaleString()}</b> of {items.length.toLocaleString()} · </> : null}
            <b>Ensure subs</b> makes any missing kept-language <code>.srt</code> using the best source available (image subs + AI are coming). Health scoring lands with the sync phase.
          </p>
          <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
            <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 900 }}>
              <thead><tr style={{ background: "var(--panel-2)" }}>{HEADERS.map((h) => (
                <th key={h.label} onClick={h.key ? () => setSortKey(h.key!) : undefined}
                  className={`px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint ${h.key ? "cursor-pointer select-none hover:text-[var(--ink)]" : ""}`}>
                  {h.label}{h.key && sort.key === h.key ? (sort.dir === "asc" ? " ▲" : " ▼") : ""}
                </th>
              ))}</tr></thead>
              <tbody>
                {view.map((f, i) => (
                  <SubRow
                    key={rowKey(f)}
                    f={f}
                    first={i === 0}
                    busy={busy}
                    queued={queued.has(rowKey(f))}
                    onEnsure={() => ensure(f)}
                    onRedo={() => ensure(f, true)}
                  />
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}
// TVLibrary lists SHOWS, not episodes.
//
// The flat list was one row per episode — 23,000 of them, each probed before the page
// could render. You cannot find a show in that, and it is slow for the same reason it
// is unusable. One row per show, a search box, and episodes probed only for the show
// you actually open.
function TVLibrary({ flash, onQueued }: { flash: (m: string) => void; onQueued: () => void }) {
  const [groups, setGroups] = useState<SubSeriesGroup[] | null>(null);
  const [scanning, setScanning] = useState(false);
  const [query, setQuery] = useState("");
  const [onlyGaps, setOnlyGaps] = useState(false);
  const [open, setOpen] = useState<number | null>(null);

  useEffect(() => {
    api.subtitleSeriesGroups().then((r) => { setGroups(r.groups); setScanning(!!r.scanning); }).catch(() => setGroups([]));
  }, []);

  const view = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (groups ?? []).filter(
      (g) => (!q || g.title.toLowerCase().includes(q)) && (!onlyGaps || g.missing > 0),
    );
  }, [groups, query, onlyGaps]);

  const totals = useMemo(() => {
    let eps = 0, missing = 0, shows = 0;
    for (const g of groups ?? []) {
      eps += g.episodes;
      missing += g.missing;
      if (g.missing > 0) shows++;
    }
    return { eps, missing, shows };
  }, [groups]);

  if (groups === null || (scanning && groups.length === 0)) {
    return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Scanning your shows…{groups !== null && " The first pass after startup takes a few minutes."}</div>;
  }
  if (groups.length === 0) {
    return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>No downloaded episodes yet.</div>;
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search shows…"
          className="w-[240px] rounded-lg px-3 py-1.5 text-[12.5px]"
          style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
        />
        <Pill active={onlyGaps} onClick={() => setOnlyGaps((v) => !v)}>
          Needs subtitles <span className="opacity-60">{totals.shows}</span>
        </Pill>
        <span className="text-[11px] text-ink-faint">
          {groups.length.toLocaleString()} shows · {totals.eps.toLocaleString()} episodes ·{" "}
          <b style={{ color: totals.missing ? "var(--avoid)" : "var(--good)" }}>{totals.missing.toLocaleString()}</b> missing a kept language
        </span>
      </div>

      <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
        {view.length === 0 ? (
          <div className="p-8 text-center text-[12.5px] text-ink-dim">No shows match that search.</div>
        ) : (
          view.map((g, i) => (
            <SeriesGroupRow
              key={g.series_id}
              g={g}
              first={i === 0}
              open={open === g.series_id}
              onToggle={() => setOpen((cur) => (cur === g.series_id ? null : g.series_id))}
              flash={flash}
              onQueued={onQueued}
            />
          ))
        )}
      </div>
    </div>
  );
}

// SeriesGroupRow is one show, expanding to its episodes. The episodes are fetched on
// first open and kept, so collapsing and reopening doesn't re-probe the files.
function SeriesGroupRow({ g, first, open, onToggle, flash, onQueued }: {
  g: SubSeriesGroup; first: boolean; open: boolean; onToggle: () => void;
  flash: (m: string) => void; onQueued: () => void;
}) {
  const [eps, setEps] = useState<SubFileEntry[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [queued, setQueued] = useState<Set<string>>(new Set());

  useEffect(() => {
    if (!open || eps !== null) return;
    api.subtitleSeriesEpisodes(g.series_id).then(setEps).catch(() => setEps([]));
  }, [open, eps, g.series_id]);

  const ensure = async (f: SubFileEntry, redo = false) => {
    if (redo && !confirmRedo(`${g.title} ${f.title}`)) return;
    const key = rowKey(f);
    setBusy(key);
    try {
      await api.subtitleQueueEpisode(f.series_id!, f.season!, f.episode!, redo);
      setQueued((q) => new Set(q).add(key));
      flash(redo ? `Redoing ${f.title}` : `Queued ${f.title}`);
      onQueued();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  const pct = g.episodes ? Math.round((g.covered / g.episodes) * 100) : 0;

  // Whole show at once — every episode still missing a kept language. The Library
  // offered one episode or the entire library and nothing between.
  const [showBusy, setShowBusy] = useState(false);
  const [showQueued, setShowQueued] = useState(false);
  const ensureShow = async () => {
    setShowBusy(true);
    try {
      const r = await api.subtitleQueueSeries(g.series_id);
      setShowQueued(true);
      flash(r.queued ? `Queued ${r.queued} episode${r.queued === 1 ? "" : "s"} of ${g.title}` : `${g.title}: nothing to do — every episode has its subtitles`);
      onQueued();
    } catch (e) { flash((e as Error).message); } finally { setShowBusy(false); }
  };

  return (
    <div style={{ borderTop: first ? "none" : "1px solid var(--line-soft)" }}>
      <div className="flex items-center gap-2 pr-3 hover:bg-[var(--panel-2)]">
      <button onClick={onToggle} className="flex min-w-0 flex-1 items-center gap-3 px-3 py-2.5 text-left">
        <span className="w-3 flex-none text-[10px] text-ink-faint">{open ? "▾" : "▸"}</span>
        <span className="min-w-0 flex-1 truncate text-[12.5px] font-semibold">
          {g.title} <span className="font-normal text-ink-faint">{g.year || ""}</span>
        </span>
        <span className="hidden font-mono text-[10.5px] text-ink-faint sm:inline">
          {g.seasons} season{g.seasons === 1 ? "" : "s"} · {g.episodes} ep
        </span>
        <span className="w-[120px] flex-none">
          <span className="block h-1.5 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
            <span className="block h-full rounded-full" style={{ width: `${pct}%`, background: g.missing ? "var(--avoid)" : "var(--good)" }} />
          </span>
        </span>
        <span className="w-[92px] flex-none text-right font-mono text-[10.5px]" style={{ color: g.missing ? "var(--avoid)" : "var(--good)" }}>
          {g.missing ? `${g.missing} missing` : "complete"}
        </span>
      </button>
      <button
        type="button"
        onClick={ensureShow}
        disabled={showBusy || showQueued || !g.missing}
        title={g.missing ? `Queue the ${g.missing} episode${g.missing === 1 ? "" : "s"} still missing a kept language` : "Every episode already has its subtitles"}
        className="flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40"
        style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink)" }}
      >
        {showQueued ? "Queued" : "Ensure show"}
      </button>
      </div>

      {open && (
        <div className="px-3 pb-3">
          {eps === null ? (
            <div className="py-6 text-center text-[12px] text-ink-dim">Scanning {g.title}…</div>
          ) : eps.length === 0 ? (
            <div className="py-6 text-center text-[12px] text-ink-dim">No episodes with files.</div>
          ) : (
            <div className="overflow-x-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
              <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 900 }}>
                <thead><tr style={{ background: "var(--panel-2)" }}>
                  {["Episode", "Audio", "Embedded", "Coverage", "Health", ""].map((h, i) => (
                    <th key={i} className="px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{h}</th>
                  ))}
                </tr></thead>
                <tbody>
                  {eps.map((f, i) => (
                    <SubRow key={rowKey(f)} f={f} first={i === 0} busy={busy} queued={queued.has(rowKey(f))} onEnsure={() => ensure(f)} onRedo={() => ensure(f, true)} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// SubRow is one file's coverage line. Shared by the flat movie table and the per-show
// episode table, so the two can't drift apart.
// A redo overwrites the sidecars that are there, so it asks first.
function confirmRedo(title: string) {
  return window.confirm(`Redo subtitles for ${title}?\n\nThe existing .srt files for your kept languages will be replaced — use this when what's there is wrong (the AI transcribed the wrong audio track, a bad download).`);
}

function SubRow({ f, first, busy, queued, onEnsure, onRedo }: { f: SubFileEntry; first: boolean; busy: string | null; queued: boolean; onEnsure: () => void; onRedo: () => void }) {
  const key = rowKey(f);
  const k = embeddedKinds(f);
  return (
    <tr style={{ borderTop: first ? "none" : "1px solid var(--line-soft)" }}>
      <td className="px-3 py-2 font-semibold">{f.title} <span className="font-normal text-ink-faint">{f.year || ""}</span></td>
      <td className="px-3 py-2 font-mono text-[10.5px] text-ink-dim">{(f.audio_langs ?? []).map((l) => l.toUpperCase()).join(" ") || "—"}</td>
      <td className="px-3 py-2">
        {(f.embedded ?? []).length === 0 ? <span className="text-ink-faint">—</span> : (
          <div className="flex items-center gap-1">
            {k.txt && <EmbBadge kind="txt" />}
            {k.pgs && <EmbBadge kind="pgs" />}
            {k.vob && <EmbBadge kind="vob" />}
          </div>
        )}
      </td>
      <td className="px-3 py-2">
        <div className="flex flex-wrap items-center gap-1">{(f.languages ?? []).map((l) => <CoverChip key={l.lang} l={l} />)}</div>
      </td>
      <td className="px-3 py-2 font-mono text-[10.5px] text-ink-faint">{f.health ? `${f.health.score}%` : "—"}</td>
      <td className="px-3 py-2">
        <div className="flex items-center justify-end gap-2">
          {queued ? (
            <span className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--good)", color: "var(--good)" }}>Queued ✓</span>
          ) : f.missing === 0 ? (
            <>
              <span className="font-mono text-[10.5px]" style={{ color: "var(--good)" }}>complete</span>
              <button onClick={onRedo} disabled={busy !== null} title="Make this file's subtitles again, replacing the ones there" className="rounded-md px-2 py-1 text-[10.5px] font-semibold text-ink-dim disabled:opacity-50" style={{ border: "1px solid var(--line)" }}>{busy === key ? "…" : "Redo"}</button>
            </>
          ) : (
            <button onClick={onEnsure} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{busy === key ? "Queuing…" : "Ensure subs"}</button>
          )}
        </div>
      </td>
    </tr>
  );
}

function EmbBadge({ kind }: { kind: "txt" | "pgs" | "vob" }) {
  const style = kind === "txt" ? { background: "var(--panel-2)", color: "var(--ink-dim)" } : { background: "var(--avoid-soft)", color: "var(--avoid)" };
  const label = kind === "txt" ? "TXT" : kind === "pgs" ? "PGS" : "VOB";
  const title = kind === "txt" ? "Embedded text — extractable to SRT" : "Image subtitle — needs OCR";
  return <span className="rounded px-1 py-0.5 text-[8.5px] font-bold uppercase" style={style} title={title}>{label}</span>;
}
function CoverChip({ l }: { l: SubLangStatus }) {
  if (l.have) return <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--good-soft, rgba(127,176,105,.16))", color: "var(--good)" }} title="External SRT present">✓ {l.lang}</span>;
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }} title={`Missing — will ${SOURCE_LABEL[l.source ?? "ai"] ?? l.source}`}>{l.lang} · {SOURCE_LABEL[l.source ?? "ai"] ?? l.source}</span>;
}

/* ============================= LOGS ============================= */
function LogsConsole() {
  const [lines, setLines] = useState<{ at: number; level: string; msg: string }[]>([]);
  const [follow, setFollow] = useState(true);
  const boxRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    let alive = true;
    const tick = () => api.subtitleLogs().then((l) => {
      if (!alive) return;
      setLines((prev) => {
        const a = prev[prev.length - 1], b = l[l.length - 1];
        if (prev.length === l.length && a?.at === b?.at && a?.msg === b?.msg) return prev;
        return l;
      });
    }).catch(() => {});
    tick();
    const t = setInterval(tick, 1500);
    return () => { alive = false; clearInterval(t); };
  }, []);
  useEffect(() => { if (follow && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight; }, [lines, follow]);
  const tone = (lvl: string) => (lvl === "error" ? "var(--reject)" : lvl === "warn" ? "var(--avoid)" : "var(--ink-dim)");
  const clock = (at: number) => new Date(at * 1000).toLocaleTimeString();
  return (
    <div className={card} style={cardStyle}>
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[14px] font-bold">Activity log</div>
        <label className="flex items-center gap-1.5 text-[11px] text-ink-faint"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Auto-scroll</label>
      </div>
      <div ref={boxRef} className="thin-scroll max-h-[62vh] overflow-y-auto rounded-lg p-3 font-mono text-[11.5px] leading-relaxed" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        {lines.length === 0 ? (
          <div className="text-ink-faint">No activity yet. Queue a file and it'll stream here.</div>
        ) : lines.map((l, i) => (
          <div key={i} className="flex gap-2"><span className="flex-none text-ink-faint">{clock(l.at)}</span><span style={{ color: tone(l.level) }}>{l.msg}</span></div>
        ))}
      </div>
    </div>
  );
}

/* ============================= SETTINGS ============================= */
function SettingsTab({ settings, onPatch, flash }: { settings: SubtitleSettings; onPatch: (b: { movies_auto?: boolean; series_auto?: boolean; languages?: string[] }) => void; flash: (m: string) => void }) {
  const toggleLang = (code: string) => {
    const has = settings.languages.includes(code);
    const next = has ? settings.languages.filter((l) => l !== code) : [...settings.languages, code];
    if (next.length === 0) { flash("Keep at least one language."); return; }
    onPatch({ languages: next });
  };
  return (
    <div className="flex flex-col gap-4">
      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">Kept languages</div>
        <div className="mt-0.5 text-[11.5px] text-ink-faint">Arrmada keeps an external <code>.srt</code> for each of these; other languages are stripped from the video (once stripping ships). Click to toggle.</div>
        <div className="mt-3 flex flex-wrap gap-1.5">
          {LANGS.map((l) => {
            const on = settings.languages.includes(l.code);
            return <button key={l.code} onClick={() => toggleLang(l.code)} className="rounded-full px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel-2)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>{l.name}</button>;
          })}
        </div>
      </div>

      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">Automatic</div>
        <div className="mt-0.5 text-[11.5px] text-ink-faint">When on, every new download gets its subtitles as soon as it imports, and a sweep every six hours catches anything still missing (including releases whose subtitles appeared later). Off = run it yourself from the Library.</div>
        <div className="mt-3 flex flex-col gap-2.5">
          <ToggleRow label="Auto-ensure movies" on={settings.movies_auto} onToggle={(v) => onPatch({ movies_auto: v })} />
          <ToggleRow label="Auto-ensure series" on={settings.series_auto} onToggle={(v) => onPatch({ series_auto: v })} />
        </div>
      </div>

      <LocalAI flash={flash} backend={settings.ai_backend} note={settings.ai_note} />

      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">OpenSubtitles (optional download source)</div>
        <div className="mt-0.5 text-[11.5px] text-ink-faint">A download source Arrmada tries before AI. Optional — embedded extraction and (soon) AI work without it.</div>
        <div className="mt-3 flex items-center gap-2 text-[12px]">
          <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: settings.can_download ? "var(--good-soft, rgba(127,176,105,.16))" : settings.provider_ready ? "var(--avoid-soft)" : "var(--panel-2)", color: settings.can_download ? "var(--good)" : settings.provider_ready ? "var(--avoid)" : "var(--ink-faint)" }}>
            {settings.can_download ? "ready" : settings.provider_ready ? "search only" : "not configured"}
          </span>
          <span className="text-ink-dim">
            {settings.can_download
              ? settings.quota_reset_at > 0
                ? `Daily download quota used up — resumes ${new Date(settings.quota_reset_at * 1000).toLocaleString()}.`
                : settings.quota_remaining >= 0
                  ? `Downloading enabled · ${settings.quota_remaining} downloads left today.`
                  : "Downloading enabled."
              : settings.provider_ready
                ? "Searching works; add your OpenSubtitles username and password under Settings → API keys to download."
                : "Add an OpenSubtitles API key (free) under Settings → API keys, plus your account username and password to download."}
          </span>
        </div>
      </div>
    </div>
  );
}
function LocalAI({ flash, backend, note }: { flash: (m: string) => void; backend?: string; note?: string }) {
  const [status, setStatus] = useState<WhisperStatus | null>(null);
  const load = useCallback(() => api.subtitleModels().then(setStatus).catch(() => setStatus(null)), []);
  useEffect(() => {
    load();
    const t = setInterval(load, 3000); // reflect download progress
    return () => clearInterval(t);
  }, [load]);
  const download = async (name: string) => {
    try { await api.subtitleDownloadModel(name); flash("Downloading — watch the Logs tab for progress."); load(); }
    catch (e) { flash((e as Error).message); }
  };
  const st = status;
  return (
    <div className={card} style={cardStyle}>
      <div className="flex items-center justify-between">
        <div className="text-[14px] font-bold">Local AI (whisper)</div>
        <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: st?.ready ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--panel-2)", color: st?.ready ? "var(--good)" : "var(--ink-faint)" }}>
          {st?.ready ? "ready" : st?.binary_ready ? "needs a model" : "not installed"}
        </span>
      </div>
      <div className="mt-0.5 text-[11.5px] text-ink-faint">Generates subtitles from a file's audio when no better source exists — 100% local, no key. Download a model to enable it (large files, runs in the background).</div>
      {/* Which compute backend the last run used. A Vulkan build with no visible device runs
          on the CPU without complaint, so "ready" alone can hide a GPU that isn't being used. */}
      {st?.ready && (
        <div className="mt-1.5 text-[11.5px]" style={{ color: backend === "vulkan" || backend === "sycl" ? "var(--good)" : "var(--ink-dim)" }}>
          {backend === "sycl"
            ? "Running on the GPU (Intel oneAPI)."
            : backend === "vulkan"
            ? "Running on the GPU (Vulkan)."
            : backend === "cpu"
              ? "Running on the CPU. If this host has a GPU, check that /dev/dri is passed to the container and vulkaninfo inside it lists a device."
              : "Backend not known yet — it's reported after the first subtitle is generated."}
        </div>
      )}
      {/* The Intel oneAPI build is tried first; when it's set aside, say why rather than
          quietly running slower. */}
      {st?.ready && note && (
        <div className="mt-1 text-[11.5px]" style={{ color: "var(--avoid)" }}>Intel oneAPI build not in use — {note}</div>
      )}
      {st && !st.binary_ready && <div className="mt-2 text-[11.5px]" style={{ color: "var(--avoid)" }}>whisper-cli isn't in this build yet — update to a build that bundles it.</div>}
      <div className="mt-3 flex flex-col gap-2">
        {(st?.models ?? []).map((m) => (
          <div key={m.name} className="flex items-center justify-between gap-3 rounded-lg px-3 py-2" style={{ background: "var(--panel-2)" }}>
            <div className="min-w-0">
              <div className="text-[12px] font-semibold">{m.label}</div>
              <div className="font-mono text-[10px] text-ink-faint">{m.name} · ~{m.size_mb >= 1000 ? `${(m.size_mb / 1000).toFixed(1)} GB` : `${m.size_mb} MB`}</div>
            </div>
            {m.present ? (
              <span className="rounded px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--good-soft, rgba(127,176,105,.16))", color: "var(--good)" }}>installed ✓</span>
            ) : m.downloading ? (
              <span className="rounded px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>downloading…</span>
            ) : (
              <button onClick={() => download(m.name)} disabled={!st?.binary_ready} className="rounded-lg px-3 py-1.5 text-[11px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Download</button>
            )}
          </div>
        ))}
      </div>
      <div className="mt-2 text-[10.5px] text-ink-faint">Get <b>turbo</b> for English, plus <b>large-v3</b> to translate foreign audio to English.</div>
    </div>
  );
}
function ToggleRow({ label, on, onToggle }: { label: string; on: boolean; onToggle: (v: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-[12.5px] font-semibold">{label}</span>
      <button role="switch" aria-checked={on} onClick={() => onToggle(!on)} className="relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors" style={{ background: on ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)" }}>
        <span className="inline-block h-4 w-4 rounded-full bg-white transition-transform" style={{ transform: on ? "translateX(22px)" : "translateX(3px)" }} />
      </button>
    </div>
  );
}

function Pill({ active, disabled, onClick, children }: { active: boolean; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" disabled={disabled} onClick={onClick}
      className="rounded-full px-2.5 py-1 text-[10.5px] font-semibold transition-colors disabled:opacity-40 disabled:cursor-default"
      style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft, rgba(198,93,59,.12))" : "transparent", color: active ? "var(--accent)" : "var(--ink-dim)" }}>
      {children}
    </button>
  );
}
