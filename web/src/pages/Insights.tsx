import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link, Navigate, useParams, useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { LINKS } from "../lib/links";
import { useTabParam } from "../lib/useTabParam";
import { usePoll } from "../lib/usePoll";
import { hwBadge } from "../lib/plexHw";
import { TabPanel, Tabs } from "../ui/Tabs";
import { IconButton, Modal, useToast } from "../ui";
import { type PlexConfig, type PlexStatus, type InsightsActivity, type InsightsServer, type InsightsServerStatus, type InsightsStream, type InsightsStats, type UserEntry, type LibraryStat, type RecentItem, type InsightsGraphs, type NameStat, type Reliability, type BufferGroup } from "../lib/api";
import { insightsApi } from "../lib/insightsApi";
import { useSearchParamState } from "../lib/useSearchState";
import { Page } from "../ui/Page";
import { ServerMark, SERVER_LABEL } from "../components/brand";
import { CAUSE, Chip, DECISION, FilterGroup, InsightsCfg, InsightsServers, JellyfinNoBuffering, RowServer, SERVER_FILTERS, ServerChips, StatTile, fmtClock, fmtDate, fmtDur, fmtHours, fmtHoursValue, fmtMbps, fmtStall, geoLabel, dayLabel, bandwidthLabels, ImportedNotice, SetupState, UserLink, Avatar, personPath, serverArg, type Metric, type ServerFilter } from "./insights/shared";
import { HistoryView } from "./insights/HistoryView";
import { LibraryView } from "./insights/LibraryView";
import { InsightsPerson } from "./InsightsPerson";
import { BarChart, ChartCard, HBarChart, Legend, LineChart } from "./insights/charts";

// Insights — Arrmada's watch monitoring for Plex and Jellyfin (a Tautulli replacement): live
// Activity, History, People, Graphs and Reliability (buffering). With both servers connected,
// server chips narrow every tab to one (?server=, in the address) and each row names its
// server. The connections and the alerts used to be tabs here; they live in Settings →
// Connections and Settings → Alerts now.
type Tab = "activity" | "history" | "people" | "graphs" | "reliability" | "library";
const TABS: { key: Tab; label: string }[] = [
  { key: "activity", label: "Activity" },
  { key: "history", label: "History" },
  { key: "people", label: "People" },
  { key: "graphs", label: "Graphs" },
  { key: "reliability", label: "Reliability" },
  { key: "library", label: "Library" },
];
// The tabs that read recorded plays: they only grow while monitoring is on, so they carry
// the "monitoring is off" banner when it isn't.
const RECORDED_TABS: Tab[] = ["history", "people", "graphs", "reliability", "library"];

// The header badge says what monitoring is doing, not just that a URL and token are saved:
// Activity asks Plex directly and works either way, so a green "connected" used to hide
// that nothing was being recorded.
const BADGE: Record<PlexStatus, { label: string; color: string; soft: string }> = {
  unconfigured: { label: "Not connected", color: "var(--avoid-text)", soft: "var(--avoid-soft)" },
  off: { label: "Connected · not recording", color: "var(--avoid-text)", soft: "var(--avoid-soft)" },
  recording: { label: "Recording", color: "var(--good-text)", soft: "var(--good-soft)" },
  unreachable: { label: "Plex unreachable", color: "var(--reject-text)", soft: "var(--reject-soft)" },
};

// MonitoringBadge is one server's monitoring state. With two servers connected each badge
// carries its server's mark (and name, for screen readers).
function MonitoringBadge({ c, server = "plex", marked }: { c: Pick<PlexConfig, "status" | "last_error" | "last_poll_at">; server?: InsightsServer; marked?: boolean }) {
  const b = BADGE[c.status] ?? BADGE.unconfigured;
  const name = SERVER_LABEL[server];
  const label = c.status === "unreachable" ? `${name} unreachable` : b.label;
  const title = c.status === "unreachable" && c.last_error ? c.last_error
    : c.status === "recording" && c.last_poll_at ? `Last answer from ${name} ${fmtDate(c.last_poll_at)}` : undefined;
  return (
    <span title={title} className="inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-sm font-semibold" style={{ border: `1px solid ${b.color}`, background: b.soft }}>
      <span className="h-2 w-2 rounded-full" style={{ background: b.color }} />
      {marked && c.status !== "unreachable" && <><ServerMark server={server} size={13} /><span className="sr-only">{name}: </span></>}
      {label}
    </span>
  );
}

// MonitoringOff sits above the recorded-plays tabs while a server is connected but its
// monitoring is switched off, so an empty or stale History isn't mistaken for nobody
// watching. With two servers it names the one that's off.
function MonitoringOff({ busy, onTurnOn, server }: { busy: boolean; onTurnOn: () => void; server?: InsightsServer }) {
  return (
    <div role="status" className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-lg px-3.5 py-2 text-sm" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--ink)" }}>
      <span>{server ? `Monitoring is off on ${SERVER_LABEL[server]}` : "Monitoring is off"} — nothing new is recorded.</span>
      <button onClick={onTurnOn} disabled={busy} className="rounded-lg px-3 py-1 text-sm font-semibold disabled:opacity-60" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
        {busy ? "Turning on…" : "Turn on"}
      </button>
    </div>
  );
}

export function Insights() {
  // /insights/people/:id is a person's People page; any other sub-path is Insights itself.
  const rest = useParams()["*"] ?? "";
  const person = /^people\/([^/]+)\/?$/.exec(rest);
  // The old Notifications tab's address (bookmarks, earlier copy) opens Alerts instead.
  const [params] = useSearchParams();
  if (person) return <InsightsPerson id={decodeURIComponent(person[1])} />;
  if (rest) return <Navigate to="/insights" replace />;
  if (params.get("tab") === "notifications") return <Navigate to={LINKS.alerts} replace />;
  // Users was renamed People; old links keep working.
  if (params.get("tab") === "users") return <Navigate to="/insights?tab=people" replace />;
  // The Plex connection moved from the old Settings tab to its card on Settings → Connections.
  if (params.get("tab") === "settings") return <Navigate to={LINKS.plexConnection} replace />;
  return <InsightsPage />;
}

function InsightsPage() {
  const [tab, setTab] = useTabParam(TABS.map((t) => t.key), "activity");
  const [cfg, setCfg] = useState<PlexConfig | null>(null);
  const toast = useToast();
  const say = (m: string, err = false) => toast(m, { tone: err ? "error" : "info", ms: 3500 });

  // cfgDone: the connection settings have answered (or failed). Until then the data tabs
  // wait, so a connected server doesn't flash the "imported history" notice or the
  // setup state while its settings load.
  const [cfgDone, setCfgDone] = useState(false);
  // Re-read every 15 s for the monitoring status, so the badge turns "unreachable" (and
  // back) on its own. The form that edits these settings lives in Settings now, so
  // nothing here is being typed into for a poll to overwrite.
  // The servers list says whether Jellyfin is connected and how its monitoring is going.
  // An older server without the list is Plex only.
  const [servers, setServers] = useState<InsightsServerStatus[]>([]);
  usePoll(() => Promise.all([
    insightsApi.insightsConfig()
      .then(setCfg)
      .catch(() => { if (!cfgDone) say("Could not load Plex settings", true); }),
    insightsApi.insightsServers().then(setServers).catch(() => setServers([])),
  ]).finally(() => setCfgDone(true)), 15000);
  const status = cfg?.status;
  const jellyfin = servers.find((s) => s.server === "jellyfin" && s.configured);
  const plexOn = !!status && status !== "unconfigured";
  const connected = plexOn || !!jellyfin;
  // Both connected: the chips show and every row names its server.
  const multi = plexOn && !!jellyfin;
  const [serverParam, setServer] = useSearchParamState<ServerFilter>("server", "all", { allowed: SERVER_FILTERS });
  // A link to one server while only the other is connected shows everything instead.
  const server = multi ? serverArg(serverParam) : undefined;

  // Turn on is the banner's one-click fix; it saves only the switch (a missing URL keeps
  // the saved one).
  const [turningOn, setTurningOn] = useState(false);
  const turnOn = async (which: InsightsServer) => {
    setTurningOn(true);
    try {
      if (which === "jellyfin") {
        await insightsApi.updateJellyfinMonitoring({ monitoring_enabled: true });
        setServers(await insightsApi.insightsServers());
      } else {
        if (!cfg) return;
        setCfg(await insightsApi.updatePlexMonitoring({ enabled: true }));
      }
      say("Monitoring is on");
    } catch (e) { say((e as Error).message, true); } finally { setTurningOn(false); }
  };
  // The first connected server (in the filter) whose monitoring is off.
  const offServer: InsightsServer | undefined =
    status === "off" && server !== "jellyfin" ? "plex" : jellyfin?.status === "off" && server !== "plex" ? "jellyfin" : undefined;

  return (
    <>
      <PageHeader title="Insights" />
      <Page>
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <p className="max-w-[64ch] text-base text-ink-dim">Watch monitoring for your {multi ? `Plex and ${SERVER_LABEL.jellyfin} servers` : jellyfin ? `${SERVER_LABEL.jellyfin} server` : "Plex server"} — who's streaming what, right now and historically, with stream quality, transcode diagnostics and buffering reliability.
            {!connected && <> <Link to={LINKS.mediaServerCards} className="font-semibold" style={{ color: "var(--accent-text)" }}>Connect Plex or Jellyfin in Settings → Connections</Link> to begin.</>}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            {cfg && (plexOn || !jellyfin) && <MonitoringBadge c={cfg} marked={multi} />}
            {jellyfin && <MonitoringBadge c={jellyfin} server="jellyfin" marked={multi || !plexOn} />}
            {/* Monitoring, library updates, sign-in and linked accounts: one page for both servers. */}
            <Link to={LINKS.mediaServers} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Media server settings →</Link>
          </div>
        </div>

        <Tabs tabs={TABS} value={tab} onChange={setTab} idPrefix="insights" label="Insights sections" />

        {multi && <div className="mb-3"><ServerChips value={serverParam} onChange={setServer} /></div>}

        {offServer && RECORDED_TABS.includes(tab) && <MonitoringOff busy={turningOn} onTurnOn={() => turnOn(offServer)} server={multi || offServer === "jellyfin" ? offServer : undefined} />}

        <InsightsCfg.Provider value={cfg}>
        <InsightsServers.Provider value={{ multi }}>
        <TabPanel idPrefix="insights" value={tab}>
          {!cfgDone ? (
            <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading…</div>
          ) : tab === "activity" ? (
            <ActivityView key={server ?? "all"} connected={!!connected} server={server} />
          ) : tab === "history" ? (
            <HistoryView connected={!!connected} server={server} userLink={(id, name) => <UserLink id={id} name={name} />} />
          ) : tab === "people" ? (
            <UsersView connected={!!connected} server={server} />
          ) : tab === "graphs" ? (
            <GraphsView connected={!!connected} server={server} />
          ) : tab === "library" ? (
            <LibraryView connected={!!connected} server={server} />
          ) : (
            <ReliabilityView connected={!!connected} server={server} jellyfin={!!jellyfin} />
          )}
        </TabPanel>
        </InsightsServers.Provider>
        </InsightsCfg.Provider>
      </Page>
    </>
  );
}

/* ============================= ACTIVITY ============================= */
function ActivityView({ connected, server }: { connected: boolean; server?: InsightsServer }) {
  const [act, setAct] = useState<InsightsActivity | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [detail, setDetail] = useState<InsightsStream | null>(null);
  // Per-session smoothed playback clock. Plex only updates viewOffset every ~10s, so
  // re-baselining to it every poll makes the bar sawtooth. Instead we run our own 1×
  // clock and only re-sync to Plex when it diverges a lot (a real seek/resume) — small
  // drift from Plex's lazy updates is ignored, so the timer counts smoothly.
  const clock = useRef<Record<string, { base: number; at: number }>>({});
  const [, tickNow] = useState(0);

  // The page mounts this afresh for each server filter (its key), so a new filter never
  // shows the other server's streams for a beat.
  usePoll(() => insightsApi.insightsActivity(server).then((a) => { setAct(a); setErr(null); }).catch((e) => { setErr((e as Error).message); }), connected ? 4000 : null);

  // Re-render once a second so playing streams' progress interpolates in real time.
  usePoll(() => tickNow((n) => n + 1), 1000, { immediate: false });

  // Reconcile the smoothed clocks whenever a fresh snapshot arrives.
  useEffect(() => {
    if (!act) return;
    const now = Date.now();
    const SEEK_MS = 15000; // treat a >15s gap vs our clock as a seek, not Plex lag
    const next: Record<string, { base: number; at: number }> = {};
    for (const s of act.streams) {
      const prev = clock.current[s.session_key];
      if (s.state !== "playing" || !prev) {
        next[s.session_key] = { base: s.offset_ms, at: now };
        continue;
      }
      const predicted = prev.base + (now - prev.at);
      next[s.session_key] = Math.abs(s.offset_ms - predicted) > SEEK_MS
        ? { base: s.offset_ms, at: now } // real seek/resume → snap to Plex
        : { base: predicted, at: now };  // keep our smooth clock, ignore Plex's jitter
    }
    clock.current = next; // ended sessions drop out
  }, [act]);

  // The watch statistics come from the database, so they stay on screen whatever the live
  // call does: without a server they sit under the setup state (when there are any), and
  // an unreachable server only replaces the live-streams part.
  if (!connected) {
    return (
      <div className="flex flex-col gap-3.5">
        <SetupState tab="activity" />
        <HomeExtras live={false} server={server} />
      </div>
    );
  }

  // liveOffset reads the smoothed clock: advance while playing, hold otherwise; capped.
  const liveOffset = (s: InsightsStream): number => {
    const c = clock.current[s.session_key];
    const off = c ? (s.state === "playing" ? c.base + (Date.now() - c.at) : c.base) : s.offset_ms;
    return s.duration_ms > 0 ? Math.min(s.duration_ms, off) : off;
  };
  let live: ReactNode;
  if (err) {
    live = <div className="rounded-xl p-6 text-base" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>Couldn’t reach {server ? SERVER_LABEL[server] : "Plex"}: {err}</div>;
  } else if (!act) {
    live = <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading activity…</div>;
  } else {
    const streams = act.streams;
    live = (
      <>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="text-base text-ink-dim">{streams.length ? `${streams.length} stream${streams.length === 1 ? "" : "s"} active` : "Nothing is playing right now."}</div>
          <div className="flex items-center gap-3 font-mono text-xs">
            <BW label="TOTAL" v={act.bandwidth.total_kbps} accent />
            <BW label="LAN" v={act.bandwidth.lan_kbps} />
            <BW label="WAN" v={act.bandwidth.wan_kbps} />
          </div>
        </div>
        {(Object.entries(act.errors ?? {}) as [InsightsServer, string][]).map(([srv, e]) => (
          <div key={srv} role="status" className="rounded-lg px-3.5 py-2 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>
            Couldn’t reach {SERVER_LABEL[srv] ?? srv} — its streams aren’t listed: {e}
          </div>
        ))}
        {streams.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-base text-ink-faint" style={{ border: "1px dashed var(--line)" }}>No active streams.</div>
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(min(360px, 100%), 1fr))" }}>
            {streams.map((s) => <StreamCard key={s.session_key} s={s} offsetMs={liveOffset(s)} onOpen={() => setDetail(s)} />)}
          </div>
        )}
        {!act.geo_active && streams.some((s) => !s.geo.local) && (
          <div className="text-xs text-ink-faint">Tip: drop a MaxMind <code>GeoLite2-City.mmdb</code> into the data dir (or set <code>ARRMADA_GEOIP_DB</code>) to resolve remote IPs to a city.</div>
        )}
      </>
    );
  }
  return (
    <div className="flex flex-col gap-3.5">
      {live}
      <HomeExtras live server={server} />
      {detail && <DeepDive s={detail} onClose={() => setDetail(null)} />}
    </div>
  );
}

/* ============================= HOME DASHBOARD (stats / libraries / recently added) ============================= */
// HomeExtras: watch statistics (from the database) plus library counts and recently added,
// which Plex answers live. Without a server (live=false) the Plex calls are skipped, and the
// block shows only when there are imported statistics to show.
function HomeExtras({ live, server }: { live: boolean; server?: InsightsServer }) {
  const [stats, setStats] = useState<InsightsStats | null>(null);
  const [libs, setLibs] = useState<LibraryStat[] | null>(null);
  const [recent, setRecent] = useState<RecentItem[] | null>(null);
  const [metric, setMetric] = useState<"plays" | "duration">("plays");
  // plexErr: a live call failed. The statistics above come from the database and stay; only
  // these two parts need the server, so they say so in place instead of vanishing.
  const [plexErr, setPlexErr] = useState<string | null>(null);

  useEffect(() => { insightsApi.insightsStats(30, metric, server).then(setStats).catch(() => setStats(null)); }, [metric, server]);
  useEffect(() => {
    if (!live) return;
    const failed = (e: unknown) => setPlexErr((e as Error).message || "no answer");
    insightsApi.insightsLibraries(server).then(setLibs).catch((e) => { setLibs([]); failed(e); });
    insightsApi.insightsRecentlyAdded(20, server).then(setRecent).catch((e) => { setRecent([]); failed(e); });
  }, [live, server]);

  const hasStats = stats && (stats.most_watched_movies.length || stats.most_watched_shows.length || stats.most_active_users.length);
  if (!live && !hasStats) return null;

  return (
    <div className="mt-4 flex flex-col gap-5">
      {/* Watch statistics */}
      <section>
        <div className="mb-2.5 flex items-center justify-between">
          <h3 className="text-md font-bold">Watch statistics <span className="font-normal text-ink-faint">· last 30 days</span></h3>
          <div className="flex gap-1">
            <Chip active={metric === "plays"} onClick={() => setMetric("plays")}>Plays</Chip>
            <Chip active={metric === "duration"} onClick={() => setMetric("duration")}>Duration</Chip>
          </div>
        </div>
        {!hasStats ? (
          <div className="rounded-xl p-6 text-center text-sm text-ink-faint" style={{ border: "1px dashed var(--line)" }}>No watch data yet — statistics build up as people stream.</div>
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(min(300px, 100%), 1fr))" }}>
            <StatCard title="Most watched movies" rows={stats!.most_watched_movies.map((m) => ({ label: m.title, thumb: m.thumb_url, v: metric === "plays" ? m.plays : m.secs }))} metric={metric} />
            <StatCard title="Most watched TV" rows={stats!.most_watched_shows.map((m) => ({ label: m.title, thumb: m.thumb_url, v: metric === "plays" ? m.plays : m.secs }))} metric={metric} />
            <StatCard title="Most active users" rows={stats!.most_active_users.map((u) => ({ label: u.name, to: u.id ? personPath(u.id) : undefined, server: u.server, v: metric === "plays" ? u.plays : u.secs }))} metric={metric} />
            <StatCard title="Most active platforms" rows={stats!.most_active_platforms.map((p) => ({ label: p.name, v: metric === "plays" ? p.plays : p.secs }))} metric={metric} />
          </div>
        )}
      </section>

      {plexErr && (
        <div className="rounded-xl px-4 py-3 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>
          Plex unreachable — library counts and recently added come straight from the server: {plexErr}
        </div>
      )}

      {/* Library statistics */}
      {libs && libs.length > 0 && (
        <section>
          <h3 className="mb-2.5 text-md font-bold">Library statistics</h3>
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(min(220px, 100%), 1fr))" }}>
            {libs.map((l) => (
              <div key={`${l.server ?? "plex"}:${l.title}`} className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
                <div className="font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">{libTypeLabel(l.type)}</div>
                <div className="mt-0.5 flex items-center gap-1.5 text-md font-semibold"><span className="min-w-0 truncate">{l.title}</span><RowServer server={l.server} /></div>
                <div className="mt-1 text-3xl font-extrabold tracking-tight">{l.count.toLocaleString()}</div>
              </div>
            ))}
          </div>
        </section>
      )}

      {/* Recently added */}
      {recent && recent.length > 0 && (
        <section>
          <h3 className="mb-2.5 text-md font-bold">Recently added</h3>
          <div className="flex gap-3 overflow-x-auto pb-2 thin-scroll">
            {recent.map((it, i) => (
              <div key={i} className="flex-none" style={{ width: 96 }}>
                <div className="relative h-[144px] w-[96px] overflow-hidden rounded-lg" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
                  {it.thumb_url ? <img src={it.thumb_url} alt="" loading="lazy" className="h-full w-full object-cover" /> : null}
                </div>
                <div className="mt-1 truncate text-xs font-semibold" title={it.title}>{it.title}</div>
                <div className="truncate text-xs text-ink-faint">{it.subtitle}</div>
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function libTypeLabel(t: string): string {
  return t === "movie" ? "Movies" : t === "show" ? "TV Shows" : t === "artist" ? "Music" : t;
}

function StatCard({ title, rows, metric }: { title: string; rows: { label: string; thumb?: string; to?: string; server?: string; v: number }[]; metric: "plays" | "duration" }) {
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="mb-2 text-base font-bold">{title}</div>
      {rows.length === 0 ? <div className="text-xs text-ink-faint">—</div> : (
        <ol className="flex flex-col gap-1.5">
          {rows.map((r, i) => (
            <li key={i} className="flex items-center gap-2 text-sm">
              <span className="w-3 flex-none font-mono text-xs text-ink-faint">{i + 1}</span>
              {r.thumb !== undefined && <span className="h-7 w-5 flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>{r.thumb ? <img src={r.thumb} alt="" loading="lazy" className="h-full w-full object-cover" /> : null}</span>}
              {r.to ? <Link to={r.to} className="min-w-0 flex-1 truncate hover:underline">{r.label}</Link> : <span className="min-w-0 flex-1 truncate">{r.label}</span>}
              <RowServer server={r.server} />
              <span className="flex-none font-mono text-xs font-semibold" style={{ color: "var(--accent-text)" }}>{metric === "plays" ? r.v : fmtHours(r.v)}</span>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

/* ============================= USERS ============================= */
function UsersView({ connected, server }: { connected: boolean; server?: InsightsServer }) {
  const [users, setUsers] = useState<UserEntry[] | null>(null);
  // Users come from recorded plays, so an import shows here before Plex is connected.
  useEffect(() => { insightsApi.insightsUsers(server).then(setUsers).catch(() => setUsers([])); }, [server]);
  if (!users) return <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading people…</div>;
  if (users.length === 0 && !connected) return <SetupState tab="people" />;
  if (users.length === 0) return <div className="rounded-xl p-10 text-center text-base text-ink-faint" style={{ border: "1px solid var(--line)" }}>Nobody has watched anything yet.</div>;
  return (
    <div className="flex flex-col gap-3">
      {!connected && <ImportedNotice />}
      <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
        <table className="w-full border-collapse text-base" style={{ minWidth: 820 }}>
          <thead><tr style={{ background: "var(--panel-2)" }}>{["User", "Last seen", "Location", "Platform", "Last played", "Plays", "Watch time"].map((h) => <th key={h} className="px-3 py-2 text-left font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">{h}</th>)}</tr></thead>
          <tbody>
            {users.map((u, i) => (
              <tr key={u.id} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                <td className="px-3 py-2 font-semibold">
                  <span className="flex items-center gap-2"><Avatar url={u.avatar_url} name={u.username} /><UserLink id={u.id} name={u.username} /><RowServer server={u.server} /></span>
                </td>
                <td className="whitespace-nowrap px-3 py-2 font-mono text-xs text-ink-dim">{u.last_seen ? fmtDate(u.last_seen) : "never"}</td>
                <td className="px-3 py-2 font-mono text-xs text-ink-dim">{u.last_ip ? geoLabel(u.geo) : "—"}</td>
                <td className="px-3 py-2 text-ink-dim">{u.last_platform || "—"}</td>
                <td className="px-3 py-2 text-ink-dim"><div className="max-w-[240px] truncate">{u.last_title || "—"}</div></td>
                <td className="px-3 py-2 font-mono tabular-nums">{u.total_plays.toLocaleString()}</td>
                <td className="whitespace-nowrap px-3 py-2 font-mono tabular-nums text-ink-dim">{fmtDur(u.total_secs)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function BW({ label, v, accent }: { label: string; v: number; accent?: boolean }) {
  return (
    <span className="inline-flex items-baseline gap-1">
      <span className="text-xs font-bold uppercase tracking-wide text-ink-faint">{label}</span>
      <span style={{ color: accent ? "var(--accent-text)" : "var(--ink-dim)" }}>{fmtMbps(v)}<span className="text-xs text-ink-faint"> Mb/s</span></span>
    </span>
  );
}

function StreamCard({ s, offsetMs, onOpen }: { s: InsightsStream; offsetMs: number; onOpen: () => void }) {
  const d = DECISION[s.decision] ?? DECISION.direct_play;
  const hw = hwBadge(s);
  const buffering = s.state === "buffering";
  const pct = s.duration_ms > 0 ? Math.min(100, (offsetMs * 100) / s.duration_ms) : s.progress_pct;
  return (
    <button onClick={onOpen} className="relative flex gap-3 overflow-hidden rounded-xl p-3 text-left transition-colors" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      {/* The title's background art, faint under a panel-coloured wash so the text keeps its
          contrast in both themes. */}
      {s.art && (
        <>
          <img src={s.art} alt="" aria-hidden loading="lazy" className="pointer-events-none absolute inset-0 h-full w-full object-cover opacity-30" />
          <span aria-hidden className="pointer-events-none absolute inset-0" style={{ background: "linear-gradient(90deg, var(--panel) 35%, color-mix(in srgb, var(--panel) 72%, transparent) 100%)" }} />
        </>
      )}
      <div className="relative h-[104px] w-[70px] flex-none overflow-hidden rounded-md" style={{ background: "var(--panel-2)" }}>
        {s.thumb ? <img src={s.thumb} alt="" className="h-full w-full object-cover" loading="lazy" /> : null}
      </div>
      <div className="relative flex min-w-0 flex-1 flex-col">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex min-w-0 items-center gap-1.5"><span className="truncate text-md font-semibold">{s.title}</span><RowServer server={s.server} /></div>
            <div className="truncate text-xs text-ink-dim">{s.subtitle}</div>
          </div>
          <span className="flex-none rounded-full px-2 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink)" }}>{d.label}</span>
        </div>
        <div className="mt-1 truncate text-xs text-ink-dim">{s.user} · {s.player || s.platform}</div>
        <div className="truncate font-mono text-xs text-ink-faint">
          {geoLabel(s.geo)} · {fmtMbps(s.bandwidth_kbps)} Mb/s
          {hw && <> · <span style={hw.fellBack ? { color: "var(--avoid-text)" } : undefined} title={hw.fellBack ? "Plex fell back to CPU — hardware transcoding was requested but not used" : s.hw_title || undefined}>{hw.label}{hw.fellBack ? " (fell back)" : ""}</span></>}
        </div>
        {/* progress */}
        <div className="mt-auto pt-2">
          <div className="h-1.5 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: buffering ? "var(--avoid)" : "var(--accent)", transition: "width 1s linear" }} />
          </div>
          <div className="mt-1 flex items-center justify-between font-mono text-xs text-ink-faint">
            <span style={{ color: buffering ? "var(--avoid-text)" : undefined }}>{buffering ? "● Buffering" : s.state === "paused" ? "⏸ Paused" : "▶ Playing"}</span>
            <span>{fmtClock(offsetMs)} / {fmtClock(s.duration_ms)}</span>
          </div>
        </div>
      </div>
    </button>
  );
}

function DeepDive({ s, onClose }: { s: InsightsStream; onClose: () => void }) {
  const d = DECISION[s.decision] ?? DECISION.direct_play;
  const hw = hwBadge(s);
  const titleId = useId();
  return (
    <Modal onClose={onClose} labelledBy={titleId} size="md">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 id={titleId} className="m-0 truncate text-lg font-bold">{s.title}</h2>
          <p className="m-0 mt-0.5 flex items-center gap-1.5 text-sm text-ink-dim"><span>{s.subtitle}</span><RowServer server={s.server} /></p>
        </div>
        <IconButton label="Close" onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</IconButton>
      </div>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="rounded-full px-2.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink)" }}>{d.label}</span>
        {hw && <span title={s.hw_title || undefined} className="rounded-full px-2.5 py-0.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{hw.label}{s.hw_title && hw.label !== "CPU" && s.server !== "jellyfin" ? ` · ${s.hw_title}` : ""}</span>}
        {hw?.fellBack && <span className="rounded-full px-2.5 py-0.5 text-xs font-semibold" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>Fell back to CPU</span>}
        {s.throttled && <span className="rounded-full px-2.5 py-0.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Throttled</span>}
      </div>

      {s.reasons && s.reasons.length > 0 && (
        <div className="mb-3 rounded-lg p-3 text-sm" style={{ background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>
          <div className="mb-1 text-xs font-bold uppercase tracking-wide">Why it's transcoding</div>
          <ul className="list-none space-y-0.5 p-0">{s.reasons.map((r, i) => <li key={i}>• {r}</li>)}</ul>
        </div>
      )}

      <div className="grid gap-px overflow-hidden rounded-lg text-sm" style={{ background: "var(--line)" }}>
        <div className="flex items-center justify-between gap-3 px-3 py-2" style={{ background: "var(--panel)" }}>
          <span className="font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">User</span>
          <UserLink id={s.user_id} name={s.user} className="text-right text-ink-dim" />
        </div>
        <DDRow label="Player" a={`${s.player || "—"} · ${s.platform} · ${s.product}`} />
        <DDRow label="Location" a={`${geoLabel(s.geo)}${s.ip ? ` · ${s.ip}` : ""} · ${s.location?.toUpperCase() || "—"}`} />
        <DDRow label="Bandwidth" a={`${fmtMbps(s.bandwidth_kbps)} Mb/s`} />
        <DDRow label="Video" a={s.video.src} b={s.video.stream} />
        <DDRow label="Audio" a={s.audio.src} b={s.audio.stream} />
        <DDRow label="Container" a={s.container.src} b={s.container.stream} />
        <DDRow label="Progress" a={`${fmtClock(s.offset_ms)} / ${fmtClock(s.duration_ms)} (${s.progress_pct}%)`} />
      </div>
    </Modal>
  );
}

function DDRow({ label, a, b }: { label: string; a: string; b?: string }) {
  return (
    <div className="flex items-center justify-between gap-3 px-3 py-2" style={{ background: "var(--panel)" }}>
      <span className="font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">{label}</span>
      <span className="text-right text-ink-dim">
        {a}{b ? <span style={{ color: "var(--avoid-text)" }}> → {b}</span> : null}
      </span>
    </div>
  );
}

/* ============================= HISTORY ============================= */
/* ============================= GRAPHS ============================= */
const SERIES = {
  tv: { label: "TV", color: "var(--accent-text)" },
  movies: { label: "Movies", color: "var(--good-text)", dash: "6 3" },
  music: { label: "Music", color: "var(--avoid-text)", dash: "2 3" },
};
// Stream types keep the decision colours; the dashes tell them apart for readers who
// can't separate those hues.
const STREAM_SERIES = {
  direct_play: { label: "Direct Play", color: DECISION.direct_play.color, dash: "6 3" },
  direct_stream: { label: "Direct Stream", color: DECISION.direct_stream.color },
  transcode: { label: "Transcode", color: DECISION.transcode.color, dash: "2 3" },
};
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const DOW_LONG = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const WINDOWS = [7, 30, 90] as const;
const METRICS = ["plays", "duration"] as const;

function GraphsView({ connected, server }: { connected: boolean; server?: InsightsServer }) {
  const [g, setG] = useState<InsightsGraphs | null>(null);
  const [failed, setFailed] = useState(false);
  // The window and the metric live in the address, so a refresh or a shared link keeps them.
  const [winParam, setWin] = useSearchParamState<string>("window", "30", { allowed: WINDOWS.map(String) });
  const [metric, setMetric] = useSearchParamState<Metric>("metric", "plays", { allowed: METRICS });
  const win = Number(winParam) || 30;
  // Graphs are drawn from recorded plays, so imported history charts before Plex is connected.
  useEffect(() => {
    let live = true;
    insightsApi.insightsGraphs(win, metric, server).then((r) => { if (live) { setG(r); setFailed(false); } }).catch(() => { if (live) setFailed(true); });
    return () => { live = false; };
  }, [win, metric, server]);
  if (failed && !g) return <div className="rounded-xl p-10 text-center text-base" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>Couldn't load the graphs.</div>;
  if (!g) return <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading graphs…</div>;

  const hours = g.metric === "duration";
  const fmt = hours ? fmtHoursValue : (v: number) => String(Math.round(v));
  const unit = hours ? "Hours watched" : "Plays";
  const anyPlays = g.daily_tv.some(Boolean) || g.daily_movies.some(Boolean) || g.daily_music.some(Boolean);
  const dayLabels = g.days.map((d) => dayLabel(d));
  const dayTips = g.days.map((d) => dayLabel(d, true));
  // Nothing to chart and no server to record from. The window chips stay above it, so an
  // older import is still one click away.
  const nothing = !connected && !anyPlays && g.bandwidth.length === 0;
  const rank = (rows: NameStat[]) => rows.map((r) => ({ label: r.name, value: hours ? r.secs / 3600 : r.plays }));

  return (
    <div className="flex flex-col gap-4">
      {!connected && !nothing && <ImportedNotice />}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <FilterGroup label="Window">{WINDOWS.map((w) => <Chip key={w} active={win === w} onClick={() => setWin(String(w))}>{w}d</Chip>)}</FilterGroup>
        <FilterGroup label="Show">
          <Chip active={!hours} onClick={() => setMetric("plays")}>Plays</Chip>
          <Chip active={hours} onClick={() => setMetric("duration")}>Hours</Chip>
        </FilterGroup>
      </div>

      {nothing ? (
        <SetupState tab="graphs" />
      ) : !anyPlays ? (
        <div className="rounded-xl p-10 text-center text-sm text-ink-faint" style={{ border: "1px dashed var(--line)" }}>No plays in this window yet — graphs fill in as history accrues.</div>
      ) : (
        <>
          <ChartCard title={`Daily ${hours ? "hours watched" : "plays"} by media type`} extra={<Legend items={[SERIES.tv, SERIES.movies, SERIES.music]} />}>
            <LineChart caption={`Daily ${unit.toLowerCase()} by media type`} xLabels={dayLabels} tipTitles={dayTips} fmt={fmt} series={[
              { ...SERIES.tv, values: g.daily_tv },
              { ...SERIES.movies, values: g.daily_movies },
              { ...SERIES.music, values: g.daily_music },
            ]} />
          </ChartCard>

          <ChartCard title="Stream type over time" extra={<Legend items={[STREAM_SERIES.direct_play, STREAM_SERIES.direct_stream, STREAM_SERIES.transcode]} />}>
            <LineChart caption={`Daily ${unit.toLowerCase()} by stream type`} xLabels={dayLabels} tipTitles={dayTips} fmt={fmt} series={[
              { ...STREAM_SERIES.direct_play, values: g.daily_direct_play ?? [] },
              { ...STREAM_SERIES.direct_stream, values: g.daily_direct_stream ?? [] },
              { ...STREAM_SERIES.transcode, values: g.daily_transcode ?? [] },
            ]} />
          </ChartCard>

          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(320px, 100%), 1fr))" }}>
            <ChartCard title={`${unit} by day of week`}><BarChart caption={`${unit} by day of week`} values={g.by_day_of_week} labels={DOW} tipTitles={DOW_LONG} fmt={fmt} /></ChartCard>
            <ChartCard title={`${unit} by hour of day`}><BarChart caption={`${unit} by hour of day`} values={g.by_hour} fmt={fmt}
              labels={g.by_hour.map((_, i) => (i % 3 === 0 ? String(i).padStart(2, "0") : ""))} tipTitles={g.by_hour.map((_, i) => `${String(i).padStart(2, "0")}:00–${String(i).padStart(2, "0")}:59`)} /></ChartCard>
            <ChartCard title="Top platforms"><HBarChart rows={rank(g.top_platforms)} fmt={fmt} /></ChartCard>
            <ChartCard title="Top users"><HBarChart rows={rank(g.top_users).map((r, i) => ({ ...r, to: g.top_users[i].id ? personPath(g.top_users[i].id) : undefined }))} fmt={fmt} /></ChartCard>
          </div>
        </>
      )}

      {!nothing && <ChartCard title="Bandwidth (peak per hour)" extra={g.bandwidth.length > 0 ? <Legend items={[{ label: "Total", color: "var(--accent-text)" }, { label: "LAN", color: "var(--good-text)", dash: "6 3" }, { label: "WAN", color: "var(--avoid-text)", dash: "2 3" }]} /> : undefined}>
        {g.bandwidth.length === 0 ? <div className="py-6 text-center text-xs text-ink-faint">No bandwidth recorded yet.</div> : (
          <LineChart caption="Peak bandwidth per hour, Mb/s" xLabels={bandwidthLabels(g.bandwidth.map((p) => p.t))} tipTitles={g.bandwidth.map((p) => `${dayLabel(p.t.slice(0, 10), true)} ${p.t.slice(11)}`)}
            fmt={(v) => `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} Mb/s`} series={[
              { label: "Total", color: "var(--accent-text)", values: g.bandwidth.map((p) => p.total_kbps / 1000) },
              { label: "LAN", color: "var(--good-text)", dash: "6 3", values: g.bandwidth.map((p) => p.lan_kbps / 1000) },
              { label: "WAN", color: "var(--avoid-text)", dash: "2 3", values: g.bandwidth.map((p) => p.wan_kbps / 1000) },
            ]} />
        )}
      </ChartCard>}
    </div>
  );
}

/* ============================= RELIABILITY (buffering history) ============================= */
function ReliabilityView({ connected, server, jellyfin }: { connected: boolean; server?: InsightsServer; jellyfin: boolean }) {
  const [r, setR] = useState<Reliability | null>(null);
  const [win, setWin] = useState(30);
  // Stalls are read from recorded sessions, so imported history shows before Plex is connected.
  useEffect(() => { insightsApi.insightsReliability(win, server).then(setR).catch(() => setR(null)); }, [win, server]);
  if (!r) return <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading reliability…</div>;

  const chips = <div className="flex gap-1">{[7, 30, 90].map((w) => <Chip key={w} active={win === w} onClick={() => setWin(w)}>{w}d</Chip>)}</div>;
  // Jellyfin reports no buffering: its plays are left out, and the tab says so.
  const jfNote = (jellyfin || (r.jellyfin_plays ?? 0) > 0) && <JellyfinNoBuffering plays={r.jellyfin_plays} />;
  if (server === "jellyfin") {
    return (
      <div className="flex flex-col gap-4">
        <div className="flex justify-end">{chips}</div>
        {jfNote || <JellyfinNoBuffering plays={r.jellyfin_plays} />}
      </div>
    );
  }

  const s = r.summary;
  const clean = s.total_events === 0;
  // No sessions in the window and no server to record more: the setup state, with the
  // window chips kept above it so an older import is still reachable.
  const nothing = !connected && s.total_sessions === 0;
  const rateColor = s.buffer_rate_pct === 0 ? "var(--good)" : s.buffer_rate_pct < 10 ? "var(--avoid)" : "var(--reject)";

  return (
    <div className="flex flex-col gap-4">
      {!connected && !nothing && <ImportedNotice />}
      <div className="flex items-center justify-between">
        <p className="max-w-[54ch] text-sm text-ink-dim">Where and when streams stalled mid-playback — sampled every few seconds, so treat counts as observed lower bounds. Startup, seeking and resume refills aren't counted.</p>
        {chips}
      </div>
      {jfNote}

      {/* Summary tiles */}
      {!nothing && <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(180px, 100%), 1fr))" }}>
        <StatTile label="Observed stall time" value={fmtStall(s.total_stall_ms)} sub="across the window" color={s.total_stall_ms ? "var(--avoid-text)" : "var(--good-text)"} />
        <StatTile label="Stall rate" value={`${s.buffer_rate_pct}%`} sub={`${s.buffered_sessions} of ${s.total_sessions} streams`} color={rateColor} />
        <StatTile label="Observed stalls" value={s.total_events.toLocaleString()} sub="sampled events" color={s.total_events ? "var(--avoid-text)" : "var(--good-text)"} />
      </div>}

      {nothing ? (
        <SetupState tab="reliability" />
      ) : clean ? (
        <div className="rounded-xl p-12 text-center" style={{ border: "1px dashed var(--line)", background: "var(--panel)" }}>
          <div className="text-3xl">✓</div>
          <div className="mt-1 text-md font-bold" style={{ color: "var(--good-text)" }}>Smooth sailing</div>
          <p className="mx-auto mt-1 max-w-[46ch] text-sm text-ink-dim">No mid-playback stalls observed in this window. When a stream stalls while playing (startup, seeks and resume refills don't count), it's logged here with who, what, and how it was playing — so you can pinpoint heavy transcodes or shaky connections.</p>
        </div>
      ) : (
        <>
          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(300px, 100%), 1fr))" }}>
            <OffenderCard title="Worst-hit users" rows={r.by_user} people />
            <OffenderCard title="Worst-hit platforms" rows={r.by_platform} />
            <OffenderCard title="Worst-hit titles" rows={r.by_title} />
          </div>

          {/* Why streams buffered — diagnosed cause breakdown */}
          {r.causes.length > 0 && (
            <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
              <div className="mb-2 text-base font-bold">Why streams stalled</div>
              <div className="flex flex-wrap gap-2">
                {r.causes.map((c) => {
                  const cc = CAUSE[c.cause] ?? CAUSE.unknown;
                  return (
                    <span key={c.cause} className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-semibold" style={{ background: "var(--panel-2)", border: `1px solid ${cc.color}` }}>
                      <span className="inline-block h-2 w-2 rounded-full" style={{ background: cc.color }} />
                      {cc.label}<span className="font-mono text-ink-faint">{c.stall_ms > 0 ? fmtStall(c.stall_ms) : c.count}</span>
                    </span>
                  );
                })}
              </div>
            </div>
          )}

          {/* Timeline */}
          <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
            <div className="mb-2 text-base font-bold">Recent observed stalls</div>
            <div className="flex flex-col">
              {r.events.map((e, i) => {
                const d = DECISION[e.decision] ?? DECISION.direct_play;
                const cc = CAUSE[e.cause] ?? CAUSE.unknown;
                return (
                  <div key={i} className="py-1.5 text-sm" style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
                      <span className="w-2 flex-none"><span className="inline-block h-2 w-2 rounded-full" style={{ background: cc.color }} /></span>
                      <span className="w-[92px] flex-none font-mono text-xs text-ink-faint">{fmtDate(e.at)}</span>
                      <span className="min-w-[140px] flex-1 truncate"><UserLink id={e.user_id} name={e.user} className="font-semibold" /> · {e.title}</span>
                      <span className="flex-none font-mono text-xs text-ink-faint">@ {fmtClock(e.offset_ms)}</span>
                      {e.duration_ms > 0 && <span className="flex-none font-mono text-xs font-semibold" style={{ color: "var(--avoid-text)" }}>{fmtStall(e.duration_ms)}</span>}
                      <span className="flex-none rounded-full px-2 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink)" }}>{d.label}</span>
                    </div>
                    {e.detail && <div className="pl-5 text-xs sm:pl-[112px]" style={{ color: cc.color }}>{e.detail}</div>}
                  </div>
                );
              })}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function OffenderCard({ title, rows, people }: { title: string; rows: BufferGroup[]; people?: boolean }) {
  // Rank/scale by observed stall time; legacy rows (pre-duration data) fall back to counts.
  const anyStall = rows.some((r) => r.stall_ms > 0);
  const metric = (r: BufferGroup) => (anyStall ? r.stall_ms : r.events);
  const max = Math.max(1, ...rows.map(metric));
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="mb-2 text-base font-bold">{title}</div>
      {rows.length === 0 ? <div className="text-xs text-ink-faint">—</div> : (
        <div className="flex flex-col gap-2">
          {rows.map((r, i) => (
            <div key={i} className="text-sm">
              <div className="flex items-center justify-between gap-2">
                <span className="min-w-0 flex-1 truncate" title={r.name}>{people ? <UserLink id={r.id} name={r.name} /> : r.name || "—"}</span>
                <span className="flex-none font-mono text-ink-dim">
                  {r.stall_ms > 0 ? `${fmtStall(r.stall_ms)} · ` : ""}{r.events} stall{r.events === 1 ? "" : "s"} · {r.rate_pct}%
                </span>
              </div>
              <span className="mt-0.5 block h-1.5 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
                <span className="block h-full rounded-full" style={{ width: `${(metric(r) / max) * 100}%`, background: "var(--avoid)" }} />
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
