import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type ConvertCandidate, type ConvertEncoder, type ConvertJob, type ConvertSample, type AppSettings } from "../lib/api";

// Convert (Tdarr replacement) — the four-tab experience from the design mockup, wired to
// the real backend. Implemented today: analysis, hardware detection, the Save-space engine
// (safe encode→verify→replace), and the rules engine. Steps the mockup shows that are still
// on the roadmap (audio/sub/HDR actions, VMAF gate, 30s sample) are marked as such — see
// CONVERT-BUILD-PLAN.md.
type Tab = "overview" | "queue" | "library" | "logs" | "settings";
const ACTIVE = new Set(["queued", "encoding", "verifying", "replacing"]);

function fmtSize(b?: number): string {
  if (!b || b <= 0) return "—";
  const tb = b / 1024 ** 4;
  if (tb >= 1) return `${tb.toFixed(2)} TB`;
  const gb = b / 1024 ** 3;
  return gb >= 1 ? `${gb.toFixed(1)} GB` : `${(b / 1024 ** 2).toFixed(0)} MB`;
}
function codecClass(c?: string): "h264" | "hevc" | "av1" | "other" {
  if (c === "h264") return "h264";
  if (c === "hevc") return "hevc";
  if (c === "av1") return "av1";
  return "other";
}

export function Convert() {
  const [tab, setTab] = useState<Tab>("overview");
  const [hw, setHw] = useState<{ selected: ConvertEncoder; encoders: ConvertEncoder[]; reclaimed_bytes: number } | null>(null);
  const [items, setItems] = useState<ConvertCandidate[] | null>(null);
  const [jobs, setJobs] = useState<ConvertJob[]>([]);
  const [toast, setToast] = useState<string | null>(null);
  const flash = useCallback((m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); }, []);

  const loadLibrary = useCallback(() => api.convertLibrary().then(setItems).catch(() => setItems([])), []);
  const loadHw = useCallback(() => api.convertHardware().then(setHw).catch(() => {}), []);
  useEffect(() => { loadHw(); loadLibrary(); }, [loadHw, loadLibrary]);

  const anyActive = jobs.some((j) => ACTIVE.has(j.state));
  useEffect(() => {
    let alive = true;
    const tick = () => api.convertJobs().then((j) => { if (alive) setJobs(j); }).catch(() => {});
    tick();
    // Poll quickly while something is encoding (so the % bar feels live), and back
    // off when idle to spare the server.
    const t = setInterval(tick, anyActive ? 1000 : 3000);
    return () => { alive = false; clearInterval(t); };
  }, [anyActive]);
  useEffect(() => { if (!anyActive) { loadLibrary(); loadHw(); } }, [anyActive, loadLibrary, loadHw]);

  const enc = hw?.selected;
  const activeCount = jobs.filter((j) => ACTIVE.has(j.state)).length;
  const candidates = (items ?? []).filter((c) => c.candidate).length;
  const convertAll = async () => { try { await api.convertSweep(); flash(candidates ? `Converting ${candidates} file${candidates === 1 ? "" : "s"}…` : "Nothing to convert — everything's already your target codec."); setTab("queue"); } catch (e) { flash((e as Error).message); } };

  const TABS: { key: Tab; label: string; n?: string }[] = [
    { key: "overview", label: "Overview" },
    { key: "queue", label: "Queue", n: activeCount ? `${activeCount} active` : undefined },
    { key: "library", label: "Library", n: items ? items.length.toLocaleString() : undefined },
    { key: "logs", label: "Logs" },
    { key: "settings", label: "Settings" },
  ];

  return (
    <>
      <PageHeader title="Convert" crumb="Library / Convert" />
      <div className="mx-auto w-full max-w-[1240px] px-4 py-6 sm:px-6">
        {/* Header */}
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <p className="max-w-[62ch] text-[12.5px] text-ink-dim">Shrink your Movies &amp; TV to a modern codec (HEVC or AV1) at maximum quality — your HDR, Dolby Vision and surround audio kept intact. Choose your format in <b>Settings</b>, then convert.</p>
          <div className="flex items-center gap-2">
            <span className="inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-[12px] font-semibold" style={{ border: `1px solid ${enc?.hardware ? "var(--good)" : "var(--avoid)"}`, background: enc?.hardware ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--avoid-soft)" }}>
              <span className="h-2 w-2 rounded-full" style={{ background: enc?.hardware ? "var(--good)" : "var(--avoid)" }} />
              {enc ? `${enc.label}${enc.hardware ? " · GPU ready" : " · CPU"}` : "Detecting…"}
            </span>
            <button onClick={() => setTab("settings")} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Settings</button>
            <button onClick={convertAll} disabled={candidates === 0} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Convert all{candidates ? ` (${candidates})` : ""}</button>
          </div>
        </div>

        {/* Tabs */}
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

        {tab === "overview" && <Overview hw={hw} items={items} jobs={jobs} />}
        {tab === "queue" && <Queue jobs={jobs} />}
        {tab === "library" && <Library flash={flash} onQueued={() => api.convertJobs().then(setJobs)} />}
        {tab === "logs" && <LogsConsole />}
        {tab === "settings" && <ConvertSettings flash={flash} />}
      </div>
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

const card = "rounded-xl p-4";
const cardStyle = { border: "1px solid var(--line)", background: "var(--panel)" } as const;
const lbl = "font-mono text-[9.5px] font-bold uppercase tracking-[0.11em] text-ink-faint";

/* ============================= OVERVIEW ============================= */
function Overview({ hw, items, jobs }: { hw: { selected: ConvertEncoder; encoders: ConvertEncoder[]; reclaimed_bytes: number } | null; items: ConvertCandidate[] | null; jobs: ConvertJob[] }) {
  const list = items ?? [];
  const breakdown = useMemo(() => {
    const b = { h264: 0, hevc: 0, av1: 0, other: 0 };
    let n = 0;
    for (const c of list) { if (c.info) { b[codecClass(c.info.video_codec)]++; n++; } }
    return { b, n };
  }, [list]);
  const candidates = list.filter((c) => c.candidate);
  const opportunity = candidates.reduce((n, c) => n + Math.max(0, (c.info?.size_bytes ?? 0) - c.est_bytes), 0);
  const pct = (x: number) => (breakdown.n ? Math.round((x / breakdown.n) * 100) : 0);
  const activeJobs = jobs.filter((j) => ACTIVE.has(j.state));

  return (
    <div className="flex flex-col gap-3.5">
      <div className="grid gap-3.5" style={{ gridTemplateColumns: "1.1fr 1fr 0.95fr" }}>
        {/* reclaimed */}
        <div className={card} style={cardStyle}>
          <div className={lbl}>Space reclaimed</div>
          <div className="mt-2 text-[30px] font-extrabold tracking-tight">{fmtSize(hw?.reclaimed_bytes)}</div>
          <div className="mt-3 border-t pt-3 text-[12px] text-ink-dim" style={{ borderColor: "var(--line-soft)" }}>
            <span style={{ color: "var(--good)" }}>~{fmtSize(opportunity)}</span> more reclaimable · <b style={{ color: "var(--ink)" }}>{candidates.length}</b> of {list.length} movies still not HEVC/AV1
          </div>
        </div>
        {/* codec breakdown */}
        <div className={card} style={cardStyle}>
          <div className={lbl}>Library video codecs</div>
          <div className="mt-2.5 flex h-4 overflow-hidden rounded-md" style={{ border: "1px solid var(--line)" }}>
            {breakdown.b.h264 > 0 && <span style={{ width: `${pct(breakdown.b.h264)}%`, background: "var(--avoid)" }} />}
            {breakdown.b.hevc > 0 && <span style={{ width: `${pct(breakdown.b.hevc)}%`, background: "var(--good)" }} />}
            {breakdown.b.av1 > 0 && <span style={{ width: `${pct(breakdown.b.av1)}%`, background: "var(--accent)" }} />}
            {breakdown.b.other > 0 && <span style={{ width: `${pct(breakdown.b.other)}%`, background: "var(--ink-faint)" }} />}
          </div>
          <div className="mt-3 flex flex-wrap gap-x-3.5 gap-y-1.5 text-[11.5px] text-ink-dim">
            <Legend c="var(--avoid)" label={`H.264 · ${pct(breakdown.b.h264)}%`} />
            <Legend c="var(--good)" label={`HEVC · ${pct(breakdown.b.hevc)}%`} />
            <Legend c="var(--accent)" label={`AV1 · ${pct(breakdown.b.av1)}%`} />
            <Legend c="var(--ink-faint)" label={`Other · ${pct(breakdown.b.other)}%`} />
          </div>
        </div>
        {/* hardware */}
        <div className={card} style={cardStyle}>
          <div className={lbl}>Hardware</div>
          <div className="mt-2 flex items-center justify-between">
            <span className="text-[13px] font-bold">{hw?.selected.label ?? "…"}</span>
            <span className="rounded-full px-2 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: hw?.selected.hardware ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--avoid-soft)", color: hw?.selected.hardware ? "var(--good)" : "var(--avoid)" }}>{hw?.selected.hardware ? "GPU" : "CPU"}</span>
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {(hw?.encoders ?? []).map((e) => (
              <span key={e.name} className="rounded border px-1.5 py-0.5 font-mono text-[10px]" style={{ borderColor: e.available ? "var(--good)" : "var(--line)", color: e.available ? "var(--good)" : "var(--ink-faint)" }}>{e.label}</span>
            ))}
          </div>
          {!hw?.selected.hardware && <div className="mt-2.5 text-[11px] text-ink-faint">No GPU render device found. On a Linux host, pass <span className="font-mono">/dev/dri</span> through for AMD/Intel hardware encoding.</div>}
        </div>
      </div>

      {/* safeguards */}
      <div className={card} style={cardStyle}>
        <div className="h2 text-[14px] font-bold">Safeguards &amp; storage</div>
        <div className="mt-0.5 text-[11.5px] text-ink-faint">The protections that keep Convert from ever damaging a file.</div>
        <div className="mt-3 grid gap-x-4 gap-y-3" style={{ gridTemplateColumns: "repeat(3, 1fr)" }}>
          <Safeguard ic="🔗" t="Seeding-safe" d="Never touches a file that's still seeding, and never breaks a hardlink to your torrents." on />
          <Safeguard ic="🩹" t="Verify & recycle" d="Stream/duration check before replacing · original moved to the recycle bin." on />
          <Safeguard ic="💽" t="Scratch + space guard" d="Encodes to a scratch dir and checks free space before every job." on />
          <Safeguard ic="🏷" t="Renames & re-tags" d="Updates the filename & library record so quality/upgrade logic stays correct." on />
          <Safeguard ic="📺" t="Streaming-aware" d="Pause a file that's being played (arrives with the media-server integration)." on={false} />
          <Safeguard ic="🛡" t="Quality gate (SSIM)" d="Re-encode at higher quality, then keep the original, if a transcode scores too low. Enable it in Settings." on={true} />
        </div>
      </div>

      {/* encoding now */}
      {activeJobs.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="text-[14px] font-bold">Encoding now</div>
          <div className="mt-2 flex flex-col gap-2.5">
            {activeJobs.map((j) => <JobBar key={j.id} j={j} />)}
          </div>
        </div>
      )}
    </div>
  );
}
function Legend({ c, label }: { c: string; label: string }) {
  return <span className="inline-flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-sm" style={{ background: c }} />{label}</span>;
}
function Safeguard({ ic, t, d, on }: { ic: string; t: string; d: string; on: boolean }) {
  return (
    <div className="flex gap-2.5" style={{ opacity: on ? 1 : 0.5 }}>
      <span className="text-[15px] leading-none">{ic}</span>
      <div className="text-[11.5px]"><b className="font-semibold">{t}</b>{!on && <span className="ml-1.5 font-mono text-[9px] uppercase text-ink-faint">soon</span>}<div className="text-ink-faint">{d}</div></div>
    </div>
  );
}

/* ============================= RULES ============================= */
const inp = "rounded-lg px-2.5 py-1.5 text-[12px]";
const inpStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

function ActToggle({ on, set, label, hint }: { on: boolean; set: (v: boolean) => void; label: string; hint: string }) {
  return (
    <label className="flex cursor-pointer items-start gap-2 text-[12px]">
      <input type="checkbox" checked={on} onChange={(e) => set(e.target.checked)} className="mt-0.5" />
      <span><b className="font-semibold">{label}</b><span className="block text-[10.5px] text-ink-faint">{hint}</span></span>
    </label>
  );
}

// Warn is an inline amber caution.
function Warn({ children }: { children: ReactNode }) {
  return <div className="flex items-start gap-1.5 rounded-md px-2 py-1.5 text-[10.5px] leading-snug" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }}><span className="flex-none">⚠</span><span>{children}</span></div>;
}

/* ============================= LOGS CONSOLE ============================= */
function LogsConsole() {
  const [lines, setLines] = useState<{ at: number; level: string; msg: string }[]>([]);
  const [follow, setFollow] = useState(true);
  const boxRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let alive = true;
    const tick = () => api.convertLogs().then((l) => { if (alive) setLines(l); }).catch(() => {});
    tick();
    const t = setInterval(tick, 1500);
    return () => { alive = false; clearInterval(t); };
  }, []);

  useEffect(() => {
    if (follow && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight;
  }, [lines, follow]);

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
          <div className="text-ink-faint">No activity yet. Queue a conversion and it'll stream here.</div>
        ) : lines.map((l, i) => (
          <div key={i} className="flex gap-2">
            <span className="flex-none text-ink-faint">{clock(l.at)}</span>
            <span style={{ color: tone(l.level) }}>{l.msg}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function Queue({ jobs }: { jobs: ConvertJob[] }) {
  const active = jobs.filter((j) => ACTIVE.has(j.state));
  const done = jobs.filter((j) => !ACTIVE.has(j.state));
  return (
    <div className="flex flex-col gap-3.5">
      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">Active <span className="font-mono text-[11px] text-ink-faint">{active.length} running</span></div>
        {active.length === 0 ? <div className="mt-2 text-[12px] text-ink-dim">Nothing converting right now.</div> : <div className="mt-2 flex flex-col gap-3">{active.map((j) => <JobBar key={j.id} j={j} rich />)}</div>}
      </div>
      {done.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="text-[14px] font-bold">Recent</div>
          <div className="mt-2 flex flex-col gap-1.5">
            {done.slice(0, 20).map((j) => (
              <div key={j.id} className="flex items-center gap-2.5 text-[12px]">
                <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: j.state === "done" ? "var(--good-soft, rgba(127,176,105,.16))" : j.state === "failed" ? "var(--reject-soft)" : "var(--panel-2)", color: j.state === "done" ? "var(--good)" : j.state === "failed" ? "var(--reject)" : "var(--ink-faint)" }}>{j.state}</span>
                <span className="flex-1 truncate font-semibold">{j.title}</span>
                {j.state === "done" ? <span className="font-mono text-ink-dim">{fmtSize(j.src_bytes)} → {fmtSize(j.out_bytes)} <span style={{ color: "var(--good)" }}>−{Math.round((1 - j.out_bytes / j.src_bytes) * 100)}%</span></span> : <span className="text-[11.5px] text-ink-faint">{j.note}</span>}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
function fmtEta(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return "—";
  if (sec < 60) return `${Math.round(sec)}s`;
  const m = Math.floor(sec / 60), s = Math.round(sec % 60);
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}
function JobBar({ j, rich }: { j: ConvertJob; rich?: boolean }) {
  const encoding = j.state === "encoding";
  const pct = Math.max(0, Math.min(100, j.progress * 100));
  const eta = encoding && j.duration_sec && j.speed_x > 0 && j.progress < 1 ? (j.duration_sec * (1 - j.progress)) / j.speed_x : 0;
  return (
    <div>
      <div className="flex items-center justify-between text-[12px]">
        <span className="font-semibold">{j.title} <span className="ml-1.5 rounded-full px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{j.encoder}</span></span>
        <span className="font-mono tabular-nums text-ink-dim">{encoding ? `${pct.toFixed(1)}%${eta > 0 ? ` · ${fmtEta(eta)} left` : ""}` : j.state}</span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        {/* transition ~matches the poll cadence so the fill glides instead of jumping */}
        <div className="h-full" style={{ width: `${pct}%`, background: "linear-gradient(90deg, var(--accent-deep), var(--accent))", transition: "width 1s linear" }} />
      </div>
      {rich && encoding && <div className="mt-1 flex gap-4 font-mono text-[11px] text-ink-faint"><span><b className="text-ink">{Math.round(j.fps)}</b> fps</span><span><b className="text-ink">{j.speed_x.toFixed(1)}×</b> realtime</span><span>{fmtSize(j.src_bytes)} source</span></div>}
    </div>
  );
}

/* ============================= LIBRARY ============================= */
function rowKey(c: ConvertCandidate): string {
  return c.kind === "episode" ? `e:${c.series_id}:${c.season}:${c.episode}` : `m:${c.movie_id}`;
}

function Library({ flash, onQueued }: { flash: (m: string) => void; onQueued: () => void }) {
  const [media, setMedia] = useState<"movies" | "tv">("movies");
  const [items, setItems] = useState<ConvertCandidate[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [queued, setQueued] = useState<Set<string>>(new Set());
  const [sampling, setSampling] = useState<number | null>(null);
  const [samples, setSamples] = useState<Record<number, ConvertSample>>({});

  useEffect(() => {
    setItems(null);
    setQueued(new Set());
    api.convertLibrary(media).then(setItems).catch(() => setItems([]));
  }, [media]);

  const convert = async (c: ConvertCandidate) => {
    const key = rowKey(c);
    setBusy(key);
    try {
      if (c.kind === "episode") await api.convertEpisode(c.series_id!, c.season!, c.episode!);
      else await api.convertMovie(c.movie_id!);
      setQueued((q) => new Set(q).add(key));
      flash(`Queued “${c.title}”`);
      onQueued();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const runSample = async (c: ConvertCandidate) => {
    if (c.kind !== "movie" || c.movie_id == null) return; // 30s sample is a movie-only tool for now
    const id = c.movie_id;
    setSampling(id);
    flash(`Encoding a 30s test of “${c.title}” at your quality — this takes a minute…`);
    try { const r = await api.convertSampleMovie(id); setSamples((s) => ({ ...s, [id]: r })); }
    catch (e) { flash((e as Error).message); } finally { setSampling(null); }
  };

  const noun = media === "tv" ? "episodes" : "movies";
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
        <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Analyzing your {noun}…</div>
      ) : items.length === 0 ? (
        <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>No downloaded {noun} yet.</div>
      ) : (
        <>
          <p className="text-[11px] text-ink-faint">“Est. after” is a rough heuristic. For a real number, run <b>Test 30s</b> — it encodes a 30-second slice at your exact quality and measures the result{media === "tv" ? " (movies only for now)" : ""}.</p>
          <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
            <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 900 }}>
              <thead><tr style={{ background: "var(--panel-2)" }}>{["Title", "Video", "Res", "HDR", "Bitrate", "Audio", "Subs", "Size", "Est. after", ""].map((h) => <th key={h} className="px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{h}</th>)}</tr></thead>
              <tbody>
                {items.map((c, i) => {
                  const eff = !c.candidate;
                  const cd = c.info?.video_codec?.toUpperCase() ?? "?";
                  const key = rowKey(c);
                  const sm = c.movie_id != null ? samples[c.movie_id] : undefined;
                  return (
                    <tr key={key} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                      <td className="px-3 py-2 font-semibold">{c.title} <span className="font-normal text-ink-faint">{c.year || ""}</span></td>
                      <td className="px-3 py-2"><span className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: eff ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--avoid-soft)", color: eff ? "var(--good)" : "var(--avoid)" }}>{cd}</span></td>
                      <td className="px-3 py-2 font-mono text-ink-dim">{c.info?.resolution ?? "—"}</td>
                      <td className="px-3 py-2 font-mono text-ink-dim">{c.info?.hdr && c.info.hdr !== "SDR" ? c.info.hdr : "—"}</td>
                      <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{c.info?.bitrate_kbps ? `${(c.info.bitrate_kbps / 1000).toFixed(0)} Mb/s` : "—"}</td>
                      <td className="px-3 py-2 font-mono text-ink-dim">{c.info?.audio_tracks ?? "—"}</td>
                      <td className="px-3 py-2 font-mono text-ink-dim">{c.info?.sub_tracks ?? "—"}</td>
                      <td className="px-3 py-2 font-mono tabular-nums">{fmtSize(c.info?.size_bytes)}</td>
                      <td className="px-3 py-2 font-mono tabular-nums">
                        {sm ? (
                          <span title={`Measured from a real ${sm.sample_sec}s encode`} style={{ color: sm.percent > 3 ? "var(--good)" : "var(--avoid)" }}>{fmtSize(sm.est_bytes)} <span className="text-[9.5px] font-bold uppercase" style={{ color: "var(--good)" }}>·meas</span></span>
                        ) : c.candidate ? (
                          <span title="Rough heuristic — run Test 30s for a real number" style={{ color: "var(--ink-faint)" }}>~{fmtSize(c.est_bytes)}</span>
                        ) : <span className="text-ink-faint">—</span>}
                      </td>
                      <td className="px-3 py-2">
                        {c.candidate ? (
                          <div className="flex items-center justify-end gap-1.5">
                            {c.kind === "movie" && !queued.has(key) && <button onClick={() => runSample(c)} disabled={sampling !== null} title="Encode a 30s slice at your quality and measure the real result" className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{sampling === c.movie_id ? "Testing…" : "Test 30s"}</button>}
                            {queued.has(key) ? (
                              <span className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--good)", color: "var(--good)" }}>Queued ✓</span>
                            ) : (
                              <button onClick={() => convert(c)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{busy === key ? "Queuing…" : "Convert"}</button>
                            )}
                          </div>
                        ) : <div className="text-right"><span className="font-mono text-[10.5px] text-ink-faint">efficient</span></div>}
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

// ConvertSettings is the Convert module's settings tab (C4): the quality gate, sweep automation
// (workers, off-hours window, failure blocklist), and the default transcode options used by the
// per-movie button + Save-space preset. Rules carry their own actions and ignore these defaults.
// FormatCard is a big selectable tile for the target codec, with plain-English education.
function FormatCard({ id, sel, on, title, tag, pros, note }: { id: string; sel: string; on: () => void; title: string; tag: string; pros: string[]; note?: string }) {
  const active = sel === id;
  return (
    <button type="button" onClick={on} className="flex flex-col gap-2 rounded-xl p-4 text-left transition-colors" style={{ border: `1.5px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft, rgba(198,93,59,.08))" : "var(--panel-2)" }}>
      <div className="flex items-center justify-between">
        <span className="text-[15px] font-bold">{title}</span>
        <span className="h-4 w-4 flex-none rounded-full" style={{ border: `2px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent)" : "transparent" }} />
      </div>
      <span className="text-[11px] font-medium text-ink-dim">{tag}</span>
      <ul className="mt-0.5 flex flex-col gap-1">
        {pros.map((p) => <li key={p} className="flex items-start gap-1.5 text-[11.5px] text-ink-dim"><span className="flex-none" style={{ color: "var(--good)" }}>✓</span>{p}</li>)}
      </ul>
      {note && <span className="mt-0.5 text-[10.5px] leading-snug text-ink-faint">{note}</span>}
    </button>
  );
}

function ConvertSettings({ flash }: { flash: (m: string) => void }) {
  const [saved, setSaved] = useState<AppSettings | null>(null);
  const [d, setD] = useState<AppSettings | null>(null); // working draft
  const [busy, setBusy] = useState(false);
  const [scratch, setScratch] = useState<{ dir: string; free: number } | null>(null);
  const [devices, setDevices] = useState<{ path: string; pci: string; vendor: string }[]>([]);
  // Load once on mount only. Depending on `flash` (a new function each parent
  // render) re-ran this every poll tick, overwriting the user's unsaved edits.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { api.settings().then((v) => { setSaved(v); setD(v); }).catch(() => flash("Could not load settings")); }, []);
  const loadScratch = useCallback(() => api.convertHardware().then((h) => { setScratch({ dir: h.scratch_dir, free: h.scratch_free_bytes }); setDevices(h.render_devices ?? []); }).catch(() => {}), []);
  useEffect(() => { loadScratch(); }, [loadScratch]);
  const set = (patch: Partial<AppSettings>) => setD((cur) => (cur ? { ...cur, ...patch } : cur));
  const dirty = useMemo(() => {
    if (!saved || !d) return false;
    const keys: (keyof AppSettings)[] = ["convert_target_codec", "convert_extract_subs", "convert_auto", "convert_sweep_start", "convert_sweep_end", "convert_workers", "convert_quality_gate", "convert_min_ssim", "convert_max_failures", "convert_skip_hardlinked", "convert_scratch_dir", "convert_vaapi_device"];
    return keys.some((k) => saved[k] !== d[k]);
  }, [saved, d]);
  const onSave = async () => {
    if (!d) return;
    setBusy(true);
    const patch = { convert_target_codec: d.convert_target_codec, convert_extract_subs: d.convert_extract_subs, convert_auto: d.convert_auto, convert_sweep_start: d.convert_sweep_start, convert_sweep_end: d.convert_sweep_end, convert_workers: d.convert_workers, convert_quality_gate: d.convert_quality_gate, convert_min_ssim: d.convert_min_ssim, convert_max_failures: d.convert_max_failures, convert_skip_hardlinked: d.convert_skip_hardlinked, convert_scratch_dir: d.convert_scratch_dir, convert_vaapi_device: d.convert_vaapi_device };
    try { const v = await api.updateSettings(patch); setSaved(v); setD(v); flash("Settings saved — restart or the next job uses the new GPU"); loadScratch(); } catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };
  if (!d) return <div className="text-[12px] text-ink-faint">Loading settings…</div>;
  const av1 = d.convert_target_codec === "av1";

  return (
    <div className="flex flex-col gap-4">
      {/* Target format */}
      <SettingCard title="Target format" desc="Everything not already in this codec gets re-encoded to it at maximum quality. Your video is visually preserved — only the file gets smaller.">
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))" }}>
          <FormatCard id="hevc" sel={d.convert_target_codec} on={() => set({ convert_target_codec: "hevc" })}
            title="HEVC · H.265" tag="The safe default"
            pros={["~50% smaller than H.264 at the same quality", "Plays on virtually everything — TVs, phones, consoles", "Fast to encode", "Full HDR10, HDR10+ & Dolby Vision passthrough"]} />
          <FormatCard id="av1" sel={d.convert_target_codec} on={() => set({ convert_target_codec: "av1" })}
            title="AV1" tag="The most efficient"
            pros={["~30% smaller again than HEVC", "Royalty-free, the modern streaming codec", "Best quality-per-byte available today"]}
            note="Slower to encode. Older TVs & devices (pre-2020) may not play it. HDR/Dolby Vision files are kept in HEVC — AV1 HDR passthrough isn't supported yet, so they're skipped." />
        </div>
        <Warn>Re-encoding is lossy by nature — both formats reproduce the picture visually, but a re-encode is never a bit-for-bit copy of the source. The quality gate below guards against a bad encode. HDR, Dolby Vision and surround audio (Atmos/TrueHD/DTS) are always copied through untouched.</Warn>
      </SettingCard>

      {/* Subtitles */}
      <SettingCard title="Subtitles" desc="Text subtitles can be pulled out into .srt sidecar files next to the video (and removed from the container) so any player can use them.">
        <ActToggle on={d.convert_extract_subs} set={(v) => set({ convert_extract_subs: v })} label="Extract subtitles → SRT sidecars" hint="image-based subs (PGS/VOBSUB) are left in the file" />
      </SettingCard>

      {/* Schedule */}
      <SettingCard title="Schedule" desc="Let Arrmada convert your library automatically in the background, on your terms.">
        <ActToggle on={d.convert_auto} set={(v) => set({ convert_auto: v })} label="Auto-convert new & existing media" hint="scans and converts unattended — otherwise use “Convert all” manually" />
        <SettingField label="Only run between" hint="quiet hours for encoding · leave empty to run any time">
          <span className="flex items-center gap-1.5">
            <input type="time" value={d.convert_sweep_start} onChange={(e) => set({ convert_sweep_start: e.target.value })} className={inp} style={inpStyle} />
            <span className="text-ink-faint">to</span>
            <input type="time" value={d.convert_sweep_end} onChange={(e) => set({ convert_sweep_end: e.target.value })} className={inp} style={inpStyle} />
          </span>
        </SettingField>
        <SettingField label="Convert at once" hint={`${av1 ? "AV1 is heavy — " : ""}keep at 1 for CPU-only, 2–3 with a GPU · applies on restart`}>
          <input type="number" min="1" max="8" value={d.convert_workers} onChange={(e) => set({ convert_workers: e.target.value })} className={`${inp} w-[70px]`} style={inpStyle} />
        </SettingField>
      </SettingCard>

      {/* GPU / VAAPI device — matters when the box has an iGPU + a discrete card */}
      {devices.length > 1 && (
        <SettingCard title="GPU device" desc="This machine has more than one GPU. Pick which render node hardware transcoding runs on — e.g. a discrete Arc card instead of the CPU's integrated graphics. Applies to the next job.">
          <SettingField label="VAAPI device" hint="the discrete card is usually the higher renderD number / a PCIe slot like 03:00.0">
            <select value={d.convert_vaapi_device || ""} onChange={(e) => set({ convert_vaapi_device: e.target.value })} className={`${inp} w-[300px]`} style={inpStyle}>
              <option value="">Default (renderD128)</option>
              {devices.map((dev) => (
                <option key={dev.path} value={dev.path}>{dev.path.replace("/dev/dri/", "")} · {dev.vendor}{dev.pci ? ` · ${dev.pci}` : ""}</option>
              ))}
            </select>
          </SettingField>
        </SettingCard>
      )}

      {/* Storage / transcode dir */}
      <SettingCard title="Transcode directory" desc="The working folder Convert encodes into before moving the finished file into your library. Put it on fast storage (an SSD/NVMe cache pool) — never the array — so the long encode doesn't hammer your parity disks. Leave blank to use the default.">
        <SettingField label="Directory" hint="e.g. /transcode (mount your SSD pool there in compose)">
          <input type="text" value={d.convert_scratch_dir} onChange={(e) => set({ convert_scratch_dir: e.target.value })} placeholder="/transcode" className={`${inp} w-[240px]`} style={inpStyle} />
        </SettingField>
        {scratch && (
          <div className="text-[11px] text-ink-faint">
            Currently using <span className="font-mono text-ink-dim">{scratch.dir}</span> · <b style={{ color: scratch.free > 20 * 1024 ** 3 ? "var(--good)" : "var(--avoid)" }}>{fmtSize(scratch.free)}</b> free
          </div>
        )}
      </SettingCard>

      {/* Safety */}
      <SettingCard title="Quality safety" desc="Guards that stop a conversion from ever making things worse.">
        <ActToggle on={d.convert_quality_gate} set={(v) => set({ convert_quality_gate: v })} label="Quality gate" hint="scores each encode vs the source; retries higher, keeps the original if it still falls short" />
        <SettingField label="Minimum SSIM" hint="0–1 · 0.95 ≈ visually safe, 0.98 ≈ near-transparent">
          <input type="number" min="0.5" max="1" step="0.01" value={d.convert_min_ssim} onChange={(e) => set({ convert_min_ssim: e.target.value })} className={`${inp} w-[88px]`} style={inpStyle} />
        </SettingField>
        <ActToggle on={d.convert_skip_hardlinked} set={(v) => set({ convert_skip_hardlinked: v })} label="Skip still-seeding files" hint="leaves hardlinked torrents alone so you keep seeding" />
        <SettingField label="Blocklist after" hint="stop retrying a file after this many failed conversions">
          <input type="number" min="1" max="20" value={d.convert_max_failures} onChange={(e) => set({ convert_max_failures: e.target.value })} className={`${inp} w-[70px]`} style={inpStyle} />
        </SettingField>
      </SettingCard>

      <div className="flex items-center justify-end gap-3">
        {dirty && <span className="text-[11.5px] text-ink-faint">Unsaved changes</span>}
        <button onClick={onSave} disabled={!dirty || busy} className="rounded-lg px-4 py-2 text-[13px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Saving…" : "Save settings"}</button>
      </div>
    </div>
  );
}

function SettingCard({ title, desc, children }: { title: string; desc: string; children: ReactNode }) {
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="text-[13.5px] font-bold">{title}</div>
      <div className="mb-3 mt-0.5 text-[11.5px] text-ink-faint">{desc}</div>
      <div className="flex flex-col gap-3">{children}</div>
    </div>
  );
}

function SettingField({ label, hint, children }: { label: string; hint: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div><div className="text-[12px] font-semibold">{label}</div><div className="text-[10.5px] text-ink-faint">{hint}</div></div>
      {children}
    </div>
  );
}
