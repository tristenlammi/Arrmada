import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { useTabParam } from "../lib/useTabParam";
import { TabPanel, Tabs, type TabItem } from "../ui/Tabs";
import { LINKS } from "../lib/links";
import { RemoveDownloadDialog, removedMessage } from "../components/RemoveDownloadDialog";
import { usePoll } from "../lib/usePoll";
import { api, type ActivityDownload, type ClientSettings, type DiskGuardHold, type DownloadClientsState, type WantedLists, type WantedRow } from "../lib/api";
import { useMe } from "../lib/me";
import { useQuery } from "../lib/query";
import { useLive } from "../lib/useLive";
import { jobFailed, useJob } from "../lib/useJob";
import { searchJobLine } from "../lib/searchOutcome";
import { wantedChip, wantedHref, wantedKey, wantedLine } from "../lib/wanted";
import { ErrorState, Menu, Skeleton } from "../ui";

type MediaFilter = "all" | "movie" | "series" | "book" | "music";
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
// red of a refusal.
const STATE_TONE: Record<string, string> = {
  downloading: "var(--accent)", seeding: "var(--good)", paused: "var(--ink-faint)", error: "var(--avoid)", checking: "var(--avoid)",
  stalled: "var(--avoid)", metadata: "var(--ink-faint)", queued: "var(--ink-faint)", moving: "var(--avoid)", allocating: "var(--ink-faint)",
};
const PHASE_TEXT: Record<string, string> = {
  downloading: "Downloading", seeding: "Seeding", paused: "Paused", error: "Error", checking: "Checking",
  stalled: "Stalled", metadata: "Fetching metadata", queued: "Queued", moving: "Moving", allocating: "Allocating",
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

// phaseLabel is the in-flight chip's text. The phase (when the server sends one) tells a
// torrent nobody is seeding, or one still fetching its file list, from a live download —
// the plain state calls all of those "downloading".
export function phaseLabel(it: ActivityDownload, nowSec = Date.now() / 1000): { text: string; tone: string; tip?: string } {
  const phase = it.phase || it.state;
  const tone = STATE_TONE[phase] ?? "var(--ink-faint)";
  if (phase === "stalled") {
    const seeds = it.seeds ?? 0;
    const swarm = it.swarm_seeds ?? 0;
    const idle = idleMinutes(it, nowSec);
    const nothingYet = !it.last_activity;
    const idleText = idle == null ? "" : nothingYet ? ` · no data yet (${mins(idle)})` : ` · no data for ${mins(idle)}`;
    return { text: `Stalled · ${seeds} seed${seeds === 1 ? "" : "s"}${idleText}`, tone, tip: `No peer is sending data. Connected to ${seeds} seed${seeds === 1 ? "" : "s"}; the tracker reports ${swarm} in the swarm. Reannounce asks the trackers for peers again.` };
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

// isProblem: a download that needs a look — errored, stalled, or stuck fetching its file
// list. What ?show=problems (the Needs-you card's link) narrows the lists to.
export function isProblem(it: Pick<ActivityDownload, "phase" | "state">): boolean {
  const phase = it.phase || it.state;
  return phase === "error" || phase === "stalled" || phase === "metadata" || it.state === "error";
}

type SortKey = "name" | "progress" | "speed" | "size" | "ratio" | "seedtime";
type Tab = "downloads" | "seeding" | "searching" | "upcoming";
const TAB_KEYS: readonly Tab[] = ["downloads", "seeding", "searching", "upcoming"];

function ProfileChip({ profile }: { profile: string }) {
  const na = profile === "n/a";
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: na ? "var(--panel-2)" : "var(--accent-soft)", color: na ? "var(--ink-faint)" : "var(--accent)" }}>{profile}</span>;
}

// How often the Wanted half refreshes on its own. It reads the search history, so it
// isn't polled with the queue every three seconds; a finished search, a grab or an import
// announced over the socket refreshes it sooner.
const WANTED_POLL_MS = 30_000;
const WANTED_TOPICS = ["search.finished", "release.grabbed", "download.imported", "series.imported", "series.searched"];

export function Downloads() {
  const [downloads, setDownloads] = useState<ActivityDownload[]>([]);
  const [totals, setTotals] = useState<{ down_speed: number; up_speed: number; active: number }>({ down_speed: 0, up_speed: 0, active: 0 });
  const [freeGb, setFreeGb] = useState<number | null>(null);
  const [diskPath, setDiskPath] = useState("");
  const [clients, setClients] = useState<DownloadClientsState | null>(null); // null = unknown
  const [guard, setGuard] = useState<DiskGuardHold | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 4500); };
  const [reconnecting, setReconnecting] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SortKey>("progress");
  const [typeFilter, setTypeFilter] = useState<MediaFilter>("all");
  const { musicEnabled } = useMe();
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [clientId, setClientId] = useState<number | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [tab, setTab] = useTabParam(TAB_KEYS, "downloads");
  const [seedSort, setSeedSort] = useState<SortKey>("ratio");
  // ?show=problems: only the downloads that need a look (linked from Needs you).
  const [params, setParams] = useSearchParams();
  const problemsOnly = params.get("show") === "problems";
  const showAll = () => setParams((p) => { const next = new URLSearchParams(p); next.delete("show"); return next; }, { replace: true });

  useEffect(() => {
    api.downloadClients().then((cs) => {
      // A switched-off client gets no downloads, so its settings aren't the ones in play.
      const on = cs.filter((c) => c.enabled);
      const qb = on.find((c) => c.kind === "qbittorrent") ?? on[0];
      if (qb) setClientId(qb.id);
    }).catch(() => {});
  }, []);

  // Consecutive failed polls; one blip isn't worth a "reconnecting" banner.
  const fails = useRef(0);
  usePoll(() =>
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
    }), 3000);

  // The Wanted view (Searching and Upcoming): its own, slower read.
  const wanted = useQuery<WantedLists>("wanted", api.wanted, { staleMs: 10_000 });
  const refetchWanted = wanted.refetch;
  usePoll(refetchWanted, WANTED_POLL_MS, { immediate: false });
  const live = useLive();
  // A sweep finishes searches in bursts: one refresh for the lot, a moment after.
  const wantedTimer = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!live.last || !WANTED_TOPICS.includes(live.last.topic) || wantedTimer.current !== undefined) return;
    wantedTimer.current = window.setTimeout(() => { wantedTimer.current = undefined; void refetchWanted(); }, 1500);
  }, [live.last, refetchWanted]);
  useEffect(() => () => window.clearTimeout(wantedTimer.current), []);
  const searching = useMemo(() => wanted.data?.searching ?? [], [wanted.data]);
  const upcoming = useMemo(() => wanted.data?.upcoming ?? [], [wanted.data]);

  // The next poll reflects what happened; a refusal (e.g. the disk guard holding a
  // torrent) is shown, since the poll alone can't say why nothing changed.
  const act = async (hash: string, fn: () => Promise<unknown>) => {
    setBusy((b) => ({ ...b, [hash]: true }));
    try { await fn(); } catch (e) { flash((e as Error).message); } finally { setBusy((b) => ({ ...b, [hash]: false })); }
  };
  const resumeAll = () => act("all", async () => {
    const r = await api.resumeDownload("all");
    const held = r.held_by_guard ?? 0;
    flash(`Resumed ${r.resumed ?? 0}${held > 0 ? ` · ${held} held by the disk guard` : ""}.`);
  });

  const activeDownloads = useMemo(() => downloads.filter((d) => d.progress < 1), [downloads]);
  const seedingDownloads = useMemo(() => downloads.filter((d) => d.progress >= 1), [downloads]);

  const filterSort = (list: ActivityDownload[], sortKey: SortKey) => {
    const q = query.trim().toLowerCase();
    let l = typeFilter === "all" ? list : list.filter((d) => (d.media_type ?? "movie") === typeFilter);
    if (problemsOnly) l = l.filter(isProblem);
    l = q ? l.filter((d) => d.name.toLowerCase().includes(q)) : [...l];
    l.sort((a, b) => {
      switch (sortKey) {
        case "name": return a.name.localeCompare(b.name);
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
  const shownSeeding = useMemo(() => filterSort(seedingDownloads, seedSort), [seedingDownloads, query, seedSort, typeFilter, problemsOnly]); // eslint-disable-line react-hooks/exhaustive-deps

  const shownSearching = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? searching.filter((s) => wantedMatches(s, q)) : searching;
  }, [searching, query]);
  const shownUpcoming = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = q ? upcoming.filter((s) => wantedMatches(s, q)) : upcoming;
    return [...list].sort((a, b) => (a.available_at ?? "").localeCompare(b.available_at ?? ""));
  }, [upcoming, query]);

  const base = tab === "seeding" ? seedingDownloads : activeDownloads;
  const typeCount = (k: MediaFilter) => (k === "all" ? base.length : base.filter((d) => (d.media_type ?? "movie") === k).length);

  const TABS: TabItem<Tab>[] = [
    { key: "downloads", label: "Downloads", count: activeDownloads.length },
    { key: "seeding", label: "Seeding", count: seedingDownloads.length },
    { key: "searching", label: "Searching", count: searching.length },
    { key: "upcoming", label: "Upcoming", count: upcoming.length },
  ];
  const isDownloadTab = tab === "downloads" || tab === "seeding";
  // No download client at all: there is no queue to show or control. Searching and
  // Upcoming still work — they're the library's side.
  const noClient = clients?.configured === 0;
  // A client that isn't answering looks exactly like an empty queue unless it's said.
  const clientDown = !!clients && clients.configured > 0 && !clients.ok;

  return (
    <>
      <PageHeader title="Downloads" />
      <div className="mx-auto w-full max-w-[1360px] px-4 py-6 sm:px-6">
        {/* Header: live totals + free disk + controls */}
        <div className="mb-4 flex flex-wrap items-center gap-x-5 gap-y-2 rounded-xl px-4 py-2.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
          {!noClient && (
            <>
              <span className="flex items-center gap-1.5 font-mono text-[11px]" style={{ color: reconnecting || clientDown ? "var(--avoid)" : "var(--ink-faint)" }}>
                <span className="inline-block h-1.5 w-1.5 rounded-full" style={{ background: reconnecting || clientDown ? "var(--avoid)" : "var(--accent)" }} />
                {reconnecting ? "Reconnecting…" : clientDown ? "Client offline" : "Live"}
              </span>
              <Stat label="↓" value={`${bytes(totals.down_speed)}/s`} tone="var(--accent)" />
              <Stat label="↑" value={`${bytes(totals.up_speed)}/s`} tone="var(--good)" />
              <Stat label="active" value={String(totals.active)} />
              {freeGb != null && <Stat label="free" value={`${freeGb.toFixed(0)} GB`} tone={freeGb < 20 ? "var(--reject)" : undefined} title={diskPath ? `Free space on ${diskPath}` : undefined} />}
            </>
          )}
          <div className="ml-auto flex items-center gap-2">
            {!noClient && (
              <>
                <button onClick={() => act("all", () => api.pauseDownload("all"))} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Pause all</button>
                <button onClick={resumeAll} title={guard ? "Torrents the disk guard paused stay paused until the volume drains" : undefined} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Resume all{guard && guard.holding > 0 ? ` (${guard.holding} held by disk guard)` : ""}</button>
              </>
            )}
            {clientId != null && (
              <button onClick={() => setShowSettings((s) => !s)} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: `1px solid ${showSettings ? "var(--accent)" : "var(--line)"}`, color: showSettings ? "var(--accent)" : "var(--ink-dim)" }}>⚙ Speed & limits</button>
            )}
          </div>
        </div>

        {showSettings && clientId != null && <SettingsPanel clientId={clientId} onClose={() => setShowSettings(false)} />}

        {clients && clients.configured > 0 && clients.enabled === 0 && (
          <div className="mb-4 rounded-lg p-3.5 text-[12.5px]" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
            Every download client is switched off, so Arrmada can't grab anything. Enable one on the{" "}
            <Link to={LINKS.downloadClients} className="font-semibold underline" style={{ color: "inherit" }}>Download clients</Link> page.
          </div>
        )}
        {clientDown && clients && (
          <div className="mb-4 rounded-lg p-3.5 text-[12.5px]" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
            {clients.name || "A download client"} unreachable{clients.since ? ` since ${sinceLabel(clients.since)}` : ""} — searches are paused until it's back
            {clients.error ? ` (${clients.error})` : ""}. Check it on the{" "}
            <Link to={LINKS.downloadClients} className="font-semibold underline" style={{ color: "inherit" }}>Download clients</Link> page.
          </div>
        )}

        <Tabs tabs={TABS} value={tab} onChange={setTab} idPrefix="downloads" label="Downloads lists" className="mb-4 border-b" />

        <TabPanel idPrefix="downloads" value={tab}>
          {/* Filters: type pills (download tabs only) + search + sort */}
          <div className="mb-3 flex flex-wrap items-center gap-2">
            {isDownloadTab && TYPE_PILLS.filter((p) => p.key !== "music" || musicEnabled).map((p) => {
              const on = typeFilter === p.key;
              return (
                <button key={p.key} onClick={() => setTypeFilter(p.key)} className="rounded-full px-3 py-1 text-[12px] font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>
                  {p.label} <span className="font-mono text-[10.5px] opacity-70">{typeCount(p.key)}</span>
                </button>
              );
            })}
            {isDownloadTab && problemsOnly && (
              <button onClick={showAll} title="Show every download" className="rounded-full px-3 py-1 text-[12px] font-semibold" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
                Problems only ✕
              </button>
            )}
            <div className="ml-auto flex items-center gap-2">
              <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search…" className="w-[200px] rounded-lg px-3 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
              {tab === "downloads" && (
                <select value={sort} onChange={(e) => setSort(e.target.value as SortKey)} className="rounded-lg px-2.5 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                  <option value="progress">Sort: Progress</option>
                  <option value="name">Sort: Name</option>
                  <option value="speed">Sort: Speed</option>
                  <option value="size">Sort: Size</option>
                </select>
              )}
              {tab === "seeding" && (
                <select value={seedSort} onChange={(e) => setSeedSort(e.target.value as SortKey)} className="rounded-lg px-2.5 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                  <option value="ratio">Sort: Ratio</option>
                  <option value="seedtime">Sort: Seed time</option>
                  <option value="name">Sort: Name</option>
                  <option value="size">Sort: Size</option>
                </select>
              )}
            </div>
          </div>

          {!loaded ? null : noClient && isDownloadTab ? (
            <NoClientCard />
          ) : tab === "downloads" ? (
            shownDownloads.length === 0 ? <Empty>{problemsOnly ? "No download needs a look right now." : "Nothing downloading. Grab a release and it'll appear here."}</Empty> : (
              <div className="flex flex-col gap-2">
                {shownDownloads.map((it) => <DownloadCard key={it.hash} it={it} guard={guard} busy={!!busy[it.hash]} act={act} onRemoved={flash} onNote={flash} />)}
              </div>
            )
          ) : tab === "seeding" ? (
            shownSeeding.length === 0 ? <Empty>Nothing seeding right now.</Empty> : (
              <div className="flex flex-col gap-2">
                <SeedingSummary items={shownSeeding} />
                {shownSeeding.map((it) => <SeedingCard key={it.hash} it={it} guard={guard} busy={!!busy[it.hash]} act={act} onRemoved={flash} />)}
              </div>
            )
          ) : !wanted.data ? (
            wanted.error ? <ErrorState what="what's wanted" message={wanted.error.message} onRetry={() => void wanted.refetch()} busy={wanted.loading} /> : <Skeleton variant="list" count={4} />
          ) : tab === "searching" ? (
            shownSearching.length === 0 ? <Empty>Nothing is being searched. Monitored, available titles that are missing a file show up here.</Empty> : (
              <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
                {shownSearching.map((s) => <WantedItem key={wantedKey(s)} item={s} kind="searching" onSearched={() => void wanted.refetch()} flash={flash} />)}
              </div>
            )
          ) : (
            shownUpcoming.length === 0 ? <Empty>Nothing upcoming. Unreleased films and albums, and unaired episodes you monitor, land here.</Empty> : (
              <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
                {shownUpcoming.map((s) => <WantedItem key={wantedKey(s)} item={s} kind="upcoming" onSearched={() => void wanted.refetch()} flash={flash} />)}
              </div>
            )
          )}
        </TabPanel>
      </div>
      {toast && <div className="fixed bottom-5 left-1/2 max-w-[90vw] -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

// GuardChip marks a torrent the disk guard paused, so it doesn't read as one paused by hand.
function GuardChip({ guard }: { guard: DiskGuardHold | null }) {
  return (
    <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }}>
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
    <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
      <div className="mb-3">No download client yet — Arrmada needs qBittorrent to download anything.</div>
      <Link to={LINKS.downloadClients} className="inline-block rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
        Add a download client →
      </Link>
    </div>
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
  return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{children}</div>;
}

// wantedMatches is the page's search box over a Wanted row: its title, author or artist.
const wantedMatches = (s: WantedRow, q: string) => s.title.toLowerCase().includes(q) || (s.byline ?? "").toLowerCase().includes(q);

function TypeChip({ mediaType }: { mediaType?: string }) {
  const label = mediaType === "series" ? "TV" : mediaType === "book" ? "Book" : mediaType === "music" ? "Music" : "Movie";
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{label}</span>;
}

// DownloadCard is an in-flight (incomplete) transfer: progress bar, speed, ETA, queue controls.
function DownloadCard({ it, guard, busy, act, onRemoved, onNote }: { it: ActivityDownload; guard: DiskGuardHold | null; busy: boolean; act: (hash: string, fn: () => Promise<unknown>) => Promise<void>; onRemoved: (m: string) => void; onNote: (m: string) => void }) {
  const paused = it.state === "paused";
  const [removing, setRemoving] = useState(false);
  const chip = phaseLabel(it);
  const stall = stallLine(it);
  return (
    <div className="rounded-xl p-3.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="truncate font-mono text-[11.5px]" title={it.name}>{it.name}</div>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            {paused && it.held_by_guard ? <GuardChip guard={guard} /> : <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" title={chip.tip} style={{ background: "var(--panel-2)", color: chip.tone }}>{chip.text}</span>}
            <TypeChip mediaType={it.media_type} />
            <ProfileChip profile={it.quality_profile} />
            <span className="font-mono text-[10px] text-ink-faint">{bytes(it.size_bytes)}</span>
            <span className="font-mono text-[10.5px]" style={{ color: it.down_speed > 0 ? "var(--accent)" : "var(--ink-faint)" }}>{it.down_speed > 0 ? `↓${bytes(it.down_speed)}/s` : "—"}</span>
            <span className="font-mono text-[10.5px] text-ink-faint">ETA {eta(it.eta_seconds)}</span>
          </div>
          {stall && <div className="mt-1 font-mono text-[10.5px]" style={{ color: "var(--avoid)" }}>{stall}</div>}
        </div>
        <div className="flex flex-none items-center gap-1">
          <ResumeBtn it={it} guard={guard} busy={busy} act={act} />
          <IconBtn label="Block" tone="var(--avoid)" title="Blocklist this release and grab a different one" disabled={busy} onClick={() => act(it.hash, async () => { const r = await api.blockDownload(it.hash, it.name); onRemoved(`Blocked for ${r.blocked_for.title} — searching for another release.`); })} />
          <IconBtn label="Delete" tone="var(--reject)" title="Remove from the client — asks what to do with the files" disabled={busy} onClick={() => setRemoving(true)} />
          <Menu
            trigger="⋯"
            label={`More actions for ${it.name}`}
            triggerClassName="rounded-md px-2 py-1 text-[11px] font-semibold"
            triggerStyle={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}
            items={[
              { label: "Reannounce", busyLabel: "Reannouncing…", disabled: busy, onSelect: () => act(it.hash, async () => { await api.torrentAction(it.hash, "reannounce"); onNote("Asked the trackers for peers again."); }) },
              { label: "Recheck files", busyLabel: "Rechecking…", disabled: busy, onSelect: () => act(it.hash, async () => { await api.torrentAction(it.hash, "recheck"); onNote("Rechecking the downloaded data."); }) },
              { label: "Move up the queue", disabled: busy, onSelect: () => act(it.hash, () => api.torrentAction(it.hash, "prio_up")) },
              { label: "Move down the queue", disabled: busy, onSelect: () => act(it.hash, () => api.torrentAction(it.hash, "prio_down")) },
            ]}
          />
        </div>
      </div>
      <div className="mt-2.5 flex items-center gap-2">
        <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
          <div className="h-full rounded-full" style={{ width: `${Math.round(it.progress * 100)}%`, background: paused ? "var(--ink-faint)" : "var(--accent)" }} />
        </div>
        <span className="w-10 text-right font-mono text-[10.5px] text-ink-dim">{Math.round(it.progress * 100)}%</span>
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
      <Stat label="avg ratio" value={avgRatio.toFixed(2)} tone="var(--good)" />
      <Stat label="sharing" value={bytes(sharing)} />
    </div>
  );
}

// SeedingCard is a completed torrent that's sharing back: ratio, seed time, goal progress.
function SeedingCard({ it, guard, busy, act, onRemoved }: { it: ActivityDownload; guard: DiskGuardHold | null; busy: boolean; act: (hash: string, fn: () => Promise<unknown>) => void; onRemoved: (m: string) => void }) {
  const paused = it.state === "paused";
  const goal = seedGoal(it);
  const [removing, setRemoving] = useState(false);
  return (
    <div className="rounded-xl p-3.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="truncate font-mono text-[11.5px]" title={it.name}>{it.name}</div>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            {paused && it.held_by_guard ? <GuardChip guard={guard} /> : <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: paused ? "var(--ink-faint)" : "var(--good)" }}>{paused ? "paused" : "seeding"}</span>}
            <TypeChip mediaType={it.media_type} />
            <span className="font-mono text-[10px] text-ink-faint">{bytes(it.size_bytes)}</span>
            <span className="font-mono text-[10.5px]" title="Share ratio" style={{ color: "var(--good)" }}>⇅ {it.ratio.toFixed(2)}</span>
            <span className="font-mono text-[10.5px] text-ink-faint" title="Time seeded">{dur(it.seeding_time ?? 0)}</span>
            <span className="font-mono text-[10.5px]" style={{ color: it.up_speed > 0 ? "var(--good)" : "var(--ink-faint)" }}>{it.up_speed > 0 ? `↑${bytes(it.up_speed)}/s` : "idle"}</span>
          </div>
        </div>
        <div className="flex flex-none items-center gap-1">
          <ResumeBtn it={it} guard={guard} busy={busy} act={act} />
          <IconBtn label="Delete" tone="var(--reject)" title="Stop seeding and remove from the client — asks what to do with the files" disabled={busy} onClick={() => setRemoving(true)} />
        </div>
      </div>
      <div className="mt-2.5 flex items-center gap-2.5">
        {goal.frac != null ? (
          <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
            <div className="h-full rounded-full" style={{ width: `${Math.round(goal.frac * 100)}%`, background: "var(--good)" }} />
          </div>
        ) : <div className="flex-1" />}
        <span className="font-mono text-[10px] text-ink-faint">{goal.label}</span>
      </div>
      {removing && <RemoveDownloadDialog it={it} onClose={() => setRemoving(false)} onDone={(r) => { setRemoving(false); onRemoved(removedMessage(r)); }} />}
    </div>
  );
}

// WantedItem is one Searching/Upcoming row: a movie, show, book or album, linking to its
// page, saying what is really happening to it (wantedChip) and why it isn't downloading
// (wantedLine), with Search now. The button sits beside the link, not inside it.
function WantedItem({ item, kind, onSearched, flash }: { item: WantedRow; kind: "searching" | "upcoming"; onSearched: () => void; flash: (m: string) => void }) {
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
      flash((e as Error).message);
    } finally {
      setStarting(false);
    }
  };
  const running = starting || search.running;
  const upcoming = kind === "upcoming";
  const chip = upcoming
    ? { text: item.available_at ? `${item.next_label ? item.next_label + " · " : ""}${fmtReleaseDate(item.available_at)}` : "Awaiting release", tone: "var(--ink-faint)", pulse: false }
    : running ? { text: "Searching now", tone: "var(--accent)", pulse: true } : wantedChip(item);
  // The result of this page's Search now stays until the next refresh brings the stored
  // attempt, which then says the same thing with its time.
  const line = upcoming ? "" : running ? "Searching your indexers now…" : result ? result.text : wantedLine(item);
  const lineTone = !upcoming && !running && result?.failed ? "var(--reject)" : "var(--ink-faint)";
  // Nothing to search while the client is down (it may be downloading) or a download
  // waits in Review; Review is where that one is settled.
  const canSearch = !upcoming && item.state !== "unknown" && item.state !== "held_for_review";
  return (
    <div className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-[var(--panel-2)]" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
      <Link to={wantedHref(item)} className="flex min-w-0 flex-1 items-center gap-3">
        <span className={`inline-block h-2 w-2 flex-none rounded-full ${chip.pulse ? "animate-pulse" : ""}`} style={{ background: chip.pulse ? chip.tone : "var(--ink-faint)" }} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[12.5px] font-medium">
            {item.title} <span className="font-mono text-[10.5px] text-ink-faint">{item.year || ""}</span>
            {item.byline && <span className="text-[11px] text-ink-faint"> · {item.byline}</span>}
          </div>
          {line && <div className="mt-0.5 line-clamp-2 text-[11px] leading-snug" style={{ color: lineTone }} data-testid="wanted-line">{line}</div>}
        </div>
        <span className="hidden flex-none items-center gap-1.5 sm:flex">
          <TypeChip mediaType={item.media_type} />
          {item.missing?.map((m) => <span key={m} className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{m}</span>)}
          <ProfileChip profile={item.quality_profile} />
        </span>
      </Link>
      {item.state === "held_for_review" && !upcoming ? (
        <Link to={LINKS.review} className="w-[120px] flex-none text-right font-mono text-[10px] uppercase underline sm:w-[150px]" style={{ color: chip.tone }}>{chip.text}</Link>
      ) : (
        <span className="w-[120px] flex-none text-right font-mono text-[10px] uppercase sm:w-[150px]" style={{ color: chip.tone }}>{chip.text}</span>
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
  return <span className="font-mono text-[11.5px]" title={title}><span className="text-ink-faint">{label} </span><span style={{ color: tone ?? "var(--ink)" }}>{value}</span></span>;
}

function IconBtn({ label, onClick, tone, title, disabled }: { label: string; onClick: () => void; tone?: string; title?: string; disabled?: boolean }) {
  return (
    <button onClick={onClick} disabled={disabled} title={title ?? label} className="rounded-lg px-2 py-1.5 text-[10.5px] font-semibold transition-colors" style={{ border: `1px solid ${tone ?? "var(--line)"}`, color: tone ?? "var(--ink-dim)", opacity: disabled ? 0.5 : 1 }}>
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

  useEffect(() => {
    api.clientSettings(clientId).then(setS).catch((e: Error) => setError(e.message));
  }, [clientId]);

  const patch = (p: Partial<ClientSettings>) => setS((x) => (x ? { ...x, ...p } : x));
  const save = async () => {
    if (!s) return;
    setError(null);
    try {
      await api.setClientSettings(clientId, s);
      setSaved(true);
      window.setTimeout(() => setSaved(false), 2000);
    } catch (e) { setError((e as Error).message); }
  };

  if (error) return <div className="mb-4 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>;
  if (!s) return <div className="mb-4 rounded-xl p-4 text-[12px] text-ink-dim" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>Loading settings…</div>;

  const num = "w-[70px] rounded-lg px-2 py-1.5 text-right text-[13px]";
  const numStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

  return (
    <div className="mb-4 rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="mb-3 flex items-center justify-between">
        <h2 className="m-0 text-[13.5px] font-bold">Speed &amp; limits</h2>
        <button onClick={onClose} className="text-[12px] text-ink-faint">✕</button>
      </div>
      <div className="flex flex-col gap-3">
        <Row label="Download limit" hint="0 = unlimited">
          <input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.dl_limit)} onChange={(e) => patch({ dl_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit>
        </Row>
        <Row label="Upload limit" hint="0 = unlimited">
          <input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.up_limit)} onChange={(e) => patch({ up_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit>
        </Row>

        <div className="rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          <label className="flex items-center gap-2 text-[12.5px] font-semibold">
            <input type="checkbox" checked={s.schedule_enabled} onChange={(e) => patch({ schedule_enabled: e.target.checked })} style={{ accentColor: "var(--accent)" }} />
            Alternate speeds on a schedule
          </label>
          <p className="mb-2.5 mt-0.5 text-[10.5px] text-ink-faint">During the window below, these alternate limits apply instead — e.g. throttle overnight.</p>
          <div className={`flex flex-col gap-2.5 ${s.schedule_enabled ? "" : "pointer-events-none opacity-50"}`}>
            <Row label="Alt download"><input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.alt_dl_limit)} onChange={(e) => patch({ alt_dl_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit></Row>
            <Row label="Alt upload"><input type="number" min={0} step={0.5} className={num} style={numStyle} value={toMB(s.alt_up_limit)} onChange={(e) => patch({ alt_up_limit: fromMB(Number(e.target.value)) })} /> <Unit>MB/s</Unit></Row>
            <Row label="Window">
              <input type="time" className="rounded-lg px-2 py-1.5 text-[13px]" style={numStyle} value={hhmm(s.from_hour, s.from_min)} onChange={(e) => { const [h, m] = e.target.value.split(":").map(Number); patch({ from_hour: h, from_min: m }); }} />
              <span className="text-ink-faint">to</span>
              <input type="time" className="rounded-lg px-2 py-1.5 text-[13px]" style={numStyle} value={hhmm(s.to_hour, s.to_min)} onChange={(e) => { const [h, m] = e.target.value.split(":").map(Number); patch({ to_hour: h, to_min: m }); }} />
              <select className="rounded-lg px-2 py-1.5 text-[12.5px]" style={numStyle} value={s.days} onChange={(e) => patch({ days: Number(e.target.value) })}>
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
        <button onClick={save} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Save</button>
        {saved && <span className="text-[12px]" style={{ color: "var(--good)" }}>Saved ✓</span>}
      </div>
    </div>
  );
}

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-3">
      <div className="w-[160px] flex-none">
        <div className="text-[12.5px]">{label}</div>
        {hint && <div className="text-[10px] text-ink-faint">{hint}</div>}
      </div>
      <div className="flex items-center gap-2">{children}</div>
    </div>
  );
}

function Unit({ children }: { children: React.ReactNode }) {
  return <span className="text-[11px] text-ink-faint">{children}</span>;
}
