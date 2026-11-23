import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { GettingStarted } from "../components/GettingStarted";
import {
  api,
  type Status,
  type Health,
  type DashboardData,
  type DashboardMusic,
  type DashboardNewRelease,
  type StorageVolume,
  type ActivityEvent,
  type InsightsStream,
  type NowListening,
} from "../lib/api";
import { LINKS } from "../lib/links";
import { eventHref } from "../lib/events";
import { isAdmin, useMe } from "../lib/me";
import { useQuery } from "../lib/query";
import { backupAge, versionLabel } from "../lib/systemStatus";
import { needsYouRows } from "../lib/needsYou";
import { useAttention } from "../lib/useAttention";
import { useLiveConnected } from "../lib/live";
import { useLiveRefresh } from "../lib/useLiveQuery";
import { QUEUE_CHANGED } from "../lib/queueLive";
import { hwBadge } from "../lib/plexHw";
import { SERVER_LABEL, ServerBadge, isMediaServer } from "../components/brand";
import { Page } from "../ui/Page";

// The dashboard fans out over Plex, the download client and the disks, so it isn't
// free — but it's the page people leave open. A grab, an import, a scan or a stream
// starting refreshes it at once; otherwise every 15 s keeps Plex's now-playing and the
// transfer speeds honest (Plex has no event for a stream's progress).
const REFRESH_MS = 15_000;
const REFRESH_TOPICS = [QUEUE_CHANGED, "release.grabbed", "*.imported", "library.scanned", "plex.stream.started"];

export function Dashboard() {
  const [status, setStatus] = useState<Status | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [data, setData] = useState<DashboardData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const connected = useLiveConnected();
  // The last backup is on the admin-only System → Status; managers don't see the stat.
  const { user } = useMe();
  const admin = isAdmin(user);
  const { data: sys } = useQuery("system:status", api.systemStatus, { enabled: admin, staleMs: 60_000 });
  const backup = sys ? backupAge(sys.last_backup?.at) : null;

  useEffect(() => {
    Promise.all([api.status(), api.health()])
      .then(([s, h]) => {
        setStatus(s);
        setHealth(h);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, []);

  // Health problems are part of the Needs-you card, which the staff layout keeps fresh; this
  // poll is only the dashboard's own data. A hidden tab doesn't poll, and coming back to it
  // refreshes at once.
  const alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useLiveRefresh(() => api.dashboard().then((d) => alive.current && setData(d)).catch(() => {}), {
    topics: REFRESH_TOPICS, fallbackMs: REFRESH_MS, debounceMs: 1000, immediate: true,
  });

  const dbOK = health?.checks?.database === "ok";
  const streams = data?.streams?.streams ?? [];
  // Which media servers Now playing covers: Plex (the default), Jellyfin, or both.
  const bothServers = !!data?.plex_configured && !!data?.jellyfin_configured;
  const serversLabel = bothServers ? `Plex & ${SERVER_LABEL.jellyfin}` : data?.jellyfin_configured && !data?.plex_configured ? SERVER_LABEL.jellyfin : "Plex";
  const listening = data?.listening ?? [];
  const lib = data?.library;
  // Music's part: the server sends it only while the Music module is on.
  const music = data?.music;

  return (
    <>
      <PageHeader title="Dashboard" />
      <Page>
        {/* A fresh install's steps to the first download, until they're done or dismissed. */}
        {admin && <GettingStarted />}

        {/* What's waiting on a person comes first: requests, held imports, download
            problems, stuck searches and health problems, one line each. */}
        <NeedsYou />

        {error && (
          <div
            className="mb-5 rounded-lg px-3.5 py-2.5 text-base"
            style={{ background: "var(--reject-soft)", border: "1px solid var(--reject)", color: "var(--reject-text)" }}
          >
            Backend unreachable: {error}
          </div>
        )}

        {/* Now playing — the thing you actually want to see when you open the page. The media
            servers (Plex, Jellyfin: one list, each stream with its server) and the audiobook
            server are separate sections: different people, different apps. */}
        <SectionLabel
          right={
            streams.length > 0 && data?.streams
              ? `${mbps(data.streams.bandwidth.total_kbps)} total`
              : undefined
          }
        >
          Now playing · {serversLabel}{streams.length > 0 ? ` · ${streams.length}` : ""}
        </SectionLabel>
        {streams.length === 0 ? (
          <Card>
            <p className="m-0 text-base text-ink-faint">
              {!data ? (
                "Loading…"
              ) : !data.plex_configured && !data.jellyfin_configured ? (
                <>
                  No media server is connected yet.{" "}
                  <Link to={LINKS.mediaServerCards} className="font-semibold" style={{ color: "var(--accent-text)" }}>
                    Connect Plex or Jellyfin
                  </Link>
                </>
              ) : data.streams_note ? (
                bothServers ? `Not every server answered — ${data.streams_note}` : `${serversLabel} isn't reachable — ${data.streams_note}`
              ) : (
                "Nothing is streaming right now."
              )}
            </p>
          </Card>
        ) : (
          <>
            <div className="grid gap-2.5 sm:grid-cols-2">
              {streams.map((s) => (
                <StreamCard key={s.session_key} s={s} badge={bothServers || s.server === "jellyfin"} />
              ))}
            </div>
            {data?.streams_note && <p className="m-0 mt-1.5 text-sm text-ink-faint">Not every server answered — {data.streams_note}</p>}
          </>
        )}

        <SectionLabel right={listening.some((l) => !l.mine) ? "who and for how long — never which book" : undefined}>
          Now listening · Audiobooks{listening.length > 0 ? ` · ${listening.length}` : ""}
        </SectionLabel>
        {listening.length === 0 ? (
          <Card>
            <p className="m-0 text-base text-ink-faint">
              {!data
                ? "Loading…"
                : data.audio_off
                  ? "The audiobook server is off."
                  : "No one is listening to an audiobook right now."}
            </p>
          </Card>
        ) : (
          <div className="grid gap-2.5 sm:grid-cols-2">
            {listening.map((l) => (
              <ListenCard key={`${l.user}-${l.started_at}-${l.device}`} l={l} />
            ))}
          </div>
        )}

        {/* Storage — one bar per filesystem, not per folder. */}
        <SectionLabel>Storage</SectionLabel>
        <Card>
          {!data ? (
            <p className="m-0 text-base text-ink-faint">Reading disks…</p>
          ) : !data.storage || data.storage.length === 0 ? (
            <p className="m-0 text-base text-ink-faint">
              Disk usage isn&apos;t available on this platform.
            </p>
          ) : (
            <div className="flex flex-col gap-4">
              {data.storage.map((v) => (
                <StorageBar key={v.path} v={v} />
              ))}
            </div>
          )}
        </Card>

        {/* Library + transfers. */}
        <SectionLabel>Library</SectionLabel>
        <div className={`grid grid-cols-2 gap-2.5 ${music ? "sm:grid-cols-3 lg:grid-cols-5" : "sm:grid-cols-4"}`}>
          <Tile
            to="/movies"
            label="Movies"
            value={lib ? lib.movies : "—"}
            sub={lib && lib.movies_missing > 0 ? `${lib.movies_missing} missing` : "complete"}
            warn={!!lib && lib.movies_missing > 0}
          />
          <Tile
            to="/series"
            label="Episodes"
            value={lib ? lib.episodes : "—"}
            sub={
              lib
                ? lib.episodes_missing > 0
                  ? `${lib.episodes_missing} missing · ${lib.series} shows`
                  : `${lib.series} shows`
                : ""
            }
            warn={!!lib && lib.episodes_missing > 0}
          />
          <Tile
            to="/books"
            label="Books"
            value={lib ? lib.books : "—"}
            sub={lib && lib.books_missing > 0 ? `${lib.books_missing} missing` : "complete"}
            warn={!!lib && lib.books_missing > 0}
          />
          {music && (
            <Tile to="/music" label="Albums" value={music.albums} sub={musicTileSub(music)} warn={music.albums_missing > 0} />
          )}
          <Tile
            className={music ? "col-span-2 sm:col-span-1" : undefined}
            to={LINKS.activityQueue}
            label="Downloading"
            value={data && !data.queue_note ? data.queue.downloading : "—"}
            sub={data ? downloadingSub(data) : ""}
            title={data?.queue_note}
            warn={!!data && (!!data.queue_note || data.queue.errored > 0 || (data.queue.stalled ?? 0) > 0)}
          />
        </div>

        {/* Music, while the module is on: its numbers, what the refresh found lately and what
            was imported (Recent activity mixes every library, so imports can be buried). */}
        {music && (
          <>
            <SectionLabel link={{ to: "/music", label: "Music →" }}>Music</SectionLabel>
            <MusicCard m={music} />
          </>
        )}

        {/* What Arrmada has been doing — the newest of Activity → History. */}
        <SectionLabel link={{ to: LINKS.history, label: "See all →" }}>Recent activity</SectionLabel>
        <Card>
          {!data ? (
            <p className="m-0 text-base text-ink-faint">Loading…</p>
          ) : data.activity.length === 0 ? (
            <p className="m-0 text-base text-ink-faint">
              Nothing yet — grabs, imports and upgrades show up here as they happen.
            </p>
          ) : (
            <ul className="m-0 flex list-none flex-col gap-0 p-0">
              {data.activity.map((e, i) => (
                <ActivityRow key={`${e.kind}-${e.id}-${e.at_ms}-${i}`} e={e} />
              ))}
            </ul>
          )}
        </Card>

        {/* System, demoted to the bottom: it's reassurance, not information. */}
        <SectionLabel>System</SectionLabel>
        <Card>
          {/* No Auth stat: sign-in is always enforced, so it could only ever say "enabled". */}
          <dl className={`grid grid-cols-2 gap-x-6 gap-y-3 font-mono text-sm text-ink-dim ${admin ? "sm:grid-cols-5" : "sm:grid-cols-4"}`}>
            <Stat k="Version" v={status ? versionLabel(status.version, status.commit) : "—"} />
            <Stat k="Uptime" v={status ? uptime(status.uptime_seconds) : "—"} />
            <Stat k="Database" v={dbOK ? "ok" : "down"} tone={dbOK ? "good" : "bad"} />
            <Stat k="Realtime" v={connected ? "connected" : "offline"} tone={connected ? "good" : "bad"} />
            {admin && <Stat k="Last backup" v={backup ? backup.text : "—"} tone={backup?.stale ? "avoid" : undefined} to={LINKS.backups} />}
          </dl>
        </Card>
      </Page>
    </>
  );
}

// NeedsYou is the first card: everything waiting on a person, one line per kind of
// problem (health problems and failing imports one per line, each with its own fix).
// It reads the staff layout's Needs-you poll, so it costs this page nothing.
function NeedsYou() {
  const attention = useAttention();
  const rows = needsYouRows(attention);
  return (
    <section aria-labelledby="needs-you" className="mb-1">
      <div id="needs-you" className="mb-3 font-mono text-2xs font-bold uppercase tracking-[0.12em] text-ink-faint">
        Needs you{attention && attention.counts.total > 0 ? ` · ${attention.counts.total}` : ""}
      </div>
      <Card>
        {!attention ? (
          <p className="m-0 text-base text-ink-faint">Checking…</p>
        ) : rows.length === 0 ? (
          <p className="m-0 text-base text-ink-faint">
            {attention.at ? "All clear — nothing needs you." : "Checking what needs you…"}
          </p>
        ) : (
          <ul className="m-0 flex list-none flex-col p-0">
            {rows.map((r, i) => (
              <li
                key={r.key}
                className="flex items-center gap-2.5 py-2 text-base"
                style={i > 0 ? { borderTop: "1px solid var(--line)" } : undefined}
                title={r.sample && r.sample.length > 0 ? r.sample.join("\n") : undefined}
              >
                <span
                  className="h-2 w-2 flex-none rounded-full"
                  style={{ background: r.level === "error" ? "var(--reject)" : "var(--avoid)" }}
                  aria-label={r.level === "error" ? "Error" : "Warning"}
                  role="img"
                />
                <span className="min-w-0 flex-1 break-words">
                  {r.text}
                  {r.detail && <span className="block text-sm text-ink-faint">{r.detail}</span>}
                </span>
                {r.to && (
                  <Link to={r.to} className="flex-none whitespace-nowrap font-semibold no-underline" style={{ color: "var(--accent-text)" }}>
                    {r.action} →
                  </Link>
                )}
              </li>
            ))}
          </ul>
        )}
        {attention?.stale && attention.at && (
          <p className="m-0 mt-2 text-xs text-ink-faint">This list hasn&apos;t been updated for a few minutes.</p>
        )}
      </Card>
    </section>
  );
}

function StreamCard({ s, badge }: { s: InsightsStream; badge?: boolean }) {
  const transcoding = s.decision === "transcode";
  const hw = hwBadge(s);
  return (
    <div
      className="flex gap-3 rounded-xl p-3"
      style={{ background: "var(--panel)", border: "1px solid var(--line)" }}
    >
      {s.thumb ? (
        <img
          src={s.thumb}
          alt=""
          className="h-[84px] w-[56px] shrink-0 rounded-md object-cover"
          style={{ background: "var(--panel-2)" }}
        />
      ) : (
        <div className="h-[84px] w-[56px] shrink-0 rounded-md" style={{ background: "var(--panel-2)" }} />
      )}
      <div className="min-w-0 flex-1">
        <div className="truncate text-md font-semibold">{s.title}</div>
        <div className="truncate text-sm text-ink-dim">{s.subtitle}</div>
        <div className="mt-1 flex flex-wrap items-center gap-1.5 font-mono text-xs">
          {badge && isMediaServer(s.server) && <ServerBadge server={s.server} />}
          <Pill>{s.user}</Pill>
          <Pill tone={transcoding ? "warn" : "good"}>
            {transcoding ? `transcode · ${hw?.label ?? "CPU"}` : "direct"}
          </Pill>
          {transcoding && hw?.fellBack && <Pill tone="warn">fell back to CPU</Pill>}
          {s.state === "paused" && <Pill>paused</Pill>}
          <span className="text-ink-faint">{s.player}</span>
        </div>
        <div className="mt-2 h-1 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
          <div
            className="h-full rounded-full"
            style={{
              width: `${Math.min(100, Math.max(0, s.progress_pct))}%`,
              background: s.state === "paused" ? "var(--ink-faint)" : "var(--good)",
            }}
          />
        </div>
      </div>
    </div>
  );
}

// ListenCard is one live audiobook session. Someone else's shows only who, on what and
// for how long; your own shows the book and how far through it you are.
function ListenCard({ l }: { l: NowListening }) {
  const pct = l.duration && l.duration > 0 && l.position !== undefined ? (l.position / l.duration) * 100 : null;
  const device = [l.device, l.client && l.client !== l.device ? l.client : ""].filter(Boolean).join(" · ");
  return (
    <div
      className="flex gap-3 rounded-xl p-3"
      style={{ background: "var(--panel)", border: "1px solid var(--line)" }}
    >
      {l.cover_url ? (
        <img
          src={l.cover_url}
          alt=""
          className="h-[64px] w-[64px] shrink-0 rounded-md object-cover"
          style={{ background: "var(--panel-2)" }}
        />
      ) : (
        <div
          className="flex h-[64px] w-[64px] shrink-0 items-center justify-center rounded-md text-3xl"
          style={{ background: "var(--panel-2)" }}
          aria-hidden
        >
          🎧
        </div>
      )}
      <div className="min-w-0 flex-1">
        {l.mine && l.title ? (
          <>
            {l.book_id ? (
              <Link
                to={`/books/${l.book_id}`}
                className="block truncate text-md font-semibold no-underline"
                style={{ color: "var(--ink)" }}
              >
                {l.title}
              </Link>
            ) : (
              <div className="truncate text-md font-semibold">{l.title}</div>
            )}
            <div className="truncate text-sm text-ink-dim">{l.author || "You"}</div>
          </>
        ) : (
          <>
            <div className="truncate text-md font-semibold">{l.mine ? "You" : l.user}</div>
            <div className="truncate text-sm text-ink-dim">Listening to an audiobook</div>
          </>
        )}
        <div className="mt-1 flex flex-wrap items-center gap-1.5 font-mono text-xs">
          {l.mine && <Pill>you</Pill>}
          <Pill tone={l.playing ? "good" : undefined}>{l.playing ? "playing" : "paused"}</Pill>
          <span className="text-ink-faint">
            {duration(l.seconds)} this session{device ? ` · ${device}` : ""}
          </span>
        </div>
        {pct !== null && (
          <div className="mt-2 h-1 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
            <div
              className="h-full rounded-full"
              style={{
                width: `${Math.min(100, Math.max(0, pct))}%`,
                background: l.playing ? "var(--good)" : "var(--ink-faint)",
              }}
            />
          </div>
        )}
      </div>
    </div>
  );
}

function duration(sec: number): string {
  const m = Math.floor(sec / 60);
  if (m < 1) return "under a minute";
  if (m < 60) return `${m}m`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

function StorageBar({ v }: { v: StorageVolume }) {
  // Anything past 85% is where imports start running out of room, so the bar changes
  // colour before it's a warning in the log rather than after.
  const tone =
    v.used_pct >= 95 ? "var(--reject)" : v.used_pct >= 85 ? "var(--avoid)" : "var(--good)";
  return (
    <div>
      <div className="mb-1.5 flex items-baseline justify-between gap-3">
        <div className="min-w-0">
          <span className="text-base font-semibold">{v.roots.join(" · ")}</span>
          <span className="ml-2 font-mono text-xs text-ink-faint">{v.path}</span>
        </div>
        <span className="shrink-0 font-mono text-sm text-ink-dim">
          {bytes(v.free_bytes)} free of {bytes(v.total_bytes)}
        </span>
      </div>
      <div className="relative h-2.5 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
        <div
          className="h-full rounded-full transition-[width] duration-500"
          style={{ width: `${Math.min(100, v.used_pct)}%`, background: tone }}
        />
      </div>
      <div className="mt-1 font-mono text-xs" style={{ color: tone }}>
        {v.used_pct.toFixed(1)}% used
      </div>
    </div>
  );
}

const EVENT_ICON: Record<string, string> = {
  grabbed: "⬇",
  imported: "📥",
  upgraded: "⬆",
  deleted: "🗑",
  renamed: "✏️",
  failed: "⛔",
  added: "✚",
  merged: "🧩",
  possible_duplicate: "⚠",
};

function ActivityRow({ e }: { e: ActivityEvent }) {
  const href = eventHref(e.kind, e.id);
  const icon = EVENT_ICON[e.event.toLowerCase()] ?? "•";
  return (
    <li className="flex items-baseline gap-2.5 py-1.5 text-base" style={{ borderTop: "1px solid var(--line)" }}>
      <span className="w-4 shrink-0 text-center">{icon}</span>
      <Link to={href} className="shrink-0 font-semibold no-underline" style={{ color: "var(--ink)" }}>
        {e.title}
      </Link>
      <span className="min-w-0 flex-1 truncate text-ink-dim">
        {e.event}
        {e.detail ? ` — ${e.detail}` : ""}
      </span>
      <span className="shrink-0 font-mono text-xs text-ink-faint">{ago(e.at_ms)}</span>
    </li>
  );
}

// downloadingSub is the Downloading tile's second line. An unreachable client used to read
// "0 seeding", which looks like a quiet queue. Stalled and errored torrents lead, since
// they are what needs a look (stalled ones aren't counted as downloading).
export function downloadingSub(d: Pick<DashboardData, "queue" | "queue_note">): string {
  if (d.queue_note) return "Client unreachable";
  const q = d.queue;
  const parts: string[] = [];
  if ((q.stalled ?? 0) > 0) parts.push(`${q.stalled} stalled`);
  if (q.errored > 0) parts.push(`${q.errored} error${q.errored === 1 ? "" : "s"}`);
  if (q.down_speed > 0) parts.push(`↓ ${bytes(q.down_speed)}/s`);
  parts.push(`${q.seeding} seeding`);
  return parts.join(" · ");
}

// musicTileSub is the Albums tile's second line: what's missing first, then the artists.
export function musicTileSub(m: Pick<DashboardMusic, "artists" | "albums_missing">): string {
  const artists = `${m.artists} artist${m.artists === 1 ? "" : "s"}`;
  return m.albums_missing > 0 ? `${m.albums_missing} missing · ${artists}` : artists;
}

// musicUpcoming is the wanted albums that aren't out yet: the Wanted page's Upcoming tab.
export function musicUpcoming(m: Pick<DashboardMusic, "albums_wanted" | "albums_missing">): number {
  return Math.max(0, m.albums_wanted - m.albums_missing);
}

// releaseLine is a new release's second line: 'Artist · EP · 2026-11-14'.
export function releaseLine(r: Pick<DashboardNewRelease, "artist_name" | "album_type" | "release_date">): string {
  return [r.artist_name, r.album_type, r.release_date].filter(Boolean).join(" · ");
}

// MusicCard is Music's numbers, the newest new releases and the newest music imports.
function MusicCard({ m }: { m: DashboardMusic }) {
  return (
    <Card>
      <dl className="m-0 grid grid-cols-2 gap-x-6 gap-y-3 font-mono text-sm text-ink-dim sm:grid-cols-3 lg:grid-cols-6">
        <Stat k="Artists" v={String(m.artists)} />
        <Stat k="Albums" v={String(m.albums)} />
        <Stat k="Tracks" v={String(m.tracks)} />
        <Stat k="Size" v={bytes(m.size_bytes)} />
        {/* The wanted albums, split as the Wanted page splits them, each opening the tab
            that lists it: released ones (searching) and ones not out yet (upcoming). */}
        <Stat k="Missing" v={String(m.albums_missing)} tone={m.albums_missing > 0 ? "avoid" : undefined} to={`${LINKS.downloadsSearching}?show=searching`} />
        <Stat k="Upcoming" v={String(musicUpcoming(m))} to={`${LINKS.downloadsSearching}?show=upcoming`} />
      </dl>
      <div className="mt-5 grid gap-5 sm:grid-cols-2">
        <section aria-labelledby="dash-new-releases" className="min-w-0">
          <h3 id="dash-new-releases" className="m-0 mb-1 text-xs uppercase tracking-[0.1em] text-ink-faint">
            New releases{m.new_releases_total > 0 ? ` · ${m.new_releases_total} in 30 days` : ""}
          </h3>
          {m.new_releases.length === 0 ? (
            <p className="m-0 text-base text-ink-faint">Nothing new in the last 30 days.</p>
          ) : (
            <ul className="m-0 flex list-none flex-col p-0">
              {m.new_releases.map((r) => (
                <li key={r.id} className="py-1.5" style={{ borderTop: "1px solid var(--line)" }}>
                  <Link to={`/music/album/${r.id}`} className="flex items-baseline gap-2.5 no-underline" style={{ color: "var(--ink)" }}>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-base font-semibold">{r.title}</span>
                      <span className="block truncate font-mono text-xs text-ink-faint">{releaseLine(r)}</span>
                    </span>
                    <span className="flex-none text-xs font-semibold" style={{ color: r.monitored ? "var(--accent-text)" : "var(--ink-faint)" }}>
                      {r.monitored ? "Wanted" : "Not wanted"}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>
        <section aria-labelledby="dash-music-imports" className="min-w-0">
          <h3 id="dash-music-imports" className="m-0 mb-1 text-xs uppercase tracking-[0.1em] text-ink-faint">Recent imports</h3>
          {m.imports.length === 0 ? (
            <p className="m-0 text-base text-ink-faint">No music imported yet.</p>
          ) : (
            <ul className="m-0 flex list-none flex-col p-0">
              {m.imports.map((e, i) => (
                <ActivityRow key={`${e.id}-${e.at_ms}-${i}`} e={e} />
              ))}
            </ul>
          )}
        </section>
      </div>
    </Card>
  );
}

function Tile({
  to,
  label,
  value,
  sub,
  warn,
  title,
  className,
}: {
  to: string;
  label: string;
  value: number | string;
  sub: string;
  warn?: boolean;
  title?: string;
  className?: string;
}) {
  return (
    <Link
      to={to}
      title={title}
      className={`rounded-[11px] p-3.5 no-underline${className ? ` ${className}` : ""}`}
      style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
    >
      <div className="text-xs uppercase tracking-[0.1em] text-ink-faint">{label}</div>
      <div className="mt-0.5 text-3xl font-bold leading-tight">{value}</div>
      <div className="text-xs" style={{ color: warn ? "var(--avoid-text)" : "var(--ink-faint)" }}>
        {sub}
      </div>
    </Link>
  );
}

function Pill({ children, tone }: { children: React.ReactNode; tone?: "good" | "warn" }) {
  const color =
    tone === "good" ? "var(--good-text)" : tone === "warn" ? "var(--avoid-text)" : "var(--ink-faint)";
  return (
    <span className="rounded px-1.5 py-0.5" style={{ background: "var(--panel-2)", color }}>
      {children}
    </span>
  );
}

function Card({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      {children}
    </div>
  );
}

function SectionLabel({ children, right, link }: { children: React.ReactNode; right?: string; link?: { to: string; label: string } }) {
  return (
    <div className="mb-3 mt-6 flex items-baseline justify-between font-mono text-2xs font-bold uppercase tracking-[0.12em] text-ink-faint">
      <span>{children}</span>
      {right && <span className="font-normal normal-case tracking-normal">{right}</span>}
      {link && <Link to={link.to} className="font-semibold normal-case tracking-normal no-underline" style={{ color: "var(--accent-text)" }}>{link.label}</Link>}
    </div>
  );
}

function Stat({ k, v, tone, to }: { k: string; v: string; tone?: "good" | "bad" | "avoid"; to?: string }) {
  const color = tone === "good" ? "var(--good)" : tone === "bad" ? "var(--reject)" : tone === "avoid" ? "var(--avoid)" : "var(--ink)";
  return (
    <div>
      <dt className="text-xs uppercase tracking-[0.1em] text-ink-faint">{k}</dt>
      <dd className="m-0 mt-0.5" style={{ color }}>
        {to ? <Link to={to} style={{ color }}>{v}</Link> : v}
      </dd>
    </div>
  );
}

function bytes(n: number): string {
  if (n <= 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
}

function mbps(kbps: number): string {
  return kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbps` : `${kbps} kbps`;
}

function uptime(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`;
  return `${Math.floor(sec / 86400)}d ${Math.floor((sec % 86400) / 3600)}h`;
}

function ago(ms: number): string {
  const s = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
