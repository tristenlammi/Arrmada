import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { PageHeader } from "../components/PageHeader";
import { RescanButton, ago } from "../components/RescanButton";
import {
  api, type ConvertCandidate, type ConvertSeriesRollup, type ConvertLibraryStats, type ConvertBlocked, type ConvertSkipped,
  type ConvertJob, type ConvertStatus, type ConvertSettings, type ConvertCompareStatus,
} from "../lib/api";

// Convert works through the library on its own: switch it on, choose the hours, and it
// re-encodes wasteful video to HEVC or AV1 at the same quality, one file at a time, pausing
// while someone watches Plex. There's no queue to manage — the Overview shows what it's
// doing, what's next and what it did; the Library lets you pick a file to do right now.
type Tab = "overview" | "library" | "problems" | "activity" | "settings";
const ACTIVE = new Set(["preparing", "testing", "encoding", "verifying", "replacing"]);

const card = "rounded-xl p-4";
const cardStyle = { border: "1px solid var(--line)", background: "var(--panel)" } as const;
const lbl = "font-mono text-[9.5px] font-bold uppercase tracking-[0.11em] text-ink-faint";
const inp = "rounded-lg px-2.5 py-1.5 text-[12px]";
const inpStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
const accentBtn = { background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" } as const;
const ghostBtn = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

function fmtSize(b?: number): string {
  if (b === undefined || b === null) return "—";
  if (b <= 0) return "0 B";
  const tb = b / 1024 ** 4;
  if (tb >= 1) return `${tb.toFixed(2)} TB`;
  const gb = b / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = b / 1024 ** 2;
  return mb >= 1 ? `${mb.toFixed(0)} MB` : `${(b / 1024).toFixed(0)} KB`;
}

function savedPct(j: ConvertJob): number | null {
  if (!j.src_bytes || j.src_bytes <= 0 || !j.out_bytes || j.out_bytes <= 0) return null;
  return Math.round((1 - j.out_bytes / j.src_bytes) * 100);
}

function fmtEta(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return "—";
  if (sec < 60) return `${Math.round(sec)}s`;
  const m = Math.floor(sec / 60), s = Math.round(sec % 60);
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

const STATE_LABEL: Record<string, string> = {
  preparing: "analysing", testing: "test-encoding clips", encoding: "encoding", verifying: "checking quality", replacing: "swapping in",
};

export function Convert() {
  const [tab, setTab] = useState<Tab>("overview");
  const [status, setStatus] = useState<ConvertStatus | null>(null);
  const [jobs, setJobs] = useState<ConvertJob[]>([]);
  const [stats, setStats] = useState<ConvertLibraryStats | null>(null);
  const [hw, setHw] = useState<{ using: string; reclaimed_bytes: number } | null>(null);
  const [settings, setSettings] = useState<ConvertSettings | null>(null);
  const [compareKey, setCompareKey] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const toastTimer = useRef<number | undefined>(undefined);
  const flash = useCallback((m: string) => {
    setToast(m);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(null), 3500);
  }, []);
  useEffect(() => () => window.clearTimeout(toastTimer.current), []);

  const loadStats = useCallback(() => api.convertStats().then(setStats).catch(() => {}), []);
  const loadHw = useCallback(() => api.convertHardware().then(setHw).catch(() => {}), []);
  const loadSettings = useCallback(() => api.convertSettings().then(setSettings).catch(() => {}), []);
  useEffect(() => { loadStats(); loadHw(); loadSettings(); }, [loadStats, loadHw, loadSettings]);

  const anyActive = jobs.some((j) => ACTIVE.has(j.state));
  useEffect(() => {
    let alive = true;
    const tick = () => {
      api.convertJobs().then((j) => { if (alive) setJobs(j); }).catch(() => {});
      api.convertStatus().then((s) => { if (alive) setStatus(s); }).catch(() => {});
    };
    tick();
    const t = setInterval(tick, anyActive ? 1500 : 5000);
    return () => { alive = false; clearInterval(t); };
  }, [anyActive]);
  useEffect(() => { if (!anyActive) { loadHw(); loadStats(); } }, [anyActive, loadHw, loadStats]);

  const refresh = useCallback(() => {
    api.convertJobs().then(setJobs).catch(() => {});
    api.convertStatus().then(setStatus).catch(() => {});
    loadStats();
  }, [loadStats]);

  const rescanLibrary = useCallback(async (): Promise<boolean> => {
    const r = await api.convertReindex();
    if (!r.started) { flash(r.reason ?? "A scan is already running."); return false; }
    flash("Rescanning the library…");
    for (let i = 0; i < 240; i++) {
      await new Promise((res) => setTimeout(res, 2000));
      const st = await api.convertReindexStatus().catch(() => ({ running: false }));
      if (!st.running) break;
    }
    await loadStats();
    refresh();
    flash("Library rescanned.");
    return true;
  }, [flash, loadStats, refresh]);

  const toggleAuto = async () => {
    if (!settings) return;
    try {
      const s = await api.updateConvertSettings({ auto: !settings.auto });
      setSettings(s);
      flash(s.auto ? "Convert is on — it works through your library during your hours" : "Automatic conversion is off");
      refresh();
    } catch (e) { flash((e as Error).message); }
  };

  const TABS: { key: Tab; label: string }[] = [
    { key: "overview", label: "Overview" },
    { key: "library", label: "Library" },
    { key: "problems", label: "Problems" },
    { key: "activity", label: "Activity" },
    { key: "settings", label: "Settings" },
  ];

  return (
    <>
      <PageHeader title="Convert" crumb="Library / Convert" />
      <div className="mx-auto w-full max-w-[1240px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <p className="max-w-[66ch] text-[12.5px] text-ink-dim">
            Makes your library smaller without making it look any different. Wasteful video is re-encoded to HEVC
            {settings?.allow_av1 ? " or AV1 (whichever is smaller at the same quality)" : ""}, one file at a time during your hours.
            Atmos and every audio track are copied untouched; HDR10 and HDR10+ are kept.
          </p>
          {settings && (
            <button onClick={toggleAuto} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={settings.auto ? ghostBtn : accentBtn}>
              {settings.auto ? "Switch off" : "Switch on"}
            </button>
          )}
        </div>

        <div className="mb-5 flex gap-1 overflow-x-auto border-b" style={{ borderColor: "var(--line)" }}>
          {TABS.map((t) => {
            const active = tab === t.key;
            return (
              <button key={t.key} onClick={() => setTab(t.key)} className="relative flex-none px-4 py-2.5 text-[13.5px] font-semibold transition-colors" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>
                {t.label}
                {active && <span className="absolute inset-x-2 bottom-0 h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
              </button>
            );
          })}
        </div>

        {tab === "overview" && <Overview status={status} jobs={jobs} stats={stats} hw={hw} flash={flash} onChanged={refresh} onRescan={rescanLibrary} onShowProblems={() => setTab("problems")} />}
        {tab === "library" && <Library flash={flash} onRequested={refresh} onRescan={rescanLibrary} onCompare={setCompareKey} running={jobs} />}
        {tab === "problems" && <Problems flash={flash} />}
        {tab === "activity" && <LogsConsole />}
        {tab === "settings" && <SettingsPanel flash={flash} onSaved={(s) => { setSettings(s); refresh(); loadHw(); }} />}
      </div>
      {compareKey && <CompareModal itemKey={compareKey} onClose={() => setCompareKey(null)} flash={flash} />}
      {toast && <div role="status" aria-live="polite" className="fixed bottom-5 left-1/2 z-[60] -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

/* ============================= OVERVIEW ============================= */

const STATUS_TONE: Record<string, string> = {
  working: "var(--good)", starting: "var(--good)", paused: "var(--avoid)", waiting: "var(--ink-faint)", off: "var(--ink-faint)", done: "var(--good)",
};

function Overview({ status, jobs, stats, hw, flash, onChanged, onRescan, onShowProblems }: {
  status: ConvertStatus | null; jobs: ConvertJob[]; stats: ConvertLibraryStats | null; hw: { using: string; reclaimed_bytes: number } | null;
  flash: (m: string) => void; onChanged: () => void; onRescan: () => Promise<boolean>; onShowProblems: () => void;
}) {
  const [rescanning, setRescanning] = useState(false);
  const running = jobs.filter((j) => ACTIVE.has(j.state));
  const recent = jobs.filter((j) => !ACTIVE.has(j.state)).slice(0, 12);
  const t = stats?.total;
  const n = t?.files ?? 0;
  const pct = (x: number) => (n ? Math.round((x / n) * 100) : 0);

  const cancelJob = async (id: number) => { try { await api.convertCancel(id); onChanged(); } catch (e) { flash((e as Error).message); } };
  const cancelRequest = async (key: string) => { try { await api.convertCancelRequest(key); onChanged(); } catch (e) { flash((e as Error).message); } };
  const doNext = async (key: string, title: string) => {
    try { await api.convertRequest(key); flash(`“${title}” is next`); onChanged(); } catch (e) { flash((e as Error).message); }
  };

  return (
    <div className="flex flex-col gap-3.5">
      {status && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl px-4 py-3" style={{ ...cardStyle, borderColor: STATUS_TONE[status.state] }}>
          <div className="flex items-center gap-2.5">
            <span className="h-2.5 w-2.5 flex-none rounded-full" style={{ background: STATUS_TONE[status.state] }} />
            <span className="text-[13.5px] font-semibold">{status.message}</span>
          </div>
          <span className="text-[12px] text-ink-faint">
            {status.remaining > 0 ? `${status.remaining.toLocaleString()} file${status.remaining === 1 ? "" : "s"} to go` : ""}
            {status.window ? ` · hours ${status.window}` : status.auto ? " · any time" : ""}
            {status.watching ? " · someone is watching Plex" : ""}
          </span>
        </div>
      )}

      <div className="grid gap-3.5" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(280px, 1fr))" }}>
        <div className={card} style={cardStyle}>
          <div className="flex items-center justify-between">
            <div className={lbl}>Space saved</div>
            <RescanButton busy={rescanning} title={stats?.as_of ? `Rescan the library · figures as of ${ago(stats.as_of)}` : "Rescan the library"}
              onClick={async () => { setRescanning(true); try { await onRescan(); } catch { /* flashed */ } finally { setRescanning(false); } }} />
          </div>
          <div className="mt-2 text-[30px] font-extrabold tracking-tight">{fmtSize(hw?.reclaimed_bytes)}</div>
          <div className="mt-3 border-t pt-3 text-[12px] text-ink-dim" style={{ borderColor: "var(--line-soft)" }}>
            <span style={{ color: "var(--good)" }}>~{fmtSize(t?.reclaimable)}</span> more to save ·{" "}
            <b style={{ color: "var(--ink)" }}>{(t?.convertible ?? 0).toLocaleString()}</b> of {n.toLocaleString()} files worth converting
            {(t?.skipped ?? 0) > 0 && (
              <button onClick={onShowProblems} className="mt-1 block text-left text-[11px] underline" style={{ color: "var(--avoid)" }}>
                {t!.skipped.toLocaleString()} file{t!.skipped === 1 ? "" : "s"} can&rsquo;t be converted — see Problems
              </button>
            )}
          </div>
        </div>
        <div className={card} style={cardStyle}>
          <div className={lbl}>Library video</div>
          <div className="mt-2.5 flex h-4 overflow-hidden rounded-md" style={{ border: "1px solid var(--line)" }}>
            {(t?.h264 ?? 0) > 0 && <span style={{ width: `${pct(t!.h264)}%`, background: "var(--avoid)" }} />}
            {(t?.hevc ?? 0) > 0 && <span style={{ width: `${pct(t!.hevc)}%`, background: "var(--good)" }} />}
            {(t?.av1 ?? 0) > 0 && <span style={{ width: `${pct(t!.av1)}%`, background: "var(--accent)" }} />}
            {(t?.other ?? 0) > 0 && <span style={{ width: `${pct(t!.other)}%`, background: "var(--ink-faint)" }} />}
          </div>
          <div className="mt-3 flex flex-wrap gap-x-3.5 gap-y-1.5 text-[11.5px] text-ink-dim">
            <Legend c="var(--avoid)" label={`H.264 · ${pct(t?.h264 ?? 0)}%`} />
            <Legend c="var(--good)" label={`HEVC · ${pct(t?.hevc ?? 0)}%`} />
            <Legend c="var(--accent)" label={`AV1 · ${pct(t?.av1 ?? 0)}%`} />
            <Legend c="var(--ink-faint)" label={`Other · ${pct(t?.other ?? 0)}%`} />
          </div>
          <div className="mt-2 font-mono text-[10.5px] text-ink-faint">{n.toLocaleString()} files · {fmtSize(t?.total_bytes)}</div>
        </div>
        <div className={card} style={cardStyle}>
          <div className={lbl}>How it works</div>
          <ul className="mt-2 flex flex-col gap-1.5 text-[11.5px] text-ink-dim">
            <li>• Converts on <b className="text-[var(--ink)]">{hw?.using ?? "…"}</b></li>
            <li>• Only re-encodes video that's wasteful for its resolution — lean and efficient files are left alone</li>
            <li>• Every result is checked against the original; it must look the same and save at least 20%, or the original stays</li>
            <li>• Never touches a file that's still seeding; originals go to the recycle bin</li>
          </ul>
        </div>
      </div>

      <div className={card} style={cardStyle}>
        <div className="text-[14px] font-bold">Now</div>
        {running.length === 0
          ? <div className="mt-2 text-[12px] text-ink-dim">Nothing converting right now.</div>
          : <div className="mt-2 flex flex-col gap-3">{running.map((j) => <JobBar key={j.id} j={j} onCancel={() => cancelJob(j.id)} />)}</div>}
      </div>

      {status && (status.requests.length > 0 || status.up_next.length > 0) && (
        <div className={card} style={cardStyle}>
          <div className="text-[14px] font-bold">Up next</div>
          <div className="mt-2 flex flex-col gap-1.5">
            {status.requests.map((r) => (
              <div key={r.key} className="flex items-center gap-2.5 text-[12px]">
                <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>requested</span>
                <span className="flex-1 truncate font-semibold">{r.title}</span>
                <button onClick={() => cancelRequest(r.key)} aria-label={`Remove ${r.title}`} className="rounded px-1.5 py-0.5 font-mono text-[10px] text-ink-faint hover:text-[var(--avoid)]">✕</button>
              </div>
            ))}
            {status.up_next.map((u) => (
              <div key={u.key} className="flex items-center gap-2.5 text-[12px]">
                <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{u.reason}</span>
                <span className="flex-1 truncate">{u.title}</span>
                <span className="font-mono text-[11px] text-ink-faint">
                  {u.video ? `${u.current} → ${u.codec.toUpperCase()} · ~${fmtSize(u.saving)} saved` : u.tracks}
                </span>
                <button onClick={() => doNext(u.key, u.title)} title="Convert this one now, regardless of the hours" className="rounded px-1.5 py-0.5 text-[10.5px] font-semibold text-ink-faint hover:text-[var(--accent)]">now</button>
              </div>
            ))}
            {status.remaining > status.up_next.length && <div className="text-[11.5px] text-ink-faint">+ {(status.remaining - status.up_next.length).toLocaleString()} more, biggest savings first</div>}
          </div>
        </div>
      )}

      {recent.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="text-[14px] font-bold">Recent</div>
          <div className="mt-2 flex flex-col gap-1.5">
            {recent.map((j) => {
              const p = savedPct(j);
              const tone = j.state === "done" ? "var(--good)" : j.state === "failed" ? "var(--reject)" : "var(--ink-faint)";
              return (
                <div key={j.id} className="flex items-center gap-2.5 text-[12px]">
                  <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--panel-2)", color: tone }}>{j.state}</span>
                  <span className="min-w-0 flex-1 truncate font-semibold">{j.title}</span>
                  {j.state === "done" && p !== null && j.codec ? (
                    <span className="font-mono text-ink-dim">
                      {fmtSize(j.src_bytes)} → {fmtSize(j.out_bytes)} <span style={{ color: "var(--good)" }}>−{p}%</span>
                      <span className="ml-1.5 text-[10.5px] text-ink-faint">{j.codec.toUpperCase()}{j.ssim ? ` · SSIM ${j.ssim.toFixed(3)}` : ""}</span>
                    </span>
                  ) : (
                    <span className="max-w-[55%] truncate text-[11.5px] text-ink-faint" title={j.note}>{j.note || j.state}</span>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

function Legend({ c, label }: { c: string; label: string }) {
  return <span className="inline-flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-sm" style={{ background: c }} />{label}</span>;
}

function JobBar({ j, onCancel }: { j: ConvertJob; onCancel?: () => void }) {
  const pct = Math.max(0, Math.min(100, j.progress * 100));
  const encoding = j.state === "encoding" && !j.paused;
  const eta = encoding && j.duration_sec && j.speed_x > 0 && j.progress < 1 ? (j.duration_sec * (1 - j.progress)) / j.speed_x : 0;
  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-2 text-[12px]">
        <span className="font-semibold">
          {j.title}
          <span className="ml-1.5 rounded-full px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>
            {j.codec ? `→ ${j.codec.toUpperCase()} · ` : ""}{j.encoder}
          </span>
          {j.requested && <span className="ml-1 rounded-full px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>requested</span>}
        </span>
        <span className="flex items-center gap-2 font-mono tabular-nums text-ink-dim">
          {j.paused ? <span style={{ color: "var(--avoid)" }}>paused — {j.paused}</span>
            : encoding ? `${pct.toFixed(1)}%${eta > 0 ? ` · ${fmtEta(eta)} left` : ""}`
            : STATE_LABEL[j.state] ?? j.state}
          {onCancel && <button onClick={onCancel} aria-label={`Cancel ${j.title}`} title="Stop — the original is untouched until a conversion completes"
            className="rounded px-1.5 py-0.5 text-[10px] hover:text-[var(--avoid)]">✕</button>}
        </span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        <div className="h-full" style={{ width: `${pct}%`, background: j.paused ? "var(--avoid)" : "linear-gradient(90deg, var(--accent-deep), var(--accent))", transition: "width 1s linear" }} />
      </div>
      {encoding && j.fps > 0 && <div className="mt-1 flex gap-4 font-mono text-[11px] text-ink-faint"><span><b className="text-ink">{Math.round(j.fps)}</b> fps</span><span><b className="text-ink">{j.speed_x.toFixed(1)}×</b> realtime</span><span>{fmtSize(j.src_bytes)} source</span></div>}
    </div>
  );
}

/* ============================= LIBRARY ============================= */

type SortKey = "title" | "video" | "res" | "bitrate" | "size" | "save";
const HEADERS: { label: string; key?: SortKey }[] = [
  { label: "Title", key: "title" }, { label: "Video", key: "video" }, { label: "Res", key: "res" }, { label: "HDR" },
  { label: "Bitrate", key: "bitrate" }, { label: "Size", key: "size" }, { label: "Saves", key: "save" }, { label: "" },
];
type ShowSortKey = "title" | "files" | "convertible" | "size" | "save";
const SHOW_HEADERS: { label: string; key?: ShowSortKey }[] = [
  { label: "Show", key: "title" }, { label: "Files", key: "files" }, { label: "Worth converting", key: "convertible" },
  { label: "Size", key: "size" }, { label: "Saves", key: "save" }, { label: "" },
];

// Saves renders an estimated saving as a reduction — "−46 GB (40%)" — with the size the
// file ends up at underneath, so it can't be mistaken for the new size.
function Saves({ bytes, of, faint }: { bytes: number; of: number; faint?: boolean }) {
  if (!bytes || bytes <= 0) return <span className="text-ink-faint">—</span>;
  const pct = of > 0 ? Math.round((bytes / of) * 100) : 0;
  return (
    <span className="inline-flex flex-col leading-tight">
      <span style={{ color: faint ? "var(--ink-faint)" : "var(--good)" }}>
        −{fmtSize(bytes)} <span className="text-[10.5px] opacity-80">({pct}%)</span>
      </span>
      {of > 0 && <span className="text-[10px] text-ink-faint">→ ~{fmtSize(of - bytes)} after</span>}
    </span>
  );
}

function Pill({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" onClick={onClick} className="rounded-full px-2.5 py-1 text-[10.5px] font-semibold transition-colors"
      style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "transparent", color: active ? "var(--accent)" : "var(--ink-dim)" }}>
      {children}
    </button>
  );
}

function NeedTag({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }} title={title}>
      {children}
    </span>
  );
}

function hdrLabel(c: ConvertCandidate): string {
  const h = c.info?.hdr;
  if (!h || h === "SDR") return "—";
  if (h === "Dolby Vision") return c.info?.dv_base && c.info.dv_base !== "SDR" ? `DV → ${c.info.dv_base}` : "DV";
  return h;
}

function Library({ flash, onRequested, onRescan, onCompare, running }: {
  flash: (m: string) => void; onRequested: () => void; onRescan: () => Promise<boolean>; onCompare: (key: string) => void; running: ConvertJob[];
}) {
  const [media, setMedia] = useState<"movies" | "tv">("movies");
  const [shows, setShows] = useState<ConvertSeriesRollup[] | null>(null);
  const [show, setShow] = useState<ConvertSeriesRollup | null>(null);
  const [items, setItems] = useState<ConvertCandidate[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [requested, setRequested] = useState<Set<string>>(new Set());
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "save", dir: "desc" });
  const [showSort, setShowSort] = useState<{ key: ShowSortKey; dir: "asc" | "desc" }>({ key: "save", dir: "desc" });
  const [codecF, setCodecF] = useState<Set<string>>(new Set());
  const [onlyConvertible, setOnlyConvertible] = useState(true);
  const [q, setQ] = useState("");
  const [loadErr, setLoadErr] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const runningKeys = useMemo(() => new Set(running.filter((j) => ACTIVE.has(j.state)).map((j) => j.key)), [running]);

  useEffect(() => {
    setItems(null); setShow(null); setRequested(new Set()); setCodecF(new Set()); setLoadErr(false);
    if (media === "tv") {
      setShows(null);
      api.convertLibrarySeries().then(setShows).catch(() => { setShows([]); setLoadErr(true); });
    } else {
      api.convertLibrary("movies", undefined, onlyConvertible).then(setItems).catch(() => { setItems([]); setLoadErr(true); });
    }
  }, [media, onlyConvertible, refreshKey]);

  useEffect(() => {
    if (media !== "tv" || !show) return;
    setItems(null); setCodecF(new Set()); setLoadErr(false);
    api.convertLibrary("tv", show.series_id, onlyConvertible).then(setItems).catch(() => { setItems([]); setLoadErr(true); });
  }, [media, show, onlyConvertible, refreshKey]);

  const convert = async (c: ConvertCandidate) => {
    setBusy(c.key);
    try {
      await api.convertRequest(c.key);
      setRequested((s) => new Set(s).add(c.key));
      flash(`“${c.title}” is next — it starts right away, whatever the hours`);
      onRequested();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const convertBulk = async (seriesID: number, season: number | undefined, label: string, count: number) => {
    if (count > 1 && !window.confirm(`Convert ${count} episode${count === 1 ? "" : "s"} from ${label} now?\n\nThey run one after another straight away, whatever your hours. Originals go to the recycle bin.`)) return;
    setBusy(`bulk:${seriesID}:${season ?? "all"}`);
    try {
      const { requested: n } = await api.convertSeries(seriesID, season);
      flash(n > 0 ? `${n} episode${n === 1 ? "" : "s"} from ${label} requested` : `Nothing to do in ${label}`);
      onRequested();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  const setSortKey = (key: SortKey) => setSort((s) => s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: key === "title" ? "asc" : "desc" });
  const setShowSortKey = (key: ShowSortKey) => setShowSort((s) => s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: key === "title" ? "asc" : "desc" });
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
        case "size": return c.info?.size_bytes ?? 0;
        case "save": return c.save_bytes ?? 0;
      }
    };
    return list.sort((a, b) => {
      const va = val(a), vb = val(b);
      return typeof va === "string" || typeof vb === "string" ? String(va).localeCompare(String(vb)) * dir : (va - vb) * dir;
    });
  }, [items, codecF, sort, q]);

  const showView = useMemo(() => {
    const dir = showSort.dir === "asc" ? 1 : -1;
    const val = (sh: ConvertSeriesRollup): number | string => {
      switch (showSort.key) {
        case "title": return sh.title.toLowerCase();
        case "files": return sh.files;
        case "convertible": return sh.convertible;
        case "size": return sh.total_bytes;
        case "save": return sh.save_bytes ?? 0;
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

  const seasons = useMemo(() => {
    const s = new Set<number>();
    for (const c of items ?? []) if (c.worth && c.season != null) s.add(c.season);
    return [...s].sort((a, b) => a - b);
  }, [items]);

  const noun = media === "tv" ? "episodes" : "movies";
  const sortHead = <K extends string>(h: { label: string; key?: K }, cur: { key: K; dir: string }, onSort: (k: K) => void) => (
    <th key={h.label} scope="col" aria-sort={h.key && cur.key === h.key ? (cur.dir === "asc" ? "ascending" : "descending") : undefined}
      className="px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">
      {h.key ? <button type="button" onClick={() => onSort(h.key!)} className="font-inherit uppercase hover:text-[var(--ink)]">{h.label}{cur.key === h.key ? (cur.dir === "asc" ? " ▲" : " ▼") : ""}</button> : h.label}
    </th>
  );

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex w-fit rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          {(["movies", "tv"] as const).map((m) => (
            <button key={m} onClick={() => setMedia(m)} className="rounded-md px-3.5 py-1.5 text-[12px] font-semibold" style={{ background: media === m ? "var(--accent)" : "transparent", color: media === m ? "var(--accent-ink)" : "var(--ink-faint)" }}>
              {m === "movies" ? "Movies" : "TV Shows"}
            </button>
          ))}
        </div>
        <button onClick={async () => { setBusy("reindex"); try { if (await onRescan()) setRefreshKey((k) => k + 1); } catch (e) { flash((e as Error).message); } finally { setBusy(null); } }}
          disabled={busy !== null} title="Pick up files added or changed since the last scan" className="rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50" style={ghostBtn}>
          {busy === "reindex" ? "Scanning…" : "Rescan library"}
        </button>
        <input type="search" aria-label="Search titles" value={q} onChange={(e) => setQ(e.target.value)} placeholder={media === "tv" ? "Search shows…" : "Search movies…"}
          className={inp} style={{ ...inpStyle, minWidth: 200 }} />
        <label className="flex cursor-pointer items-center gap-1.5 text-[12px] text-ink-dim">
          <input type="checkbox" checked={onlyConvertible} onChange={(e) => setOnlyConvertible(e.target.checked)} />
          Only files worth converting
        </label>
      </div>

      {loadErr && <div role="status" className="rounded-lg px-3 py-2 text-[12px]" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>Couldn't load the library — this is an error, not an empty library.</div>}

      {media === "tv" && !show ? (
        shows === null ? <Empty>Loading your shows…</Empty> : shows.length === 0 ? <Empty>No downloaded episodes yet.</Empty> : (
          <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
            <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 720 }}>
              <thead><tr style={{ background: "var(--panel-2)" }}>{SHOW_HEADERS.map((h) => sortHead(h, showSort, setShowSortKey))}</tr></thead>
              <tbody>
                {showView.map((sh, i) => {
                  const done = sh.convertible === 0;
                  return (
                    <tr key={sh.series_id} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                      <td className="px-3 py-2 font-semibold"><button onClick={() => setShow(sh)} className="text-left hover:underline">{sh.title} <span className="font-normal text-ink-faint">{sh.year || ""}</span></button></td>
                      <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{sh.files.toLocaleString()}</td>
                      <td className="px-3 py-2 font-mono tabular-nums">
                        {done ? <span className="text-ink-faint">none</span> : <span style={{ color: "var(--avoid)" }}>{sh.convertible.toLocaleString()}</span>}
                        {sh.reencode > 0 && sh.reencode < sh.convertible && <span className="ml-1.5 text-[10.5px] text-ink-faint">{sh.reencode} re-encode</span>}
                        {sh.tidy_only > 0 && <span className="ml-1.5 text-[10.5px] text-ink-faint" title="Only their tracks differ, and tidying them would free little — done when they're re-encoded, or with Fix tracks">+{sh.tidy_only} tracks only</span>}
                      </td>
                      <td className="px-3 py-2 font-mono tabular-nums">{fmtSize(sh.total_bytes)}</td>
                      <td className="px-3 py-2 font-mono tabular-nums"><Saves bytes={sh.save_bytes} of={sh.total_bytes} /></td>
                      <td className="px-3 py-2">
                        <div className="flex items-center justify-end gap-1.5">
                          <button onClick={() => setShow(sh)} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Episodes</button>
                          {!done && <button onClick={() => convertBulk(sh.series_id, undefined, sh.title, sh.convertible)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{busy === `bulk:${sh.series_id}:all` ? "…" : "Convert now"}</button>}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )
      ) : items === null ? <Empty>Loading your {noun}…</Empty> : items.length === 0 ? <Empty>{onlyConvertible ? `No ${noun} are worth converting.` : `No downloaded ${noun} yet.`}</Empty> : (
        <>
          {show && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl px-3 py-2" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
              <div className="flex items-center gap-2">
                <button onClick={() => setShow(null)} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>← All shows</button>
                <span className="text-[13px] font-bold">{show.title}</span>
              </div>
              <div className="flex flex-wrap items-center gap-1.5">
                {seasons.map((n) => (
                  <button key={n} onClick={() => convertBulk(show.series_id, n, `${show.title} season ${n}`, (items ?? []).filter((c) => c.worth && c.season === n).length)} disabled={busy !== null}
                    className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
                    {busy === `bulk:${show.series_id}:${n}` ? "…" : `Convert S${n}`}
                  </button>
                ))}
              </div>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-0.5 font-mono text-[9.5px] uppercase text-ink-faint">Codec</span>
            {codecs.map(([cc, n]) => <Pill key={cc} active={codecF.has(cc)} onClick={() => toggleCodec(cc)}>{cc} <span className="opacity-60">{n}</span></Pill>)}
            {codecF.size > 0 && <button onClick={() => setCodecF(new Set())} className="ml-1 text-[10.5px] text-ink-faint underline hover:text-[var(--ink)]">clear</button>}
          </div>
          <p className="text-[11px] text-ink-faint">“Saves” is a deliberately cautious estimate from each file's own bitrate — <b>Compare</b> encodes a few clips for the real number, and lets you watch them.</p>
          <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
            <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 900 }}>
              <thead><tr style={{ background: "var(--panel-2)" }}>{HEADERS.map((h) => sortHead(h, sort, setSortKey))}</tr></thead>
              <tbody>
                {view.map((c, i) => {
                  const cd = c.info?.video_codec?.toUpperCase() ?? "?";
                  const isRunning = runningKeys.has(c.key);
                  return (
                    <tr key={c.key} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                      <td className="px-3 py-2 font-semibold">{c.title} <span className="font-normal text-ink-faint">{c.year || ""}</span></td>
                      <td className="px-3 py-2">
                        <div className="flex flex-wrap items-center gap-1">
                          <span className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: c.needs?.video ? "var(--avoid-soft)" : "var(--good-soft, rgba(127,176,105,.16))", color: c.needs?.video ? "var(--avoid)" : "var(--good)" }} title={c.needs?.why}>{cd}</span>
                          {(c.needs?.subs || c.needs?.audio) && <NeedTag title={c.tracks}>tracks</NeedTag>}
                        </div>
                      </td>
                      <td className="px-3 py-2 font-mono text-ink-dim">{c.info?.resolution ?? "—"}</td>
                      <td className="px-3 py-2 font-mono text-ink-dim">{hdrLabel(c)}</td>
                      <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{c.info?.bitrate_kbps ? `${(c.info.bitrate_kbps / 1000).toFixed(1)} Mb/s` : "—"}</td>
                      <td className="px-3 py-2 font-mono tabular-nums">{fmtSize(c.info?.size_bytes)}</td>
                      <td className="px-3 py-2 font-mono tabular-nums" title={c.tracks || undefined}>
                        {c.candidate ? <Saves bytes={c.save_bytes} of={c.info?.size_bytes ?? 0} faint={!c.worth} /> : <span className="text-ink-faint">—</span>}
                        {c.candidate && !c.needs?.video && <span className="ml-1 text-[10px] text-ink-faint">tracks</span>}
                      </td>
                      <td className="px-3 py-2">
                        {isRunning ? <div className="text-right"><span className="font-mono text-[10.5px]" style={{ color: "var(--accent)" }}>converting…</span></div>
                          : c.candidate ? (
                          <div className="flex items-center justify-end gap-1.5">
                            {!c.worth && <span className="font-mono text-[10px] text-ink-faint" title="Only the tracks differ, and tidying them would free little. Done when it's re-encoded, or now with Fix tracks.">little to gain</span>}
                            {c.needs?.video && <button onClick={() => onCompare(c.key)} title="Encode a few clips as HEVC and AV1, compare size and quality, and keep the clips to watch" className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Compare</button>}
                            {requested.has(c.key) ? <span className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--good)", color: "var(--good)" }}>Next ✓</span>
                              : <button onClick={() => convert(c)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={c.worth ? { border: "1px solid var(--accent-line)", color: "var(--accent)" } : { border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{busy === c.key ? "…" : c.needs?.video ? "Convert now" : "Fix tracks"}</button>}
                          </div>
                        ) : <div className="text-right"><span className="font-mono text-[10.5px] text-ink-faint">{c.needs?.why || "on target"}</span></div>}
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

function Empty({ children }: { children: ReactNode }) {
  return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{children}</div>;
}

/* ============================= COMPARE ============================= */

function CompareModal({ itemKey, onClose, flash }: { itemKey: string; onClose: () => void; flash: (m: string) => void }) {
  const [st, setSt] = useState<ConvertCompareStatus | null>(null);
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    api.convertCompare(itemKey).then(setSt).catch(async (e) => {
      const cur = await api.convertCompareStatus().catch(() => null);
      if (cur?.running && cur.key !== itemKey) flash(`Another comparison is running (${cur.title})`);
      else flash((e as Error).message);
      setSt(cur);
    });
  }, [itemKey, flash]);
  useEffect(() => {
    const t = setInterval(() => api.convertCompareStatus().then(setSt).catch(() => {}), 2500);
    return () => clearInterval(t);
  }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const r = st?.result && st.key === itemKey ? st.result : null;
  const side = (s: { codec: string; encoder: string; bytes: number; ssim: number; est_bytes: number }, win: boolean) => (
    <div className="flex-1 rounded-lg p-3" style={{ border: `1.5px solid ${win ? "var(--good)" : "var(--line)"}`, background: "var(--panel-2)" }}>
      <div className="flex items-center justify-between"><span className="text-[14px] font-bold">{s.codec.toUpperCase()}</span>{win && <span className="font-mono text-[10px] font-bold" style={{ color: "var(--good)" }}>CHOSEN</span>}</div>
      <div className="mt-1 text-[11px] text-ink-faint">{s.encoder}</div>
      <div className="mt-2 font-mono text-[12px]">~{fmtSize(s.est_bytes)} <span className="text-ink-faint">whole file (video)</span></div>
      <div className="font-mono text-[12px]">SSIM {s.ssim ? s.ssim.toFixed(4) : "—"}</div>
    </div>
  );
  return (
    <div role="dialog" aria-modal="true" aria-labelledby="cmp-title" className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="w-full max-w-[560px] rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <h3 id="cmp-title" className="text-[15px] font-bold">HEVC vs AV1{st?.title ? ` — ${st.title}` : ""}</h3>
        {!r && !st?.error && (
          <p className="mt-2 text-[12.5px] text-ink-dim">
            Encoding a few clips both ways at the real settings and scoring each against the original… This takes a few minutes
            (longer for 4K). You can close this — it keeps going, and the result shows here when you open Compare again.
          </p>
        )}
        {st?.error && st.key === itemKey && <p className="mt-2 text-[12.5px]" style={{ color: "var(--reject)" }}>{st.error}</p>}
        {r && (
          <>
            <div className="mt-3 flex gap-2.5">{side(r.hevc, r.pick === "hevc")}{side(r.av1, r.pick === "av1")}</div>
            <p className="mt-3 text-[12.5px]">{r.why}</p>
            <p className="mt-1 text-[11px] text-ink-faint">Original: {fmtSize(r.src_bytes)} · tested on {r.clips} clip{r.clips === 1 ? "" : "s"} ({Math.round(r.seconds)}s). Audio is copied as is, so it adds the same to both.</p>
            {r.files && r.files.length > 0 && (
              <div className="mt-3">
                <div className={lbl}>Watch the clips (VLC or mpv)</div>
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  {r.files.map((f) => <a key={f} href={api.convertCompareFileURL(f)} className="rounded-md px-2 py-1 font-mono text-[11px]" style={ghostBtn}>{f}</a>)}
                </div>
                <p className="mt-1.5 text-[10.5px] text-ink-faint">Dark scenes and skies are where codecs differ — look there. The original clip may start a moment earlier than the encoded ones.</p>
              </div>
            )}
          </>
        )}
        <div className="mt-4 flex justify-end"><button onClick={onClose} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={ghostBtn}>Close</button></div>
      </div>
    </div>
  );
}

/* ============================= PROBLEMS ============================= */

const SKIP_LABEL: Record<string, string> = {
  hdr_unsupported: "Their HDR can't be carried through a conversion (Dolby Vision profile 5, or HDR10+ that couldn't be read), so they were left as they are",
  hardlinked: "Still seeding — Convert won't replace a file your torrent client is sharing. Tried again twice a day",
  not_smaller: "Converting them didn't save enough to be worth it, so the originals were kept",
  quality_gate: "The conversion couldn't match the original's quality, so the originals were kept",
  cancelled: "You stopped these — they're left alone for a month unless you try them again",
};

function Problems({ flash }: { flash: (m: string) => void }) {
  const [skips, setSkips] = useState<ConvertSkipped[] | null>(null);
  const [blocked, setBlocked] = useState<ConvertBlocked[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const load = useCallback(() => {
    api.convertSkips().then(setSkips).catch(() => setSkips([]));
    api.convertBlocklist().then(setBlocked).catch(() => setBlocked([]));
  }, []);
  useEffect(() => { load(); }, [load]);

  const retrySkips = async (kind?: string, key?: string) => {
    setBusy(key ?? kind ?? "all");
    try {
      if (key) await api.convertSkipsClear(key);
      else for (const s of (skips ?? []).filter((x) => !kind || x.kind === kind)) await api.convertSkipsClear(s.key);
      flash("Cleared — they'll be picked up again");
      load();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const retryBlocked = async (key?: string) => {
    setBusy(key ?? "blocked");
    try { await api.convertBlocklistClear(key); flash("Cleared — will be tried again"); load(); }
    catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  if (skips === null || blocked === null) return <div className="text-[12px] text-ink-faint">Loading…</div>;
  const groups = new Map<string, ConvertSkipped[]>();
  for (const s of skips) groups.set(s.kind, [...(groups.get(s.kind) ?? []), s]);
  if (!skips.length && !blocked.length) {
    return <div className={card} style={cardStyle}><div className="text-[14px] font-bold">Nothing stuck</div><p className="mt-1 text-[12px] text-ink-dim">Every file has either converted or is waiting its turn.</p></div>;
  }
  return (
    <div className="flex flex-col gap-3.5">
      {[...groups.entries()].map(([kind, list]) => (
        <div key={kind} className={card} style={cardStyle}>
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div>
              <div className="text-[14px] font-bold">{list.length.toLocaleString()} file{list.length === 1 ? "" : "s"}{list[0].permanent ? null : <span className="ml-2 font-mono text-[10px] font-normal text-ink-faint">TEMPORARY</span>}</div>
              <p className="mt-0.5 text-[12px] text-ink-dim">{SKIP_LABEL[kind] ?? list[0].reason}</p>
            </div>
            <button onClick={() => retrySkips(kind)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{busy === kind ? "…" : "Try these again"}</button>
          </div>
          <div className="mt-2.5 flex flex-col gap-1">
            {list.slice(0, 12).map((s) => (
              <div key={s.key} className="flex items-center gap-2.5 text-[12px]">
                <span className="min-w-0 flex-1 truncate text-ink-dim" title={s.reason}>{s.title}</span>
                <span className="hidden max-w-[45%] truncate text-[11px] text-ink-faint sm:block">{s.reason}</span>
                <button onClick={() => retrySkips(undefined, s.key)} disabled={busy !== null} className="rounded px-1.5 py-0.5 font-mono text-[10px] text-ink-faint hover:text-[var(--accent)]">retry</button>
              </div>
            ))}
            {list.length > 12 && <div className="text-[11.5px] text-ink-faint">+ {(list.length - 12).toLocaleString()} more</div>}
          </div>
        </div>
      ))}
      {blocked.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <div className="text-[14px] font-bold">Repeated failures <span className="font-mono text-[11px] text-ink-faint">{blocked.length}</span></div>
              <p className="mt-0.5 text-[11.5px] text-ink-dim">These failed three times, so Convert stopped trying. The last error is shown.</p>
            </div>
            <button onClick={() => retryBlocked()} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Clear all</button>
          </div>
          <div className="mt-2.5 flex flex-col gap-1.5">
            {blocked.map((b) => (
              <div key={b.key} className="flex items-start gap-2.5 text-[12px]">
                <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>{b.count}×</span>
                <div className="min-w-0 flex-1"><div className="font-semibold">{b.title}</div>{b.last_error && <div className="text-[11px] leading-snug text-ink-faint">{b.last_error}</div>}</div>
                <button onClick={() => retryBlocked(b.key)} disabled={busy !== null} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Retry</button>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

/* ============================= ACTIVITY ============================= */

function LogsConsole() {
  const [lines, setLines] = useState<{ at: number; level: string; msg: string }[]>([]);
  const [follow, setFollow] = useState(true);
  const boxRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    let alive = true;
    const tick = () => api.convertLogs().then((l) => {
      if (!alive) return;
      setLines((prev) => {
        const a = prev[prev.length - 1], b = l[l.length - 1];
        if (prev.length === l.length && a?.at === b?.at && a?.msg === b?.msg) return prev;
        return l;
      });
    }).catch(() => {});
    tick();
    const t = setInterval(tick, 2000);
    return () => { alive = false; clearInterval(t); };
  }, []);
  useEffect(() => { if (follow && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight; }, [lines, follow]);
  const tone = (lvl: string) => (lvl === "error" ? "var(--reject)" : lvl === "warn" ? "var(--avoid)" : "var(--ink-dim)");
  return (
    <div className={card} style={cardStyle}>
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[14px] font-bold">Activity</div>
        <label className="flex items-center gap-1.5 text-[11px] text-ink-faint"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Auto-scroll</label>
      </div>
      <div ref={boxRef} className="thin-scroll max-h-[62vh] overflow-y-auto rounded-lg p-3 font-mono text-[11.5px] leading-relaxed" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        {lines.length === 0 ? <div className="text-ink-faint">Nothing yet — switch Convert on, or convert a file from the Library.</div>
          : lines.map((l, i) => (
            <div key={`${l.at}-${i}`} className="flex gap-2"><span className="flex-none text-ink-faint">{new Date(l.at * 1000).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}</span><span style={{ color: tone(l.level) }}>{l.msg}</span></div>
          ))}
      </div>
    </div>
  );
}

/* ============================= SETTINGS ============================= */

function minutesApart(a: string | undefined, b: string): number {
  const mins = (t?: string) => { const m = /^(\d{1,2}):(\d{2})$/.exec(t ?? ""); return m ? +m[1] * 60 + +m[2] : null; };
  const x = mins(a), y = mins(b);
  if (x === null || y === null) return 0;
  const d = Math.abs(x - y);
  return Math.min(d, 1440 - d);
}

function Toggle({ on, set, label, hint, disabled }: { on: boolean; set: (v: boolean) => void; label: string; hint: ReactNode; disabled?: boolean }) {
  return (
    <label className={`flex items-start gap-2.5 text-[12.5px] ${disabled ? "opacity-50" : "cursor-pointer"}`}>
      <input type="checkbox" checked={on} disabled={disabled} onChange={(e) => set(e.target.checked)} className="mt-0.5" />
      <span><b className="font-semibold">{label}</b><span className="mt-0.5 block text-[11px] leading-snug text-ink-faint">{hint}</span></span>
    </label>
  );
}

function Section({ title, desc, children }: { title: string; desc?: string; children: ReactNode }) {
  return (
    <div className="rounded-xl p-4" style={cardStyle}>
      <div className="text-[13.5px] font-bold">{title}</div>
      {desc && <div className="mt-0.5 text-[11.5px] text-ink-faint">{desc}</div>}
      <div className="mt-3 flex flex-col gap-3.5">{children}</div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint: string; children: ReactNode }) {
  return (
    <label className="flex flex-wrap items-center justify-between gap-3">
      <span><span className="block text-[12px] font-semibold">{label}</span><span className="block text-[10.5px] text-ink-faint">{hint}</span></span>
      {children}
    </label>
  );
}

const SETTING_KEYS: (keyof ConvertSettings)[] = ["auto", "hours_start", "hours_end", "allow_av1", "use_gpu", "pause_watching", "keep_audio_langs", "keep_original_lang",
  "drop_commentary", "keep_sub_langs", "image_subs", "tidy_tracks", "scratch_dir", "vaapi_device", "cpu_cores", "workers"];

function SettingsPanel({ flash, onSaved }: { flash: (m: string) => void; onSaved: (s: ConvertSettings) => void }) {
  const [saved, setSaved] = useState<ConvertSettings | null>(null);
  const [d, setD] = useState<ConvertSettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [hw, setHw] = useState<{ dir: string; free: number; devices: { path: string; pci: string; vendor: string }[] } | null>(null);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { api.convertSettings().then((v) => { setSaved(v); setD(v); }).catch(() => flash("Could not load settings")); }, []);
  useEffect(() => { api.convertHardware().then((h) => setHw({ dir: h.scratch_dir, free: h.scratch_free_bytes, devices: h.render_devices ?? [] })).catch(() => {}); }, []);
  const set = (patch: Partial<ConvertSettings>) => setD((cur) => (cur ? { ...cur, ...patch } : cur));
  const dirty = useMemo(() => !!saved && !!d && SETTING_KEYS.some((k) => saved[k] !== d[k]), [saved, d]);
  const onSave = async () => {
    if (!d || !saved) return;
    const patch: Partial<ConvertSettings> = {};
    for (const k of SETTING_KEYS) if (saved[k] !== d[k]) (patch as Record<string, unknown>)[k] = d[k];
    setBusy(true);
    try { const v = await api.updateConvertSettings(patch); setSaved(v); setD(v); onSaved(v); flash("Saved"); }
    catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };
  if (!d) return <div className="text-[12px] text-ink-faint">Loading settings…</div>;
  const scheduled = !!(d.hours_start && d.hours_end);
  const browserTime = new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  const clockSkewed = minutesApart(d.server_time, browserTime) > 5;

  return (
    <div className="flex flex-col gap-4">
      <Section title="Automatic conversion">
        <Toggle on={d.auto} set={(v) => set({ auto: v })} label="Convert my library automatically"
          hint="Works through everything that's worth converting, biggest savings first, one file at a time. Files you convert from the Library run straight away either way." />
        <Toggle on={scheduled} set={(v) => set(v ? { hours_start: "01:00", hours_end: "07:00" } : { hours_start: "", hours_end: "" })}
          label="Only during set hours" hint={`A conversion still running when the hours end is paused and picks up again next time${d.can_pause ? "" : " (pausing needs the Linux container — elsewhere it finishes first)"}.`} />
        {scheduled && (
          <div className="ml-6 flex flex-col gap-1.5">
            <span className="flex flex-wrap items-center gap-1.5 text-[12px]">
              <input type="time" aria-label="Start" value={d.hours_start} onChange={(e) => set({ hours_start: e.target.value })} className={inp} style={inpStyle} />
              <span className="text-ink-faint">to</span>
              <input type="time" aria-label="End" value={d.hours_end} onChange={(e) => set({ hours_end: e.target.value })} className={inp} style={inpStyle} />
            </span>
            <span className={`text-[11px] ${clockSkewed ? "" : "text-ink-faint"}`} style={clockSkewed ? { color: "var(--avoid)" } : undefined}>
              {clockSkewed ? "⚠ " : ""}Read on the server's clock: {d.server_time} {d.server_tz}{clockSkewed ? ` — yours says ${browserTime}. Set TZ in .env and run ./update.sh.` : "."}
            </span>
          </div>
        )}
        <Toggle on={d.pause_watching} set={(v) => set({ pause_watching: v })} label="Pause while someone is watching Plex"
          hint={d.plex_watching_known ? "Nothing new starts, and a running conversion is frozen until the stream stops." : "Needs Plex connected in Insights — until then this does nothing."} />
      </Section>

      <Section title="Format" desc="HEVC by default: the best quality and it plays everywhere.">
        <Toggle on={d.allow_av1} set={(v) => set({ allow_av1: v })} label="My devices can play AV1"
          hint={<>Turn on only if every TV, phone and streaming box you watch on decodes AV1 — older Apple TVs, Shields and Fire Sticks don't, and Plex would transcode on the fly. When on, each file gets a quick side-by-side test and goes to AV1 only if it's clearly smaller at the same quality; otherwise HEVC. Files with HDR10+ always stay HEVC.</>} />
        {d.has_gpu && (
          <Toggle on={d.use_gpu} set={(v) => set({ use_gpu: v })} label="Use my GPU"
            hint={`Many times faster, but files come out somewhat bigger for the same quality. HDR always encodes on the CPU (the GPU can't carry its metadata).${d.gpu_does_av1 ? "" : " Your GPU has no AV1 encoder, so GPU mode converts to HEVC."}`} />
        )}
        {!d.hdr10plus_tool && <p className="text-[11px]" style={{ color: "var(--avoid)" }}>hdr10plus_tool isn't installed, so HDR10 files in HEVC are left alone (their HDR10+ couldn't be checked).</p>}
      </Section>

      <Section title="Audio & subtitles" desc="Tracks you don't keep are removed while converting — or on their own, which only copies the file (no re-encode). Audio is never re-encoded: Atmos, TrueHD and DTS-HD pass through untouched.">
        <Field label="Keep audio in" hint="comma-separated, e.g. en, ja · blank = keep every language · the first one becomes the default track">
          <input value={d.keep_audio_langs} onChange={(e) => set({ keep_audio_langs: e.target.value })} placeholder="all" className={`${inp} w-[190px]`} style={inpStyle} />
        </Field>
        <Toggle on={d.keep_original_lang} set={(v) => set({ keep_original_lang: v })} label="Also keep the original language"
          hint="A Japanese film keeps its Japanese track even if you only listed English." />
        <Toggle on={d.drop_commentary} set={(v) => set({ drop_commentary: v })} label="Remove commentary tracks" hint="Tracks flagged or titled as commentary." />
        <Field label="Keep subtitles in" hint="comma-separated, e.g. en · blank = keep every language">
          <input value={d.keep_sub_langs} onChange={(e) => set({ keep_sub_langs: e.target.value })} placeholder="all" className={`${inp} w-[190px]`} style={inpStyle} />
        </Field>
        <div>
          <div className="text-[12px] font-semibold">Image subtitles (PGS, VobSub)</div>
          <div className="mb-1.5 text-[10.5px] text-ink-faint">Plex has to burn these into the picture, which transcodes the video every time they're on.</div>
          {([["keep", "Keep them"], ["when_text", "Remove them when a text version exists (embedded, or an .srt from Subtitles)"], ["remove", "Always remove them — Subtitles can fetch text ones"]] as const).map(([v, label]) => (
            <label key={v} className="flex cursor-pointer items-center gap-2 py-0.5 text-[12px]">
              <input type="radio" name="image_subs" checked={d.image_subs === v} onChange={() => set({ image_subs: v })} />{label}
            </label>
          ))}
        </div>
        <Toggle on={d.tidy_tracks} set={(v) => set({ tidy_tracks: v })} label="Tidy tracks even when that's all a file needs"
          hint="Off: a file whose picture is already fine is only rewritten when dropping tracks frees at least 10% (a big foreign-language audio track does). Its other track changes happen when it's re-encoded, or with Fix tracks. On: every file is tidied — which rewrites it end to end, often to shed a few small subtitle tracks." />
        <p className="m-0 text-[11px] text-ink-faint">Untagged tracks are kept. A language filter that would remove every audio track or every subtitle removes none — the tags are wrong.</p>
      </Section>

      <details className="rounded-xl p-4" style={cardStyle}>
        <summary className="cursor-pointer text-[13.5px] font-bold">Advanced</summary>
        <div className="mt-3 flex flex-col gap-3.5">
          <Field label="Transcode folder" hint="Fast storage (an SSD/NVMe pool), never the array. Blank = the default.">
            <input type="text" value={d.scratch_dir} onChange={(e) => set({ scratch_dir: e.target.value })} placeholder="/transcode" className={`${inp} w-[240px]`} style={inpStyle} />
          </Field>
          {hw && <div className="text-[11px] text-ink-faint">Using <span className="font-mono text-ink-dim">{hw.dir}</span> · <b style={{ color: hw.free > 20 * 1024 ** 3 ? "var(--good)" : "var(--avoid)" }}>{fmtSize(hw.free)}</b> free</div>}
          {hw && hw.devices.length > 1 && (
            <Field label="GPU device" hint="The discrete card is usually the higher renderD number">
              <select value={d.vaapi_device || ""} onChange={(e) => set({ vaapi_device: e.target.value })} className={`${inp} w-[280px]`} style={inpStyle}>
                <option value="">Default (renderD128)</option>
                {hw.devices.map((dev) => <option key={dev.path} value={dev.path}>{dev.path.replace("/dev/dri/", "")} · {dev.vendor}{dev.pci ? ` · ${dev.pci}` : ""}</option>)}
              </select>
            </Field>
          )}
          <Field label="CPU cores" hint="How many cores a CPU conversion may use · 0 = half the machine">
            <input type="number" min="0" max="256" value={d.cpu_cores} onChange={(e) => set({ cpu_cores: Number(e.target.value) || 0 })} className={`${inp} w-[80px]`} style={inpStyle} />
          </Field>
          <Field label="Conversions at once" hint="1 is right for CPU; 2 can help with a GPU">
            <input type="number" min="1" max="4" value={d.workers} onChange={(e) => set({ workers: Math.min(4, Math.max(1, Number(e.target.value) || 1)) })} className={`${inp} w-[70px]`} style={inpStyle} />
          </Field>
        </div>
      </details>

      <div className="sticky bottom-3 flex items-center justify-end gap-3">
        {dirty && <span className="text-[11.5px] text-ink-faint">Unsaved changes</span>}
        <button onClick={onSave} disabled={!dirty || busy} className="rounded-lg px-4 py-2 text-[13px] font-semibold disabled:opacity-50" style={accentBtn}>{busy ? "Saving…" : "Save"}</button>
      </div>
    </div>
  );
}
