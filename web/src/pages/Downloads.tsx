import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useTabParam } from "../lib/useTabParam";
import { useUnsavedGuard } from "../lib/useUnsavedGuard";
import { useSearchParamState } from "../lib/useSearchState";
import { LINKS } from "../lib/links";
import { RemoveDownloadDialog, removedMessage } from "../components/RemoveDownloadDialog";
import { api, type ActivityDownload, type ClientSettings, type DiskGuardHold, type DownloadClientsState, type Job, type WantedLists, type WantedRow } from "../lib/api";
import { downloadClientApi } from "../lib/downloadClientApi";
import { useMe } from "../lib/me";
import { useLiveEvent } from "../lib/live";
import { useLiveQuery, useLiveRefresh } from "../lib/useLiveQuery";
import { QUEUE_CHANGED, QUEUE_PROGRESS, patchProgress, progressByHash } from "../lib/queueLive";
import { jobFailed, jobToast, useJob } from "../lib/useJob";
import { searchJobLine } from "../lib/searchOutcome";
import { wantedChip, wantedHref, wantedKey, wantedLine } from "../lib/wanted";
import { STAGE, stageGroup, usenetStage, type QueueGroup } from "../lib/stages";
import { isUsenetDownload, leftLine, usenetFailLine, usenetFailed } from "../lib/network";
import { inClient, isStalled, queueClients, showClientNames, stalledMinutes } from "../lib/queueFilter";
import { CardActions, ItemChips, TransferPoster, TransferTitle } from "./activity/transfer";
import { EmptyState, ErrorState, IconButton, Skeleton, StatusChip, useToast } from "../ui";
import { formatPercent } from "../lib/format";

type MediaFilter = "all" | "movie" | "series" | "book" | "music";
const MEDIA_FILTERS: readonly MediaFilter[] = ["all", "movie", "series", "book", "music"];
const TYPE_PILLS: { key: MediaFilter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "movie", label: "Movies" },
  { key: "series", label: "Series" },
  { key: "book", label: "Books" },
  { key: "music", label: "Music" },
];

function bytes(n: number): string {
  if (n <= 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
}
function eta(sec: number): string {
  if (!sec || sec >= 8640000) return "∞";
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.round(sec / 60)}m`;
  return `${Math.round(sec / 3600)}h`;
}
// dur humanizes a seed duration in seconds: "12m", "3h 40m", "2d 4h".
function dur(sec: number): string {
  if (!sec || sec < 60) return "<1m";
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}
// fmtReleaseDate renders a YYYY-MM-DD release date as e.g. "Jul 4, 2026" (year
// only when the day is unknown), parsed as a plain date to avoid TZ drift.
function fmtReleaseDate(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number);
  if (!y) return iso;
  if (!m || !d) return String(y);
  const dt = new Date(y, m - 1, d);
  return dt.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// Stalled and errored transfers share the avoid tone: both need a look, neither is the
// red of a refusal. A moment with no peer sending (the client's "stalled" phase, before it
// counts as stalled) is quiet: it usually passes on its own.
const STATE_TONE: Record<string, string> = {
  downloading: "var(--accent-text)", seeding: "var(--good-text)", paused: "var(--ink-faint)", error: "var(--avoid-text)", checking: "var(--avoid-text)",
  stalled: "var(--ink-faint)", metadata: "var(--ink-faint)", queued: "var(--ink-faint)", moving: "var(--avoid-text)", allocating: "var(--ink-faint)",
  unpacking: "var(--accent-text)",
};
// The client's phases in the words of the shared stage model (lib/stages), so this chip
// and a requester's card say the same thing; the client-only phases keep their own.
const PHASE_TEXT: Record<string, string> = {
  downloading: STAGE.downloading.label, seeding: STAGE.seeding.label, paused: STAGE.paused.label, error: STAGE.failed.label, checking: "Checking",
  stalled: STAGE.stalled.label, metadata: STAGE.fetching_metadata.label, queued: STAGE.queued.label, moving: "Moving", allocating: "Allocating",
  unpacking: STAGE.unpacking.label,
};

// mins humanizes a span of minutes: "45m", "3h", "2d 4h".
export function mins(m: number): string {
  if (m < 60) return `${Math.max(1, Math.round(m))}m`;
  if (m < 24 * 60) return `${Math.round(m / 60)}h`;
  const d = Math.floor(m / (24 * 60)), h = Math.round((m % (24 * 60)) / 60);
  return h > 0 ? `${d}d ${h}h` : `${d}d`;
}

// idleMinutes is how long a torrent has gone without moving any data, from the client's
// own last-activity time (or when it was added, if nothing ever arrived). null = unknown.
function idleMinutes(it: ActivityDownload, nowSec: number): number | null {
  const since = it.last_activity || it.added_on || 0;
  if (since <= 0 || since > nowSec) return null;
  return (nowSec - since) / 60;
}

// phaseLabel is the in-flight chip's text. A download the server calls stalled (no
// progress for half an hour, lib/queueFilter) reads "Stalled 2h"; otherwise the phase tells
// a torrent no peer is sending to right now, or one still fetching its file list, from a
// live download — the plain state calls all of those "downloading".
export function phaseLabel(it: ActivityDownload, nowSec = Date.now() / 1000): { text: string; tone: string; tip?: string } {
  const phase = it.phase || it.state;
  const tone = STATE_TONE[phase] ?? "var(--ink-faint)";
  const seeds = it.seeds ?? 0;
  const swarm = it.swarm_seeds ?? 0;
  const seedText = `${seeds} seed${seeds === 1 ? "" : "s"}`;
  const swarmTip = `Connected to ${seedText}; the tracker reports ${swarm} in the swarm. Reannounce asks the trackers for peers again.`;
  if (isStalled(it)) {
    const m = stalledMinutes(it, nowSec);
    const what = phase === "metadata" ? "no file list" : seedText;
    return { text: `Stalled${m == null ? "" : ` ${mins(m)}`} · ${what}`, tone: "var(--avoid-text)", tip: `No progress for ${m == null ? "half an hour or more" : mins(m)}. ${swarmTip}` };
  }
  if (phase === "stalled") {
    const idle = idleMinutes(it, nowSec);
    const nothingYet = !it.last_activity;
    const idleText = idle == null ? "" : nothingYet ? ` · no data yet (${mins(idle)})` : ` · no data for ${mins(idle)}`;
    return { text: `Waiting for peers · ${seedText}${idleText}`, tone, tip: `No peer is sending data right now. ${swarmTip}` };
  }
  if (phase === "metadata") return { text: "Fetching metadata", tone, tip: "Waiting for peers to send the torrent's file list." };
  if (phase === "queued") return { text: "Queued", tone, tip: "Waiting for a slot — the client's active-download limit is reached." };
  return { text: PHASE_TEXT[phase] ?? phase, tone };
}

// stallLine is the card's second line for a grab Arrmada is watching: how long it has made
// no progress and when stall fail-over tries another release. Shown only once there is
// something to say — a torrent that is moving doesn't need it.
export function stallLine(it: ActivityDownload): string | null {
  const st = it.stall;
  if (!st) return null;
  const phase = it.phase || it.state;
  if (st.idle_minutes < 10 && phase !== "stalled") return null;
  if (st.off) return st.idle_minutes > 0 ? `No progress for ${mins(st.idle_minutes)} · auto-retry off` : "Auto-retry off";
  if (st.idle_minutes <= 0) return null; // the clock is held (paused, queued, checking)
  const next = st.failover_in_minutes > 0 ? `trying another release in ${mins(st.failover_in_minutes)}` : "trying another release on the next check";
  return `No progress for ${mins(st.idle_minutes)} · ${next}`;
}

// isProblem: a download that needs a look — errored, stalled (no peer sending, or the file
// list never coming, for half an hour: lib/queueFilter), or finished and not importing (the
// import keeps failing, it's held, it's in a category nothing imports from). What
// ?filter=problems (the Needs-you links) narrows to.
export function isProblem(it: Pick<ActivityDownload, "phase" | "state" | "stage" | "stalled">): boolean {
  if (it.stage === "import_retrying" || it.stage === "wrong_category" || it.stage === "needs_review") return true;
  const phase = it.phase || it.state;
  return phase === "error" || isStalled(it) || it.state === "error";
}

// groupOf is the Queue view a download sits in. A usenet job never seeds: once it's
// downloaded it stays with Finishing (lib/stages).
export const groupOf = (d: ActivityDownload): QueueGroup => stageGroup(d.stage, d.progress, isUsenetDownload(d));

type SortKey = "name" | "progress" | "speed" | "size" | "ratio" | "seedtime";

// The Queue tab's sub-views (?show=): what's still arriving, what's finished but not in the
// library yet, and what's in the library and sharing — split on the shared stage model.
// Stalled is the part of Downloading that has stopped making progress, beside it so it's
// one tap away.
type QueueView = QueueGroup | "stalled";
const QUEUE_VIEWS: readonly QueueView[] = ["downloading", "stalled", "finishing", "seeding"];
const QUEUE_VIEW_LABEL: Record<QueueView, string> = { downloading: "Downloading", stalled: "Stalled", finishing: "Finishing", seeding: "Seeding" };
// The Wanted tab's sub-views (?show=): what's being searched for, and what isn't out yet.
type WantedView = "searching" | "upcoming";
const WANTED_VIEWS: readonly WantedView[] = ["searching", "upcoming"];

function ProfileChip({ profile }: { profile: string }) {
  const na = profile === "n/a";
  return <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: na ? "var(--panel-2)" : "var(--accent-soft)", color: na ? "var(--ink-faint)" : "var(--accent-text)" }}>{profile}</span>;
}

// How often the Wanted tab refreshes on its own. It reads the search history, so it
// isn't refreshed with the queue; a finished search, a grab or an import announced over
// the socket refreshes it sooner, so this is only the backstop for a dropped event.
const WANTED_POLL_MS = 120_000;
const WANTED_TOPICS = ["search.finished", "release.grabbed", "download.imported", "series.imported", "series.searched"];
// What changes the queue's rows (beyond the bars, which queue.progress moves in place) —
// including a finished download moving from Finishing to Seeding once any importer has
// it — and the backstop for an event the socket dropped.
const QUEUE_TOPICS = [QUEUE_CHANGED, "release.grabbed", "*.imported", "import.held", "download.stalled"];
const QUEUE_FALLBACK_MS = 10_000;

// SubPills switches a tab's sub-view; each is a pill with its count, like the type filter.
function SubPills<T extends string>({ views, labels, counts, value, onChange, label }: { views: readonly T[]; labels: Record<T, string>; counts: Record<T, number>; value: T; onChange: (v: T) => void; label: string }) {
  return (
    <div role="group" aria-label={label} className="flex flex-wrap items-center gap-1.5">
      {views.map((v) => {
        const on = v === value;
        return (
          <button key={v} type="button" aria-pressed={on} onClick={() => onChange(v)} className="rounded-full px-3 py-1 text-sm font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent-text)" : "var(--ink-faint)" }}>
            {labels[v]} <span className="font-mono text-xs opacity-70">{counts[v]}</span>
          </button>
        );
      })}
    </div>
  );
}

function SearchBox({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return <input value={value} onChange={(e) => onChange(e.target.value)} placeholder="Search…" aria-label="Search this list" className="w-[200px] rounded-lg px-3 py-1.5 text-sm" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />;
}

// QueuePanel is Activity → Queue: the download client's transfers, live, with the queue
// controls. Mounted only while its tab shows, so the three-second poll stops elsewhere.
export function QueuePanel() {
  const [downloads, setDownloads] = useState<ActivityDownload[]>([]);
  const [totals, setTotals] = useState<{ down_speed: number; up_speed: number; active: number }>({ down_speed: 0, up_speed: 0, active: 0 });
  const [freeGb, setFreeGb] = useState<number | null>(null);
  const [diskPath, setDiskPath] = useState("");
  const [clients, setClients] = useState<DownloadClientsState | null>(null); // null = unknown
  const [guard, setGuard] = useState<DiskGuardHold | null>(null);
  // Short confirmations and failures go through the kit's toasts; a failed action (pause,
  // resume, remove, reannounce) says why in the error tone rather than leaving the next
  // refresh to show that nothing changed.
  const toast = useToast();
  const note = (m: string) => toast(m, { ms: 4500 });
  const fail = (m: string) => toast(m, { tone: "error", ms: 6000 });
  const [reconnecting, setReconnecting] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [query, setQuery] = useSearchParamState<string>("q", "", { debounceMs: 300 });
  const [sort, setSort] = useState<SortKey>("progress");
  const [typeFilter, setTypeFilter] = useSearchParamState<MediaFilter>("type", "all", { allowed: MEDIA_FILTERS });
  // ?client=<id>: one download client's downloads, offered only with several clients.
  const [clientFilter, setClientFilter] = useSearchParamState<string>("client", "");
  const { musicEnabled } = useMe();
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [clientId, setClientId] = useState<number | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [view, setView] = useTabParam(QUEUE_VIEWS, "downloading", "show");
  const [seedSort, setSeedSort] = useState<SortKey>("ratio");
  // ?filter=problems: only the downloads that need a look (linked from Needs you).
  const [params, setParams] = useSearchParams();
  const problemsOnly = params.get("filter") === "problems";
  const showAll = () => setParams((p) => { const next = new URLSearchParams(p); next.delete("filter"); return next; }, { replace: true });

  useEffect(() => {
    downloadClientApi.downloadClients().then((cs) => {
      // A switched-off client gets no downloads, so its settings aren't the ones in play.
      // The speed limits are qBittorrent's; a SABnzbd has none here.
      const qb = cs.find((c) => c.enabled && c.kind === "qbittorrent");
      if (qb) setClientId(qb.id);
    }).catch(() => {});
  }, []);

  // Consecutive failed reads; one blip isn't worth a "reconnecting" banner.
  const fails = useRef(0);
  // The queue refreshes when the server says its shape moved (a torrent arrived, left,
  // finished or changed phase) and on a slow backstop; between those the bars move from
  // queue.progress below. Both come from the server's one shared read of the client. Only
  // this tab is mounted while it shows, so nothing reads the queue from another tab.
  const refreshQueue = useLiveRefresh(() =>
    api.activity().then((a) => {
      fails.current = 0;
      setDownloads(a.downloads ?? []);
      if (a.totals) setTotals(a.totals);
      setFreeGb(typeof a.free_gb === "number" ? a.free_gb : null); // null/absent = couldn't be measured
      setDiskPath(a.disk_path ?? "");
      if (a.clients) setClients(a.clients);
      setGuard(a.disk_guard ?? null);
      setReconnecting(false);
      setLoaded(true);
    }).catch(() => {
      fails.current += 1;
      setLoaded(true);
      if (fails.current >= 2) setReconnecting(true);
    }), { topics: QUEUE_TOPICS, fallbackMs: QUEUE_FALLBACK_MS, immediate: true });
  useLiveEvent(QUEUE_PROGRESS, (ev) => {
    const live = progressByHash(ev);
    if (!live) return;
    // Paused or errored (or back) from elsewhere — the client's own UI, the disk guard:
    // the row's buttons follow its state, so read it properly rather than patch the bar.
    const halted = (s?: string) => s === "paused" || s === "error";
    if (downloads.some((d) => { const p = live.get(d.hash.toLowerCase()); return !!p && halted(p.state) !== halted(d.phase || d.state); })) {
      refreshQueue();
      return;
    }
    // A bar moving forward is progress, which ends a stall at once rather than at the next
    // read; a download only becomes stalled by the server's clock, on the next read.
    setDownloads((ds) => patchProgress(ds, live, (d) => ({ hash: d.hash, progress: d.progress, phase: d.phase || d.state }),
      (d, p) => ({ ...d, progress: p.progress, eta_seconds: p.eta_seconds, phase: p.state, ...(p.progress > d.progress ? { stalled: false, stalled_since: undefined } : {}) })));
  });

  // The next refresh shows what happened; a failure (the client down, the disk guard
  // holding a torrent) is an error toast, since a refresh alone can't say why nothing changed.
  const act = async (hash: string, fn: () => Promise<unknown>) => {
    setBusy((b) => ({ ...b, [hash]: true }));
    try { await fn(); refreshQueue(); } catch (e) { fail((e as Error).message); } finally { setBusy((b) => ({ ...b, [hash]: false })); }
  };
  const resumeAll = () => act("all", async () => {
    const r = await api.resumeDownload("all");
    const held = r.held_by_guard ?? 0;
    note(`Resumed ${r.resumed ?? 0}${held > 0 ? ` · ${held} held by the disk guard` : ""}.`);
  });
  // Try another release: the search runs as a job, followed here rather than in the row, so
  // "Grabbed X instead" (or why not) still shows after the row has left the list — which
  // is what a successful replacement does to it. By hash, one search per download.
  const [failovers, setFailovers] = useState<Record<string, number>>({});
  const tryAnother = (hash: string) => act(hash, async () => {
    const id = (await api.failOverDownload(hash)).job_id;
    if (id) setFailovers((f) => ({ ...f, [hash]: id }));
    else note("Looking for another release…");
  });
  const failoverDone = (hash: string, j: Job) => {
    setFailovers((f) => { const next = { ...f }; delete next[hash]; return next; });
    toast(jobToast(j), { tone: jobFailed(j) ? "error" : undefined, ms: 8000 });
    refreshQueue();
  };

  // Which client each download is in, named on the rows and offered as a filter only when
  // more than one is switched on. A ?client= no row is in any more shows everything.
  const showClients = showClientNames(clients);
  const clientList = useMemo(() => queueClients(downloads), [downloads]);
  const activeClient = showClients && clientList.some((c) => String(c.id) === clientFilter) ? clientFilter : "";
  const scoped = useMemo(() => inClient(downloads, activeClient), [downloads, activeClient]);

  const activeDownloads = useMemo(() => scoped.filter((d) => groupOf(d) === "downloading"), [scoped]);
  const stalledDownloads = useMemo(() => activeDownloads.filter(isStalled), [activeDownloads]);
  const finishingDownloads = useMemo(() => scoped.filter((d) => groupOf(d) === "finishing"), [scoped]);
  const seedingDownloads = useMemo(() => scoped.filter((d) => groupOf(d) === "seeding"), [scoped]);

  const filterSort = (list: ActivityDownload[], sortKey: SortKey) => {
    const q = query.trim().toLowerCase();
    let l = typeFilter === "all" ? list : list.filter((d) => (d.media_type ?? "movie") === typeFilter);
    if (problemsOnly) l = l.filter(isProblem);
    // The search box finds a transfer by its title as well as its release name.
    l = q ? l.filter((d) => d.name.toLowerCase().includes(q) || (d.item?.title ?? "").toLowerCase().includes(q)) : [...l];
    l.sort((a, b) => {
      switch (sortKey) {
        case "name": return (a.item?.title ?? a.name).localeCompare(b.item?.title ?? b.name);
        case "speed": return b.down_speed - a.down_speed;
        case "size": return b.size_bytes - a.size_bytes;
        case "ratio": return b.ratio - a.ratio;
        case "seedtime": return (b.seeding_time ?? 0) - (a.seeding_time ?? 0);
        default: return b.progress - a.progress;
      }
    });
    return l;
  };
  const shownDownloads = useMemo(() => filterSort(activeDownloads, sort), [activeDownloads, query, sort, typeFilter, problemsOnly]); // eslint-disable-line react-hooks/exhaustive-deps
  const shownStalled = useMemo(() => filterSort(stalledDownloads, sort), [stalledDownloads, query, sort, typeFilter, problemsOnly]); // eslint-disable-line react-hooks/exhaustive-deps
  const shownSeeding = useMemo(() => filterSort(seedingDownloads, seedSort), [seedingDownloads, query, seedSort, typeFilter, problemsOnly]); // eslint-disable-line react-hooks/exhaustive-deps
  const shownFinishing = useMemo(() => filterSort(finishingDownloads, "name"), [finishingDownloads, query, typeFilter, problemsOnly]); // eslint-disable-line react-hooks/exhaustive-deps

  const base = view === "seeding" ? seedingDownloads : view === "finishing" ? finishingDownloads : view === "stalled" ? stalledDownloads : activeDownloads;
  const typeCount = (k: MediaFilter) => (k === "all" ? base.length : base.filter((d) => (d.media_type ?? "movie") === k).length);
  // With the problems filter on, the counts are of problems, so the pills say where they are.
  const viewCount = (l: ActivityDownload[]) => (problemsOnly ? l.filter(isProblem).length : l.length);

  // No download client at all: there is no queue to show or control.
  const noClient = clients?.configured === 0;
  // A client that isn't answering looks exactly like an empty queue unless it's said.
  const clientDown = !!clients && clients.configured > 0 && !clients.ok;

  return (
    <>
      {Object.entries(failovers).map(([hash, id]) => <FailoverJob key={hash} id={id} onDone={(j) => failoverDone(hash, j)} />)}
      {/* Header: live totals + free disk + controls */}
      <div className="mb-4 flex flex-wrap items-center gap-x-5 gap-y-2 rounded-xl px-4 py-2.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
        {!noClient && (
          <>
            <span className="flex items-center gap-1.5 font-mono text-xs" style={{ color: reconnecting || clientDown ? "var(--avoid-text)" : "var(--ink-faint)" }}>
              <span className="inline-block h-1.5 w-1.5 rounded-full" style={{ background: reconnecting || clientDown ? "var(--avoid)" : "var(--accent)" }} />
              {reconnecting ? "Reconnecting…" : clientDown ? "Client offline" : "Live"}
            </span>
            <Stat label="↓" value={`${bytes(totals.down_speed)}/s`} tone="var(--accent-text)" />
            <Stat label="↑" value={`${bytes(totals.up_speed)}/s`} tone="var(--good-text)" />
            <Stat label="active" value={String(totals.active)} />
            {freeGb != null && <Stat label="free" value={`${freeGb.toFixed(0)} GB`} tone={freeGb < 20 ? "var(--reject-text)" : undefined} title={diskPath ? `Free space on ${diskPath}` : undefined} />}
          </>
        )}
        <div className="ml-auto flex items-center gap-2">
          {!noClient && (
            <>
              <button onClick={() => act("all", () => api.pauseDownload("all"))} className="rounded-lg px-2.5 py-1.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Pause all</button>
              <button onClick={resumeAll} title={guard ? "Torrents the disk guard paused stay paused until the volume drains" : undefined} className="rounded-lg px-2.5 py-1.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Resume all{guard && guard.holding > 0 ? ` (${guard.holding} held by disk guard)` : ""}</button>
            </>
          )}
          {clientId != null && (
            <button onClick={() => setShowSettings((s) => !s)} className="rounded-lg px-2.5 py-1.5 text-xs font-semibold" style={{ border: `1px solid ${showSettings ? "var(--accent)" : "var(--line)"}`, color: showSettings ? "var(--accent-text)" : "var(--ink-dim)" }}>⚙ Speed & limits</button>
          )}
        </div>
      </div>

      {showSettings && clientId != null && <SettingsPanel clientId={clientId} onClose={() => setShowSettings(false)} />}

      {clients && clients.configured > 0 && clients.enabled === 0 && (
        <div className="mb-4 rounded-lg p-3.5 text-base" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>
          Every download client is switched off, so Arrmada can't grab anything. Enable one in{" "}
          <Link to={LINKS.downloadClients} className="font-semibold underline" style={{ color: "inherit" }}>Settings → Connections</Link>.
        </div>
      )}
      {clientDown && clients && (
        <div className="mb-4 rounded-lg p-3.5 text-base" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>
          {clients.name || "A download client"} unreachable{clients.since ? ` since ${sinceLabel(clients.since)}` : ""} — searches are paused until it's back
          {clients.error ? ` (${clients.error})` : ""}. Check it in{" "}
          <Link to={LINKS.downloadClients} className="font-semibold underline" style={{ color: "inherit" }}>Settings → Connections</Link>.
        </div>
      )}

      {/* Sub-view + search + sort, then the type filter */}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <SubPills views={QUEUE_VIEWS} labels={QUEUE_VIEW_LABEL} counts={{ downloading: viewCount(activeDownloads), stalled: viewCount(stalledDownloads), finishing: viewCount(finishingDownloads), seeding: viewCount(seedingDownloads) }} value={view} onChange={setView} label="Queue views" />
        <div className="ml-auto flex items-center gap-2">
          <SearchBox value={query} onChange={setQuery} />
          {(view === "downloading" || view === "stalled") && (
            <select value={sort} onChange={(e) => setSort(e.target.value as SortKey)} aria-label="Sort downloads" className="rounded-lg px-2.5 py-1.5 text-sm" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
              <option value="progress">Sort: Progress</option>
              <option value="name">Sort: Name</option>
              <option value="speed">Sort: Speed</option>
              <option value="size">Sort: Size</option>
            </select>
          )}
          {view === "seeding" && (
            <select value={seedSort} onChange={(e) => setSeedSort(e.target.value as SortKey)} aria-label="Sort seeding" className="rounded-lg px-2.5 py-1.5 text-sm" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
              <option value="ratio">Sort: Ratio</option>
              <option value="seedtime">Sort: Seed time</option>
              <option value="name">Sort: Name</option>
              <option value="size">Sort: Size</option>
            </select>
          )}
        </div>
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        {TYPE_PILLS.filter((p) => p.key !== "music" || musicEnabled).map((p) => {
          const on = typeFilter === p.key;
          return (
            <button key={p.key} onClick={() => setTypeFilter(p.key)} className="rounded-full px-3 py-1 text-sm font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent-text)" : "var(--ink-faint)" }}>
              {p.label} <span className="font-mono text-xs opacity-70">{typeCount(p.key)}</span>
            </button>
          );
        })}
        {problemsOnly && (
          <button onClick={showAll} title="Show every download" className="rounded-full px-3 py-1 text-sm font-semibold" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>
            Problems only ✕
          </button>
        )}
        {showClients && clientList.length > 0 && (
          <select value={activeClient} onChange={(e) => setClientFilter(e.target.value)} aria-label="Download client" className="ml-auto max-w-[220px] rounded-lg px-2.5 py-1.5 text-sm" style={{ background: "var(--panel-2)", border: `1px solid ${activeClient ? "var(--accent)" : "var(--line)"}`, color: "var(--ink)" }}>
            <option value="">All clients</option>
            {clientList.map((c) => <option key={c.id} value={String(c.id)}>{c.name} ({c.count})</option>)}
          </select>
        )}
      </div>

      {!loaded ? null : noClient ? (
        <NoClientCard />
      ) : view === "downloading" ? (
        shownDownloads.length === 0 ? <Empty>{problemsOnly ? "No download needs a look right now." : "Nothing downloading. Grab a release and it'll appear here."}</Empty> : (
          <div className="flex flex-col gap-2">
            {shownDownloads.map((it) => <DownloadCard key={it.hash} it={it} guard={guard} busy={!!busy[it.hash]} act={act} onRemoved={note} onNote={note} showClient={showClients} searching={it.hash in failovers} onTryAnother={tryAnother} />)}
          </div>
        )
      ) : view === "stalled" ? (
        shownStalled.length === 0 ? <Empty>Nothing has stalled. A download counts as stalled after half an hour with no progress.</Empty> : (
          <div className="flex flex-col gap-2">
            {shownStalled.map((it) => <DownloadCard key={it.hash} it={it} guard={guard} busy={!!busy[it.hash]} act={act} onRemoved={note} onNote={note} showClient={showClients} searching={it.hash in failovers} onTryAnother={tryAnother} />)}
          </div>
        )
      ) : view === "finishing" ? (
        shownFinishing.length === 0 ? <Empty>{problemsOnly ? "No finished download needs a look right now." : "Nothing waiting to import. Finished downloads pass through here on their way into the library."}</Empty> : (
          <div className="flex flex-col gap-2">
            {shownFinishing.map((it) => <FinishingCard key={it.hash} it={it} busy={!!busy[it.hash]} onRemoved={(m) => { note(m); refreshQueue(); }} showClient={showClients} />)}
          </div>
        )
      ) : (
        shownSeeding.length === 0 ? <Empty>{problemsOnly ? "No seeding download needs a look right now." : "Nothing seeding right now."}</Empty> : (
          <div className="flex flex-col gap-2">
            <SeedingSummary items={shownSeeding} />
            {shownSeeding.map((it) => <SeedingCard key={it.hash} it={it} guard={guard} busy={!!busy[it.hash]} act={act} onRemoved={note} showClient={showClients} />)}
          </div>
        )
      )}
    </>
  );
}

// WantedPanel is Activity → Wanted: the titles Arrmada is still looking for (Searching)
// and the ones that aren't out yet (Upcoming), across movies, series, books and albums.
export function WantedPanel() {
  const [query, setQuery] = useSearchParamState<string>("q", "", { debounceMs: 300 });
  const [view, setView] = useTabParam(WANTED_VIEWS, "searching", "show");
  const toast = useToast();
  const fail = (m: string) => toast(m, { tone: "error", ms: 6000 });

  // Its own, slower read, refreshed by events. A sweep finishes searches in bursts: one
  // refresh for the lot, a moment after.
  const wanted = useLiveQuery<WantedLists>("wanted", api.wanted, { staleMs: 10_000, topics: WANTED_TOPICS, fallbackMs: WANTED_POLL_MS, debounceMs: 3000 });
  const searching = useMemo(() => wanted.data?.searching ?? [], [wanted.data]);
  const upcoming = useMemo(() => wanted.data?.upcoming ?? [], [wanted.data]);

  const shownSearching = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? searching.filter((s) => wantedMatches(s, q)) : searching;
  }, [searching, query]);
  const shownUpcoming = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = q ? upcoming.filter((s) => wantedMatches(s, q)) : upcoming;
    return [...list].sort((a, b) => (a.available_at ?? "").localeCompare(b.available_at ?? ""));
  }, [upcoming, query]);

  return (
    <>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <SubPills views={WANTED_VIEWS} labels={{ searching: "Searching", upcoming: "Upcoming" }} counts={{ searching: searching.length, upcoming: upcoming.length }} value={view} onChange={setView} label="Wanted views" />
        <div className="ml-auto"><SearchBox value={query} onChange={setQuery} /></div>
      </div>
      {!wanted.data ? (
        wanted.error ? <ErrorState what="what's wanted" message={wanted.error.message} onRetry={() => void wanted.refetch()} busy={wanted.loading} /> : <Skeleton variant="list" count={4} />
      ) : view === "searching" ? (
        shownSearching.length === 0 ? <Empty>Nothing is being searched. Monitored, available titles that are missing a file show up here.</Empty> : (
          <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
            {shownSearching.map((s) => <WantedItem key={wantedKey(s)} item={s} kind="searching" onSearched={() => void wanted.refetch()} onError={fail} />)}
          </div>
        )
      ) : (
        shownUpcoming.length === 0 ? <Empty>Nothing upcoming. Unreleased films and albums, and unaired episodes you monitor, land here.</Empty> : (
          <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
            {shownUpcoming.map((s) => <WantedItem key={wantedKey(s)} item={s} kind="upcoming" onSearched={() => void wanted.refetch()} onError={fail} />)}
          </div>
        )
      )}
    </>
  );
}

// GuardChip marks a torrent the disk guard paused, so it doesn't read as one paused by hand.
function GuardChip({ guard }: { guard: DiskGuardHold | null }) {
  return (
    <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>
      Paused · disk {guard ? `${Math.round(guard.used_pct)}% ` : ""}full
    </span>
  );
}

// guardTip explains why a held torrent's Resume is unavailable.
function guardTip(guard: DiskGuardHold | null): string {
  const when = guard ? ` once the downloads volume drops below ${guard.resume_pct}%` : "";
  return `Held by the disk guard — it resumes on its own${when}. Free some space, or turn the guard off in Settings → Downloads.`;
}

// ResumeBtn is Pause/Resume, disabled (with the reason on hover) while the disk guard holds the torrent.
function ResumeBtn({ it, guard, busy, act }: { it: ActivityDownload; guard: DiskGuardHold | null; busy: boolean; act: (hash: string, fn: () => Promise<unknown>) => void }) {
  const paused = it.state === "paused";
  if (paused && it.held_by_guard) {
    return <span title={guardTip(guard)}><IconBtn label="Resume" title={guardTip(guard)} disabled onClick={() => {}} /></span>;
  }
  return <IconBtn label={paused ? "Resume" : "Pause"} disabled={busy} onClick={() => act(it.hash, () => (paused ? api.resumeDownload(it.hash) : api.pauseDownload(it.hash)))} />;
}

// NoClientCard stands in for the Downloads and Seeding lists on an install with no download
// client: there is no queue, and nothing can be grabbed until one is added.
function NoClientCard() {
  return (
    <EmptyState
      title="No download client yet"
      body="Arrmada can't grab anything until one is set up: torrents go through qBittorrent, Usenet through SABnzbd."
      action={
        <Link to={LINKS.downloadClients} className="inline-block rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
          Add a download client →
        </Link>
      }
    />
  );
}

// sinceLabel renders when an outage began: the time today, or the date and time before that.
export function sinceLabel(iso: string, now: Date = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return d.toDateString() === now.toDateString() ? time : `${d.toLocaleDateString([], { day: "numeric", month: "short" })} ${time}`;
}

function Empty({ children }: { children: React.ReactNode }) {
  return <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>{children}</div>;
}

// wantedMatches is the page's search box over a Wanted row: its title, author or artist.
const wantedMatches = (s: WantedRow, q: string) => s.title.toLowerCase().includes(q) || (s.byline ?? "").toLowerCase().includes(q);

// FailoverJob follows one Try another release search to its end; it draws nothing.
function FailoverJob({ id, onDone }: { id: number; onDone: (j: Job) => void }) {
  useJob(id, { onDone });
  return null;
}

// ClientChip names the download client a transfer is in, as its card in Settings →
// Connections does. Shown only when more than one client is switched on.
function ClientChip({ it }: { it: ActivityDownload }) {
  if (!it.client_name) return null;
  return <span className="max-w-[140px] truncate rounded px-1.5 py-0.5 text-2xs font-semibold" title="Download client" data-testid="client-chip" style={{ background: "var(--panel-2)", color: "var(--ink-dim)", border: "1px solid var(--line-soft)" }}>{it.client_name}</span>;
}

function TypeChip({ mediaType }: { mediaType?: string }) {
  const label = mediaType === "series" ? "TV" : mediaType === "book" ? "Book" : mediaType === "music" ? "Music" : "Movie";
  return <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{label}</span>;
}

// DownloadCard is an in-flight (incomplete) transfer: the title it's for, progress bar,
// speed, ETA, queue controls.
//
// A usenet job has no peers, seeds or trackers: it shows what's left to fetch, no Reannounce
// or Recheck, and when SABnzbd failed it, its reason and Retry.
function DownloadCard({ it, guard, busy, act, onRemoved, onNote, showClient, searching, onTryAnother }: { it: ActivityDownload; guard: DiskGuardHold | null; busy: boolean; act: (hash: string, fn: () => Promise<unknown>) => Promise<void>; onRemoved: (m: string) => void; onNote: (m: string) => void; showClient: boolean; searching: boolean; onTryAnother: (hash: string) => void }) {
  const paused = it.state === "paused";
  const [removing, setRemoving] = useState(false);
  const usenet = isUsenetDownload(it);
  const failed = usenet && usenetFailed(it);
  const stalled = isStalled(it);
  const chip = phaseLabel(it);
  // A stalled download a pending grab recorded by hash (it carries a stall state: what the
  // fail-over endpoint accepts) can try another release now, as Needs you's does: this one
  // is removed only once something else has been grabbed in its place. The search is
  // followed by the panel, so its result shows even once the row has gone.
  const tryAnother = {
    label: searching ? "Searching…" : "Try another release", title: "Search for a different release; this one is removed only once another is grabbed", disabled: busy || searching,
    onSelect: () => onTryAnother(it.hash),
  };
  const stall = stallLine(it);
  const failLine = failed ? usenetFailLine(it) : null;
  const retry = (
    <IconBtn label="Retry" title="Ask SABnzbd to try this download again" tone="var(--accent-text)" disabled={busy} onClick={() => void act(it.hash, async () => { await api.torrentAction(it.hash, "retry"); onNote("SABnzbd is trying it again."); })} />
  );
  return (
    <div className="rounded-xl p-3.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }} data-testid="transfer-card">
      <div className="flex items-start gap-3">
        <TransferPoster it={it} />
        <div className="min-w-0 flex-1">
          <TransferTitle it={it} />
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1">
            {paused && it.held_by_guard ? <GuardChip guard={guard} /> : <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" title={chip.tip} data-testid={stalled ? "stalled-chip" : undefined} style={{ background: stalled ? "var(--avoid-soft)" : "var(--panel-2)", color: chip.tone }}>{chip.text}</span>}
            <ItemChips it={it} />
            {showClient && <ClientChip it={it} />}
            <TypeChip mediaType={it.media_type} />
            <ProfileChip profile={it.quality_profile} />
            <span className="font-mono text-xs text-ink-faint">{bytes(it.size_bytes)}</span>
            {!failed && <span className="font-mono text-xs" style={{ color: it.down_speed > 0 ? "var(--accent-text)" : "var(--ink-faint)" }}>{it.down_speed > 0 ? `↓${bytes(it.down_speed)}/s` : "—"}</span>}
            {usenet ? (!failed && <span className="font-mono text-xs text-ink-faint" data-testid="usenet-left">{leftLine(it, bytes, eta)}</span>) : <span className="font-mono text-xs text-ink-faint">ETA {eta(it.eta_seconds)}</span>}
          </div>
          {failLine && <div className="mt-1 break-words text-sm" style={{ color: "var(--avoid-text)" }} data-testid="usenet-fail">{failLine}</div>}
          {stall && <div className="mt-1 font-mono text-xs" style={{ color: "var(--avoid-text)" }}>{stall}</div>}
        </div>
        <CardActions
          name={it.item?.title ?? it.name}
          primary={failed ? retry : <ResumeBtn it={it} guard={guard} busy={busy} act={act} />}
          secondary={[
            ...(stalled && it.stall && !usenet ? [tryAnother] : []),
            { label: "Block", tone: "var(--avoid-text)", title: "Blocklist this release and grab a different one", disabled: busy, onSelect: () => act(it.hash, async () => { const r = await api.blockDownload(it.hash, it.name); onRemoved(`Blocked for ${r.blocked_for.title} — searching for another release.`); }) },
            { label: "Delete", danger: true, title: "Remove from the client — asks what to do with the files", disabled: busy, onSelect: () => setRemoving(true) },
          ]}
          // Trackers, piece checks and queue order are qBittorrent's; SABnzbd takes none of them.
          extras={usenet ? [] : [
            { label: "Reannounce", busyLabel: "Reannouncing…", disabled: busy, onSelect: () => act(it.hash, async () => { await api.torrentAction(it.hash, "reannounce"); onNote("Asked the trackers for peers again."); }) },
            { label: "Recheck files", busyLabel: "Rechecking…", disabled: busy, onSelect: () => act(it.hash, async () => { await api.torrentAction(it.hash, "recheck"); onNote("Rechecking the downloaded data."); }) },
            { label: "Move up the queue", disabled: busy, onSelect: () => act(it.hash, () => api.torrentAction(it.hash, "prio_up")) },
            { label: "Move down the queue", disabled: busy, onSelect: () => act(it.hash, () => api.torrentAction(it.hash, "prio_down")) },
          ]}
        />
      </div>
      <div className="mt-2.5 flex items-center gap-2">
        <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
          <div className="h-full rounded-full" style={{ width: `${Math.round(it.progress * 100)}%`, background: paused || failed ? "var(--ink-faint)" : "var(--accent)" }} />
        </div>
        <span className="w-10 text-right font-mono text-xs text-ink-dim">{formatPercent(it.progress)}</span>
      </div>
      {removing && <RemoveDownloadDialog it={it} onClose={() => setRemoving(false)} onDone={(r) => { setRemoving(false); onRemoved(removedMessage(r)); }} />}
    </div>
  );
}

// FinishingCard is a finished transfer that isn't in the library yet: importing, held for
// review, an import being retried, or filed where nothing imports it. Its stage chip and
// why line say which, so it never passes for a healthy seed.
function FinishingCard({ it, busy, onRemoved, showClient }: { it: ActivityDownload; busy: boolean; onRemoved: (m: string) => void; showClient: boolean }) {
  const [removing, setRemoving] = useState(false);
  const stage = isUsenetDownload(it) ? usenetStage(it.stage) : it.stage;
  const st = stage ? STAGE[stage] : STAGE.importing;
  // Anything that needs a decision is settled on Needs you, in the section with the
  // action for it.
  const decide = stage === "needs_review" ? LINKS.review : stage === "import_retrying" ? LINKS.importProblems : stage === "wrong_category" ? LINKS.wrongCategory : null;
  return (
    <div className="rounded-xl p-3.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }} data-testid="transfer-card">
      <div className="flex items-start gap-3">
        <TransferPoster it={it} />
        <div className="min-w-0 flex-1">
          <TransferTitle it={it} />
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1">
            <StatusChip tone={st.tone}>{st.label}</StatusChip>
            <ItemChips it={it} />
            {showClient && <ClientChip it={it} />}
            <TypeChip mediaType={it.media_type} />
            <ProfileChip profile={it.quality_profile} />
            <span className="font-mono text-xs text-ink-faint">{bytes(it.size_bytes)}</span>
          </div>
          {it.why && <div className="mt-1 text-sm text-ink-dim" data-testid="stage-why">{it.why}</div>}
        </div>
        <CardActions
          name={it.item?.title ?? it.name}
          primary={decide ? <Link to={decide} className="rounded-lg px-2 py-1.5 text-xs font-semibold" style={{ border: "1px solid var(--accent)", color: "var(--accent-text)" }}>Decide →</Link> : undefined}
          secondary={[{ label: "Delete", danger: true, title: "Remove from the client — asks what to do with the files", disabled: busy, onSelect: () => setRemoving(true) }]}
        />
      </div>
      {removing && <RemoveDownloadDialog it={it} onClose={() => setRemoving(false)} onDone={(r) => { setRemoving(false); onRemoved(removedMessage(r)); }} />}
    </div>
  );
}

// seedGoal computes the removal target for a seeding torrent and how far along it is.
function seedGoal(it: ActivityDownload): { label: string; frac: number | null } {
  if (!it.seed_known) return { label: "Not managed by Arrmada — no seed rule", frac: null };
  if (it.seed_enabled === false) return { label: "No seeding — removes after import", frac: null };
  const ratioTarget = it.seed_ratio ?? 0;
  const hoursTarget = it.seed_hours ?? 0;
  const seededSec = it.seeding_time ?? 0;
  if (ratioTarget <= 0 && hoursTarget <= 0) {
    return { label: "Seeds indefinitely", frac: null };
  }
  const parts: string[] = [];
  let frac = 0;
  if (ratioTarget > 0) { parts.push(`ratio ${it.ratio.toFixed(2)} / ${ratioTarget.toFixed(2)}`); frac = Math.max(frac, it.ratio / ratioTarget); }
  if (hoursTarget > 0) { parts.push(`${dur(seededSec)} / ${hoursTarget}h`); frac = Math.max(frac, seededSec / (hoursTarget * 3600)); }
  return { label: `until ${parts.join(" or ")}`, frac: Math.min(1, frac) };
}

// SeedingSummary is the at-a-glance strip above the seeding list: how many, live
// upload, average share ratio, and the total data being shared.
function SeedingSummary({ items }: { items: ActivityDownload[] }) {
  const count = items.length;
  const totalUp = items.reduce((n, it) => n + it.up_speed, 0);
  const avgRatio = count ? items.reduce((n, it) => n + it.ratio, 0) / count : 0;
  const sharing = items.reduce((n, it) => n + it.size_bytes, 0);
  const activeUp = items.filter((it) => it.up_speed > 0).length;
  return (
    <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 rounded-xl px-4 py-2.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <Stat label="seeding" value={String(count)} />
      <Stat label="↑ now" value={`${bytes(totalUp)}/s`} tone={totalUp > 0 ? "var(--good)" : undefined} />
      <Stat label="uploading" value={`${activeUp}/${count}`} />
      <Stat label="avg ratio" value={avgRatio.toFixed(2)} tone="var(--good-text)" />
      <Stat label="sharing" value={bytes(sharing)} />
    </div>
  );
}

// SeedingCard is a completed torrent that's sharing back: the title it's for, ratio, seed
// time, goal progress.
function SeedingCard({ it, guard, busy, act, onRemoved, showClient }: { it: ActivityDownload; guard: DiskGuardHold | null; busy: boolean; act: (hash: string, fn: () => Promise<unknown>) => void; onRemoved: (m: string) => void; showClient: boolean }) {
  const paused = it.state === "paused";
  const goal = seedGoal(it);
  const [removing, setRemoving] = useState(false);
  return (
    <div className="rounded-xl p-3.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }} data-testid="transfer-card">
      <div className="flex items-start gap-3">
        <TransferPoster it={it} />
        <div className="min-w-0 flex-1">
          <TransferTitle it={it} />
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1">
            {paused && it.held_by_guard ? <GuardChip guard={guard} /> : it.stage === "not_managed" ? (
              // Nothing in Arrmada grabbed or imported it: a muted chip, no seed rule.
              <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{STAGE.not_managed.label}</span>
            ) : <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--panel-2)", color: paused ? "var(--ink-faint)" : "var(--good-text)" }}>{paused ? STAGE.paused.label : STAGE.seeding.label}</span>}
            <ItemChips it={it} />
            {showClient && <ClientChip it={it} />}
            <TypeChip mediaType={it.media_type} />
            <span className="font-mono text-xs text-ink-faint">{bytes(it.size_bytes)}</span>
            <span className="font-mono text-xs" title="Share ratio" style={{ color: "var(--good-text)" }}>⇅ {it.ratio.toFixed(2)}</span>
            <span className="font-mono text-xs text-ink-faint" title="Time seeded">{dur(it.seeding_time ?? 0)}</span>
            <span className="font-mono text-xs" style={{ color: it.up_speed > 0 ? "var(--good-text)" : "var(--ink-faint)" }}>{it.up_speed > 0 ? `↑${bytes(it.up_speed)}/s` : "idle"}</span>
          </div>
        </div>
        <CardActions
          name={it.item?.title ?? it.name}
          primary={<ResumeBtn it={it} guard={guard} busy={busy} act={act} />}
          secondary={[{ label: "Delete", danger: true, title: "Stop seeding and remove from the client — asks what to do with the files", disabled: busy, onSelect: () => setRemoving(true) }]}
        />
      </div>
      <div className="mt-2.5 flex items-center gap-2.5">
        {goal.frac != null ? (
          <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
            <div className="h-full rounded-full" style={{ width: `${Math.round(goal.frac * 100)}%`, background: "var(--good)" }} />
          </div>
        ) : <div className="flex-1" />}
        <span className="font-mono text-xs text-ink-faint">{goal.label}</span>
      </div>
      {removing && <RemoveDownloadDialog it={it} onClose={() => setRemoving(false)} onDone={(r) => { setRemoving(false); onRemoved(removedMessage(r)); }} />}
    </div>
  );
}

// WantedItem is one Searching/Upcoming row: a movie, show, book or album, linking to its
// page, saying what is really happening to it (wantedChip) and why it isn't downloading
// (wantedLine), with Search now. The button sits beside the link, not inside it.
function WantedItem({ item, kind, onSearched, onError }: { item: WantedRow; kind: "searching" | "upcoming"; onSearched: () => void; onError: (m: string) => void }) {
  const [job, setJob] = useState<number | null>(null);
  const [starting, setStarting] = useState(false);
  const [result, setResult] = useState<{ text: string; failed: boolean } | null>(null);
  // Once a refresh brings a newer stored attempt, it says the same with its time.
  const latestID = item.last_search?.latest.id;
  useEffect(() => { setResult(null); }, [latestID]);
  const search = useJob(job, {
    onDone: (j) => {
      setJob(null);
      setResult({ text: searchJobLine(j), failed: jobFailed(j) });
      onSearched();
    },
  });
  const searchNow = async () => {
    setStarting(true);
    setResult(null);
    try {
      const r = await api.wantedSearch(item.media_type, item.id);
      if (r.job_id) setJob(r.job_id);
      else onSearched();
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setStarting(false);
    }
  };
  const running = starting || search.running;
  const upcoming = kind === "upcoming";
  const chip = upcoming
    ? { text: item.available_at ? `${item.next_label ? item.next_label + " · " : ""}${fmtReleaseDate(item.available_at)}` : "Awaiting release", tone: "var(--ink-faint)", pulse: false }
    : running ? { text: "Searching now", tone: "var(--accent-text)", pulse: true } : wantedChip(item);
  // The result of this page's Search now stays until the next refresh brings the stored
  // attempt, which then says the same thing with its time.
  const line = upcoming ? "" : running ? "Searching your indexers now…" : result ? result.text : wantedLine(item);
  const lineTone = !upcoming && !running && result?.failed ? "var(--reject)" : "var(--ink-faint)";
  // Nothing to search while the client is down (it may be downloading) or a download
  // waits in Needs you, which is where that one is settled.
  const canSearch = !upcoming && item.state !== "unknown" && item.state !== "held_for_review";
  return (
    <div className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-[var(--panel-2)]" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
      <Link to={wantedHref(item)} className="flex min-w-0 flex-1 items-center gap-3">
        <span className={`inline-block h-2 w-2 flex-none rounded-full ${chip.pulse ? "animate-pulse" : ""}`} style={{ background: chip.pulse ? chip.tone : "var(--ink-faint)" }} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-base font-medium">
            {item.title} <span className="font-mono text-xs text-ink-faint">{item.year || ""}</span>
            {item.byline && <span className="text-xs text-ink-faint"> · {item.byline}</span>}
          </div>
          {line && <div className="mt-0.5 line-clamp-2 text-xs leading-snug" style={{ color: lineTone }} data-testid="wanted-line">{line}</div>}
        </div>
        <span className="hidden flex-none items-center gap-1.5 sm:flex">
          <TypeChip mediaType={item.media_type} />
          {item.missing?.map((m) => <span key={m} className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{m}</span>)}
          <ProfileChip profile={item.quality_profile} />
        </span>
      </Link>
      {item.state === "held_for_review" && !upcoming ? (
        <Link to={LINKS.review} className="w-[120px] flex-none text-right font-mono text-2xs uppercase underline sm:w-[150px]" style={{ color: chip.tone }}>{chip.text}</Link>
      ) : (
        <span className="w-[120px] flex-none text-right font-mono text-2xs uppercase sm:w-[150px]" style={{ color: chip.tone }}>{chip.text}</span>
      )}
      {!upcoming && (
        <span className="flex w-[84px] flex-none justify-end">
          {canSearch && <IconBtn label={running ? "Searching…" : "Search now"} title="Search your indexers for it now; the automatic searches start again from the shortest wait" disabled={running} onClick={() => void searchNow()} />}
        </span>
      )}
    </div>
  );
}

function Stat({ label, value, tone, title }: { label: string; value: string; tone?: string; title?: string }) {
  return <span className="font-mono text-sm" title={title}><span className="text-ink-faint">{label} </span><span style={{ color: tone ?? "var(--ink)" }}>{value}</span></span>;
}

function IconBtn({ label, onClick, tone, title, disabled }: { label: string; onClick: () => void; tone?: string; title?: string; disabled?: boolean }) {
  return (
    <button onClick={onClick} disabled={disabled} title={title ?? label} className="rounded-lg px-2 py-1.5 text-xs font-semibold transition-colors" style={{ border: `1px solid ${tone ?? "var(--line)"}`, color: tone ?? "var(--ink-dim)", opacity: disabled ? 0.5 : 1 }}>
      {label}
    </button>
  );
}

const MIB = 1024 * 1024;
const toMB = (b: number) => (b > 0 ? +(b / MIB).toFixed(1) : 0);
const fromMB = (m: number) => Math.round(Math.max(0, m) * MIB);
const hhmm = (h: number, m: number) => `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;

function SettingsPanel({ clientId, onClose }: { clientId: number; onClose: () => void }) {
  const [s, setS] = useState<ClientSettings | null>(null);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // What the client last said (or was last saved as), to tell an unsaved edit.
  const [stored, setStored] = useState("");
  useUnsavedGuard(!!s && JSON.stringify(s) !== stored);

  useEffect(() => {
    api.clientSettings(clientId).then((v) => { setS(v); setStored(JSON.stringify(v)); }).catch((e: Error) => setError(e.message));
  }, [clientId]);

  const patch = (p: Partial<ClientSettings>) => setS((x) => (x ? { ...x, ...p } : x));
  const save = async () => {
    if (!s) return;
    setError(null);
    try {
      await api.setClientSettings(clientId, s);
      setStored(JSON.stringify(s));
      setSaved(true);
      window.setTimeout(() => setSaved(false), 2000);
    } catch (e) { setError((e as Error).message); }
  };

  if (error) return <div className="mb-4 rounded-lg p-3 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>{error}</div>;
  if (!s) return <div className="mb-4 rounded-xl p-4 text-sm text-ink-dim" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>Loading settings…</div>;

  const num = "w-[70px] rounded-lg px-2 py-1.5 text-right text-md";
  const numStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

  return (
    <div className="mb-4 rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="mb-3 flex items-center justify-between">
        <h2 className="m-0 text-md font-bold">Speed &amp; limits</h2>
        <IconButton label="Close" onClick={onClose} className="text-sm text-ink-faint">✕</IconButton>
      </div>
      <div className="flex flex-col gap-3">
        <Row label="Download limit" hint="0 = unlimited">
          <input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.dl_limit)} onChange={(e) => patch({ dl_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit>
        </Row>
        <Row label="Upload limit" hint="0 = unlimited">
          <input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.up_limit)} onChange={(e) => patch({ up_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit>
        </Row>

        <div className="rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          <label className="flex items-center gap-2 text-base font-semibold">
            <input type="checkbox" checked={s.schedule_enabled} onChange={(e) => patch({ schedule_enabled: e.target.checked })} style={{ accentColor: "var(--accent)" }} />
            Alternate speeds on a schedule
          </label>
          <p className="mb-2.5 mt-0.5 text-xs text-ink-faint">During the window below, these alternate limits apply instead — e.g. throttle overnight.</p>
          <div className={`flex flex-col gap-2.5 ${s.schedule_enabled ? "" : "pointer-events-none opacity-50"}`}>
            <Row label="Alt download"><input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.alt_dl_limit)} onChange={(e) => patch({ alt_dl_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit></Row>
            <Row label="Alt upload"><input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.alt_up_limit)} onChange={(e) => patch({ alt_up_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit></Row>
            <Row label="Window">
              <input type="time" className="rounded-lg px-2 py-1.5 text-md" style={numStyle} value={hhmm(s.from_hour, s.from_min)} onChange={(e) => { const [h, m] = e.target.value.split(":").map(Number); patch({ from_hour: h, from_min: m }); }} />
              <span className="text-ink-faint">to</span>
              <input type="time" aria-label="Window ends" className="rounded-lg px-2 py-1.5 text-md" style={numStyle} value={hhmm(s.to_hour, s.to_min)} onChange={(e) => { const [h, m] = e.target.value.split(":").map(Number); patch({ to_hour: h, to_min: m }); }} />
              <select aria-label="On" className="rounded-lg px-2 py-1.5 text-base" style={numStyle} value={s.days} onChange={(e) => patch({ days: Number(e.target.value) })}>
                <option value={0}>Every day</option>
                <option value={1}>Weekdays</option>
                <option value={2}>Weekends</option>
              </select>
            </Row>
          </div>
        </div>

        <Row label="Max active downloads" hint="Queue the rest"><input type="number" min={1} className={num} style={numStyle} value={s.max_active_downloads} onChange={(e) => patch({ max_active_downloads: Math.max(1, Number(e.target.value)) })} /></Row>
        <Row label="Max active seeding"><input type="number" min={1} className={num} style={numStyle} value={s.max_active_uploads} onChange={(e) => patch({ max_active_uploads: Math.max(1, Number(e.target.value)) })} /></Row>
      </div>
      <div className="mt-3.5 flex items-center gap-3">
        <button onClick={save} className="rounded-lg px-4 py-2 text-base font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Save</button>
        {saved && <span className="text-sm" style={{ color: "var(--good-text)" }}>Saved ✓</span>}
      </div>
    </div>
  );
}

// Row is a <label> around its control, so the name (and hint) is the control's label and
// clicking it focuses the control.
function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="flex items-center gap-3">
      <span className="block w-[160px] flex-none">
        <span className="block text-base">{label}</span>
        {hint && <span className="block text-xs text-ink-faint">{hint}</span>}
      </span>
      <span className="flex items-center gap-2">{children}</span>
    </label>
  );
}

function Unit({ children }: { children: React.ReactNode }) {
  return <span className="text-xs text-ink-faint">{children}</span>;
}
