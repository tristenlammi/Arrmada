import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import {
  api,
  type Status,
  type Health,
  type SystemHealth,
  type DashboardData,
  type StorageVolume,
  type ActivityEvent,
  type InsightsStream,
} from "../lib/api";
import { useLive } from "../lib/useLive";

// The dashboard fans out over Plex, the download client and the disks, so it isn't
// free — but it's the page people leave open. Ten seconds keeps the streams and the
// transfer speeds honest without hammering anything.
const REFRESH_MS = 10_000;

export function Dashboard() {
  const [status, setStatus] = useState<Status | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [system, setSystem] = useState<SystemHealth | null>(null);
  const [data, setData] = useState<DashboardData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const { connected } = useLive();

  useEffect(() => {
    Promise.all([api.status(), api.health()])
      .then(([s, h]) => {
        setStatus(s);
        setHealth(h);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
    api.systemHealth().then(setSystem).catch(() => {});
  }, []);

  useEffect(() => {
    let alive = true;
    const pull = () => {
      api
        .dashboard()
        .then((d) => alive && setData(d))
        .catch(() => {});
    };
    pull();
    const t = setInterval(pull, REFRESH_MS);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  const dbOK = health?.checks?.database === "ok";
  const streams = data?.streams?.streams ?? [];
  const lib = data?.library;

  return (
    <>
      <PageHeader title="Dashboard" crumb="Overview" />
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        {system && system.warnings.length > 0 && (
          <div className="mb-5 flex flex-col gap-2">
            {system.warnings.map((wrn, i) => (
              <div
                key={i}
                className="flex items-center gap-2.5 rounded-lg px-3.5 py-2.5 text-[12.5px]"
                style={{
                  background:
                    wrn.level === "error" ? "var(--reject-soft)" : "var(--avoid-soft, var(--panel-2))",
                  border: `1px solid ${wrn.level === "error" ? "var(--reject)" : "var(--avoid, var(--line))"}`,
                  color: wrn.level === "error" ? "var(--reject)" : "var(--ink-dim)",
                }}
              >
                <span>{wrn.level === "error" ? "⛔" : "⚠️"}</span>
                <span>{wrn.message}</span>
              </div>
            ))}
          </div>
        )}

        {error && (
          <div
            className="mb-5 rounded-lg px-3.5 py-2.5 text-[12.5px]"
            style={{ background: "var(--reject-soft)", border: "1px solid var(--reject)", color: "var(--reject)" }}
          >
            Backend unreachable: {error}
          </div>
        )}

        {/* Now playing — the thing you actually want to see when you open the page. */}
        <SectionLabel
          right={
            streams.length > 0 && data?.streams
              ? `${mbps(data.streams.bandwidth.total_kbps)} total`
              : undefined
          }
        >
          Now playing{streams.length > 0 ? ` · ${streams.length}` : ""}
        </SectionLabel>
        {streams.length === 0 ? (
          <Card>
            <p className="m-0 text-[12.5px] text-ink-faint">
              {data?.streams_note
                ? `Plex isn't reachable — ${data.streams_note}`
                : "Nothing is streaming right now."}
            </p>
          </Card>
        ) : (
          <div className="grid gap-2.5 sm:grid-cols-2">
            {streams.map((s) => (
              <StreamCard key={s.session_key} s={s} />
            ))}
          </div>
        )}

        {/* Storage — one bar per filesystem, not per folder. */}
        <SectionLabel>Storage</SectionLabel>
        <Card>
          {!data ? (
            <p className="m-0 text-[12.5px] text-ink-faint">Reading disks…</p>
          ) : data.storage.length === 0 ? (
            <p className="m-0 text-[12.5px] text-ink-faint">
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
        <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
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
          <Tile
            to="/downloads"
            label="Downloading"
            value={data ? data.queue.downloading : "—"}
            sub={
              data
                ? data.queue.down_speed > 0
                  ? `↓ ${bytes(data.queue.down_speed)}/s · ${data.queue.seeding} seeding`
                  : `${data.queue.seeding} seeding`
                : ""
            }
            warn={!!data && data.queue.errored > 0}
          />
        </div>

        {/* What Arrmada has been doing — the per-title event tables, unified. */}
        <SectionLabel>Recent activity</SectionLabel>
        <Card>
          {!data ? (
            <p className="m-0 text-[12.5px] text-ink-faint">Loading…</p>
          ) : data.activity.length === 0 ? (
            <p className="m-0 text-[12.5px] text-ink-faint">
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
          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 font-mono text-[12px] text-ink-dim sm:grid-cols-5">
            <Stat k="Version" v={status?.version ?? "—"} />
            <Stat k="Uptime" v={status ? uptime(status.uptime_seconds) : "—"} />
            <Stat k="Database" v={dbOK ? "ok" : "down"} tone={dbOK ? "good" : "bad"} />
            <Stat k="Auth" v={status?.auth_enabled ? "enabled" : "disabled"} />
            <Stat k="Realtime" v={connected ? "connected" : "offline"} tone={connected ? "good" : "bad"} />
          </dl>
        </Card>
      </div>
    </>
  );
}

function StreamCard({ s }: { s: InsightsStream }) {
  const transcoding = s.decision === "transcode";
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
        <div className="truncate text-[13px] font-semibold">{s.title}</div>
        <div className="truncate text-[11.5px] text-ink-dim">{s.subtitle}</div>
        <div className="mt-1 flex flex-wrap items-center gap-1.5 font-mono text-[10px]">
          <Pill>{s.user}</Pill>
          <Pill tone={transcoding ? "warn" : "good"}>
            {transcoding ? (s.hw_transcode ? "transcode (hw)" : "transcode") : "direct"}
          </Pill>
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

function StorageBar({ v }: { v: StorageVolume }) {
  // Anything past 85% is where imports start running out of room, so the bar changes
  // colour before it's a warning in the log rather than after.
  const tone =
    v.used_pct >= 95 ? "var(--reject)" : v.used_pct >= 85 ? "var(--avoid, #d08b3c)" : "var(--good)";
  return (
    <div>
      <div className="mb-1.5 flex items-baseline justify-between gap-3">
        <div className="min-w-0">
          <span className="text-[12.5px] font-semibold">{v.roots.join(" · ")}</span>
          <span className="ml-2 font-mono text-[10.5px] text-ink-faint">{v.path}</span>
        </div>
        <span className="shrink-0 font-mono text-[11.5px] text-ink-dim">
          {bytes(v.free_bytes)} free of {bytes(v.total_bytes)}
        </span>
      </div>
      <div className="relative h-2.5 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
        <div
          className="h-full rounded-full transition-[width] duration-500"
          style={{ width: `${Math.min(100, v.used_pct)}%`, background: tone }}
        />
      </div>
      <div className="mt-1 font-mono text-[10.5px]" style={{ color: tone }}>
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
};

function ActivityRow({ e }: { e: ActivityEvent }) {
  const href =
    e.kind === "movie" ? `/movies/${e.id}` : e.kind === "series" ? `/series/${e.id}` : `/books/${e.id}`;
  const icon = EVENT_ICON[e.event.toLowerCase()] ?? "•";
  return (
    <li className="flex items-baseline gap-2.5 py-1.5 text-[12.5px]" style={{ borderTop: "1px solid var(--line)" }}>
      <span className="w-4 shrink-0 text-center">{icon}</span>
      <Link to={href} className="shrink-0 font-semibold no-underline" style={{ color: "var(--ink)" }}>
        {e.title}
      </Link>
      <span className="min-w-0 flex-1 truncate text-ink-dim">
        {e.event}
        {e.detail ? ` — ${e.detail}` : ""}
      </span>
      <span className="shrink-0 font-mono text-[10.5px] text-ink-faint">{ago(e.at_ms)}</span>
    </li>
  );
}

function Tile({
  to,
  label,
  value,
  sub,
  warn,
}: {
  to: string;
  label: string;
  value: number | string;
  sub: string;
  warn?: boolean;
}) {
  return (
    <Link
      to={to}
      className="rounded-[11px] p-3.5 no-underline"
      style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
    >
      <div className="text-[10px] uppercase tracking-[0.1em] text-ink-faint">{label}</div>
      <div className="mt-0.5 text-[22px] font-bold leading-tight">{value}</div>
      <div className="text-[11px]" style={{ color: warn ? "var(--avoid, var(--ink-dim))" : "var(--ink-faint)" }}>
        {sub}
      </div>
    </Link>
  );
}

function Pill({ children, tone }: { children: React.ReactNode; tone?: "good" | "warn" }) {
  const color =
    tone === "good" ? "var(--good)" : tone === "warn" ? "var(--avoid, #d08b3c)" : "var(--ink-faint)";
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

function SectionLabel({ children, right }: { children: React.ReactNode; right?: string }) {
  return (
    <div className="mb-3 mt-6 flex items-baseline justify-between font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-ink-faint">
      <span>{children}</span>
      {right && <span className="font-normal normal-case tracking-normal">{right}</span>}
    </div>
  );
}

function Stat({ k, v, tone }: { k: string; v: string; tone?: "good" | "bad" }) {
  const color = tone === "good" ? "var(--good)" : tone === "bad" ? "var(--reject)" : "var(--ink)";
  return (
    <div>
      <dt className="text-[9px] uppercase tracking-[0.1em] text-ink-faint">{k}</dt>
      <dd className="m-0 mt-0.5" style={{ color }}>
        {v}
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
