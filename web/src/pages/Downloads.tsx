import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type ActivityDownload, type ClientSettings, type SearchingItem } from "../lib/api";
import { useMe } from "../lib/me";

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
// fmtReleaseDate renders a YYYY-MM-DD release date as e.g. "Jul 4, 2026" (year
// only when the day is unknown), parsed as a plain date to avoid TZ drift.
function fmtReleaseDate(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number);
  if (!y) return iso;
  if (!m || !d) return String(y);
  const dt = new Date(y, m - 1, d);
  return dt.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

const STATE_TONE: Record<string, string> = {
  downloading: "var(--accent)", seeding: "var(--good)", paused: "var(--ink-faint)", error: "var(--reject)", checking: "var(--avoid)",
};

type SortKey = "name" | "progress" | "speed" | "size";

function ProfileChip({ profile }: { profile: string }) {
  const na = profile === "n/a";
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: na ? "var(--panel-2)" : "var(--accent-soft)", color: na ? "var(--ink-faint)" : "var(--accent)" }}>{profile}</span>;
}

export function Downloads() {
  const [searching, setSearching] = useState<SearchingItem[]>([]);
  const [upcoming, setUpcoming] = useState<SearchingItem[]>([]);
  const [downloads, setDownloads] = useState<ActivityDownload[]>([]);
  const [totals, setTotals] = useState<{ down_speed: number; up_speed: number; active: number }>({ down_speed: 0, up_speed: 0, active: 0 });
  const [freeGb, setFreeGb] = useState<number | null>(null);
  const [reconnecting, setReconnecting] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SortKey>("progress");
  const [typeFilter, setTypeFilter] = useState<MediaFilter>("all");
  const { musicEnabled } = useMe();
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [clientId, setClientId] = useState<number | null>(null);
  const [showSettings, setShowSettings] = useState(false);

  useEffect(() => {
    api.downloadClients().then((cs) => {
      const qb = cs.find((c) => c.kind === "qbittorrent") ?? cs[0];
      if (qb) setClientId(qb.id);
    }).catch(() => {});
  }, []);

  useEffect(() => {
    let alive = true;
    let fails = 0;
    const load = () =>
      api.activity().then((a) => {
        if (!alive) return;
        fails = 0;
        setSearching(a.searching ?? []);
        setUpcoming(a.upcoming ?? []);
        setDownloads(a.downloads ?? []);
        if (a.totals) setTotals(a.totals);
        if (typeof a.free_gb === "number") setFreeGb(a.free_gb);
        setReconnecting(false);
        setLoaded(true);
      }).catch(() => {
        if (!alive) return;
        fails += 1;
        setLoaded(true);
        if (fails >= 2) setReconnecting(true);
      });
    load();
    const t = setInterval(load, 3000);
    return () => { alive = false; clearInterval(t); };
  }, []);

  const act = async (hash: string, fn: () => Promise<unknown>) => {
    setBusy((b) => ({ ...b, [hash]: true }));
    try { await fn(); } catch { /* next poll reflects reality */ } finally { setBusy((b) => ({ ...b, [hash]: false })); }
  };

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    let list = typeFilter === "all" ? downloads : downloads.filter((d) => (d.media_type ?? "movie") === typeFilter);
    list = q ? list.filter((d) => d.name.toLowerCase().includes(q)) : [...list];
    list.sort((a, b) => {
      switch (sort) {
        case "name": return a.name.localeCompare(b.name);
        case "speed": return b.down_speed - a.down_speed;
        case "size": return b.size_bytes - a.size_bytes;
        default: return b.progress - a.progress;
      }
    });
    return list;
  }, [downloads, query, sort, typeFilter]);

  const typeCount = (k: MediaFilter) => (k === "all" ? downloads.length : downloads.filter((d) => (d.media_type ?? "movie") === k).length);

  const shownSearching = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? searching.filter((s) => s.title.toLowerCase().includes(q)) : searching;
  }, [searching, query]);

  const shownUpcoming = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = q ? upcoming.filter((s) => s.title.toLowerCase().includes(q)) : upcoming;
    return [...list].sort((a, b) => (a.available_at ?? "").localeCompare(b.available_at ?? ""));
  }, [upcoming, query]);

  const empty = loaded && searching.length === 0 && upcoming.length === 0 && downloads.length === 0;

  return (
    <>
      <PageHeader title="Downloads" crumb="Transfers" />
      <div className="mx-auto w-full max-w-[1360px] px-4 py-6 sm:px-6">
        {/* Header: live totals + free disk + controls */}
        <div className="mb-4 flex flex-wrap items-center gap-x-5 gap-y-2 rounded-xl px-4 py-2.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
          <span className="flex items-center gap-1.5 font-mono text-[11px]" style={{ color: reconnecting ? "var(--avoid)" : "var(--ink-faint)" }}>
            <span className="inline-block h-1.5 w-1.5 rounded-full" style={{ background: reconnecting ? "var(--avoid)" : "var(--accent)" }} />
            {reconnecting ? "Reconnecting…" : "Live"}
          </span>
          <Stat label="↓" value={`${bytes(totals.down_speed)}/s`} tone="var(--accent)" />
          <Stat label="↑" value={`${bytes(totals.up_speed)}/s`} tone="var(--good)" />
          <Stat label="active" value={String(totals.active)} />
          {freeGb != null && <Stat label="free" value={`${freeGb.toFixed(0)} GB`} tone={freeGb < 20 ? "var(--reject)" : undefined} />}
          <div className="ml-auto flex items-center gap-2">
            <button onClick={() => act("all", () => api.pauseDownload("all"))} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Pause all</button>
            <button onClick={() => act("all", () => api.resumeDownload("all"))} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Resume all</button>
            {clientId != null && (
              <button onClick={() => setShowSettings((s) => !s)} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: `1px solid ${showSettings ? "var(--accent)" : "var(--line)"}`, color: showSettings ? "var(--accent)" : "var(--ink-dim)" }}>⚙ Speed & limits</button>
            )}
          </div>
        </div>

        {showSettings && clientId != null && <SettingsPanel clientId={clientId} onClose={() => setShowSettings(false)} />}

        <div className="mb-3 flex flex-wrap gap-2">
          {TYPE_PILLS.filter((p) => p.key !== "music" || musicEnabled).map((p) => {
            const active = typeFilter === p.key;
            return (
              <button key={p.key} onClick={() => setTypeFilter(p.key)} className="rounded-full px-3 py-1 text-[12px] font-semibold" style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "var(--panel)", color: active ? "var(--accent)" : "var(--ink-faint)" }}>
                {p.label} <span className="font-mono text-[10.5px] opacity-70">{typeCount(p.key)}</span>
              </button>
            );
          })}
        </div>

        <div className="mb-3 flex items-center gap-2">
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search downloads…" className="w-[220px] rounded-lg px-3 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <select value={sort} onChange={(e) => setSort(e.target.value as SortKey)} className="rounded-lg px-2.5 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
            <option value="progress">Sort: Progress</option>
            <option value="name">Sort: Name</option>
            <option value="speed">Sort: Speed</option>
            <option value="size">Sort: Size</option>
          </select>
        </div>

        {empty ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            Nothing happening. Add a movie or grab a release and it'll show up here.
          </div>
        ) : (
          <>
            {shownSearching.length > 0 && (
              <div className="mb-5">
                <div className="mb-2 font-mono text-[9.5px] font-bold uppercase tracking-[0.08em] text-ink-faint">Searching · {shownSearching.length}</div>
                <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
                  {shownSearching.map((s) => (
                    <Link key={s.movie_id} to={`/movies/${s.movie_id}`} className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-[var(--panel-2)]" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
                      <span className="inline-block h-2 w-2 flex-none animate-pulse rounded-full" style={{ background: "var(--avoid)" }} />
                      <div className="min-w-0 flex-1"><div className="truncate text-[12.5px] font-medium">{s.title} <span className="font-mono text-[10.5px] text-ink-faint">{s.year || ""}</span></div></div>
                      <ProfileChip profile={s.quality_profile} />
                      <span className="font-mono text-[10px] uppercase" style={{ color: "var(--avoid)" }}>Searching…</span>
                    </Link>
                  ))}
                </div>
              </div>
            )}

            {shownUpcoming.length > 0 && (
              <div className="mb-5">
                <div className="mb-2 font-mono text-[9.5px] font-bold uppercase tracking-[0.08em] text-ink-faint">Upcoming · {shownUpcoming.length}</div>
                <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
                  {shownUpcoming.map((s) => (
                    <Link key={s.movie_id} to={`/movies/${s.movie_id}`} className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-[var(--panel-2)]" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
                      <span className="inline-block h-2 w-2 flex-none rounded-full" style={{ background: "var(--ink-faint)" }} />
                      <div className="min-w-0 flex-1"><div className="truncate text-[12.5px] font-medium">{s.title} <span className="font-mono text-[10.5px] text-ink-faint">{s.year || ""}</span></div></div>
                      <ProfileChip profile={s.quality_profile} />
                      <span className="font-mono text-[10px] uppercase text-ink-faint">{s.available_at ? `Available ${fmtReleaseDate(s.available_at)}` : "Awaiting release"}</span>
                    </Link>
                  ))}
                </div>
              </div>
            )}

            {shown.length > 0 && (
              <div>
                <div className="mb-2 font-mono text-[9.5px] font-bold uppercase tracking-[0.08em] text-ink-faint">Downloads · {shown.length}</div>
                <div className="flex flex-col gap-2">
                  {shown.map((it) => {
                    const paused = it.state === "paused";
                    const b = !!busy[it.hash];
                    return (
                      <div key={it.hash} className="rounded-xl p-3.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
                        <div className="flex items-center gap-3">
                          <div className="min-w-0 flex-1">
                            <div className="truncate font-mono text-[11.5px]" title={it.name}>{it.name}</div>
                            <div className="mt-1 flex flex-wrap items-center gap-2">
                              <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: STATE_TONE[it.state] ?? "var(--ink-faint)" }}>{it.state}</span>
                              <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{it.media_type === "series" ? "TV" : it.media_type === "book" ? "Book" : it.media_type === "music" ? "Music" : "Movie"}</span>
                              <ProfileChip profile={it.quality_profile} />
                              <span className="font-mono text-[10px] text-ink-faint">{bytes(it.size_bytes)}</span>
                              <span className="font-mono text-[10.5px]" style={{ color: it.down_speed > 0 ? "var(--accent)" : "var(--ink-faint)" }}>{it.down_speed > 0 ? `↓${bytes(it.down_speed)}/s` : it.up_speed > 0 ? `↑${bytes(it.up_speed)}/s` : "—"}</span>
                              <span className="font-mono text-[10.5px] text-ink-faint">ETA {eta(it.eta_seconds)}</span>
                            </div>
                          </div>
                          <div className="flex flex-none items-center gap-1">
                            <IconBtn label={paused ? "Resume" : "Pause"} disabled={b} onClick={() => act(it.hash, () => (paused ? api.resumeDownload(it.hash) : api.pauseDownload(it.hash)))} />
                            <IconBtn label="↑" title="Move up the queue" disabled={b} onClick={() => act(it.hash, () => api.torrentAction(it.hash, "prio_up"))} />
                            <IconBtn label="↓" title="Move down the queue" disabled={b} onClick={() => act(it.hash, () => api.torrentAction(it.hash, "prio_down"))} />
                            <IconBtn label="Block" tone="var(--avoid)" title="Blocklist this release and grab a different one" disabled={b} onClick={() => act(it.hash, () => api.blockDownload(it.hash, it.name))} />
                            <IconBtn label="Delete" tone="var(--reject)" disabled={b} onClick={() => act(it.hash, () => api.deleteDownload(it.hash, true))} />
                          </div>
                        </div>
                        <div className="mt-2.5 flex items-center gap-2">
                          <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
                            <div className="h-full rounded-full" style={{ width: `${Math.round(it.progress * 100)}%`, background: it.state === "seeding" ? "var(--good)" : paused ? "var(--ink-faint)" : "var(--accent)" }} />
                          </div>
                          <span className="w-10 text-right font-mono text-[10.5px] text-ink-dim">{Math.round(it.progress * 100)}%</span>
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return <span className="font-mono text-[11.5px]"><span className="text-ink-faint">{label} </span><span style={{ color: tone ?? "var(--ink)" }}>{value}</span></span>;
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
