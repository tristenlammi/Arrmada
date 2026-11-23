import { createContext, useContext, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ServerBadge, ServerMark, SERVER_LABEL, isMediaServer } from "../../components/brand";
import type { GeoLocation, InsightsServer, PlexConfig } from "../../lib/api";
import { LINKS } from "../../lib/links";
import { EmptyState } from "../../ui/EmptyState";

// Pieces every Insights view shares: the stream-decision and buffer-cause vocabularies,
// number and date formats, and the small filter chips.

export const DECISION: Record<string, { label: string; color: string }> = {
  direct_play: { label: "Direct Play", color: "var(--good-text)" },
  direct_stream: { label: "Direct Stream", color: "var(--accent-text)" },
  transcode: { label: "Transcode", color: "var(--avoid-text)" },
};

// Diagnosed buffer-cause styling (keys match BufferEvent.cause / CauseCount.cause).
export const CAUSE: Record<string, { label: string; color: string }> = {
  transcode: { label: "Transcode overloaded", color: "var(--reject-text)" },
  transcode_cpu: { label: "CPU transcode (no HW)", color: "var(--reject-text)" },
  transcode_fallback: { label: "Plex fell back to CPU", color: "var(--reject-text)" },
  bandwidth: { label: "Bandwidth / network", color: "var(--avoid-text)" },
  unknown: { label: "Inconclusive", color: "var(--ink-faint)" },
};

// InsightsCfg hands the latest Plex settings to deep components (History's HW badge)
// without threading a prop through every view.
export const InsightsCfg = createContext<PlexConfig | null>(null);

// InsightsServers says which media servers are in play: multi when Plex and Jellyfin are
// both connected (then every row names its server and the server chips show).
export const InsightsServers = createContext<{ multi: boolean }>({ multi: false });

// The server filter: "all" in the address is both servers (the API's ?server= absent).
export const SERVER_FILTERS = ["all", "plex", "jellyfin"] as const;
export type ServerFilter = (typeof SERVER_FILTERS)[number];
export function serverArg(f: ServerFilter): InsightsServer | undefined {
  return f === "all" ? undefined : f;
}

// ServerChips is the "Server: All · Plex · Jellyfin" filter, shown while both are connected.
export function ServerChips({ value, onChange }: { value: ServerFilter; onChange: (v: ServerFilter) => void }) {
  return (
    <FilterGroup label="Server">
      {SERVER_FILTERS.map((s) => (
        <Chip key={s} active={value === s} onClick={() => onChange(s)}>
          {s === "all" ? "All" : <span className="inline-flex items-center gap-1"><ServerMark server={s} size={11} />{SERVER_LABEL[s]}</span>}
        </Chip>
      ))}
    </FilterGroup>
  );
}

// RowServer is the server badge on a stream, play or person. With only Plex connected the
// page looks as it always did; a Jellyfin row always says so.
export function RowServer({ server, className }: { server?: string | null; className?: string }) {
  const { multi } = useContext(InsightsServers);
  if (!isMediaServer(server) || (!multi && server !== "jellyfin")) return null;
  return <ServerBadge server={server} className={className} />;
}

// JellyfinNoBuffering is the Reliability note: Jellyfin plays are left out of every rate.
export function JellyfinNoBuffering({ plays }: { plays?: number }) {
  return (
    <div role="note" className="flex flex-wrap items-center gap-2 rounded-lg px-3.5 py-2 text-sm text-ink-dim" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <ServerBadge server="jellyfin" />
      <span>{SERVER_LABEL.jellyfin} doesn't report buffering, so {plays ? `its ${plays.toLocaleString()} play${plays === 1 ? " in this window isn't" : "s in this window aren't"}` : "its plays aren't"} counted here.</span>
    </div>
  );
}

export function geoLabel(g: GeoLocation): string {
  if (g.local) return "Local";
  if (g.city && g.country_code) return `${g.city}, ${g.country_code}`;
  if (g.country) return g.country;
  return g.ip || "—";
}

export function fmtMbps(kbps: number): string {
  if (!kbps) return "0";
  const mb = kbps / 1000;
  return mb >= 10 ? mb.toFixed(0) : mb.toFixed(1);
}

export function fmtClock(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), ss = s % 60;
  const mm = String(m).padStart(2, "0"), sss = String(ss).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${sss}` : `${m}:${sss}`;
}

// fmtHours renders seconds compactly for ranked lists ("12h", "1.5h", "40m").
export function fmtHours(secs: number): string {
  if (!secs) return "0";
  const h = secs / 3600;
  return h >= 10 ? `${h.toFixed(0)}h` : h >= 1 ? `${h.toFixed(1)}h` : `${Math.round(secs / 60)}m`;
}

// fmtHoursValue renders an hours figure (a chart value) the same way.
export function fmtHoursValue(h: number): string {
  return fmtHours(h * 3600);
}

export function fmtDate(epoch: number): string {
  if (!epoch) return "—";
  const d = new Date(epoch * 1000);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) + " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function fmtDay(epoch: number): string {
  if (!epoch) return "—";
  return new Date(epoch * 1000).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

export function fmtDur(secs: number): string {
  if (!secs) return "—";
  const h = Math.floor(secs / 3600), m = Math.round((secs % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

// dayLabel turns "2026-10-09" into "Oct 9" (local; no time zone shift).
export function dayLabel(d: string, long = false): string {
  const [y, m, day] = d.split("-").map(Number);
  return new Date(y, m - 1, day).toLocaleDateString(undefined, long ? { weekday: "short", month: "short", day: "numeric" } : { month: "short", day: "numeric" });
}

// bandwidthLabels puts the date on the first bucket of each day and leaves the rest
// blank; bucket times are "YYYY-MM-DD HH:00" (local).
export function bandwidthLabels(ts: string[]): string[] {
  let last = "";
  return ts.map((t) => {
    const d = t.slice(0, 10);
    if (d === last) return "";
    last = d;
    return dayLabel(d);
  });
}

// fmtStall renders observed stall milliseconds compactly ("47s", "3m 12s").
export function fmtStall(ms: number): string {
  const secs = Math.round(ms / 1000);
  if (secs < 60) return `${secs}s`;
  return `${Math.floor(secs / 60)}m ${secs % 60}s`;
}

export function FilterGroup({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="font-mono text-2xs font-bold uppercase tracking-[0.09em] text-ink-faint">{label}</span>
      <div className="flex flex-wrap gap-1">{children}</div>
    </div>
  );
}

export function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-pressed={active} onClick={onClick} className="rounded-full px-2.5 py-1 text-xs font-semibold transition-colors" style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "var(--panel-2)", color: active ? "var(--accent-text)" : "var(--ink-dim)" }}>
      {children}
    </button>
  );
}

export function StatTile({ label, value, sub, color }: { label: string; value: string; sub: string; color?: string }) {
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">{label}</div>
      <div className="mt-1 text-3xl font-extrabold tracking-tight" style={color ? { color } : undefined}>{value}</div>
      <div className="text-xs text-ink-faint">{sub}</div>
    </div>
  );
}

// personPath is a Plex user's People page.
export function personPath(id: string): string {
  return `/insights/people/${encodeURIComponent(id)}`;
}

// UserLink is a person's name, linking to their People page (plain text without an id).
// It stops the click there, so a link inside a clickable row opens the person, not the row.
export function UserLink({ id, name, className = "" }: { id?: string; name: string; className?: string }) {
  if (!id) return <span className={className}>{name || "—"}</span>;
  return (
    <Link to={personPath(id)} onClick={(e) => e.stopPropagation()} className={`hover:underline ${className}`}>{name || "Someone"}</Link>
  );
}

// Avatar is a Plex account picture (a plex.tv address, or nothing: the initial stands in).
// It's fetched without a referrer, so plex.tv isn't told which Insights page asked.
export function Avatar({ url, name, size = 28 }: { url?: string; name: string; size?: number }) {
  return (
    <span className={`inline-grid flex-none place-items-center overflow-hidden rounded-full font-semibold text-ink-dim ${size >= 48 ? "text-2xl" : "text-xs"}`} aria-hidden
      style={{ width: size, height: size, background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      {url ? <img src={url} alt="" loading="lazy" referrerPolicy="no-referrer" className="h-full w-full object-cover" /> : (name || "?").slice(0, 1).toUpperCase()}
    </span>
  );
}

// METRIC is how the Graphs (and the People page) count: plays, or hours watched.
export type Metric = "plays" | "duration";

const ABOUT = {
  activity: "Live now-playing — who's streaming what, on which device, with progress, transcode decision, bandwidth and geolocation.",
  history: "Every play recorded — a filterable table with stream-type, geolocated IP and a click-through deep-dive.",
  people: "Per-person activity — last seen, platform, total plays and watch time.",
  graphs: "Plays by day, hour, platform and user, plus bandwidth over time.",
  reliability: "The buffering view — see historically when and where streams choked, by user, platform and title.",
  library: "Your library by what's watched — most watched, never watched, and the titles your media server keeps transcoding, linked to Convert.",
};

// SetupState stands in for a tab only while no media server is set up and the database has
// nothing for it yet (an imported Tautulli history shows without a server). Every tab is
// built, so it says what the tab will show and links to the media server cards.
export function SetupState({ tab }: { tab: keyof typeof ABOUT }) {
  return (
    <EmptyState
      title={<span className="capitalize">{tab}</span>}
      body={<p className="mx-auto max-w-[52ch] text-sm">{ABOUT[tab]}</p>}
      action={<Link to={LINKS.mediaServerCards} className="rounded-lg px-3.5 py-2 text-base font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Connect Plex or Jellyfin →</Link>}
    />
  );
}

// ImportedNotice sits above data a tab already has while no server is connected — plays
// imported from Tautulli, or recorded before the connection was removed. The data is real;
// it just won't grow until a server is connected.
export function ImportedNotice() {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg px-3.5 py-2 text-sm text-ink-dim" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <span>Showing history already recorded — connect Plex or Jellyfin to record new plays.</span>
      <Link to={LINKS.mediaServerCards} className="font-semibold" style={{ color: "var(--accent-text)" }}>Connect Plex or Jellyfin →</Link>
    </div>
  );
}
