import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type ConvertCandidate, type ConvertSeriesRollup, type ConvertLibraryStats, type ConvertBlocked, type ConvertEncoder, type ConvertJob, type ConvertSample, type AppSettings } from "../lib/api";

// Convert (Tdarr replacement) — the four-tab experience from the design mockup, wired to
// the real backend. Implemented today: analysis, hardware detection, the Save-space engine
// (safe encode→verify→replace), and the rules engine. Steps the mockup shows that are still
// on the roadmap (audio/sub/HDR actions, VMAF gate, 30s sample) are marked as such.
type Tab = "overview" | "queue" | "library" | "problems" | "logs" | "settings";
const ACTIVE = new Set(["queued", "encoding", "verifying", "replacing"]);
// RUNNING excludes "queued". A whole-library sweep leaves thousands of jobs queued, and
// counting or rendering those as "active" made the Queue tab claim thousands of concurrent
// encodes and paint a progress bar for every one of them.
const RUNNING = new Set(["encoding", "verifying", "replacing"]);

function fmtSize(b?: number): string {
  if (b === undefined || b === null) return "—";
  if (b <= 0) return "0 B"; // a real zero is a fact, not missing data
  const tb = b / 1024 ** 4;
  if (tb >= 1) return `${tb.toFixed(2)} TB`;
  const gb = b / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = b / 1024 ** 2;
  return mb >= 1 ? `${mb.toFixed(0)} MB` : `${(b / 1024).toFixed(0)} KB`;
}

// savedPct is the size reduction of a finished job, or null when it can't be computed —
// health-check jobs never set out_bytes, which used to render as a scary "−100%".
function savedPct(j: ConvertJob): number | null {
  if (!j.src_bytes || j.src_bytes <= 0 || !j.out_bytes || j.out_bytes <= 0) return null;
  return Math.round((1 - j.out_bytes / j.src_bytes) * 100);
}
export function Convert() {
  const [tab, setTab] = useState<Tab>("overview");
  const [hw, setHw] = useState<{ selected: ConvertEncoder; encoders: ConvertEncoder[]; reclaimed_bytes: number } | null>(null);
  const [jobs, setJobs] = useState<ConvertJob[]>([]);
  // Library-wide counts (movies + TV) come from the index in one query, so the Overview no
  // longer has to fetch every movie — and no longer silently ignores thousands of episodes.
  const [stats, setStats] = useState<ConvertLibraryStats | null>(null);
  const [confirmAll, setConfirmAll] = useState(false);
  const [statsErr, setStatsErr] = useState(false);
  const [settings, setSettings] = useState<AppSettings | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const toastTimer = useRef<number | undefined>(undefined);
  const flash = useCallback((m: string) => {
    setToast(m);
    window.clearTimeout(toastTimer.current); // otherwise a second message inherits the first's expiry
    toastTimer.current = window.setTimeout(() => setToast(null), 3500);
  }, []);
  useEffect(() => () => window.clearTimeout(toastTimer.current), []);

  const loadStats = useCallback(
    () => api.convertStats().then((v) => { setStats(v); setStatsErr(false); }).catch(() => setStatsErr(true)),
    []);
  const loadHw = useCallback(() => api.convertHardware().then(setHw).catch(() => {}), []);
  const loadSettings = useCallback(() => api.settings().then(setSettings).catch(() => {}), []);
  useEffect(() => { loadHw(); loadStats(); loadSettings(); }, [loadHw, loadStats, loadSettings]);

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
  useEffect(() => { if (!anyActive) { loadHw(); loadStats(); } }, [anyActive, loadHw, loadStats]);

  const enc = hw?.selected;
  const runningCount = jobs.filter((j) => RUNNING.has(j.state)).length;
  const queuedCount = jobs.filter((j) => j.state === "queued").length;
  // Escape closes the modal and focus lands on its primary action — it was a bare div that
  // trapped nothing and dismissed on nothing but a backdrop click.
  const confirmBtn = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!confirmAll) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setConfirmAll(false); };
    window.addEventListener("keydown", onKey);
    confirmBtn.current?.focus();
    return () => window.removeEventListener("keydown", onKey);
  }, [confirmAll]);

  // Wall-clock estimate from what this machine has actually achieved recently — the single
  // most decision-changing number, and the UI never showed it.
  const etaSeconds = useMemo(() => {
    const measured = jobs.filter((j) => j.state === "done" && j.duration_sec && j.speed_x > 0);
    if (!measured.length || !stats?.total.convertible) return 0;
    const avgSpeed = measured.reduce((n, j) => n + j.speed_x, 0) / measured.length;
    const avgRuntime = measured.reduce((n, j) => n + (j.duration_sec ?? 0), 0) / measured.length;
    const workers = Math.max(1, Number(settings?.convert_workers) || 1);
    return (stats.total.convertible * (avgRuntime / avgSpeed)) / workers;
  }, [jobs, stats, settings]);

  // A whole-library sweep pushes every original into the recycle bin; if that exceeds the
  // configured cap they're pruned by size before the user could restore anything.
  const recycleWarning = useMemo(() => {
    const capGB = Number(settings?.recycle_max_gb);
    if (!capGB || !stats) return false;
    return stats.total.convertible_bytes > capGB * 1024 ** 3;
  }, [settings, stats]);

  // The WHOLE library, not just movies — this button queues TV too.
  const candidates = stats?.total.convertible ?? 0;
  const convertAll = async () => {
    setConfirmAll(false);
    try {
      const r = await api.convertSweep();
      const parts = [r.movies ? `${r.movies.toLocaleString()} movie${r.movies === 1 ? "" : "s"}` : "", r.episodes ? `${r.episodes.toLocaleString()} episode${r.episodes === 1 ? "" : "s"}` : ""].filter(Boolean);
      flash(r.queued ? `Queued ${parts.join(" + ")}${r.blocklisted ? ` · ${r.blocklisted} skipped (repeated failures)` : ""}` : "Nothing to convert — everything's already your target codec.");
      setTab("queue");
    } catch (e) { flash((e as Error).message); }
  };

  const TABS: { key: Tab; label: string; n?: string }[] = [
    { key: "overview", label: "Overview" },
    { key: "queue", label: "Queue", n: runningCount || queuedCount
      ? [runningCount ? `${runningCount} running` : "", queuedCount ? `${queuedCount.toLocaleString()} waiting` : ""].filter(Boolean).join(" · ")
      : undefined },
    { key: "library", label: "Library", n: stats ? stats.total.files.toLocaleString() : undefined },
    { key: "problems", label: "Problems" },
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
            <button onClick={() => setConfirmAll(true)} disabled={candidates === 0} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Convert all{candidates ? ` (${candidates})` : ""}</button>
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

        {statsErr && (
          <div role="status" className="mb-3 flex items-center justify-between gap-3 rounded-lg px-3 py-2 text-[12px]"
            style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
            <span>Couldn't read the library index — these numbers may be stale or missing.</span>
            <button onClick={() => { loadStats(); loadHw(); }} className="rounded px-2 py-1 text-[11.5px] font-semibold" style={{ border: "1px solid var(--avoid)" }}>Retry</button>
          </div>
        )}
        {tab === "overview" && <Overview hw={hw} stats={stats} jobs={jobs} settings={settings} />}
        {tab === "queue" && <Queue jobs={jobs} flash={flash} onChanged={() => { api.convertJobs().then(setJobs).catch(() => {}); loadStats(); }} />}
        {tab === "library" && <Library flash={flash} onQueued={() => api.convertJobs().then(setJobs)} />}
        {tab === "problems" && <Blocklist flash={flash} />}
        {tab === "logs" && <LogsConsole />}
        {tab === "settings" && <ConvertSettings flash={flash} />}
      </div>
      {confirmAll && stats && (
        <div role="dialog" aria-modal="true" aria-labelledby="convert-all-title"
          className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: "rgba(0,0,0,.55)" }} onClick={() => setConfirmAll(false)}>
          <div className="w-full max-w-[460px] rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
            <h3 id="convert-all-title" className="text-[15px] font-bold">Convert your whole library?</h3>
            <p className="mt-1.5 text-[12.5px] leading-relaxed text-ink-dim">
              This queues every file in your library that isn't already in your target codec.
            </p>
            <div className="mt-3 flex flex-col gap-1.5 rounded-lg p-3 font-mono text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
              <Row k="Movies" v={stats.movies.convertible.toLocaleString()} />
              <Row k="Episodes" v={stats.tv.convertible.toLocaleString()} />
              <Row k="Total jobs" v={stats.total.convertible.toLocaleString()} strong />
              <Row k="Space reclaimed" v={`~${fmtSize(stats.total.reclaimable)}`} />
            </div>
            {etaSeconds > 0 && (
              <p className="mt-3 text-[12px] font-semibold" style={{ color: "var(--avoid)" }}>
                Roughly {fmtDuration(etaSeconds)} of encoding at your recent speed
                {settings?.convert_sweep_start && settings.convert_sweep_end
                  ? `, spread across your ${settings.convert_sweep_start}–${settings.convert_sweep_end} window`
                  : ""}.
              </p>
            )}
            {recycleWarning && (
              <p className="mt-2 text-[11.5px] leading-snug" style={{ color: "var(--avoid)" }}>
                ⚠ Originals total ~{fmtSize(stats.total.convertible_bytes)}, more than your{" "}
                {settings?.recycle_max_gb} GB recycle-bin cap — older originals will be pruned before
                you can restore them.
              </p>
            )}
            <p className="mt-3 text-[11.5px] leading-snug text-ink-faint">
              Encoding respects your schedule window — anything queued outside it waits rather
              than starting. Originals go to the recycle bin, a file that keeps failing is
              skipped instead of retried forever, and you can cancel from the Queue tab at any
              time.
            </p>
            <div className="mt-4 flex justify-end gap-2">
              <button onClick={() => setConfirmAll(false)} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
              <button ref={confirmBtn} onClick={convertAll} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
                Queue {stats.total.convertible.toLocaleString()} job{stats.total.convertible === 1 ? "" : "s"}
              </button>
            </div>
          </div>
        </div>
      )}
      {toast && <div role="status" aria-live="polite" className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

const card = "rounded-xl p-4";
const cardStyle = { border: "1px solid var(--line)", background: "var(--panel)" } as const;
const lbl = "font-mono text-[9.5px] font-bold uppercase tracking-[0.11em] text-ink-faint";

/* ============================= OVERVIEW ============================= */
function Overview({ hw, stats, jobs, settings }: { hw: { selected: ConvertEncoder; encoders: ConvertEncoder[]; reclaimed_bytes: number } | null; stats: ConvertLibraryStats | null; jobs: ConvertJob[]; settings: AppSettings | null }) {
  // Which slice of the library the codec bar describes. It covered movies only before, which
  // hid the fact that TV is the overwhelming majority of files.
  const [scope, setScope] = useState<"total" | "movies" | "tv">("total");
  const t = stats?.total;
  const b = stats ? stats[scope] : null;
  const n = b?.files ?? 0;
  const pct = (x: number) => (n ? Math.round((x / n) * 100) : 0);
  const activeJobs = jobs.filter((j) => RUNNING.has(j.state));

  return (
    <div className="flex flex-col gap-3.5">
      <div className="grid gap-3.5" style={{ gridTemplateColumns: "1.1fr 1fr 0.95fr" }}>
        {/* reclaimed */}
        <div className={card} style={cardStyle}>
          <div className={lbl}>Space reclaimed</div>
          <div className="mt-2 text-[30px] font-extrabold tracking-tight">{fmtSize(hw?.reclaimed_bytes)}</div>
          <div className="mt-3 border-t pt-3 text-[12px] text-ink-dim" style={{ borderColor: "var(--line-soft)" }}>
            <span style={{ color: "var(--good)" }}>~{fmtSize(t?.reclaimable)}</span> more reclaimable ·{" "}
            <b style={{ color: "var(--ink)" }}>{(t?.convertible ?? 0).toLocaleString()}</b> of {(t?.files ?? 0).toLocaleString()} files still to convert
            <div className="mt-1 text-[11px] text-ink-faint">
              {(stats?.movies.convertible ?? 0).toLocaleString()} movies · {(stats?.tv.convertible ?? 0).toLocaleString()} episodes
            </div>
          </div>
        </div>
        {/* codec breakdown */}
        <div className={card} style={cardStyle}>
          <div className="flex items-center justify-between">
            <div className={lbl}>Library video codecs</div>
            <div className="inline-flex rounded-md p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
              {(["total", "movies", "tv"] as const).map((k) => (
                <button key={k} onClick={() => setScope(k)} className="rounded px-2 py-0.5 font-mono text-[9.5px] font-bold uppercase"
                  style={{ background: scope === k ? "var(--accent)" : "transparent", color: scope === k ? "var(--accent-ink)" : "var(--ink-faint)" }}>
                  {k === "total" ? "All" : k === "movies" ? "Movies" : "TV"}
                </button>
              ))}
            </div>
          </div>
          <div className="mt-2.5 flex h-4 overflow-hidden rounded-md" style={{ border: "1px solid var(--line)" }}>
            {(b?.h264 ?? 0) > 0 && <span style={{ width: `${pct(b!.h264)}%`, background: "var(--avoid)" }} />}
            {(b?.hevc ?? 0) > 0 && <span style={{ width: `${pct(b!.hevc)}%`, background: "var(--good)" }} />}
            {(b?.av1 ?? 0) > 0 && <span style={{ width: `${pct(b!.av1)}%`, background: "var(--accent)" }} />}
            {(b?.other ?? 0) > 0 && <span style={{ width: `${pct(b!.other)}%`, background: "var(--ink-faint)" }} />}
          </div>
          <div className="mt-3 flex flex-wrap gap-x-3.5 gap-y-1.5 text-[11.5px] text-ink-dim">
            <Legend c="var(--avoid)" label={`H.264 · ${pct(b?.h264 ?? 0)}%`} />
            <Legend c="var(--good)" label={`HEVC · ${pct(b?.hevc ?? 0)}%`} />
            <Legend c="var(--accent)" label={`AV1 · ${pct(b?.av1 ?? 0)}%`} />
            <Legend c="var(--ink-faint)" label={`Other · ${pct(b?.other ?? 0)}%`} />
          </div>
          <div className="mt-2 font-mono text-[10.5px] text-ink-faint">{n.toLocaleString()} files · {fmtSize(b?.total_bytes)}</div>
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
          {/* These reflect the ACTUAL settings. They were hardcoded on, which meant the page
              could promise a protection the user had switched off. */}
          <Safeguard ic="🔗" t="Seeding-safe" on={settings?.convert_skip_hardlinked !== false}
            d={settings?.convert_skip_hardlinked === false
              ? "OFF — Convert may re-encode a file that's still seeding or hardlinked. Enable it in Settings."
              : "Never touches a file that's still seeding, and never breaks a hardlink to your torrents."} />
          <Safeguard ic="🩹" t="Verify & recycle" d="Stream/duration check before replacing · original moved to the recycle bin." on />
          <Safeguard ic="💽" t="Scratch + space guard" d="Encodes to a scratch dir and checks free space before every job." on />
          <Safeguard ic="🏷" t="Renames & re-tags" d="Updates the filename & library record so quality/upgrade logic stays correct." on />
          <Safeguard ic="📺" t="Streaming-aware" d="Pause a file that's being played (arrives with the media-server integration)." on={false} />
          <Safeguard ic="🛡" t="Quality gate (SSIM)" on={settings?.convert_quality_gate !== false}
            d={settings?.convert_quality_gate === false
              ? "OFF — encodes are not scored against the source. Enable it in Settings."
              : `Scores each encode against the source and keeps the original if it falls below ${settings?.convert_min_ssim || "0.98"}.`} />
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
    // Only swap state when the log actually changed (count or newest line) — the history can be
    // up to 5000 lines, so re-rendering every 1.5s poll when nothing's new would be wasteful.
    const tick = () => api.convertLogs().then((l) => {
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
          <div key={`${l.at}-${i}`} className="flex gap-2">
            <span className="flex-none text-ink-faint">{clock(l.at)}</span>
            <span style={{ color: tone(l.level) }}>{l.msg}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function Queue({ jobs, flash, onChanged }: { jobs: ConvertJob[]; flash: (m: string) => void; onChanged: () => void }) {
  const [filter, setFilter] = useState<"all" | "failed">("all");
  const [busy, setBusy] = useState(false);

  const running = jobs.filter((j) => RUNNING.has(j.state));
  const queued = jobs.filter((j) => j.state === "queued");
  const finished = jobs.filter((j) => !ACTIVE.has(j.state));
  // Successes push failures out of a 20-row list within minutes of a sweep starting, and the
  // notes on skipped jobs ("file is hardlinked", "wasn't smaller") are the useful ones.
  const problems = finished.filter((j) => j.state === "failed" || j.state === "skipped");
  const shown = (filter === "failed" ? problems : finished).slice(0, 40);

  const cancel = async (id: number) => {
    try { await api.convertCancel(id); onChanged(); } catch (e) { flash((e as Error).message); }
  };
  const cancelAllQueued = async () => {
    setBusy(true);
    try {
      const { cancelled } = await api.convertCancelQueued();
      flash(`Cancelled ${cancelled.toLocaleString()} queued job${cancelled === 1 ? "" : "s"}`);
      onChanged();
    } catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };

  return (
    <div className="flex flex-col gap-3.5">
      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">Converting now <span className="font-mono text-[11px] text-ink-faint">{running.length}</span></div>
        {running.length === 0
          ? <div className="mt-2 text-[12px] text-ink-dim">Nothing converting right now.</div>
          : <div className="mt-2 flex flex-col gap-3">{running.map((j) => <JobBar key={j.id} j={j} rich onCancel={() => cancel(j.id)} />)}</div>}
      </div>

      {queued.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-[14px] font-bold">
              Waiting <span className="font-mono text-[11px] text-ink-faint">{queued.length.toLocaleString()}</span>
            </div>
            <button onClick={cancelAllQueued} disabled={busy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50"
              style={{ border: "1px solid var(--avoid)", color: "var(--avoid)" }}>
              {busy ? "Cancelling…" : `Cancel all ${queued.length.toLocaleString()} waiting`}
            </button>
          </div>
          {queued[0]?.note && <div className="mt-1.5 text-[11.5px] text-ink-faint">{queued[0].note}</div>}
          {/* Only the head of the queue is listed: rendering thousands of rows is what made
              this tab lock up after a whole-library sweep. */}
          <div className="mt-2 flex flex-col gap-1">
            {queued.slice(0, 12).map((j) => (
              <div key={j.id} className="flex items-center gap-2.5 text-[12px]">
                <span className="flex-1 truncate text-ink-dim">{j.title}</span>
                <button onClick={() => cancel(j.id)} aria-label={`Cancel ${j.title}`}
                  className="rounded px-1.5 py-0.5 font-mono text-[10px] text-ink-faint hover:text-[var(--avoid)]">✕</button>
              </div>
            ))}
            {queued.length > 12 && <div className="text-[11.5px] text-ink-faint">+ {(queued.length - 12).toLocaleString()} more waiting</div>}
          </div>
        </div>
      )}

      {finished.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-[14px] font-bold">Recent</div>
            <div className="flex items-center gap-1.5">
              <Pill active={filter === "all"} onClick={() => setFilter("all")}>All {finished.length}</Pill>
              <Pill active={filter === "failed"} onClick={() => setFilter("failed")}>Problems {problems.length}</Pill>
            </div>
          </div>
          <div className="mt-2 flex flex-col gap-1.5">
            {shown.map((j) => {
              const pct = savedPct(j);
              const tone = j.state === "done" ? "var(--good)" : j.state === "failed" ? "var(--reject)" : "var(--ink-faint)";
              const bg = j.state === "done" ? "var(--good-soft, rgba(127,176,105,.16))" : j.state === "failed" ? "var(--reject-soft)" : "var(--panel-2)";
              return (
                <div key={j.id} className="flex items-center gap-2.5 text-[12px]">
                  <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: bg, color: tone }}>{j.state}</span>
                  <span className="flex-1 truncate font-semibold">{j.title}</span>
                  {j.state === "done" && pct !== null ? (
                    <span className="font-mono text-ink-dim">
                      {fmtSize(j.src_bytes)} → {fmtSize(j.out_bytes)} <span style={{ color: "var(--good)" }}>−{pct}%</span>
                      {j.ssim ? <span className="ml-1.5 text-[10.5px] text-ink-faint">SSIM {j.ssim.toFixed(3)}</span> : null}
                    </span>
                  ) : (
                    <span className="text-[11.5px] text-ink-faint">{j.note || j.state}</span>
                  )}
                </div>
              );
            })}
            {shown.length === 0 && <div className="text-[12px] text-ink-dim">No problems — everything finished cleanly.</div>}
          </div>
        </div>
      )}
    </div>
  );
}
// fmtDuration renders a long span in human terms — used for the whole-library ETA, where the
// answer is often days or weeks and that is exactly the number worth seeing before committing.
// Blocklist surfaces files automation has given up on. They were previously invisible: the
// engine skipped them silently forever and the only hint was a transient toast.
function Blocklist({ flash }: { flash: (m: string) => void }) {
  const [items, setItems] = useState<ConvertBlocked[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const load = useCallback(() => api.convertBlocklist().then(setItems).catch(() => setItems([])), []);
  useEffect(() => { load(); }, [load]);

  const clear = async (key?: string) => {
    setBusy(key ?? "all");
    try {
      await api.convertBlocklistClear(key);
      flash(key ? "Cleared — it'll be retried on the next convert" : "Blocklist cleared");
      load();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  if (items === null) return <div className="text-[12px] text-ink-faint">Loading…</div>;
  if (!items.length) {
    return (
      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">No problem files</div>
        <p className="mt-1 text-[12px] text-ink-dim">Nothing has failed enough times to be skipped by automation.</p>
      </div>
    );
  }
  return (
    <div className={card} style={cardStyle}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-[14px] font-bold">Problem files <span className="font-mono text-[11px] text-ink-faint">{items.length}</span></div>
          <p className="mt-0.5 text-[11.5px] text-ink-dim">
            These kept failing, so bulk converts skip them. Clear one to have it retried.
          </p>
        </div>
        <button onClick={() => clear()} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50"
          style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Clear all</button>
      </div>
      <div className="mt-3 flex flex-col gap-1.5">
        {items.map((b) => (
          <div key={b.key} className="flex items-start gap-2.5 text-[12px]">
            <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>{b.count}×</span>
            <div className="flex-1">
              <div className="font-semibold">{b.title}</div>
              {b.last_error && <div className="text-[11px] leading-snug text-ink-faint">{b.last_error}</div>}
            </div>
            <button onClick={() => clear(b.key)} disabled={busy !== null} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold disabled:opacity-50"
              style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
              {busy === b.key ? "…" : "Retry"}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}

function fmtDuration(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return "—";
  const h = sec / 3600;
  if (h < 1) return `${Math.round(sec / 60)} min`;
  if (h < 48) return `${h.toFixed(h < 10 ? 1 : 0)} hours`;
  const d = h / 24;
  return d < 14 ? `${d.toFixed(1)} days` : `${Math.round(d / 7)} weeks`;
}

function fmtEta(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return "—";
  if (sec < 60) return `${Math.round(sec)}s`;
  const m = Math.floor(sec / 60), s = Math.round(sec % 60);
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}
function JobBar({ j, rich, onCancel }: { j: ConvertJob; rich?: boolean; onCancel?: () => void }) {
  const encoding = j.state === "encoding";
  const pct = Math.max(0, Math.min(100, j.progress * 100));
  const eta = encoding && j.duration_sec && j.speed_x > 0 && j.progress < 1 ? (j.duration_sec * (1 - j.progress)) / j.speed_x : 0;
  return (
    <div>
      <div className="flex items-center justify-between text-[12px]">
        <span className="font-semibold">{j.title} <span className="ml-1.5 rounded-full px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{j.encoder}</span></span>
        <span className="flex items-center gap-2 font-mono tabular-nums text-ink-dim">
          {encoding ? `${pct.toFixed(1)}%${eta > 0 ? ` · ${fmtEta(eta)} left` : ""}` : j.state}
          {onCancel && <button onClick={onCancel} aria-label={`Cancel ${j.title}`} title="Cancel — the original is untouched until a job completes"
            className="rounded px-1.5 py-0.5 text-[10px] hover:text-[var(--avoid)]">✕</button>}
        </span>
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

type SortKey = "title" | "video" | "res" | "bitrate" | "audio" | "subs" | "size" | "est";
const HEADERS: { label: string; key?: SortKey }[] = [
  { label: "Title", key: "title" }, { label: "Video", key: "video" }, { label: "Res", key: "res" }, { label: "HDR" },
  { label: "Bitrate", key: "bitrate" }, { label: "Audio", key: "audio" }, { label: "Subs", key: "subs" },
  { label: "Size", key: "size" }, { label: "Est. after", key: "est" }, { label: "" },
];

// The TV tab's show roll-up is its own table with its own columns, sorted independently
// of the episode table below it.
type ShowSortKey = "title" | "files" | "convertible" | "size" | "est";
const SHOW_HEADERS: { label: string; key?: ShowSortKey; align?: string }[] = [
  { label: "Show", key: "title" }, { label: "Files", key: "files" },
  { label: "Convertible", key: "convertible" }, { label: "Size", key: "size" },
  { label: "Est. after", key: "est" }, { label: "" },
];

// Row is a key/value line in the Convert-all confirmation.
function Row({ k, v, strong }: { k: string; v: string; strong?: boolean }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-ink-faint">{k}</span>
      <span className={strong ? "font-bold" : "text-ink-dim"} style={strong ? { color: "var(--accent)" } : undefined}>{v}</span>
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

function Library({ flash, onQueued }: { flash: (m: string) => void; onQueued: () => void }) {
  const [media, setMedia] = useState<"movies" | "tv">("movies");
  // The TV tab browses shows first and only fetches episodes for the one you open —
  // fetching every episode in the library is what used to make this tab unusable.
  const [shows, setShows] = useState<ConvertSeriesRollup[] | null>(null);
  const [show, setShow] = useState<ConvertSeriesRollup | null>(null);
  const [items, setItems] = useState<ConvertCandidate[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [queued, setQueued] = useState<Set<string>>(new Set());
  const [sampling, setSampling] = useState<number | null>(null);
  const [samples, setSamples] = useState<Record<number, ConvertSample>>({});
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "size", dir: "desc" });
  const [showSort, setShowSort] = useState<{ key: ShowSortKey; dir: "asc" | "desc" }>({ key: "convertible", dir: "desc" });
  const [codecF, setCodecF] = useState<Set<string>>(new Set());
  // Default to hiding already-efficient rows: on a large library most rows are noise, and
  // the "what should I convert" question is the only one this table answers.
  const [onlyConvertible, setOnlyConvertible] = useState(true);
  const [q, setQ] = useState("");
  const [loadErr, setLoadErr] = useState(false);

  useEffect(() => {
    setItems(null);
    setShow(null);
    setQueued(new Set());
    setCodecF(new Set());
    setLoadErr(false);
    if (media === "tv") {
      setShows(null);
      api.convertLibrarySeries().then(setShows).catch(() => { setShows([]); setLoadErr(true); });
    } else {
      api.convertLibrary("movies", undefined, onlyConvertible)
        .then(setItems).catch(() => { setItems([]); setLoadErr(true); });
    }
  }, [media, onlyConvertible]);

  // Opening a show loads just that show's episodes.
  useEffect(() => {
    if (media !== "tv" || !show) return;
    setItems(null);
    setCodecF(new Set());
    setLoadErr(false);
    api.convertLibrary("tv", show.series_id, onlyConvertible)
      .then(setItems).catch(() => { setItems([]); setLoadErr(true); });
  }, [media, show, onlyConvertible]);

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
  // Queue a whole show, or one season of it, in a single call.
  const convertBulk = async (seriesID: number, season: number | undefined, label: string, count: number) => {
    if (count > 1 && !window.confirm(`Queue ${count} episode${count === 1 ? "" : "s"} from ${label}?

Originals move to the recycle bin. You can cancel from the Queue tab.`)) return;
    const key = `bulk:${seriesID}:${season ?? "all"}`;
    setBusy(key);
    try {
      const { queued: n } = await api.convertSeries(seriesID, season);
      flash(n > 0 ? `Queued ${n} episode${n === 1 ? "" : "s"} from ${label}` : `Nothing to convert in ${label} — already efficient`);
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

  const setSortKey = (key: SortKey) =>
    setSort((s) => s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: key === "title" ? "asc" : "desc" });
  const toggleCodec = (cc: string) => setCodecF((s) => { const n = new Set(s); n.has(cc) ? n.delete(cc) : n.add(cc); return n; });

  const codecs = useMemo(() => {
    const m = new Map<string, number>();
    for (const c of items ?? []) { const cc = c.info?.video_codec?.toUpperCase(); if (cc) m.set(cc, (m.get(cc) ?? 0) + 1); }
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [items]);

  const view = useMemo(() => {
    let list = (items ?? []).slice();
    if (codecF.size) list = list.filter((c) => codecF.has(c.info?.video_codec?.toUpperCase() ?? ""));
    const needle = q.trim().toLowerCase();
    if (needle) list = list.filter((c) => c.title.toLowerCase().includes(needle));
    const dir = sort.dir === "asc" ? 1 : -1;
    const val = (c: ConvertCandidate): number | string => {
      switch (sort.key) {
        case "title": return c.title.toLowerCase();
        case "video": return c.info?.video_codec ?? "";
        case "res": return c.info?.height ?? 0;
        case "bitrate": return c.info?.bitrate_kbps ?? 0;
        case "audio": return c.info?.audio_tracks ?? 0;
        case "subs": return c.info?.sub_tracks ?? 0;
        case "size": return c.info?.size_bytes ?? 0;
        case "est": return c.candidate ? c.est_bytes : -1;
      }
    };
    return list.sort((a, b) => {
      const va = val(a), vb = val(b);
      return typeof va === "string" || typeof vb === "string" ? String(va).localeCompare(String(vb)) * dir : (va - vb) * dir;
    });
  }, [items, codecF, sort, q]);

  const setShowSortKey = (key: ShowSortKey) =>
    setShowSort((s) => s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: key === "title" ? "asc" : "desc" });

  const showView = useMemo(() => {
    const dir = showSort.dir === "asc" ? 1 : -1;
    const val = (sh: ConvertSeriesRollup): number | string => {
      switch (showSort.key) {
        case "title": return sh.title.toLowerCase();
        case "files": return sh.files;
        case "convertible": return sh.convertible;
        case "size": return sh.total_bytes;
        case "est": return sh.convertible ? sh.est_bytes : -1;
      }
    };
    const needle = q.trim().toLowerCase();
    let base = shows ?? [];
    if (onlyConvertible) base = base.filter((sh) => sh.convertible > 0);
    if (needle) base = base.filter((sh) => sh.title.toLowerCase().includes(needle));
    return base.slice().sort((a, b) => {
      const va = val(a), vb = val(b);
      return typeof va === "string" || typeof vb === "string" ? String(va).localeCompare(String(vb)) * dir : (va - vb) * dir;
    });
  }, [shows, showSort, q, onlyConvertible]);

  // Seasons of the open show that still have convertible episodes — nothing else is
  // worth offering a bulk button for.
  const seasons = useMemo(() => {
    const set = new Set<number>();
    for (const c of items ?? []) if (c.candidate && c.season != null) set.add(c.season);
    return [...set].sort((a, b) => a - b);
  }, [items]);

  const noun = media === "tv" ? "episodes" : "movies";
  const filtering = codecF.size > 0;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex w-fit rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          {(["movies", "tv"] as const).map((m) => (
          <button key={m} onClick={() => setMedia(m)} className="rounded-md px-3.5 py-1.5 text-[12px] font-semibold" style={{ background: media === m ? "var(--accent)" : "transparent", color: media === m ? "var(--accent-ink)" : "var(--ink-faint)" }}>
            {m === "movies" ? "Movies" : "TV Shows"}
            {media === m ? <span className="ml-1.5 font-mono text-[10px] opacity-70">{(m === "tv" ? shows?.length : items?.length)?.toLocaleString() ?? ""}</span> : null}
            </button>
          ))}
        </div>

        <label className="sr-only" htmlFor="convert-search">Search titles</label>
        <input id="convert-search" type="search" value={q} onChange={(e) => setQ(e.target.value)}
          placeholder={media === "tv" ? "Search shows…" : "Search movies…"}
          className="rounded-lg px-2.5 py-1.5 text-[12px]"
          style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)", minWidth: 200 }} />

        <label className="flex cursor-pointer items-center gap-1.5 text-[12px] text-ink-dim">
          <input type="checkbox" checked={onlyConvertible} onChange={(e) => setOnlyConvertible(e.target.checked)} />
          Only show files needing conversion
        </label>
      </div>

      {loadErr && (
        <div role="status" className="rounded-lg px-3 py-2 text-[12px]"
          style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
          Couldn't load the library — this is an error, not an empty library.
        </div>
      )}

      {media === "tv" && !show ? (
        shows === null ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading your shows…</div>
        ) : shows.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>No downloaded episodes yet.</div>
        ) : (
          <>
            <p className="text-[11px] text-ink-faint">
              <b style={{ color: "var(--ink)" }}>{shows.reduce((n, x) => n + x.convertible, 0).toLocaleString()}</b> convertible
              {" "}episode{shows.reduce((n, x) => n + x.convertible, 0) === 1 ? "" : "s"} across {shows.length.toLocaleString()} show{shows.length === 1 ? "" : "s"}.
              Open a show to see its episodes, or convert the whole thing.
            </p>
            <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
              <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 720 }}>
                <thead><tr style={{ background: "var(--panel-2)" }}>{SHOW_HEADERS.map((h) => (
                  <th key={h.label} scope="col" aria-sort={h.key && showSort.key === h.key ? (showSort.dir === "asc" ? "ascending" : "descending") : undefined}
                    className="px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">
                    {h.key
                      ? <button type="button" onClick={() => setShowSortKey(h.key!)} className="font-inherit uppercase hover:text-[var(--ink)]">
                          {h.label}{showSort.key === h.key ? (showSort.dir === "asc" ? " ▲" : " ▼") : ""}
                        </button>
                      : h.label}
                  </th>
                ))}</tr></thead>
                <tbody>
                  {showView.map((sh, i) => {
                    const done = sh.convertible === 0;
                    return (
                      <tr key={sh.series_id} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                        <td className="px-3 py-2 font-semibold">
                          <button onClick={() => setShow(sh)} className="text-left hover:underline">
                            {sh.title} <span className="font-normal text-ink-faint">{sh.year || ""}</span>
                          </button>
                        </td>
                        <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{sh.files.toLocaleString()}</td>
                        <td className="px-3 py-2 font-mono tabular-nums">
                          {done
                            ? <span style={{ color: "var(--good)" }}>all efficient ✓</span>
                            : <span style={{ color: "var(--avoid)" }}>{sh.convertible.toLocaleString()}</span>}
                        </td>
                        <td className="px-3 py-2 font-mono tabular-nums">{fmtSize(sh.total_bytes)}</td>
                        <td className="px-3 py-2 font-mono tabular-nums">
                          {done ? <span className="text-ink-faint">—</span>
                                : <span style={{ color: "var(--ink-faint)" }}>~{fmtSize(sh.est_bytes)}</span>}
                        </td>
                        <td className="px-3 py-2">
                          <div className="flex items-center justify-end gap-1.5">
                            <button onClick={() => setShow(sh)} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Episodes</button>
                            {!done && (
                              <button onClick={() => convertBulk(sh.series_id, undefined, sh.title, sh.convertible)} disabled={busy !== null}
                                className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50"
                                style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
                                {busy === `bulk:${sh.series_id}:all` ? "Queuing…" : "Convert all"}
                              </button>
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
        )
      ) : items === null ? (
        <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Analyzing your {noun}…</div>
      ) : items.length === 0 ? (
        <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>No downloaded {noun} yet.</div>
      ) : (
        <>
          {show && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl px-3 py-2" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
              <div className="flex items-center gap-2">
                <button onClick={() => setShow(null)} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>← All shows</button>
                <span className="text-[13px] font-bold">{show.title}</span>
              </div>
              <div className="flex flex-wrap items-center gap-1.5">
                {seasons.map((n) => (
                  <button key={n} onClick={() => convertBulk(show.series_id, n, `${show.title} season ${n}`, (items ?? []).filter((c) => c.candidate && c.season === n).length)} disabled={busy !== null}
                    className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold disabled:opacity-50"
                    style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
                    {busy === `bulk:${show.series_id}:${n}` ? "Queuing…" : `Convert S${n}`}
                  </button>
                ))}
              </div>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-0.5 font-mono text-[9.5px] uppercase text-ink-faint">Codec</span>
            {codecs.map(([cc, n]) => <Pill key={cc} active={codecF.has(cc)} onClick={() => toggleCodec(cc)}>{cc} <span className="opacity-60">{n}</span></Pill>)}
            {filtering && <button onClick={() => setCodecF(new Set())} className="ml-1 text-[10.5px] text-ink-faint underline hover:text-[var(--ink)]">clear</button>}
          </div>
          <p className="text-[11px] text-ink-faint">
            {filtering ? <><b style={{ color: "var(--ink)" }}>{view.length.toLocaleString()}</b> of {items.length.toLocaleString()} · </> : null}
            “Est. after” is a rough heuristic — run <b>Test 30s</b> for a real number{media === "tv" ? " (movies only for now)" : ""}.
          </p>
          <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
            <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 940 }}>
              <thead><tr style={{ background: "var(--panel-2)" }}>{HEADERS.map((h) => (
                <th key={h.label} scope="col" aria-sort={h.key && sort.key === h.key ? (sort.dir === "asc" ? "ascending" : "descending") : undefined}
                  className="px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">
                  {h.key
                    ? <button type="button" onClick={() => setSortKey(h.key!)} className="font-inherit uppercase hover:text-[var(--ink)]">
                        {h.label}{sort.key === h.key ? (sort.dir === "asc" ? " ▲" : " ▼") : ""}
                      </button>
                    : h.label}
                </th>
              ))}</tr></thead>
              <tbody>
                {view.map((c, i) => {
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
    const keys: (keyof AppSettings)[] = ["convert_target_codec", "convert_auto", "convert_sweep_start", "convert_sweep_end", "convert_workers", "convert_quality_gate", "convert_min_ssim", "convert_max_failures", "convert_skip_hardlinked", "convert_scratch_dir", "convert_vaapi_device", "convert_scan_at"];
    return keys.some((k) => saved[k] !== d[k]);
  }, [saved, d]);
  const onSave = async () => {
    if (!d) return;
    setBusy(true);
    const patch = { convert_target_codec: d.convert_target_codec, convert_auto: d.convert_auto, convert_sweep_start: d.convert_sweep_start, convert_sweep_end: d.convert_sweep_end, convert_workers: d.convert_workers, convert_quality_gate: d.convert_quality_gate, convert_min_ssim: d.convert_min_ssim, convert_max_failures: d.convert_max_failures, convert_skip_hardlinked: d.convert_skip_hardlinked, convert_scratch_dir: d.convert_scratch_dir, convert_vaapi_device: d.convert_vaapi_device, convert_scan_at: d.convert_scan_at };
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
        <SettingField label="Scan the library at" hint="a daily pass to spot files changed outside Arrmada · imports are picked up instantly, so this can sit overnight">
          <input type="time" value={d.convert_scan_at} onChange={(e) => set({ convert_scan_at: e.target.value })} className={inp} style={inpStyle} />
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

// SettingField associates its label with the control it wraps — screen readers announced
// every Convert setting as an unlabeled "edit, blank" before this.
// SettingField wraps its control in a <label> so the visible text is the accessible name —
// screen readers announced every Convert setting as an unlabeled "edit, blank" before this.
function SettingField({ label, hint, children }: { label: string; hint: string; children: ReactNode }) {
  return (
    <label className="flex items-center justify-between gap-3">
      <span><span className="block text-[12px] font-semibold">{label}</span><span className="block text-[10.5px] text-ink-faint">{hint}</span></span>
      {children}
    </label>
  );
}
