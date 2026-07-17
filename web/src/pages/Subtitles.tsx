import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type SubtitleSettings, type SubFileEntry, type SubtitleJob, type SubLangStatus } from "../lib/api";

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

        {tab === "overview" && <Overview jobs={jobs} settings={settings} />}
        {tab === "queue" && <Queue jobs={jobs} />}
        {tab === "library" && <Library flash={flash} onQueued={() => api.subtitleJobs().then(setJobs)} />}
        {tab === "logs" && <LogsConsole />}
        {tab === "settings" && settings && <SettingsTab settings={settings} onPatch={patchSettings} flash={flash} />}
      </div>
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

/* ============================= OVERVIEW ============================= */
function Overview({ jobs, settings }: { jobs: SubtitleJob[]; settings: SubtitleSettings | null }) {
  const [cov, setCov] = useState<{ files: number; covered: number; missing: number } | null>(null);
  useEffect(() => {
    let alive = true;
    Promise.all([api.subtitleLibrary("movies"), api.subtitleLibrary("tv")]).then(([m, tv]) => {
      if (!alive) return;
      const all = [...m, ...tv];
      const covered = all.filter((f) => f.missing === 0).length;
      setCov({ files: all.length, covered, missing: all.length - covered });
    }).catch(() => setCov({ files: 0, covered: 0, missing: 0 }));
    return () => { alive = false; };
  }, []);
  const active = jobs.filter((j) => ACTIVE.has(j.state));
  const pct = cov && cov.files ? Math.round((cov.covered / cov.files) * 100) : 0;

  return (
    <div className="flex flex-col gap-3.5">
      <div className="grid gap-3.5" style={{ gridTemplateColumns: "1.1fr 1fr 1fr" }}>
        <div className={card} style={cardStyle}>
          <div className={lbl}>Coverage</div>
          <div className="mt-2 text-[30px] font-extrabold tracking-tight">{cov ? `${pct}%` : "…"}</div>
          <div className="mt-3 border-t pt-3 text-[12px] text-ink-dim" style={{ borderColor: "var(--line-soft)" }}>
            {cov ? <><b style={{ color: "var(--good)" }}>{cov.covered}</b> fully subtitled · <b style={{ color: cov.missing ? "var(--avoid)" : "var(--ink)" }}>{cov.missing}</b> missing a language · {cov.files} files</> : "Scanning your library…"}
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
function Queue({ jobs }: { jobs: SubtitleJob[] }) {
  const active = jobs.filter((j) => ACTIVE.has(j.state));
  const done = jobs.filter((j) => !ACTIVE.has(j.state));
  return (
    <div className="flex flex-col gap-3.5">
      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">Active <span className="font-mono text-[11px] text-ink-faint">{active.length} running</span></div>
        {active.length === 0 ? <div className="mt-2 text-[12px] text-ink-dim">Nothing in the queue right now.</div> : <div className="mt-2 flex flex-col gap-1.5">{active.map((j) => <ActiveRow key={j.id} j={j} />)}</div>}
      </div>
      {done.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="text-[14px] font-bold">Recent</div>
          <div className="mt-2 flex flex-col gap-1.5">
            {done.slice(0, 30).map((j) => (
              <div key={j.id} className="flex items-center gap-2.5 text-[12px]">
                <StateBadge state={j.state} />
                <span className="flex-1 truncate font-semibold">{j.title}</span>
                <span className="text-[11.5px] text-ink-faint">{j.note}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
function ActiveRow({ j }: { j: SubtitleJob }) {
  return (
    <div className="flex items-center gap-2.5 text-[12px]">
      <StateBadge state={j.state} />
      <span className="flex-1 truncate font-semibold">{j.title}</span>
      {j.state === "running" && <span className="h-3 w-3 flex-none animate-spin rounded-full" style={{ border: "2px solid var(--line)", borderTopColor: "var(--accent)" }} />}
    </div>
  );
}
function StateBadge({ state }: { state: string }) {
  const s = state === "done" ? { bg: "var(--good-soft, rgba(127,176,105,.16))", fg: "var(--good)" }
    : state === "failed" ? { bg: "var(--reject-soft)", fg: "var(--reject)" }
    : state === "running" ? { bg: "var(--accent-soft)", fg: "var(--accent)" }
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
  for (const t of f.embedded) {
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
  const [busy, setBusy] = useState<string | null>(null);
  const [queued, setQueued] = useState<Set<string>>(new Set());
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "missing", dir: "desc" });
  const [filters, setFilters] = useState<Set<Filter>>(new Set());

  useEffect(() => {
    setItems(null); setQueued(new Set()); setFilters(new Set());
    api.subtitleLibrary(media).then(setItems).catch(() => setItems([]));
  }, [media]);

  const ensure = async (f: SubFileEntry) => {
    const key = rowKey(f);
    setBusy(key);
    try {
      if (f.kind === "episode") await api.subtitleQueueEpisode(f.series_id!, f.season!, f.episode!);
      else await api.subtitleQueueMovie(f.movie_id!);
      setQueued((q) => new Set(q).add(key));
      flash(`Queued “${f.title}”`);
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
      if (f.external.length > 0) c.external++;
    }
    return c;
  }, [items]);

  const view = useMemo(() => {
    let list = (items ?? []).slice();
    if (filters.has("missing")) list = list.filter((f) => f.missing > 0);
    if (filters.has("pgs")) list = list.filter((f) => { const k = embeddedKinds(f); return k.pgs || k.vob; });
    if (filters.has("text")) list = list.filter((f) => embeddedKinds(f).txt);
    if (filters.has("external")) list = list.filter((f) => f.external.length > 0);
    const dir = sort.dir === "asc" ? 1 : -1;
    const val = (f: SubFileEntry): number | string => {
      switch (sort.key) {
        case "title": return f.title.toLowerCase();
        case "missing": return f.missing;
        case "embedded": return f.embedded.length;
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
      <div className="inline-flex w-fit rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        {(["movies", "tv"] as const).map((m) => (
          <button key={m} onClick={() => setMedia(m)} className="rounded-md px-3.5 py-1.5 text-[12px] font-semibold" style={{ background: media === m ? "var(--accent)" : "transparent", color: media === m ? "var(--accent-ink)" : "var(--ink-faint)" }}>
            {m === "movies" ? "Movies" : "TV Shows"}{items && media === m ? <span className="ml-1.5 font-mono text-[10px] opacity-70">{items.length.toLocaleString()}</span> : null}
          </button>
        ))}
      </div>

      {items === null ? (
        <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Scanning your {noun}…</div>
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
                {view.map((f, i) => {
                  const key = rowKey(f);
                  const k = embeddedKinds(f);
                  return (
                    <tr key={key} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                      <td className="px-3 py-2 font-semibold">{f.title} <span className="font-normal text-ink-faint">{f.year || ""}</span></td>
                      <td className="px-3 py-2 font-mono text-[10.5px] text-ink-dim">{(f.audio_langs ?? []).map((l) => l.toUpperCase()).join(" ") || "—"}</td>
                      <td className="px-3 py-2">
                        {f.embedded.length === 0 ? <span className="text-ink-faint">—</span> : (
                          <div className="flex items-center gap-1">
                            {k.txt && <EmbBadge kind="txt" />}
                            {k.pgs && <EmbBadge kind="pgs" />}
                            {k.vob && <EmbBadge kind="vob" />}
                          </div>
                        )}
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex flex-wrap items-center gap-1">{f.languages.map((l) => <CoverChip key={l.lang} l={l} />)}</div>
                      </td>
                      <td className="px-3 py-2 font-mono text-[10.5px] text-ink-faint">{f.health ? `${f.health.score}%` : "—"}</td>
                      <td className="px-3 py-2">
                        <div className="flex items-center justify-end">
                          {f.missing === 0 ? (
                            <span className="font-mono text-[10.5px]" style={{ color: "var(--good)" }}>complete</span>
                          ) : queued.has(key) ? (
                            <span className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--good)", color: "var(--good)" }}>Queued ✓</span>
                          ) : (
                            <button onClick={() => ensure(f)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{busy === key ? "Queuing…" : "Ensure subs"}</button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
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
        <div className="mt-0.5 text-[11.5px] text-ink-faint">When on, Arrmada periodically ensures missing subtitles in the background. Off = run it yourself from the Library.</div>
        <div className="mt-3 flex flex-col gap-2.5">
          <ToggleRow label="Auto-ensure movies" on={settings.movies_auto} onToggle={(v) => onPatch({ movies_auto: v })} />
          <ToggleRow label="Auto-ensure series" on={settings.series_auto} onToggle={(v) => onPatch({ series_auto: v })} />
        </div>
      </div>

      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">OpenSubtitles (optional download source)</div>
        <div className="mt-0.5 text-[11.5px] text-ink-faint">A download source Arrmada tries before AI. Optional — embedded extraction and (soon) AI work without it.</div>
        <div className="mt-3 flex items-center gap-2 text-[12px]">
          <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: settings.can_download ? "var(--good-soft, rgba(127,176,105,.16))" : settings.provider_ready ? "var(--avoid-soft)" : "var(--panel-2)", color: settings.can_download ? "var(--good)" : settings.provider_ready ? "var(--avoid)" : "var(--ink-faint)" }}>
            {settings.can_download ? "ready" : settings.provider_ready ? "search only" : "not configured"}
          </span>
          <span className="text-ink-dim">
            {settings.can_download ? "Downloading enabled." : "Set ARRMADA_OPENSUBTITLES_API_KEY (+ USERNAME/PASSWORD to download) in .env. In-app credentials are coming."}
          </span>
        </div>
      </div>
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
