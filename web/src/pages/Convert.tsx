import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, Navigate, useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { useTabParam } from "../lib/useTabParam";
import { useSearchParamState } from "../lib/useSearchState";
import { usePoll } from "../lib/usePoll";
import { TabPanel, Tabs } from "../ui/Tabs";
import { Button, Modal, useToast } from "../ui";
import { LINKS } from "../lib/links";
import { RescanButton, ago } from "../components/RescanButton";
import { backlogLine, fileForecast, restartLoss } from "../lib/convertForecast";
import { groupVersions, nestedVersions } from "../lib/convertVersions";
import { type ConvertCandidate, type ConvertSeriesRollup, type ConvertLibraryStats, type ConvertBlocked, type ConvertSkipped, type ConvertJob, type ConvertStatus, type ConvertSettings, type ConvertCompareStatus, type ConvertHoldStats } from "../lib/api";
import { convertApi } from "../lib/convertApi";
import { HeldSummary, RevertButton } from "./convert/hold";
import { HistoryTab, RecentCard } from "./convert/History";
import { PlanCard, PlanToggle, Poster } from "./convert/PlanCard";
import { BulkConvertDialog, ConvertNowDialog, SwitchOnDialog } from "./convert/ConfirmConvert";
import { leftOutCount, leftOutLine, requestedToast, type Asking } from "./convert/plan";
import { Page } from "../ui/Page";

// Convert works through the library on its own: switch it on, choose the hours, and it
// re-encodes wasteful video to HEVC or AV1 at the same quality, one file at a time, pausing
// while someone watches Plex. There's no queue to manage — the Overview shows what it's
// doing, what's next and what it did; the Library lets you pick a file to do right now.
// The settings live in Settings → Convert; this page is the working view.
type Tab = "overview" | "library" | "problems" | "history" | "activity";
const TAB_KEYS: readonly Tab[] = ["overview", "library", "problems", "history", "activity"];
const ACTIVE = new Set(["preparing", "testing", "encoding", "verifying", "replacing"]);

const card = "rounded-xl p-4";
const cardStyle = { border: "1px solid var(--line)", background: "var(--panel)" } as const;
const lbl = "font-mono text-2xs font-bold uppercase tracking-[0.11em] text-ink-faint";
const inp = "rounded-lg px-2.5 py-1.5 text-sm";
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


function fmtEta(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return "—";
  if (sec < 60) return `${Math.round(sec)}s`;
  const m = Math.floor(sec / 60), s = Math.round(sec % 60);
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

// originalsCopy says, literally, what happens to an original once its conversion is
// verified: it waits in the originals hold, on the same disk, for the undo window (Convert's
// settings, Undo). null = the settings couldn't be read, so it stays general.
function originalsCopy(holdDays: number | null): string {
  if (holdDays === 0) return "Originals are deleted once the conversion is verified — there is no undo (change this under Undo in Convert's settings)";
  const days = holdDays ? `${holdDays} day${holdDays === 1 ? "" : "s"}` : "a while";
  return `Originals are kept for ${days} on the same disk, so a conversion can be undone`;
}

const STATE_LABEL: Record<string, string> = {
  preparing: "analysing", testing: "test-encoding clips", encoding: "encoding", verifying: "checking quality", replacing: "swapping in",
};

export function Convert() {
  // The old Settings tab's address (bookmarks, earlier copy) opens the settings in the hub.
  const [params] = useSearchParams();
  if (params.get("tab") === "settings") return <Navigate to={LINKS.convertSettings} replace />;
  return <ConvertPage />;
}

function ConvertPage() {
  const [tab, setTab] = useTabParam(TAB_KEYS, "overview");
  const [status, setStatus] = useState<ConvertStatus | null>(null);
  const [jobs, setJobs] = useState<ConvertJob[]>([]);
  const [stats, setStats] = useState<ConvertLibraryStats | null>(null);
  const [hw, setHw] = useState<{ using: string; reclaimed_bytes: number; hold?: ConvertHoldStats } | null>(null);
  const [settings, setSettings] = useState<ConvertSettings | null>(null);
  const [compareKey, setCompareKey] = useState<string | null>(null);
  const [libReload, setLibReload] = useState(0); // bumped when Compare closes: it may have measured a file
  const toast = useToast();
  const say = useCallback((m: string) => toast(m, { ms: 3500 }), [toast]);

  const loadStats = useCallback(() => convertApi.convertStats().then(setStats).catch(() => {}), []);
  const loadHw = useCallback(() => convertApi.convertHardware().then(setHw).catch(() => {}), []);
  const loadSettings = useCallback(() => convertApi.convertSettings().then(setSettings).catch(() => {}), []);
  useEffect(() => { loadStats(); loadHw(); loadSettings(); }, [loadStats, loadHw, loadSettings]);
  const originals = originalsCopy(settings ? settings.hold_days : null);

  const anyActive = jobs.some((j) => ACTIVE.has(j.state));
  // Bumps as jobs finish, so the ledger views re-read what was just recorded.
  const finished = jobs.filter((j) => !ACTIVE.has(j.state)).length;
  usePoll(() => Promise.all([
    convertApi.convertJobs().then(setJobs).catch(() => {}),
    convertApi.convertStatus().then(setStatus).catch(() => {}),
  ]), anyActive ? 1500 : 5000);
  useEffect(() => { if (!anyActive) { loadHw(); loadStats(); } }, [anyActive, loadHw, loadStats]);

  const refresh = useCallback(() => {
    convertApi.convertJobs().then(setJobs).catch(() => {});
    convertApi.convertStatus().then(setStatus).catch(() => {});
    loadStats();
    loadHw(); // freed and held space (a release moves bytes from one to the other)
  }, [loadStats, loadHw]);

  const rescanLibrary = useCallback(async (): Promise<boolean> => {
    const r = await convertApi.convertReindex();
    if (!r.started) { say(r.reason ?? "A scan is already running."); return false; }
    say("Rescanning the library…");
    for (let i = 0; i < 240; i++) {
      await new Promise((res) => setTimeout(res, 2000));
      const st = await convertApi.convertReindexStatus().catch(() => ({ running: false }));
      if (!st.running) break;
    }
    await loadStats();
    refresh();
    say("Library rescanned.");
    return true;
  }, [say, loadStats, refresh]);

  // Switch on asks first, with what it will do across the library (SwitchOnDialog);
  // Switch off is one click.
  const [askSwitchOn, setAskSwitchOn] = useState(false);
  const autoChanged = (s: ConvertSettings) => {
    setSettings(s);
    say(s.auto ? "Convert is on — it works through your library during your hours" : "Automatic conversion is off");
    refresh();
  };
  const toggleAuto = async () => {
    if (!settings) return;
    if (!settings.auto) { setAskSwitchOn(true); return; }
    try { autoChanged(await convertApi.updateConvertSettings({ auto: false })); } catch (e) { toast((e as Error).message, { tone: "error", ms: 3500 }); }
  };

  const TABS: { key: Tab; label: string }[] = [
    { key: "overview", label: "Overview" },
    { key: "library", label: "Library" },
    { key: "problems", label: "Problems" },
    { key: "history", label: "History" },
    { key: "activity", label: "Activity" },
  ];

  return (
    <>
      <PageHeader title="Convert" />
      <Page>
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <p className="max-w-[66ch] text-base text-ink-dim">
            Makes your library smaller at a quality checked against the original. Wasteful video is re-encoded to HEVC
            {settings?.allow_av1 ? " or AV1 (whichever is smaller at the same quality)" : ""} during your hours.
            Audio is never re-encoded: Atmos, TrueHD and DTS-HD pass through, and tracks you filter out in <Link to={LINKS.convertSettings} style={{ color: "var(--accent-text)" }}>Convert's settings</Link> are removed.
            HDR10, HDR10+ and HLG are kept; {settings?.dolby_vision === "skip"
              ? "Dolby Vision files are left alone, as you chose"
              : settings?.dolby_vision === "keep"
                ? "Dolby Vision files keep their Dolby Vision layer as profile 8.1 (a preview), or are left alone where it can't be kept"
                : "Dolby Vision files keep their HDR10 or HLG base and lose the Dolby Vision layer"}
            {stats?.total?.dolby_vision_files ? ` (${stats.total.dolby_vision_files.toLocaleString()} in your library)` : ""}.
          </p>
          <div className="flex items-center gap-2">
            <Link to={LINKS.convertSettings} className="rounded-lg px-3 py-2 text-base font-semibold" style={ghostBtn}>Configure →</Link>
            {settings && (
              <button onClick={toggleAuto} className="rounded-lg px-3.5 py-2 text-base font-semibold" style={settings.auto ? ghostBtn : accentBtn}>
                {settings.auto ? "Switch off" : "Switch on"}
              </button>
            )}
          </div>
        </div>

        <Tabs tabs={TABS} value={tab} onChange={setTab} idPrefix="convert" label="Convert sections" />

        <TabPanel idPrefix="convert" value={tab}>
          {tab === "overview" && <Overview status={status} jobs={jobs} stats={stats} hw={hw} originals={originals} flash={say} onChanged={refresh} onRescan={rescanLibrary} onShowProblems={() => setTab("problems")} onShowHistory={() => setTab("history")} />}
          {tab === "library" && <Library flash={say} onRequested={refresh} onRescan={rescanLibrary} onCompare={setCompareKey} running={jobs} reload={libReload} originals={originals} keepDV={settings?.dolby_vision === "keep"} />}
          {tab === "problems" && <Problems flash={say} />}
          {tab === "history" && <HistoryTab reload={finished} flash={(m) => { say(m); refresh(); }} />}
          {tab === "activity" && <LogsConsole />}
        </TabPanel>
      </Page>
      {askSwitchOn && <SwitchOnDialog onCancel={() => setAskSwitchOn(false)} switchOn={async () => { const s = await convertApi.updateConvertSettings({ auto: true }); setAskSwitchOn(false); autoChanged(s); }} />}
      {compareKey && <CompareModal itemKey={compareKey} onClose={() => { setCompareKey(null); setLibReload((n) => n + 1); }} flash={say} />}
    </>
  );
}

/* ============================= OVERVIEW ============================= */

const STATUS_TONE: Record<string, string> = {
  working: "var(--good)", starting: "var(--good)", paused: "var(--avoid)", waiting: "var(--ink-faint)", off: "var(--ink-faint)", done: "var(--good)",
};

function Overview({ status, jobs, stats, hw, originals, flash, onChanged, onRescan, onShowProblems, onShowHistory }: {
  status: ConvertStatus | null; jobs: ConvertJob[]; stats: ConvertLibraryStats | null; hw: { using: string; reclaimed_bytes: number; hold?: ConvertHoldStats } | null; originals: string;
  flash: (m: string) => void; onChanged: () => void; onRescan: () => Promise<boolean>; onShowProblems: () => void; onShowHistory: () => void;
}) {
  const [rescanning, setRescanning] = useState(false);
  const running = jobs.filter((j) => ACTIVE.has(j.state));
  const finished = jobs.length - running.length;
  const t = stats?.total;
  const n = t?.files ?? 0;
  const pct = (x: number) => (n ? Math.round((x / n) * 100) : 0);

  const cancelJob = async (id: number) => { try { await convertApi.convertCancel(id); onChanged(); } catch (e) { flash((e as Error).message); } };
  const cancelRequest = async (key: string) => { try { await convertApi.convertCancelRequest(key); onChanged(); } catch (e) { flash((e as Error).message); } };
  const [asking, setAsking] = useState<Asking | null>(null); // the Convert-now confirmation that's open
  // The hours as the status reports them ("01:00–07:00"), for the time forecast.
  const [winStart, winEnd] = status?.window ? status.window.split("–") : ["", ""];
  const backlog = status && status.remaining > 0 ? backlogLine(status.backlog_sec, winStart, winEnd) : "";
  const loss = restartLoss(status?.in_flight_secs);

  return (
    <div className="flex flex-col gap-3.5">
      {asking && (
        <ConvertNowDialog itemKey={asking.key} title={asking.title} tracksOnly={asking.tracksOnly} originals={originals} onCancel={() => setAsking(null)}
          onDone={(res) => { setAsking(null); flash(requestedToast(asking.title, res)); onChanged(); }} />
      )}
      {status && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl px-4 py-3" style={{ ...cardStyle, borderColor: STATUS_TONE[status.state] }}>
          <div className="flex items-center gap-2.5">
            <span className="h-2.5 w-2.5 flex-none rounded-full" style={{ background: STATUS_TONE[status.state] }} />
            <span className="text-md font-semibold">{status.message}</span>
            {status.state === "done" && (status.problems ?? 0) > 0 && (
              <button onClick={onShowProblems} className="text-sm font-semibold underline" style={{ color: "var(--avoid-text)" }}>Open Problems</button>
            )}
          </div>
          <span className="text-sm text-ink-faint">
            {status.remaining > 0 ? `${status.remaining.toLocaleString()} file${status.remaining === 1 ? "" : "s"} to go` : ""}
            {status.window ? ` · hours ${status.window}` : status.auto ? " · any time" : ""}
            {status.watching ? " · someone is watching" : ""}
          </span>
          {(backlog || loss) && (
            <div className="flex w-full flex-wrap gap-x-3 gap-y-1 text-sm">
              {backlog && <span className="text-ink-dim" title="Estimated from this machine's own conversion speed; pauses while someone watches aren't counted">{backlog}</span>}
              {loss && <span role="status" style={{ color: "var(--avoid-text)" }}>⚠ {loss}</span>}
            </div>
          )}
        </div>
      )}

      <div className="grid gap-3.5" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(280px, 1fr))" }}>
        <div className={card} style={cardStyle}>
          <div className="flex items-center justify-between">
            <div className={lbl}>Space saved</div>
            <RescanButton busy={rescanning} title={stats?.as_of ? `Rescan the library · figures as of ${ago(stats.as_of)}` : "Rescan the library"}
              onClick={async () => { setRescanning(true); try { await onRescan(); } catch { /* flashed */ } finally { setRescanning(false); } }} />
          </div>
          <div className="mt-2 flex items-baseline gap-2">
            <span className="text-display font-extrabold tracking-tight">{fmtSize(hw?.reclaimed_bytes)}</span>
            <span className="text-sm text-ink-faint">freed</span>
          </div>
          <HeldSummary hold={hw?.hold} flash={flash} onChanged={onChanged} />
          <div className="mt-3 border-t pt-3 text-sm text-ink-dim" style={{ borderColor: "var(--line-soft)" }}>
            <span style={{ color: "var(--good-text)" }}>~{fmtSize(t?.reclaimable)}</span> more to save ·{" "}
            <b style={{ color: "var(--ink)" }}>{(t?.convertible ?? 0).toLocaleString()}</b> of {n.toLocaleString()} files worth converting
            {(t?.skipped ?? 0) > 0 && (
              <button onClick={onShowProblems} className="mt-1 block text-left text-xs underline" style={{ color: "var(--avoid-text)" }}>
                {t!.skipped.toLocaleString()} file{t!.skipped === 1 ? "" : "s"} can&rsquo;t be converted — see Problems
              </button>
            )}
            {leftOutLine(t?.left_out) && (
              <Link to={LINKS.convertSettings} data-testid="convert-left-out-summary" className="mt-1 block text-xs text-ink-faint underline" title="Change the folder and resolution rules in Settings → Convert; a title's own switch is on its page and its plan card">
                {leftOutLine(t?.left_out)}
              </Link>
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
          <div className="mt-3 flex flex-wrap gap-x-3.5 gap-y-1.5 text-sm text-ink-dim">
            <Legend c="var(--avoid)" label={`H.264 · ${pct(t?.h264 ?? 0)}%`} />
            <Legend c="var(--good)" label={`HEVC · ${pct(t?.hevc ?? 0)}%`} />
            <Legend c="var(--accent)" label={`AV1 · ${pct(t?.av1 ?? 0)}%`} />
            <Legend c="var(--ink-faint)" label={`Other · ${pct(t?.other ?? 0)}%`} />
          </div>
          <div className="mt-2 font-mono text-xs text-ink-faint">{n.toLocaleString()} files · {fmtSize(t?.total_bytes)}</div>
        </div>
        <div className={card} style={cardStyle}>
          <div className={lbl}>How it works</div>
          <ul className="mt-2 flex flex-col gap-1.5 text-sm text-ink-dim">
            <li>• Converts on <b className="text-[var(--ink)]">{hw?.using ?? "…"}</b></li>
            <li>• Only re-encodes video that's wasteful for its resolution — lean and efficient files are left alone</li>
            <li>• Every re-encode is compared with the original on ten scenes spread through it: SSIM must average 0.97 or better with no scene below 0.96, and the file must save at least 20% — otherwise the original stays</li>
            <li>• Before any file replaces its original, every frame of it is decoded to prove it plays cleanly, all the way through</li>
            <li>• Never touches a file that's still seeding</li>
            <li>• {originals}</li>
          </ul>
        </div>
      </div>

      <div className={card} style={cardStyle}>
        <div className="text-md font-bold">Now</div>
        {running.length === 0
          ? <div className="mt-2 text-sm text-ink-dim">Nothing converting right now.</div>
          : <div className="mt-2 flex flex-col gap-3">{running.map((j) => <JobBar key={j.id} j={j} onCancel={() => cancelJob(j.id)} />)}</div>}
      </div>

      {status && (status.requests.length > 0 || status.up_next.length > 0) && (
        <div className={card} style={cardStyle}>
          <div className="text-md font-bold">Up next</div>
          <div className="mt-2 flex flex-col gap-1.5">
            {status.requests.map((r) => (
              <div key={r.key} className="flex items-center gap-2.5 text-sm">
                <span className="rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent-text)" }}>requested</span>
                <span className="flex-1 truncate font-semibold">{r.title}</span>
                <button onClick={() => cancelRequest(r.key)} aria-label={`Remove ${r.title}`} className="rounded px-1.5 py-0.5 font-mono text-xs text-ink-faint hover:text-[var(--avoid)]">✕</button>
              </div>
            ))}
            {status.up_next.map((u) => (
              <div key={u.key} className="flex items-center gap-2.5 text-sm">
                <span className="rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{u.reason}</span>
                <span className="flex-1 truncate">{u.title}</span>
                <span className="font-mono text-xs text-ink-faint">
                  {u.video ? `${u.current} → ${u.codec.toUpperCase()} · ~${fmtSize(u.saving)} saved` : u.tracks}
                  {u.eta_sec ? ` · ${fileForecast(u.eta_sec, !!status.window, u.nights)}` : ""}
                </span>
                <button onClick={() => setAsking({ key: u.key, title: u.title, tracksOnly: !u.video })} title="Convert this one now, regardless of the hours" className="rounded px-1.5 py-0.5 text-xs font-semibold text-ink-faint hover:text-[var(--accent)]">now</button>
              </div>
            ))}
            {status.remaining > status.up_next.length && <div className="text-sm text-ink-faint">+ {(status.remaining - status.up_next.length).toLocaleString()} more, biggest savings first</div>}
          </div>
        </div>
      )}

      <RecentCard reload={finished} onSeeAll={onShowHistory} />
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
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
        <span className="font-semibold">
          {j.title}
          <span className="ml-1.5 rounded-full px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>
            {j.codec ? `→ ${j.codec.toUpperCase()} · ` : ""}{j.encoder}
          </span>
          {j.requested && <span className="ml-1 rounded-full px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent-text)" }}>requested</span>}
        </span>
        <span className="flex items-center gap-2 font-mono tabular-nums text-ink-dim">
          {j.paused ? <span style={{ color: "var(--avoid-text)" }}>paused — {j.paused}</span>
            : encoding ? `${pct.toFixed(1)}%${eta > 0 ? ` · ${fmtEta(eta)} left` : ""}`
            : STATE_LABEL[j.state] ?? j.state}
          {onCancel && <button onClick={onCancel} aria-label={`Cancel ${j.title}`} title="Stop — the original is untouched until a conversion completes"
            className="rounded px-1.5 py-0.5 text-xs hover:text-[var(--avoid)]">✕</button>}
        </span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        <div className="h-full" style={{ width: `${pct}%`, background: j.paused ? "var(--avoid)" : "linear-gradient(90deg, var(--accent-deep), var(--accent))", transition: "width 1s linear" }} />
      </div>
      {encoding && j.fps > 0 && <div className="mt-1 flex gap-4 font-mono text-xs text-ink-faint"><span><b className="text-ink">{Math.round(j.fps)}</b> fps</span><span><b className="text-ink">{j.speed_x.toFixed(1)}×</b> realtime</span><span>{fmtSize(j.src_bytes)} source</span></div>}
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

// Saves renders an expected saving as a reduction — "−46 GB (40%)" — with the size the
// file ends up at underneath, so it can't be mistaken for the new size. measured marks a
// figure from a test encode of the file rather than the estimate.
function Saves({ bytes, of, faint, measured }: { bytes: number; of: number; faint?: boolean; measured?: boolean }) {
  if (!bytes || bytes <= 0) return <span className="text-ink-faint">—</span>;
  const pct = of > 0 ? Math.round((bytes / of) * 100) : 0;
  return (
    <span className="inline-flex flex-col leading-tight">
      <span style={{ color: faint ? "var(--ink-faint)" : "var(--good-text)" }}>
        −{fmtSize(bytes)} <span className="text-xs opacity-80">({pct}%)</span>
      </span>
      {of > 0 && (
        <span className="text-xs text-ink-faint">
          → ~{fmtSize(of - bytes)} after
          {measured && <span className="ml-1 rounded px-1 font-semibold" style={{ color: "var(--good-text)", border: "1px solid var(--good)" }} title="Measured by a test encode of this file at the real settings — not the estimate">measured</span>}
        </span>
      )}
    </span>
  );
}

function Pill({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" onClick={onClick} className="rounded-full px-2.5 py-1 text-xs font-semibold transition-colors"
      style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "transparent", color: active ? "var(--accent-text)" : "var(--ink-dim)" }}>
      {children}
    </button>
  );
}

// VersionChip names an extra version of a film (a 4K remux kept next to the 1080p copy).
function VersionChip({ label }: { label?: string }) {
  return (
    <span className="rounded px-1.5 py-0.5 font-mono text-xs font-bold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }} title="An extra version of this film — converted on its own">
      {label || "Version"}
    </span>
  );
}

function NeedTag({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span className="rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid-text)" }} title={title}>
      {children}
    </span>
  );
}

function hdrLabel(c: ConvertCandidate, keepDV: boolean): string {
  const h = c.info?.hdr;
  if (!h || h === "SDR") return "—";
  // "DV → HDR10" only for a file a conversion would actually take down to its base; with
  // Dolby Vision kept, "DV → 8.1".
  if (h === "Dolby Vision") {
    if (!c.needs?.video || !c.info?.dv_base || c.info.dv_base === "SDR") return "DV";
    return keepDV ? "DV → 8.1" : `DV → ${c.info.dv_base}`;
  }
  return h;
}

// The library list is for movies or shows (?media=tv).
const MEDIA_KINDS = ["movies", "tv"] as const;

function Library({ flash, onRequested, onRescan, onCompare, running, reload, originals, keepDV }: {
  flash: (m: string) => void; onRequested: () => void; onRescan: () => Promise<boolean>; onCompare: (key: string) => void; running: ConvertJob[]; reload: number; originals: string;
  keepDV: boolean;
}) {
  const [media, setMedia] = useSearchParamState<"movies" | "tv">("media", "movies", { allowed: MEDIA_KINDS });
  const [shows, setShows] = useState<ConvertSeriesRollup[] | null>(null);
  const [show, setShow] = useState<ConvertSeriesRollup | null>(null);
  const [items, setItems] = useState<ConvertCandidate[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [requested, setRequested] = useState<Set<string>>(new Set());
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "save", dir: "desc" });
  const [showSort, setShowSort] = useState<{ key: ShowSortKey; dir: "asc" | "desc" }>({ key: "save", dir: "desc" });
  const [codecF, setCodecF] = useState<Set<string>>(new Set());
  const [q, setQ] = useSearchParamState<string>("q", "", { debounceMs: 300 });
  // A link that names a title (?q=, from Insights' Library tab) must land on it whether or
  // not it's worth converting, so it starts with every file shown.
  const [onlyConvertible, setOnlyConvertible] = useState(() => q === "");
  const [loadErr, setLoadErr] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const [openPlan, setOpenPlan] = useState<string | null>(null); // the row whose plan card is open
  const runningKeys = useMemo(() => new Set(running.filter((j) => ACTIVE.has(j.state)).map((j) => j.key)), [running]);

  useEffect(() => {
    setItems(null); setShow(null); setRequested(new Set()); setCodecF(new Set()); setLoadErr(false);
    if (media === "tv") {
      setShows(null);
      convertApi.convertLibrarySeries().then(setShows).catch(() => { setShows([]); setLoadErr(true); });
    } else {
      convertApi.convertLibrary("movies", undefined, onlyConvertible).then(setItems).catch(() => { setItems([]); setLoadErr(true); });
    }
  }, [media, onlyConvertible, refreshKey]);

  useEffect(() => {
    if (media !== "tv" || !show) return;
    setItems(null); setCodecF(new Set()); setLoadErr(false);
    convertApi.convertLibrary("tv", show.series_id, onlyConvertible).then(setItems).catch(() => { setItems([]); setLoadErr(true); });
  }, [media, show, onlyConvertible, refreshKey]);

  // Re-read in place (keeping the open show and filters) when something outside this view
  // changed the numbers — a Compare that just measured a file.
  useEffect(() => {
    if (!reload) return;
    if (media === "tv") {
      convertApi.convertLibrarySeries().then(setShows).catch(() => {});
      if (show) convertApi.convertLibrary("tv", show.series_id, onlyConvertible).then(setItems).catch(() => {});
    } else {
      convertApi.convertLibrary("movies", undefined, onlyConvertible).then(setItems).catch(() => {});
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload]);

  // Convert now / Fix tracks and the bulk show or season buttons confirm first, saying what
  // will change (ConfirmConvert.tsx); the request is sent from the dialog.
  const [asking, setAsking] = useState<Asking | null>(null);
  const [askingBulk, setAskingBulk] = useState<{ seriesID: number; season?: number; label: string } | null>(null);
  const convert = (c: ConvertCandidate) => setAsking({ key: c.key, title: c.version_id ? `${c.title} · ${c.version_label || "version"}` : c.title, tracksOnly: !c.needs?.video });
  const convertBulk = (seriesID: number, season: number | undefined, label: string) => setAskingBulk({ seriesID, season, label });

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
    // A film's extra versions sit under its default file, whatever the sort.
    return groupVersions(list.sort((a, b) => {
      const va = val(a), vb = val(b);
      return typeof va === "string" || typeof vb === "string" ? String(va).localeCompare(String(vb)) * dir : (va - vb) * dir;
    }));
  }, [items, codecF, sort, q]);
  const nested = useMemo(() => nestedVersions(view), [view]);

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
      className="px-3 py-2 text-left font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">
      {h.key ? <button type="button" onClick={() => onSort(h.key!)} className="font-inherit uppercase hover:text-[var(--ink)]">{h.label}{cur.key === h.key ? (cur.dir === "asc" ? " ▲" : " ▼") : ""}</button> : h.label}
    </th>
  );

  return (
    <div className="flex flex-col gap-2">
      {asking && (
        <ConvertNowDialog itemKey={asking.key} title={asking.title} tracksOnly={asking.tracksOnly} originals={originals} onCancel={() => setAsking(null)}
          onDone={(res) => { setAsking(null); setRequested((s) => new Set(s).add(asking.key)); flash(requestedToast(asking.title, res)); onRequested(); }} />
      )}
      {askingBulk && (
        <BulkConvertDialog seriesID={askingBulk.seriesID} season={askingBulk.season} label={askingBulk.label} originals={originals} onCancel={() => setAskingBulk(null)}
          onDone={(n) => { setAskingBulk(null); flash(n > 0 ? `${n} episode${n === 1 ? "" : "s"} from ${askingBulk.label} requested` : `Nothing to do in ${askingBulk.label}`); onRequested(); }} />
      )}
      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex w-fit rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          {(["movies", "tv"] as const).map((m) => (
            <button key={m} onClick={() => setMedia(m)} className="rounded-md px-3.5 py-1.5 text-sm font-semibold" style={{ background: media === m ? "var(--accent)" : "transparent", color: media === m ? "var(--accent-ink)" : "var(--ink-faint)" }}>
              {m === "movies" ? "Movies" : "TV Shows"}
            </button>
          ))}
        </div>
        <button onClick={async () => { setBusy("reindex"); try { if (await onRescan()) setRefreshKey((k) => k + 1); } catch (e) { flash((e as Error).message); } finally { setBusy(null); } }}
          disabled={busy !== null} title="Pick up files added or changed since the last scan" className="rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-50" style={ghostBtn}>
          {busy === "reindex" ? "Scanning…" : "Rescan library"}
        </button>
        <input type="search" aria-label="Search titles" value={q} onChange={(e) => setQ(e.target.value)} placeholder={media === "tv" ? "Search shows…" : "Search movies…"}
          className={inp} style={{ ...inpStyle, minWidth: 200 }} />
        <label className="flex cursor-pointer items-center gap-1.5 text-sm text-ink-dim">
          <input type="checkbox" checked={onlyConvertible} onChange={(e) => setOnlyConvertible(e.target.checked)} />
          Only files worth converting
        </label>
      </div>

      {loadErr && <div role="status" className="rounded-lg px-3 py-2 text-sm" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>Couldn't load the library — this is an error, not an empty library.</div>}

      {media === "tv" && !show ? (
        shows === null ? <Empty>Loading your shows…</Empty> : shows.length === 0 ? <Empty>No downloaded episodes yet.</Empty> : (
          <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
            <table className="w-full border-collapse text-base" style={{ minWidth: 720 }}>
              <thead><tr style={{ background: "var(--panel-2)" }}>{SHOW_HEADERS.map((h) => sortHead(h, showSort, setShowSortKey))}</tr></thead>
              <tbody>
                {showView.map((sh, i) => {
                  const done = sh.convertible === 0;
                  return (
                    <tr key={sh.series_id} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                      <td className="px-3 py-2 font-semibold"><button onClick={() => setShow(sh)} className="flex items-center gap-2 text-left hover:underline"><Poster url={sh.poster_url} title={sh.title} /><span>{sh.title} <span className="font-normal text-ink-faint">{sh.year || ""}</span></span></button></td>
                      <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{sh.files.toLocaleString()}</td>
                      <td className="px-3 py-2 font-mono tabular-nums">
                        {done ? <span className="text-ink-faint">none</span> : <span style={{ color: "var(--avoid-text)" }}>{sh.convertible.toLocaleString()}</span>}
                        {sh.reencode > 0 && sh.reencode < sh.convertible && <span className="ml-1.5 text-xs text-ink-faint">{sh.reencode} re-encode</span>}
                        {sh.tidy_only > 0 && <span className="ml-1.5 text-xs text-ink-faint" title="Only their tracks differ, and tidying them would free little — done when they're re-encoded, or with Fix tracks">+{sh.tidy_only} tracks only</span>}
                        {sh.excluded
                          ? <span className="ml-1.5 text-xs text-ink-faint" title="You told Convert to leave this show alone">show left out</span>
                          : leftOutCount(sh.left_out) > 0 && <span className="ml-1.5 text-xs text-ink-faint" title="Worth converting, but left alone by your choices (a season, a library folder or a resolution)">{leftOutCount(sh.left_out)} left out</span>}
                      </td>
                      <td className="px-3 py-2 font-mono tabular-nums">{fmtSize(sh.total_bytes)}</td>
                      <td className="px-3 py-2 font-mono tabular-nums"><Saves bytes={sh.save_bytes} of={sh.total_bytes} /></td>
                      <td className="px-3 py-2">
                        <div className="flex items-center justify-end gap-1.5">
                          <button onClick={() => setShow(sh)} className="rounded-lg px-2.5 py-1.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Episodes</button>
                          {!done && <button onClick={() => convertBulk(sh.series_id, undefined, sh.title)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>Convert now</button>}
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
                <button onClick={() => setShow(null)} className="rounded-lg px-2.5 py-1.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>← All shows</button>
                <span className="text-md font-bold">{show.title}</span>
              </div>
              <div className="flex flex-wrap items-center gap-1.5">
                {seasons.map((n) => (
                  <button key={n} onClick={() => convertBulk(show.series_id, n, `${show.title} season ${n}`)} disabled={busy !== null}
                    className="rounded-lg px-2.5 py-1.5 text-xs font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>
                    {`Convert S${n}`}
                  </button>
                ))}
              </div>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-0.5 font-mono text-2xs uppercase text-ink-faint">Codec</span>
            {codecs.map(([cc, n]) => <Pill key={cc} active={codecF.has(cc)} onClick={() => toggleCodec(cc)}>{cc} <span className="opacity-60">{n}</span></Pill>)}
            {codecF.size > 0 && <button onClick={() => setCodecF(new Set())} className="ml-1 text-xs text-ink-faint underline hover:text-[var(--ink)]">clear</button>}
          </div>
          <p className="text-xs text-ink-faint">“Saves” is a deliberately cautious estimate from each file's own bitrate until a file has been test-encoded — then it's the <b>measured</b> number. <b>Compare</b> measures one now, and lets you watch the clips.</p>
          <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
            <table className="w-full border-collapse text-base" style={{ minWidth: 900 }}>
              <thead><tr style={{ background: "var(--panel-2)" }}>{HEADERS.map((h) => sortHead(h, sort, setSortKey))}</tr></thead>
              <tbody>
                {view.map((c, i) => {
                  const cd = c.info?.video_codec?.toUpperCase() ?? "?";
                  const isRunning = runningKeys.has(c.key);
                  const planOpen = openPlan === c.key;
                  return (
                    <Fragment key={c.key}>
                    <tr style={{ borderTop: i === 0 ? "none" : nested.has(c) ? "1px dashed var(--line-soft)" : "1px solid var(--line-soft)" }}>
                      <td className="px-3 py-2 font-semibold">
                        <PlanToggle open={planOpen} title={c.version_id ? `${c.title} · ${c.version_label || "version"}` : c.title} onToggle={() => setOpenPlan(planOpen ? null : c.key)}>
                          {nested.has(c) ? (
                            // Under its film's default file: the version's name says which file it is.
                            <span className="flex items-center gap-1.5 pl-4 font-normal text-ink-dim"><span aria-hidden className="text-ink-faint">↳</span><VersionChip label={c.version_label} /></span>
                          ) : (
                            <>
                              <Poster url={c.poster_url} title={c.title} />
                              <span>{c.title} <span className="font-normal text-ink-faint">{c.year || ""}</span>{c.version_id ? <> <VersionChip label={c.version_label} /></> : null}</span>
                            </>
                          )}
                        </PlanToggle>
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex flex-wrap items-center gap-1">
                          <span className="rounded px-1.5 py-0.5 font-mono text-xs font-bold" style={{ background: c.needs?.video ? "var(--avoid-soft)" : "var(--good-soft, rgba(127,176,105,.16))", color: c.needs?.video ? "var(--avoid-text)" : "var(--good-text)" }} title={c.needs?.why}>{cd}</span>
                          {(c.needs?.subs || c.needs?.audio) && <NeedTag title={c.tracks}>tracks</NeedTag>}
                          {c.needs?.excluded && <NeedTag title={c.needs.why}>left out</NeedTag>}
                        </div>
                      </td>
                      <td className="px-3 py-2 font-mono text-ink-dim">{c.info?.resolution ?? "—"}</td>
                      <td className="px-3 py-2 font-mono text-ink-dim" title={c.info?.hdr === "Dolby Vision" ? (c.needs?.video ? (keepDV ? "Kept as Dolby Vision profile 8.1 when converted (preview)" : "The Dolby Vision layer is removed when converted") : c.needs?.why) : undefined}>{hdrLabel(c, keepDV)}</td>
                      <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{c.info?.bitrate_kbps ? `${(c.info.bitrate_kbps / 1000).toFixed(1)} Mb/s` : "—"}</td>
                      <td className="px-3 py-2 font-mono tabular-nums">{fmtSize(c.info?.size_bytes)}</td>
                      <td className="px-3 py-2 font-mono tabular-nums" title={c.tracks || undefined}>
                        {c.candidate ? <Saves bytes={c.save_bytes} of={c.info?.size_bytes ?? 0} faint={!c.worth} measured={c.needs?.measured} /> : <span className="text-ink-faint">—</span>}
                        {c.candidate && !c.needs?.video && <span className="ml-1 text-xs text-ink-faint">tracks</span>}
                      </td>
                      <td className="px-3 py-2">
                        {isRunning ? <div className="text-right"><span className="font-mono text-xs" style={{ color: "var(--accent-text)" }}>converting…</span></div>
                          : c.candidate ? (
                          <div className="flex items-center justify-end gap-1.5">
                            {!c.worth && <span className="font-mono text-xs text-ink-faint" title="Only the tracks differ, and tidying them would free little. Done when it's re-encoded, or now with Fix tracks.">little to gain</span>}
                            {c.needs?.video && <button onClick={() => onCompare(c.key)} title="Encode a few clips as HEVC and AV1, compare size and quality, and keep the clips to watch" className="rounded-lg px-2.5 py-1.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Compare</button>}
                            {requested.has(c.key) ? <span className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--good)", color: "var(--good-text)" }}>Next ✓</span>
                              : <button onClick={() => convert(c)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-50" style={c.worth ? { border: "1px solid var(--accent-line)", color: "var(--accent-text)" } : { border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{c.needs?.video ? "Convert now" : "Fix tracks"}</button>}
                          </div>
                        ) : (
                          <div className="flex items-center justify-end gap-1.5">
                            <span className="font-mono text-xs text-ink-faint">{c.needs?.why || "on target"}</span>
                            {c.revert_history_id ? <RevertButton id={c.revert_history_id} flash={flash} onDone={() => { setRefreshKey((n) => n + 1); onRequested(); }} /> : null}
                          </div>
                        )}
                      </td>
                    </tr>
                    {planOpen && <tr><td colSpan={HEADERS.length} className="px-3 pb-3"><PlanCard itemKey={c.key} /></td></tr>}
                    </Fragment>
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
  return <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>{children}</div>;
}

/* ============================= COMPARE ============================= */

function CompareModal({ itemKey, onClose, flash }: { itemKey: string; onClose: () => void; flash: (m: string) => void }) {
  const [st, setSt] = useState<ConvertCompareStatus | null>(null);
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    convertApi.convertCompare(itemKey).then(setSt).catch(async (e) => {
      const cur = await convertApi.convertCompareStatus().catch(() => null);
      if (cur?.running && cur.key !== itemKey) flash(`Another comparison is running (${cur.title})`);
      else flash((e as Error).message);
      setSt(cur);
    });
  }, [itemKey, flash]);
  usePoll(() => convertApi.convertCompareStatus().then(setSt).catch(() => {}), 2500, { immediate: false });

  const r = st?.result && st.key === itemKey ? st.result : null;
  const side = (s: { codec: string; encoder: string; bytes: number; ssim: number; est_bytes: number }, win: boolean) => (
    <div className="flex-1 rounded-lg p-3" style={{ border: `1.5px solid ${win ? "var(--good)" : "var(--line)"}`, background: "var(--panel-2)" }}>
      <div className="flex items-center justify-between"><span className="text-md font-bold">{s.codec.toUpperCase()}</span>{win && <span className="font-mono text-xs font-bold" style={{ color: "var(--good-text)" }}>CHOSEN</span>}</div>
      <div className="mt-1 text-xs text-ink-faint">{s.encoder}</div>
      <div className="mt-2 font-mono text-sm">~{fmtSize(s.est_bytes)} <span className="text-ink-faint">whole file (video)</span></div>
      <div className="font-mono text-sm">SSIM {s.ssim ? s.ssim.toFixed(4) : "—"}</div>
    </div>
  );
  return (
    <Modal onClose={onClose} size="md" title={`HEVC vs AV1${st?.title ? ` — ${st.title}` : ""}`} footer={<Button onClick={onClose}>Close</Button>}>
      {!r && !st?.error && (
        <p className="mt-2 text-base text-ink-dim">
          Encoding a few clips both ways at the real settings and scoring each against the original… This takes a few minutes
          (longer for 4K). You can close this — it keeps going, and the result shows here when you open Compare again.
        </p>
      )}
      {st?.error && st.key === itemKey && <p className="mt-2 text-base" style={{ color: "var(--reject-text)" }}>{st.error}</p>}
      {r && (
        <>
          <div className="mt-3 flex gap-2.5">{side(r.hevc, r.pick === "hevc")}{side(r.av1, r.pick === "av1")}</div>
          <p className="mt-3 text-base">{r.why}</p>
          <p className="mt-1 text-xs text-ink-faint">Original: {fmtSize(r.src_bytes)} · tested on {r.clips} clip{r.clips === 1 ? "" : "s"} ({Math.round(r.seconds)}s). Audio is copied as is, so it adds the same to both. The Library now shows this file's measured size.</p>
          {r.files && r.files.length > 0 && (
            <div className="mt-3">
              <div className={lbl}>Watch the clips (VLC or mpv)</div>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {r.files.map((f) => <a key={f} href={convertApi.convertCompareFileURL(f)} className="rounded-md px-2 py-1 font-mono text-xs" style={ghostBtn}>{f}</a>)}
              </div>
              <p className="mt-1.5 text-xs text-ink-faint">Dark scenes and skies are where codecs differ — look there. The original clip may start a moment earlier than the encoded ones.</p>
            </div>
          )}
        </>
      )}
    </Modal>
  );
}

/* ============================= PROBLEMS ============================= */

const SKIP_LABEL: Record<string, string> = {
  hdr_unsupported: "Their HDR can't be carried through a conversion (Dolby Vision profile 5, HDR10+ that couldn't be read, or Dolby Vision you chose to keep that didn't come through intact), so they were left as they are",
  hardlinked: "Still seeding — Convert won't replace a file your torrent client is sharing. Tried again twice a day",
  not_smaller: "Converting them didn't save enough to be worth it, so the originals were kept",
  quality_gate: "The conversion couldn't match the original's quality, so the originals were kept",
  cancelled: "You stopped these — they're left alone for a month unless you try them again",
  no_scratch: "The transcode folder hasn't enough free space for these. Each is tried again after an hour, then six, then once a day — or free some space and try them now",
  library_full: "The library disk hasn't room for the converted file next to the original. The originals were kept; each is tried again after an hour, then six, then once a day",
  source_gone: "The library file was missing when Convert went to read it — usually mid-import or mid-upgrade. Checked again later",
  transient: "These hit a problem that usually clears on its own (often a full disk). Each is tried again after an hour, then six, then once a day",
  reverted: "You reverted their conversions, so Convert leaves them alone. Converting one by hand from the Library still works",
  hold_full: "The originals hold is full: each converted file's original is kept for the undo window, and the hold has reached the size you set. Nothing is deleted to make room — these convert after the next original is released (its window ends, or you release some from History), or once you raise the hold's size in Convert's settings",
  name_taken: "The converted file would take the name of a file that's already there: one another library record points at (a second version, say), or one Arrmada doesn't track (a copy you keep beside the film). That file was left alone and the originals kept. Rename or remove it to let these convert; each is tried again after an hour, then six, then once a day",
  no_hold: "There's nowhere on the file's own disk to keep the original while the conversion can be undone — usually the file is outside your library folders, or a library folder isn't mounted. The originals were kept; each is tried again after an hour, then six, then once a day",
};

// retryIn says when a temporary skip is next tried, or "" when it isn't waiting.
function retryIn(unix: number): string {
  const sec = unix - Date.now() / 1000;
  if (!unix || sec <= 0) return "";
  if (sec < 3600) return `tries again in ${Math.max(1, Math.round(sec / 60))} min`;
  if (sec < 48 * 3600) return `tries again in ${Math.round(sec / 3600)} h`;
  return `tries again in ${Math.round(sec / 86400)} days`;
}

function Problems({ flash }: { flash: (m: string) => void }) {
  const [skips, setSkips] = useState<ConvertSkipped[] | null>(null);
  const [blocked, setBlocked] = useState<ConvertBlocked[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const load = useCallback(() => {
    convertApi.convertSkips().then(setSkips).catch(() => setSkips([]));
    convertApi.convertBlocklist().then(setBlocked).catch(() => setBlocked([]));
  }, []);
  useEffect(() => { load(); }, [load]);

  const retrySkips = async (kind?: string, key?: string) => {
    setBusy(key ?? kind ?? "all");
    try {
      if (key) await convertApi.convertSkipsClear(key);
      else if (kind) await convertApi.convertSkipsClearKind(kind); // the whole group, one request
      else await convertApi.convertSkipsClear();
      flash("Cleared — they'll be picked up again");
      load();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const retryBlocked = async (key?: string) => {
    setBusy(key ?? "blocked");
    try { await convertApi.convertBlocklistClear(key); flash("Cleared — will be tried again"); load(); }
    catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  if (skips === null || blocked === null) return <div className="text-sm text-ink-faint">Loading…</div>;
  const groups = new Map<string, ConvertSkipped[]>();
  for (const s of skips) groups.set(s.kind, [...(groups.get(s.kind) ?? []), s]);
  if (!skips.length && !blocked.length) {
    return <div className={card} style={cardStyle}><div className="text-md font-bold">Nothing stuck</div><p className="mt-1 text-sm text-ink-dim">Every file has either converted or is waiting its turn.</p></div>;
  }
  return (
    <div className="flex flex-col gap-3.5">
      {[...groups.entries()].map(([kind, list]) => (
        <div key={kind} className={card} style={cardStyle}>
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div>
              <div className="text-md font-bold">{list.length.toLocaleString()} file{list.length === 1 ? "" : "s"}{list[0].permanent ? null : <span className="ml-2 font-mono text-xs font-normal text-ink-faint">TEMPORARY</span>}</div>
              <p className="mt-0.5 text-sm text-ink-dim">{SKIP_LABEL[kind] ?? list[0].reason}</p>
            </div>
            <button onClick={() => retrySkips(kind)} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{busy === kind ? "…" : "Try these again"}</button>
          </div>
          <div className="mt-2.5 flex flex-col gap-1">
            {list.slice(0, 12).map((s) => (
              <div key={s.key} className="flex items-center gap-2.5 text-sm">
                <span className="min-w-0 flex-1 truncate text-ink-dim" title={s.reason}>{s.title}</span>
                <span className="hidden max-w-[45%] truncate text-xs text-ink-faint sm:block" title={s.reason}>{s.reason}</span>
                {!s.permanent && retryIn(s.retry_after) && <span className="shrink-0 font-mono text-xs text-ink-faint">{retryIn(s.retry_after)}</span>}
                <button onClick={() => retrySkips(undefined, s.key)} disabled={busy !== null} className="rounded px-1.5 py-0.5 font-mono text-xs text-ink-faint hover:text-[var(--accent)]">retry</button>
              </div>
            ))}
            {list.length > 12 && <div className="text-sm text-ink-faint">+ {(list.length - 12).toLocaleString()} more</div>}
          </div>
        </div>
      ))}
      {blocked.length > 0 && (
        <div className={card} style={cardStyle}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <div className="text-md font-bold">Repeated failures <span className="font-mono text-xs text-ink-faint">{blocked.length}</span></div>
              <p className="mt-0.5 text-sm text-ink-dim">These failed three times, so Convert stopped trying. The last error is shown.</p>
            </div>
            <button onClick={() => retryBlocked()} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Clear all</button>
          </div>
          <div className="mt-2.5 flex flex-col gap-1.5">
            {blocked.map((b) => (
              <div key={b.key} className="flex items-start gap-2.5 text-sm">
                <span className="rounded px-1.5 py-0.5 font-mono text-xs font-bold" style={{ background: "var(--reject-soft)", color: "var(--reject-text)" }}>{b.count}×</span>
                <div className="min-w-0 flex-1"><div className="font-semibold">{b.title}</div>{b.last_error && <div className="text-xs leading-snug text-ink-faint">{b.last_error}</div>}</div>
                <button onClick={() => retryBlocked(b.key)} disabled={busy !== null} className="rounded-lg px-2.5 py-1 text-xs font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>Retry</button>
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
  usePoll(() => convertApi.convertLogs().then((l) => {
    setLines((prev) => {
      const a = prev[prev.length - 1], b = l[l.length - 1];
      if (prev.length === l.length && a?.at === b?.at && a?.msg === b?.msg) return prev;
      return l;
    });
  }).catch(() => {}), 2000);
  useEffect(() => { if (follow && boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight; }, [lines, follow]);
  const tone = (lvl: string) => (lvl === "error" ? "var(--reject)" : lvl === "warn" ? "var(--avoid)" : "var(--ink-dim)");
  return (
    <div className={card} style={cardStyle}>
      <div className="mb-2 flex items-center justify-between">
        <div className="text-md font-bold">Activity</div>
        <label className="flex items-center gap-1.5 text-xs text-ink-faint"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Auto-scroll</label>
      </div>
      <div ref={boxRef} className="thin-scroll max-h-[62vh] overflow-y-auto rounded-lg p-3 font-mono text-sm leading-relaxed" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
        {lines.length === 0 ? <div className="text-ink-faint">Nothing yet — switch Convert on, or convert a file from the Library.</div>
          : lines.map((l, i) => (
            <div key={`${l.at}-${i}`} className="flex gap-2"><span className="flex-none text-ink-faint">{new Date(l.at * 1000).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}</span><span style={{ color: tone(l.level) }}>{l.msg}</span></div>
          ))}
      </div>
    </div>
  );
}
