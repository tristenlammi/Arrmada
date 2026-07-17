import { useEffect, useRef, useState, type ReactNode } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type PlexConfig, type PlexTestResult, type InsightsActivity, type InsightsStream, type HistoryEntry, type InsightsStats, type UserEntry, type LibraryStat, type RecentItem, type InsightsGraphs, type Reliability, type BufferGroup, type NotificationConn } from "../lib/api";

// Insights — Arrmada's Plex watch-monitoring module (Tautulli replacement). Built in slices.
// I0 (this): the Plex connection — configure + test. Activity, History,
// Users, Graphs and the Reliability/buffering view land in later slices and show as "coming soon".
type Tab = "activity" | "history" | "users" | "graphs" | "reliability" | "notifications" | "settings";
const TABS: { key: Tab; label: string }[] = [
  { key: "activity", label: "Activity" },
  { key: "history", label: "History" },
  { key: "users", label: "Users" },
  { key: "graphs", label: "Graphs" },
  { key: "reliability", label: "Reliability" },
  { key: "notifications", label: "Notifications" },
  { key: "settings", label: "Settings" },
];

export function Insights() {
  const [tab, setTab] = useState<Tab>("activity");
  const [cfg, setCfg] = useState<PlexConfig | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  useEffect(() => { api.insightsConfig().then(setCfg).catch(() => flash("Could not load Plex settings")); }, []);
  const connected = cfg?.token_set && !!cfg?.url;

  return (
    <>
      <PageHeader title="Insights" crumb="Services / Insights" />
      <div className="mx-auto w-full max-w-[1240px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <p className="max-w-[64ch] text-[12.5px] text-ink-dim">Watch monitoring for your Plex server — who's streaming what, right now and historically, with stream quality, transcode diagnostics and buffering reliability. Connect your server in <b>Settings</b> to begin.</p>
          <span className="inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-[12px] font-semibold" style={{ border: `1px solid ${connected ? "var(--good)" : "var(--avoid)"}`, background: connected ? "var(--good-soft, rgba(127,176,105,.16))" : "var(--avoid-soft)" }}>
            <span className="h-2 w-2 rounded-full" style={{ background: connected ? "var(--good)" : "var(--avoid)" }} />
            {connected ? "Plex connected" : "Not connected"}
          </span>
        </div>

        {/* Tabs */}
        <div className="mb-5 flex gap-1 border-b" style={{ borderColor: "var(--line)" }}>
          {TABS.map((t) => {
            const active = tab === t.key;
            return (
              <button key={t.key} onClick={() => setTab(t.key)} className="relative px-4 py-2.5 text-[13.5px] font-semibold transition-colors" style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}>
                {t.label}
                {active && <span className="absolute inset-x-2 -bottom-px h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
              </button>
            );
          })}
        </div>

        {tab === "settings" ? (
          <PlexSettings cfg={cfg} onSaved={setCfg} flash={flash} />
        ) : tab === "activity" ? (
          <ActivityView connected={!!connected} onConfigure={() => setTab("settings")} />
        ) : tab === "history" ? (
          <HistoryView connected={!!connected} onConfigure={() => setTab("settings")} />
        ) : tab === "users" ? (
          <UsersView connected={!!connected} onConfigure={() => setTab("settings")} />
        ) : tab === "graphs" ? (
          <GraphsView connected={!!connected} onConfigure={() => setTab("settings")} />
        ) : tab === "reliability" ? (
          <ReliabilityView connected={!!connected} onConfigure={() => setTab("settings")} />
        ) : tab === "notifications" ? (
          <NotificationsView flash={flash} />
        ) : (
          <ComingSoon tab={tab} connected={!!connected} onConfigure={() => setTab("settings")} />
        )}
      </div>
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

/* ============================= ACTIVITY ============================= */
function fmtMbps(kbps: number): string {
  if (!kbps) return "0";
  const mb = kbps / 1000;
  return mb >= 10 ? mb.toFixed(0) : mb.toFixed(1);
}
function fmtClock(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), ss = s % 60;
  const mm = String(m).padStart(2, "0"), sss = String(ss).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${sss}` : `${m}:${sss}`;
}
const DECISION: Record<string, { label: string; color: string }> = {
  direct_play: { label: "Direct Play", color: "var(--good)" },
  direct_stream: { label: "Direct Stream", color: "var(--accent)" },
  transcode: { label: "Transcode", color: "var(--avoid)" },
};
// Diagnosed buffer-cause styling (keys match BufferEvent.cause / CauseCount.cause).
const CAUSE: Record<string, { label: string; color: string }> = {
  transcode: { label: "Transcode overloaded", color: "var(--reject)" },
  transcode_cpu: { label: "CPU transcode (no HW)", color: "var(--reject)" },
  bandwidth: { label: "Bandwidth / network", color: "var(--avoid)" },
  unknown: { label: "Inconclusive", color: "var(--ink-faint)" },
};
function geoLabel(g: InsightsStream["geo"]): string {
  if (g.local) return "Local";
  if (g.city && g.country_code) return `${g.city}, ${g.country_code}`;
  if (g.country) return g.country;
  return g.ip || "—";
}

function ActivityView({ connected, onConfigure }: { connected: boolean; onConfigure: () => void }) {
  const [act, setAct] = useState<InsightsActivity | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [detail, setDetail] = useState<InsightsStream | null>(null);
  // Per-session smoothed playback clock. Plex only updates viewOffset every ~10s, so
  // re-baselining to it every poll makes the bar sawtooth. Instead we run our own 1×
  // clock and only re-sync to Plex when it diverges a lot (a real seek/resume) — small
  // drift from Plex's lazy updates is ignored, so the timer counts smoothly.
  const clock = useRef<Record<string, { base: number; at: number }>>({});
  const [, tickNow] = useState(0);

  useEffect(() => {
    if (!connected) return;
    let alive = true;
    const tick = () => api.insightsActivity().then((a) => { if (alive) { setAct(a); setErr(null); } }).catch((e) => { if (alive) setErr((e as Error).message); });
    tick();
    const t = setInterval(tick, 4000);
    return () => { alive = false; clearInterval(t); };
  }, [connected]);

  // Re-render once a second so playing streams' progress interpolates in real time.
  useEffect(() => {
    const t = setInterval(() => tickNow((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);

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

  if (!connected) return <ComingSoon tab="activity" connected={false} onConfigure={onConfigure} />;
  if (err) return <div className="rounded-xl p-6 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Couldn’t reach Plex: {err}</div>;
  if (!act) return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading activity…</div>;

  const streams = act.streams;
  // liveOffset reads the smoothed clock: advance while playing, hold otherwise; capped.
  const liveOffset = (s: InsightsStream): number => {
    const c = clock.current[s.session_key];
    const off = c ? (s.state === "playing" ? c.base + (Date.now() - c.at) : c.base) : s.offset_ms;
    return s.duration_ms > 0 ? Math.min(s.duration_ms, off) : off;
  };
  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="text-[12.5px] text-ink-dim">{streams.length ? `${streams.length} stream${streams.length === 1 ? "" : "s"} active` : "Nothing is playing right now."}</div>
        <div className="flex items-center gap-3 font-mono text-[11px]">
          <BW label="TOTAL" v={act.bandwidth.total_kbps} accent />
          <BW label="LAN" v={act.bandwidth.lan_kbps} />
          <BW label="WAN" v={act.bandwidth.wan_kbps} />
        </div>
      </div>
      {streams.length === 0 ? (
        <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-faint" style={{ border: "1px dashed var(--line)" }}>No active streams.</div>
      ) : (
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(360px, 1fr))" }}>
          {streams.map((s) => <StreamCard key={s.session_key} s={s} offsetMs={liveOffset(s)} onOpen={() => setDetail(s)} />)}
        </div>
      )}
      {!act.geo_active && streams.some((s) => !s.geo.local) && (
        <div className="text-[10.5px] text-ink-faint">Tip: drop a MaxMind <code>GeoLite2-City.mmdb</code> into the data dir (or set <code>ARRMADA_GEOIP_DB</code>) to resolve remote IPs to a city.</div>
      )}
      <HomeExtras />
      {detail && <DeepDive s={detail} onClose={() => setDetail(null)} />}
    </div>
  );
}

/* ============================= HOME DASHBOARD (stats / libraries / recently added) ============================= */
function fmtHours(secs: number): string {
  if (!secs) return "0";
  const h = secs / 3600;
  return h >= 10 ? `${h.toFixed(0)}h` : h >= 1 ? `${h.toFixed(1)}h` : `${Math.round(secs / 60)}m`;
}

function HomeExtras() {
  const [stats, setStats] = useState<InsightsStats | null>(null);
  const [libs, setLibs] = useState<LibraryStat[] | null>(null);
  const [recent, setRecent] = useState<RecentItem[] | null>(null);
  const [metric, setMetric] = useState<"plays" | "duration">("plays");

  useEffect(() => { api.insightsStats(30, metric).then(setStats).catch(() => setStats(null)); }, [metric]);
  useEffect(() => {
    api.insightsLibraries().then(setLibs).catch(() => setLibs([]));
    api.insightsRecentlyAdded(20).then(setRecent).catch(() => setRecent([]));
  }, []);

  const hasStats = stats && (stats.most_watched_movies.length || stats.most_watched_shows.length || stats.most_active_users.length);

  return (
    <div className="mt-4 flex flex-col gap-5">
      {/* Watch statistics */}
      <section>
        <div className="mb-2.5 flex items-center justify-between">
          <h3 className="text-[13px] font-bold">Watch statistics <span className="font-normal text-ink-faint">· last 30 days</span></h3>
          <div className="flex gap-1">
            <Chip active={metric === "plays"} onClick={() => setMetric("plays")}>Plays</Chip>
            <Chip active={metric === "duration"} onClick={() => setMetric("duration")}>Duration</Chip>
          </div>
        </div>
        {!hasStats ? (
          <div className="rounded-xl p-6 text-center text-[12px] text-ink-faint" style={{ border: "1px dashed var(--line)" }}>No watch data yet — statistics build up as people stream.</div>
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(300px, 1fr))" }}>
            <StatCard title="Most watched movies" rows={stats!.most_watched_movies.map((m) => ({ label: m.title, thumb: m.thumb_url, v: metric === "plays" ? m.plays : m.secs }))} metric={metric} />
            <StatCard title="Most watched TV" rows={stats!.most_watched_shows.map((m) => ({ label: m.title, thumb: m.thumb_url, v: metric === "plays" ? m.plays : m.secs }))} metric={metric} />
            <StatCard title="Most active users" rows={stats!.most_active_users.map((u) => ({ label: u.name, v: metric === "plays" ? u.plays : u.secs }))} metric={metric} />
            <StatCard title="Most active platforms" rows={stats!.most_active_platforms.map((p) => ({ label: p.name, v: metric === "plays" ? p.plays : p.secs }))} metric={metric} />
          </div>
        )}
      </section>

      {/* Library statistics */}
      {libs && libs.length > 0 && (
        <section>
          <h3 className="mb-2.5 text-[13px] font-bold">Library statistics</h3>
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))" }}>
            {libs.map((l) => (
              <div key={l.title} className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
                <div className="font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{libTypeLabel(l.type)}</div>
                <div className="mt-0.5 text-[13px] font-semibold">{l.title}</div>
                <div className="mt-1 text-[24px] font-extrabold tracking-tight">{l.count.toLocaleString()}</div>
              </div>
            ))}
          </div>
        </section>
      )}

      {/* Recently added */}
      {recent && recent.length > 0 && (
        <section>
          <h3 className="mb-2.5 text-[13px] font-bold">Recently added</h3>
          <div className="flex gap-3 overflow-x-auto pb-2 thin-scroll">
            {recent.map((it, i) => (
              <div key={i} className="flex-none" style={{ width: 96 }}>
                <div className="relative h-[144px] w-[96px] overflow-hidden rounded-lg" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
                  {it.thumb_url ? <img src={it.thumb_url} alt="" loading="lazy" className="h-full w-full object-cover" /> : null}
                </div>
                <div className="mt-1 truncate text-[11px] font-semibold" title={it.title}>{it.title}</div>
                <div className="truncate text-[10px] text-ink-faint">{it.subtitle}</div>
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

function StatCard({ title, rows, metric }: { title: string; rows: { label: string; thumb?: string; v: number }[]; metric: "plays" | "duration" }) {
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="mb-2 text-[12.5px] font-bold">{title}</div>
      {rows.length === 0 ? <div className="text-[11px] text-ink-faint">—</div> : (
        <ol className="flex flex-col gap-1.5">
          {rows.map((r, i) => (
            <li key={i} className="flex items-center gap-2 text-[12px]">
              <span className="w-3 flex-none font-mono text-[10px] text-ink-faint">{i + 1}</span>
              {r.thumb !== undefined && <span className="h-7 w-5 flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>{r.thumb ? <img src={r.thumb} alt="" loading="lazy" className="h-full w-full object-cover" /> : null}</span>}
              <span className="min-w-0 flex-1 truncate">{r.label}</span>
              <span className="flex-none font-mono text-[11px] font-semibold" style={{ color: "var(--accent)" }}>{metric === "plays" ? r.v : fmtHours(r.v)}</span>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

/* ============================= USERS ============================= */
function UsersView({ connected, onConfigure }: { connected: boolean; onConfigure: () => void }) {
  const [users, setUsers] = useState<UserEntry[] | null>(null);
  useEffect(() => { if (connected) api.insightsUsers().then(setUsers).catch(() => setUsers([])); }, [connected]);
  if (!connected) return <ComingSoon tab="users" connected={false} onConfigure={onConfigure} />;
  if (!users) return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading users…</div>;
  if (users.length === 0) return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-faint" style={{ border: "1px solid var(--line)" }}>No users seen yet.</div>;
  return (
    <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
      <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 820 }}>
        <thead><tr style={{ background: "var(--panel-2)" }}>{["User", "Last seen", "Location", "Platform", "Last played", "Plays", "Watch time"].map((h) => <th key={h} className="px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{h}</th>)}</tr></thead>
        <tbody>
          {users.map((u, i) => (
            <tr key={u.id} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
              <td className="px-3 py-2 font-semibold">{u.username}</td>
              <td className="whitespace-nowrap px-3 py-2 font-mono text-[11px] text-ink-dim">{u.last_seen ? fmtDate(u.last_seen) : "never"}</td>
              <td className="px-3 py-2 font-mono text-[10.5px] text-ink-dim">{u.last_ip ? geoLabel(u.geo) : "—"}</td>
              <td className="px-3 py-2 text-ink-dim">{u.last_platform || "—"}</td>
              <td className="px-3 py-2 text-ink-dim"><div className="max-w-[240px] truncate">{u.last_title || "—"}</div></td>
              <td className="px-3 py-2 font-mono tabular-nums">{u.total_plays.toLocaleString()}</td>
              <td className="whitespace-nowrap px-3 py-2 font-mono tabular-nums text-ink-dim">{fmtDur(u.total_secs)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function BW({ label, v, accent }: { label: string; v: number; accent?: boolean }) {
  return (
    <span className="inline-flex items-baseline gap-1">
      <span className="text-[8.5px] font-bold uppercase tracking-wide text-ink-faint">{label}</span>
      <span style={{ color: accent ? "var(--accent)" : "var(--ink-dim)" }}>{fmtMbps(v)}<span className="text-[9px] text-ink-faint"> Mb/s</span></span>
    </span>
  );
}

function StreamCard({ s, offsetMs, onOpen }: { s: InsightsStream; offsetMs: number; onOpen: () => void }) {
  const d = DECISION[s.decision] ?? DECISION.direct_play;
  const buffering = s.state === "buffering";
  const pct = s.duration_ms > 0 ? Math.min(100, (offsetMs * 100) / s.duration_ms) : s.progress_pct;
  return (
    <button onClick={onOpen} className="flex gap-3 rounded-xl p-3 text-left transition-colors" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="relative h-[104px] w-[70px] flex-none overflow-hidden rounded-md" style={{ background: "var(--panel-2)" }}>
        {s.thumb ? <img src={s.thumb} alt="" className="h-full w-full object-cover" loading="lazy" /> : null}
      </div>
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="truncate text-[13px] font-semibold">{s.title}</div>
            <div className="truncate text-[11px] text-ink-dim">{s.subtitle}</div>
          </div>
          <span className="flex-none rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink, #fff)" }}>{d.label}</span>
        </div>
        <div className="mt-1 truncate text-[11px] text-ink-dim">{s.user} · {s.player || s.platform}</div>
        <div className="truncate font-mono text-[10px] text-ink-faint">{geoLabel(s.geo)} · {fmtMbps(s.bandwidth_kbps)} Mb/s{s.hw_transcode ? " · HW" : ""}</div>
        {/* progress */}
        <div className="mt-auto pt-2">
          <div className="h-1.5 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: buffering ? "var(--avoid)" : "var(--accent)", transition: "width 1s linear" }} />
          </div>
          <div className="mt-1 flex items-center justify-between font-mono text-[9.5px] text-ink-faint">
            <span style={{ color: buffering ? "var(--avoid)" : undefined }}>{buffering ? "● Buffering" : s.state === "paused" ? "⏸ Paused" : "▶ Playing"}</span>
            <span>{fmtClock(offsetMs)} / {fmtClock(s.duration_ms)}</span>
          </div>
        </div>
      </div>
    </button>
  );
}

function DeepDive({ s, onClose }: { s: InsightsStream; onClose: () => void }) {
  const d = DECISION[s.decision] ?? DECISION.direct_play;
  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-10 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="m-0 truncate text-[15px] font-bold">{s.title}</h2>
            <p className="m-0 mt-0.5 text-[11.5px] text-ink-dim">{s.subtitle}</p>
          </div>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>

        <div className="mb-3 flex flex-wrap items-center gap-2">
          <span className="rounded-full px-2.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink, #fff)" }}>{d.label}</span>
          {s.hw_transcode && <span className="rounded-full px-2.5 py-0.5 text-[10px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>HW transcode</span>}
          {s.throttled && <span className="rounded-full px-2.5 py-0.5 text-[10px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Throttled</span>}
        </div>

        {s.reasons && s.reasons.length > 0 && (
          <div className="mb-3 rounded-lg p-3 text-[11.5px]" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }}>
            <div className="mb-1 font-bold uppercase tracking-wide" style={{ fontSize: 9 }}>Why it's transcoding</div>
            <ul className="list-none space-y-0.5 p-0">{s.reasons.map((r, i) => <li key={i}>• {r}</li>)}</ul>
          </div>
        )}

        <div className="grid gap-px overflow-hidden rounded-lg text-[12px]" style={{ background: "var(--line)" }}>
          <DDRow label="User" a={s.user} />
          <DDRow label="Player" a={`${s.player || "—"} · ${s.platform} · ${s.product}`} />
          <DDRow label="Location" a={`${geoLabel(s.geo)}${s.ip ? ` · ${s.ip}` : ""} · ${s.location?.toUpperCase() || "—"}`} />
          <DDRow label="Bandwidth" a={`${fmtMbps(s.bandwidth_kbps)} Mb/s`} />
          <DDRow label="Video" a={s.video.src} b={s.video.stream} />
          <DDRow label="Audio" a={s.audio.src} b={s.audio.stream} />
          <DDRow label="Container" a={s.container.src} b={s.container.stream} />
          <DDRow label="Progress" a={`${fmtClock(s.offset_ms)} / ${fmtClock(s.duration_ms)} (${s.progress_pct}%)`} />
        </div>
      </div>
    </div>
  );
}

function DDRow({ label, a, b }: { label: string; a: string; b?: string }) {
  return (
    <div className="flex items-center justify-between gap-3 px-3 py-2" style={{ background: "var(--panel)" }}>
      <span className="font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{label}</span>
      <span className="text-right text-ink-dim">
        {a}{b ? <span style={{ color: "var(--avoid)" }}> → {b}</span> : null}
      </span>
    </div>
  );
}

/* ============================= HISTORY ============================= */
function FilterGroup({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="font-mono text-[9px] font-bold uppercase tracking-[0.09em] text-ink-faint">{label}</span>
      <div className="flex flex-wrap gap-1">{children}</div>
    </div>
  );
}
function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button onClick={onClick} className="rounded-full px-2.5 py-1 text-[10.5px] font-semibold transition-colors" style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "var(--panel-2)", color: active ? "var(--accent)" : "var(--ink-dim)" }}>
      {children}
    </button>
  );
}

function fmtDate(epoch: number): string {
  if (!epoch) return "—";
  const d = new Date(epoch * 1000);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) + " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}
function fmtDur(secs: number): string {
  if (!secs) return "—";
  const h = Math.floor(secs / 3600), m = Math.round((secs % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}
const TYPE_FILTERS: { key: string; label: string }[] = [
  { key: "", label: "All" }, { key: "movie", label: "Movies" }, { key: "episode", label: "TV" }, { key: "track", label: "Music" },
];
const DEC_FILTERS: { key: string; label: string }[] = [
  { key: "", label: "All" }, { key: "direct_play", label: "Direct Play" }, { key: "direct_stream", label: "Direct Stream" }, { key: "transcode", label: "Transcode" },
];

function HistoryView({ connected, onConfigure }: { connected: boolean; onConfigure: () => void }) {
  const [rows, setRows] = useState<HistoryEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [type, setType] = useState("");
  const [decision, setDecision] = useState("");
  const [q, setQ] = useState("");
  const [loading, setLoading] = useState(true);
  const [detail, setDetail] = useState<HistoryEntry | null>(null);
  const pageSize = 50;

  useEffect(() => { setPage(1); }, [type, decision, q]);
  useEffect(() => {
    if (!connected) return;
    let alive = true;
    setLoading(true);
    const run = () => api.insightsHistory({ type, decision, q, page, page_size: pageSize })
      .then((r) => { if (alive) { setRows(r.rows ?? []); setTotal(r.total); } })
      .catch(() => { if (alive) setRows([]); })
      .finally(() => { if (alive) setLoading(false); });
    const t = setTimeout(run, q ? 300 : 0); // debounce search
    return () => { alive = false; clearTimeout(t); };
  }, [connected, type, decision, q, page]);

  if (!connected) return <ComingSoon tab="history" connected={false} onConfigure={onConfigure} />;

  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(page * pageSize, total);
  const maxPage = Math.max(1, Math.ceil(total / pageSize));

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <FilterGroup label="Type">{TYPE_FILTERS.map((f) => <Chip key={f.key} active={type === f.key} onClick={() => setType(f.key)}>{f.label}</Chip>)}</FilterGroup>
        <FilterGroup label="Stream">{DEC_FILTERS.map((f) => <Chip key={f.key} active={decision === f.key} onClick={() => setDecision(f.key)}>{f.label}</Chip>)}</FilterGroup>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search title or user…" className="ml-auto rounded-lg px-3 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)", minWidth: 200 }} />
      </div>

      <div className="overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
        <table className="w-full border-collapse text-[12.5px]" style={{ minWidth: 820 }}>
          <thead><tr style={{ background: "var(--panel-2)" }}>{["When", "User", "Title", "Player", "Location", "Stream", "Watched", ""].map((h) => <th key={h} className="px-3 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{h}</th>)}</tr></thead>
          <tbody>
            {loading && rows.length === 0 ? (
              <tr><td colSpan={8} className="px-3 py-10 text-center text-[12px] text-ink-dim">Loading history…</td></tr>
            ) : rows.length === 0 ? (
              <tr><td colSpan={8} className="px-3 py-10 text-center text-[12px] text-ink-faint">No plays recorded yet. History fills in as people watch.</td></tr>
            ) : rows.map((r, i) => {
              const d = DECISION[r.decision] ?? DECISION.direct_play;
              return (
                <tr key={r.id} onClick={() => setDetail(r)} className="cursor-pointer" style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-[11px] text-ink-dim">{fmtDate(r.started_at)}</td>
                  <td className="px-3 py-2">{r.user_name}</td>
                  <td className="px-3 py-2"><div className="font-semibold">{r.title}</div>{r.subtitle && <div className="text-[10.5px] text-ink-faint">{r.subtitle}</div>}</td>
                  <td className="px-3 py-2 text-ink-dim">{r.player || r.platform}</td>
                  <td className="px-3 py-2 font-mono text-[10.5px] text-ink-dim">{geoLabel(r.geo)}</td>
                  <td className="px-3 py-2"><span className="rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink, #fff)" }}>{d.label}</span></td>
                  <td className="whitespace-nowrap px-3 py-2 font-mono tabular-nums text-ink-dim">{fmtDur(r.watched_secs)}{r.buffer_count > 0 && <span title={`${r.buffer_count} buffer event(s)`} style={{ color: "var(--avoid)" }}> · ⚠{r.buffer_count}</span>}</td>
                  <td className="px-3 py-2 text-right text-ink-faint">›</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between text-[11.5px] text-ink-faint">
        <span>{total > 0 ? `${from}–${to} of ${total.toLocaleString()}` : "0 plays"}</span>
        <span className="flex items-center gap-2">
          <button disabled={page <= 1} onClick={() => setPage((p) => p - 1)} className="rounded-lg px-2.5 py-1 font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Prev</button>
          <span className="font-mono">{page}/{maxPage}</span>
          <button disabled={page >= maxPage} onClick={() => setPage((p) => p + 1)} className="rounded-lg px-2.5 py-1 font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Next</button>
        </span>
      </div>

      {detail && <HistoryDetail r={detail} onClose={() => setDetail(null)} />}
    </div>
  );
}

function HistoryDetail({ r, onClose }: { r: HistoryEntry; onClose: () => void }) {
  const d = DECISION[r.decision] ?? DECISION.direct_play;
  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-10 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="m-0 truncate text-[15px] font-bold">{r.title}</h2>
            <p className="m-0 mt-0.5 text-[11.5px] text-ink-dim">{r.subtitle}</p>
          </div>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <span className="rounded-full px-2.5 py-0.5 font-mono text-[9.5px] font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink, #fff)" }}>{d.label}</span>
          {r.hw_transcode && <span className="rounded-full px-2.5 py-0.5 text-[10px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>HW transcode</span>}
          {r.buffer_count > 0 && <span className="rounded-full px-2.5 py-0.5 text-[10px] font-semibold" style={{ background: "var(--avoid-soft)", color: "var(--avoid)" }}>{r.buffer_count} buffer event{r.buffer_count === 1 ? "" : "s"}</span>}
        </div>
        <div className="grid gap-px overflow-hidden rounded-lg text-[12px]" style={{ background: "var(--line)" }}>
          <DDRow label="User" a={r.user_name} />
          <DDRow label="Player" a={`${r.player || "—"} · ${r.platform} · ${r.product}`} />
          <DDRow label="Location" a={`${geoLabel(r.geo)}${r.ip_address ? ` · ${r.ip_address}` : ""} · ${r.location?.toUpperCase() || "—"}`} />
          <DDRow label="Started" a={fmtDate(r.started_at)} />
          <DDRow label="Watched" a={`${fmtDur(r.watched_secs)}${r.paused_ms > 0 ? ` (paused ${fmtDur(Math.round(r.paused_ms / 1000))})` : ""}`} />
          <DDRow label="Video" a={r.video_src} b={r.video_stream} />
          <DDRow label="Audio" a={r.audio_src} b={r.audio_stream} />
          <DDRow label="Container" a={r.container_src} b={r.container_stream} />
        </div>
      </div>
    </div>
  );
}

/* ============================= GRAPHS ============================= */
const SERIES_COLORS = { tv: "var(--accent)", movies: "var(--good)", music: "var(--avoid)" };
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function GraphsView({ connected, onConfigure }: { connected: boolean; onConfigure: () => void }) {
  const [g, setG] = useState<InsightsGraphs | null>(null);
  const [win, setWin] = useState(30);
  useEffect(() => { if (connected) api.insightsGraphs(win).then(setG).catch(() => setG(null)); }, [connected, win]);
  if (!connected) return <ComingSoon tab="graphs" connected={false} onConfigure={onConfigure} />;
  if (!g) return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading graphs…</div>;

  const anyPlays = g.daily_tv.some(Boolean) || g.daily_movies.some(Boolean) || g.daily_music.some(Boolean);
  const dayLabels = g.days.map((d) => { const [, m, day] = d.split("-"); return `${+m}/${+day}`; });

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <div className="flex gap-1">{[7, 30, 90].map((w) => <Chip key={w} active={win === w} onClick={() => setWin(w)}>{w}d</Chip>)}</div>
        <div className="flex items-center gap-3 text-[10.5px]">
          <Legend c={SERIES_COLORS.tv} label="TV" /><Legend c={SERIES_COLORS.movies} label="Movies" /><Legend c={SERIES_COLORS.music} label="Music" />
        </div>
      </div>

      {!anyPlays ? (
        <div className="rounded-xl p-10 text-center text-[12px] text-ink-faint" style={{ border: "1px dashed var(--line)" }}>No plays in this window yet — graphs fill in as history accrues.</div>
      ) : (
        <>
          <ChartCard title="Daily plays by media type">
            <LineChart xLabels={dayLabels} series={[
              { color: SERIES_COLORS.tv, values: g.daily_tv },
              { color: SERIES_COLORS.movies, values: g.daily_movies },
              { color: SERIES_COLORS.music, values: g.daily_music },
            ]} />
          </ChartCard>

          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(320px, 1fr))" }}>
            <ChartCard title="Plays by day of week"><BarChart values={g.by_day_of_week} labels={DOW} /></ChartCard>
            <ChartCard title="Plays by hour of day"><BarChart values={g.by_hour} labels={g.by_hour.map((_, i) => (i % 3 === 0 ? String(i).padStart(2, "0") : ""))} /></ChartCard>
            <ChartCard title="Top platforms"><HBarChart rows={g.top_platforms.map((p) => ({ label: p.name, value: p.plays }))} /></ChartCard>
            <ChartCard title="Top users"><HBarChart rows={g.top_users.map((u) => ({ label: u.name, value: u.plays }))} /></ChartCard>
          </div>
        </>
      )}

      <ChartCard title="Bandwidth (peak per hour)">
        {g.bandwidth.length === 0 ? <div className="py-6 text-center text-[11px] text-ink-faint">No bandwidth recorded yet.</div> : (
          <>
            <div className="mb-1 flex items-center gap-3 text-[10.5px]"><Legend c="var(--accent)" label="Total" /><Legend c="var(--good)" label="LAN" /><Legend c="var(--avoid)" label="WAN" /></div>
            <LineChart xLabels={g.bandwidth.map(() => "")} series={[
              { color: "var(--accent)", values: g.bandwidth.map((p) => p.total_kbps / 1000) },
              { color: "var(--good)", values: g.bandwidth.map((p) => p.lan_kbps / 1000) },
              { color: "var(--avoid)", values: g.bandwidth.map((p) => p.wan_kbps / 1000) },
            ]} unit=" Mb/s" />
          </>
        )}
      </ChartCard>
    </div>
  );
}

function Legend({ c, label }: { c: string; label: string }) {
  return <span className="inline-flex items-center gap-1 text-ink-dim"><span className="inline-block h-2 w-2 rounded-full" style={{ background: c }} />{label}</span>;
}
function ChartCard({ title, children }: { title: string; children: ReactNode }) {
  return <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}><div className="mb-2 text-[12.5px] font-bold">{title}</div>{children}</div>;
}

function LineChart({ series, xLabels, unit }: { series: { color: string; values: number[] }[]; xLabels: string[]; unit?: string }) {
  const W = 720, H = 200, padL = 34, padB = 20, padT = 8, padR = 8;
  const n = Math.max(1, xLabels.length);
  const max = Math.max(1, ...series.flatMap((s) => s.values));
  const x = (i: number) => padL + (n <= 1 ? 0 : (i / (n - 1)) * (W - padL - padR));
  const y = (v: number) => padT + (1 - v / max) * (H - padT - padB);
  const ticks = [0, Math.round(max / 2), max];
  const labelEvery = Math.max(1, Math.ceil(n / 8));
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full" style={{ height: "auto", maxHeight: 240 }} preserveAspectRatio="none">
      {ticks.map((t, i) => (
        <g key={i}>
          <line x1={padL} x2={W - padR} y1={y(t)} y2={y(t)} stroke="var(--line)" strokeWidth={1} />
          <text x={padL - 5} y={y(t) + 3} textAnchor="end" fontSize={9} fill="var(--ink-faint)">{t}{unit || ""}</text>
        </g>
      ))}
      {series.map((s, si) => (
        <polyline key={si} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" points={s.values.map((v, i) => `${x(i)},${y(v)}`).join(" ")} />
      ))}
      {xLabels.map((l, i) => (l && i % labelEvery === 0 ? <text key={i} x={x(i)} y={H - 6} textAnchor="middle" fontSize={9} fill="var(--ink-faint)">{l}</text> : null))}
    </svg>
  );
}

function BarChart({ values, labels }: { values: number[]; labels: string[] }) {
  const W = 360, H = 180, padL = 24, padB = 18, padT = 6, padR = 4;
  const max = Math.max(1, ...values);
  const bw = (W - padL - padR) / values.length;
  const y = (v: number) => padT + (1 - v / max) * (H - padT - padB);
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full" style={{ height: "auto", maxHeight: 200 }}>
      <line x1={padL} x2={W - padR} y1={H - padB} y2={H - padB} stroke="var(--line)" strokeWidth={1} />
      {values.map((v, i) => (
        <rect key={i} x={padL + i * bw + bw * 0.15} y={y(v)} width={bw * 0.7} height={Math.max(0, H - padB - y(v))} rx={1.5} fill="var(--accent)" />
      ))}
      {labels.map((l, i) => (l ? <text key={i} x={padL + i * bw + bw / 2} y={H - 5} textAnchor="middle" fontSize={8.5} fill="var(--ink-faint)">{l}</text> : null))}
    </svg>
  );
}

function HBarChart({ rows }: { rows: { label: string; value: number }[] }) {
  if (rows.length === 0) return <div className="py-6 text-center text-[11px] text-ink-faint">No data yet.</div>;
  const max = Math.max(1, ...rows.map((r) => r.value));
  return (
    <div className="flex flex-col gap-1.5">
      {rows.map((r, i) => (
        <div key={i} className="flex items-center gap-2 text-[11px]">
          <span className="w-[92px] flex-none truncate text-ink-dim" title={r.label}>{r.label || "—"}</span>
          <span className="h-3.5 flex-1 overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
            <span className="block h-full rounded" style={{ width: `${(r.value / max) * 100}%`, background: "var(--accent)" }} />
          </span>
          <span className="w-8 flex-none text-right font-mono text-ink-faint">{r.value}</span>
        </div>
      ))}
    </div>
  );
}

/* ============================= RELIABILITY (buffering history) ============================= */
function ReliabilityView({ connected, onConfigure }: { connected: boolean; onConfigure: () => void }) {
  const [r, setR] = useState<Reliability | null>(null);
  const [win, setWin] = useState(30);
  useEffect(() => { if (connected) api.insightsReliability(win).then(setR).catch(() => setR(null)); }, [connected, win]);
  if (!connected) return <ComingSoon tab="reliability" connected={false} onConfigure={onConfigure} />;
  if (!r) return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading reliability…</div>;

  const s = r.summary;
  const clean = s.total_events === 0;
  const rateColor = s.buffer_rate_pct === 0 ? "var(--good)" : s.buffer_rate_pct < 10 ? "var(--avoid)" : "var(--reject)";

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <p className="max-w-[54ch] text-[12px] text-ink-dim">Where and when streams stuttered — buffering logged as it happens, so you can see who's affected, on what, and why.</p>
        <div className="flex gap-1">{[7, 30, 90].map((w) => <Chip key={w} active={win === w} onClick={() => setWin(w)}>{w}d</Chip>)}</div>
      </div>

      {/* Summary tiles */}
      <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))" }}>
        <StatTile label="Buffer rate" value={`${s.buffer_rate_pct}%`} sub={`${s.buffered_sessions} of ${s.total_sessions} streams`} color={rateColor} />
        <StatTile label="Buffer events" value={s.total_events.toLocaleString()} sub="spells logged" color={s.total_events ? "var(--avoid)" : "var(--good)"} />
        <StatTile label="Streams affected" value={s.buffered_sessions.toLocaleString()} sub={`of ${s.total_sessions.toLocaleString()} total`} color={s.buffered_sessions ? "var(--avoid)" : "var(--good)"} />
      </div>

      {clean ? (
        <div className="rounded-xl p-12 text-center" style={{ border: "1px dashed var(--line)", background: "var(--panel)" }}>
          <div className="text-[26px]">✓</div>
          <div className="mt-1 text-[13.5px] font-bold" style={{ color: "var(--good)" }}>Smooth sailing</div>
          <p className="mx-auto mt-1 max-w-[46ch] text-[12px] text-ink-dim">No buffering recorded in this window. When a stream stutters, the spell is logged here with who, what, and how it was playing — so you can pinpoint bad clients, heavy transcodes, or shaky connections.</p>
        </div>
      ) : (
        <>
          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(300px, 1fr))" }}>
            <OffenderCard title="Worst-hit users" rows={r.by_user} />
            <OffenderCard title="Worst-hit platforms" rows={r.by_platform} />
            <OffenderCard title="Worst-hit titles" rows={r.by_title} />
          </div>

          {/* Why streams buffered — diagnosed cause breakdown */}
          {r.causes.length > 0 && (
            <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
              <div className="mb-2 text-[12.5px] font-bold">Why streams buffered</div>
              <div className="flex flex-wrap gap-2">
                {r.causes.map((c) => {
                  const cc = CAUSE[c.cause] ?? CAUSE.unknown;
                  return (
                    <span key={c.cause} className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-semibold" style={{ background: "var(--panel-2)", border: `1px solid ${cc.color}` }}>
                      <span className="inline-block h-2 w-2 rounded-full" style={{ background: cc.color }} />
                      {cc.label}<span className="font-mono text-ink-faint">{c.count}</span>
                    </span>
                  );
                })}
              </div>
            </div>
          )}

          {/* Timeline */}
          <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
            <div className="mb-2 text-[12.5px] font-bold">Recent buffer events</div>
            <div className="flex flex-col">
              {r.events.map((e, i) => {
                const d = DECISION[e.decision] ?? DECISION.direct_play;
                const cc = CAUSE[e.cause] ?? CAUSE.unknown;
                return (
                  <div key={i} className="py-1.5 text-[12px]" style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                    <div className="flex items-center gap-3">
                      <span className="w-2 flex-none"><span className="inline-block h-2 w-2 rounded-full" style={{ background: cc.color }} /></span>
                      <span className="w-[92px] flex-none font-mono text-[10.5px] text-ink-faint">{fmtDate(e.at)}</span>
                      <span className="min-w-0 flex-1 truncate"><b className="font-semibold">{e.user}</b> · {e.title}</span>
                      <span className="flex-none font-mono text-[10px] text-ink-faint">@ {fmtClock(e.offset_ms)}</span>
                      <span className="flex-none rounded-full px-2 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink, #fff)" }}>{d.label}</span>
                    </div>
                    {e.detail && <div className="pl-[112px] text-[11px]" style={{ color: cc.color }}>{e.detail}</div>}
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

function StatTile({ label, value, sub, color }: { label: string; value: string; sub: string; color: string }) {
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{label}</div>
      <div className="mt-1 text-[26px] font-extrabold tracking-tight" style={{ color }}>{value}</div>
      <div className="text-[11px] text-ink-faint">{sub}</div>
    </div>
  );
}

function OffenderCard({ title, rows }: { title: string; rows: BufferGroup[] }) {
  const max = Math.max(1, ...rows.map((r) => r.events));
  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="mb-2 text-[12.5px] font-bold">{title}</div>
      {rows.length === 0 ? <div className="text-[11px] text-ink-faint">—</div> : (
        <div className="flex flex-col gap-2">
          {rows.map((r, i) => (
            <div key={i} className="text-[11.5px]">
              <div className="flex items-center justify-between gap-2">
                <span className="min-w-0 flex-1 truncate" title={r.name}>{r.name || "—"}</span>
                <span className="flex-none font-mono text-ink-dim">{r.events} event{r.events === 1 ? "" : "s"} · {r.rate_pct}%</span>
              </div>
              <span className="mt-0.5 block h-1.5 w-full overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }}>
                <span className="block h-full rounded-full" style={{ width: `${(r.events / max) * 100}%`, background: "var(--avoid)" }} />
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/* ============================= NOTIFICATIONS (admin) ============================= */
const EVENTS: { key: keyof NotificationConn; label: string }[] = [
  { key: "on_grab", label: "Grabbed" },
  { key: "on_import", label: "Imported" },
  { key: "on_stream", label: "Stream started" },
  { key: "on_buffering", label: "Buffering" },
];
const BLANK_CONN: NotificationConn = { name: "", kind: "", url: "", on_grab: false, on_import: true, on_stream: false, on_buffering: false, enabled: true };

function NotificationsView({ flash }: { flash: (m: string) => void }) {
  const [conns, setConns] = useState<NotificationConn[] | null>(null);
  const [adding, setAdding] = useState(false);
  const load = () => api.notifications().then(setConns).catch(() => setConns([]));
  useEffect(() => { load(); }, []);
  if (!conns) return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading…</div>;

  return (
    <div className="flex flex-col gap-3" style={{ maxWidth: 640 }}>
      <p className="text-[12px] text-ink-dim">Send alerts anywhere via <b>Apprise</b> — Discord, Telegram, email, ntfy, Slack and 80+ more. Each connection is one <a href="https://github.com/caronc/apprise/wiki" target="_blank" rel="noreferrer" style={{ color: "var(--accent)" }}>Apprise URL</a> (e.g. <code>discord://id/token</code>, <code>tgram://token/chatid</code>, <code>ntfy://topic</code>).</p>

      {conns.map((c) => <ConnCard key={c.id} conn={c} onChange={load} flash={flash} />)}

      {adding ? (
        <ConnCard conn={BLANK_CONN} isNew onChange={() => { setAdding(false); load(); }} onCancel={() => setAdding(false)} flash={flash} />
      ) : (
        <button onClick={() => setAdding(true)} className="self-start rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>+ Add notification</button>
      )}
    </div>
  );
}

function ConnCard({ conn, isNew, onChange, onCancel, flash }: { conn: NotificationConn; isNew?: boolean; onChange: () => void; onCancel?: () => void; flash: (m: string) => void }) {
  const [c, setC] = useState<NotificationConn>(conn);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const set = (patch: Partial<NotificationConn>) => setC((p) => ({ ...p, ...patch }));

  const save = async () => {
    if (!c.name.trim() || !c.url.trim()) { flash("Name and Apprise URL are required"); return; }
    setBusy("save");
    try {
      if (isNew) await api.createNotification(c);
      else await api.updateNotification(c.id!, c);
      flash("Saved"); onChange();
    } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const test = async () => {
    if (!c.url.trim()) { flash("Enter an Apprise URL first"); return; }
    setBusy("test");
    try { const r = await api.testNotification(c); flash(r.ok ? "✓ Test sent" : `✕ ${r.error || "failed"}`); }
    catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const del = async () => { if (c.id && confirm(`Delete "${c.name}"?`)) { await api.deleteNotification(c.id); onChange(); } };

  return (
    <div className="rounded-xl p-4" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="flex flex-wrap gap-2">
        <input value={c.name} onChange={(e) => set({ name: e.target.value })} placeholder="Name (e.g. My Discord)" className="flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={{ minWidth: 160, background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <label className="flex cursor-pointer items-center gap-1.5 text-[11.5px]">
          <input type="checkbox" checked={c.enabled} onChange={(e) => set({ enabled: e.target.checked })} /> Enabled
        </label>
      </div>
      <input value={c.url} onChange={(e) => set({ url: e.target.value })} placeholder="discord://webhook_id/token" className="mt-2 w-full rounded-lg px-2.5 py-1.5 font-mono text-[11.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
      <div className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1.5">
        {EVENTS.map((ev) => (
          <label key={ev.key} className="flex cursor-pointer items-center gap-1.5 text-[11.5px] text-ink-dim">
            <input type="checkbox" checked={!!c[ev.key]} onChange={(e) => set({ [ev.key]: e.target.checked } as Partial<NotificationConn>)} /> {ev.label}
          </label>
        ))}
      </div>
      <div className="mt-3 flex items-center gap-2">
        <button onClick={save} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy === "save" ? "Saving…" : isNew ? "Add" : "Save"}</button>
        <button onClick={test} disabled={busy !== null} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{busy === "test" ? "Testing…" : "Test"}</button>
        <span className="flex-1" />
        {isNew ? <button onClick={onCancel} className="text-[12px] text-ink-faint">Cancel</button> : <button onClick={del} className="text-[12px]" style={{ color: "var(--reject)" }}>Delete</button>}
      </div>
    </div>
  );
}

const NEXT: Record<string, string> = {
  activity: "Live now-playing — who's streaming what, on which device, with progress, transcode decision, bandwidth and geolocation.",
  history: "Every play recorded — a filterable table with stream-type, geolocated IP and a click-through deep-dive.",
  users: "Per-user activity — last seen, platform, total plays and watch time.",
  graphs: "Plays by day, hour, platform and user, plus bandwidth over time.",
  reliability: "The buffering view — see historically when and where streams choked, by user, platform and title.",
};

function ComingSoon({ tab, connected, onConfigure }: { tab: string; connected: boolean; onConfigure: () => void }) {
  return (
    <div className="rounded-xl p-10 text-center" style={{ border: "1px dashed var(--line)", background: "var(--panel)" }}>
      <div className="text-[13.5px] font-bold capitalize">{tab}</div>
      <p className="mx-auto mt-1.5 max-w-[52ch] text-[12px] text-ink-dim">{NEXT[tab]}</p>
      <div className="mt-3 inline-flex items-center gap-2 rounded-full px-3 py-1 font-mono text-[10px] font-bold uppercase tracking-wide" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>Coming soon</div>
      {!connected && <div className="mt-4"><button onClick={onConfigure} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Connect your Plex server →</button></div>}
    </div>
  );
}

const inp = "w-full rounded-lg px-3 py-2 text-[13px]";
const inpStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

function PlexSettings({ cfg, onSaved, flash }: { cfg: PlexConfig | null; onSaved: (c: PlexConfig) => void; flash: (m: string) => void }) {
  const [url, setUrl] = useState("");
  const [token, setToken] = useState("");
  const [poll, setPoll] = useState("5");
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);
  const [test, setTest] = useState<PlexTestResult | null>(null);
  const [signingIn, setSigningIn] = useState(false);

  useEffect(() => {
    if (!cfg) return;
    setUrl(cfg.url);
    setPoll(String(cfg.poll_seconds || 5));
    setEnabled(cfg.enabled);
  }, [cfg]);

  // Sign in with Plex: open plex.tv's auth popup, then poll until the user approves,
  // at which point the token (and server URL, if unset) are stored on the server.
  const signIn = async () => {
    setSigningIn(true);
    setTest(null);
    try {
      const { id, auth_url } = await api.insightsPlexAuthStart();
      const popup = window.open(auth_url, "plex-auth", "width=800,height=720");
      const deadline = Date.now() + 3 * 60 * 1000;
      // eslint-disable-next-line no-constant-condition
      while (true) {
        await new Promise((r) => setTimeout(r, 2000));
        if (Date.now() > deadline) { flash("Plex sign-in timed out — try again."); break; }
        let authorized = false;
        try { authorized = (await api.insightsPlexAuthPoll(id)).authorized; } catch { /* keep polling */ }
        if (authorized) {
          popup?.close();
          const c = await api.insightsConfig();
          onSaved(c);
          setUrl(c.url);
          flash("Signed in with Plex ✓");
          break;
        }
        if (popup && popup.closed) { flash("Sign-in window closed before finishing."); break; }
      }
    } catch (e) {
      flash((e as Error).message);
    } finally {
      setSigningIn(false);
    }
  };

  const body = () => ({ url: url.trim(), token: token.trim() || undefined, enabled, poll_seconds: Number(poll) || 5 });

  const save = async () => {
    setBusy("save");
    try { const c = await api.updateInsightsConfig(body()); onSaved(c); setToken(""); flash("Plex settings saved"); }
    catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };
  const runTest = async () => {
    setBusy("test"); setTest(null);
    try { setTest(await api.testInsights({ url: url.trim() || undefined, token: token.trim() || undefined })); }
    catch (e) { setTest({ ok: false, error: (e as Error).message }); } finally { setBusy(null); }
  };

  return (
    <div className="grid gap-4" style={{ gridTemplateColumns: "minmax(0,1fr)" }}>
      <div className="rounded-xl p-5" style={{ border: "1px solid var(--line)", background: "var(--panel)", maxWidth: 560 }}>
        <div className="text-[13.5px] font-bold">Plex connection</div>
        <div className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">Point Arrmada at your Plex Media Server. Your token stays on this server and is never shown back in full.</div>

        {/* One-click sign-in — no token hunting. */}
        <button onClick={signIn} disabled={signingIn} className="mb-4 flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-[13px] font-semibold disabled:opacity-60" style={{ background: "#e5a00d", color: "#1f1200" }}>
          {signingIn ? "Waiting for Plex…" : (cfg?.token_set ? "Re-sign in with Plex" : "Sign in with Plex")}
        </button>
        <div className="mb-4 flex items-center gap-2 text-[10.5px] text-ink-faint">
          <span className="h-px flex-1" style={{ background: "var(--line)" }} /> or enter manually <span className="h-px flex-1" style={{ background: "var(--line)" }} />
        </div>

        <label className="mb-3 block">
          <span className="mb-1 block text-[12px] font-semibold">Server URL</span>
          <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.1.10:32400" className={inp} style={inpStyle} />
        </label>

        <label className="mb-3 block">
          <span className="mb-1 block text-[12px] font-semibold">X-Plex-Token</span>
          <input value={token} onChange={(e) => setToken(e.target.value)} type="password" placeholder={cfg?.token_set ? "•••••••••• (saved — leave blank to keep)" : "paste your token"} className={inp} style={inpStyle} />
          <span className="mt-1 block text-[10.5px] text-ink-faint">In Plex web: play an item → ⋯ → Get Info → View XML — the URL ends with <code>X-Plex-Token=…</code></span>
        </label>

        <div className="mb-4 flex items-center gap-4">
          <label className="block">
            <span className="mb-1 block text-[12px] font-semibold">Poll interval</span>
            <span className="flex items-center gap-1.5"><input value={poll} onChange={(e) => setPoll(e.target.value)} type="number" min="2" max="60" className="w-[70px] rounded-lg px-2.5 py-1.5 text-[12px]" style={inpStyle} /><span className="text-[11px] text-ink-faint">seconds</span></span>
          </label>
          <label className="flex cursor-pointer items-center gap-2 pt-4 text-[12px]">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            <span><b className="font-semibold">Enable monitoring</b><span className="block text-[10.5px] text-ink-faint">record activity in the background</span></span>
          </label>
        </div>

        <div className="flex items-center gap-2">
          <button onClick={runTest} disabled={busy !== null || !url.trim()} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{busy === "test" ? "Testing…" : "Test connection"}</button>
          <button onClick={save} disabled={busy !== null || !url.trim()} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy === "save" ? "Saving…" : "Save"}</button>
        </div>

        {test && (
          <div className="mt-4 rounded-lg p-3 text-[12px]" style={{ border: `1px solid ${test.ok ? "var(--good)" : "var(--reject)"}`, background: test.ok ? "var(--good-soft, rgba(127,176,105,.12))" : "var(--reject-soft)", color: test.ok ? "var(--good)" : "var(--reject)" }}>
            {test.ok ? (
              <div>
                <div className="font-semibold">✓ Connected{test.version ? ` · Plex ${test.version}` : ""}</div>
                {test.libraries && test.libraries.length > 0 && (
                  <div className="mt-1 text-ink-dim">Libraries: {test.libraries.map((l) => l.title).join(", ")}</div>
                )}
              </div>
            ) : (
              <div className="font-semibold">✕ {test.error || "Connection failed"}</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
