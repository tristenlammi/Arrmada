import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { useTabParam } from "../lib/useTabParam";
import { TabPanel, Tabs, type TabItem } from "../ui/Tabs";
import { LINKS } from "../lib/links";
import { RemoveDownloadDialog, removedMessage } from "../components/RemoveDownloadDialog";
import { usePoll } from "../lib/usePoll";
import { api, type ActivityDownload, type ClientSettings, type DiskGuardHold, type SearchingItem } from "../lib/api";
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

const STATE_TONE: Record<string, string> = {
  downloading: "var(--accent)", seeding: "var(--good)", paused: "var(--ink-faint)", error: "var(--reject)", checking: "var(--avoid)",
  stalled: "var(--avoid)", metadata: "var(--ink-faint)", queued: "var(--ink-faint)", moving: "var(--avoid)", allocating: "var(--ink-faint)",
};

// phaseLabel is the in-flight chip's text. The phase (when the server sends one) tells a
// torrent nobody is seeding, or one still fetching its file list, from a live download —
// the plain state calls all of those "downloading".
function phaseLabel(it: ActivityDownload): { text: string; tone: string; tip?: string } {
  const phase = it.phase || it.state;
  const tone = STATE_TONE[phase] ?? "var(--ink-faint)";
  if (phase === "stalled") {
    const seeds = it.seeds ?? 0;
    const swarm = it.swarm_seeds ?? 0;
    return { text: `stalled · ${seeds} seed${seeds === 1 ? "" : "s"}`, tone, tip: `No peer is sending data. Connected to ${seeds} seed${seeds === 1 ? "" : "s"}; the tracker reports ${swarm} in the swarm.` };
  }
  if (phase === "metadata") return { text: "fetching metadata", tone, tip: "Waiting for peers to send the torrent's file list." };
  if (phase === "queued") return { text: "queued", tone, tip: "Waiting for a slot — the client's active-download limit is reached." };
  return { text: phase, tone };
}

type SortKey = "name" | "progress" | "speed" | "size" | "ratio" | "seedtime";
type Tab = "downloads" | "seeding" | "searching" | "upcoming";
const TAB_KEYS: readonly Tab[] = ["downloads", "seeding", "searching", "upcoming"];

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
  const [clients, setClients] = useState<number | null>(null); // configured download clients; null = unknown
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

  useEffect(() => {
    api.downloadClients().then((cs) => {
      const qb = cs.find((c) => c.kind === "qbittorrent") ?? cs[0];
      if (qb) setClientId(qb.id);
    }).catch(() => {});
  }, []);

  // Consecutive failed polls; one blip isn't worth a "reconnecting" banner.
  const fails = useRef(0);
  usePoll(() =>
    api.activity().then((a) => {
      fails.current = 0;
      setSearching(a.searching ?? []);
      setUpcoming(a.upcoming ?? []);
      setDownloads(a.downloads ?? []);
      if (a.totals) setTotals(a.totals);
      setFreeGb(typeof a.free_gb === "number" ? a.free_gb : null); // absent = couldn't be measured
      if (typeof a.clients === "number") setClients(a.clients);
      setGuard(a.disk_guard ?? null);
      setReconnecting(false);
      setLoaded(true);
    }).catch(() => {
      fails.current += 1;
      setLoaded(true);
      if (fails.current >= 2) setReconnecting(true);
    }), 3000);

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
  const shownDownloads = useMemo(() => filterSort(activeDownloads, sort), [activeDownloads, query, sort, typeFilter]); // eslint-disable-line react-hooks/exhaustive-deps
  const shownSeeding = useMemo(() => filterSort(seedingDownloads, seedSort), [seedingDownloads, query, seedSort, typeFilter]); // eslint-disable-line react-hooks/exhaustive-deps

  const shownSearching = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? searching.filter((s) => s.title.toLowerCase().includes(q)) : searching;
  }, [searching, query]);
  const shownUpcoming = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = q ? upcoming.filter((s) => s.title.toLowerCase().includes(q)) : upcoming;
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

  return (
    <>
      <PageHeader title="Downloads" />
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
            <button onClick={resumeAll} title={guard ? "Torrents the disk guard paused stay paused until the volume drains" : undefined} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Resume all{guard && guard.holding > 0 ? ` (${guard.holding} held by disk guard)` : ""}</button>
            {clientId != null && (
              <button onClick={() => setShowSettings((s) => !s)} className="rounded-lg px-2.5 py-1.5 text-[11px] font-semibold" style={{ border: `1px solid ${showSettings ? "var(--accent)" : "var(--line)"}`, color: showSettings ? "var(--accent)" : "var(--ink-dim)" }}>⚙ Speed & limits</button>
            )}
          </div>
        </div>

        {showSettings && clientId != null && <SettingsPanel clientId={clientId} onClose={() => setShowSettings(false)} />}

        {clients === 0 && (
          <div className="mb-4 rounded-lg p-3.5 text-[12.5px]" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
            No download client is set up, so Arrmada can't grab anything. Add one on the{" "}
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

          {!loaded ? null : tab === "downloads" ? (
            shownDownloads.length === 0 ? <Empty>Nothing downloading. Grab a release and it'll appear here.</Empty> : (
              <div className="flex flex-col gap-2">
                {shownDownloads.map((it) => <DownloadCard key={it.hash} it={it} guard={guard} busy={!!busy[it.hash]} act={act} onRemoved={flash} />)}
              </div>
            )
          ) : tab === "seeding" ? (
            shownSeeding.length === 0 ? <Empty>Nothing seeding right now.</Empty> : (
              <div className="flex flex-col gap-2">
                <SeedingSummary items={shownSeeding} />
                {shownSeeding.map((it) => <SeedingCard key={it.hash} it={it} guard={guard} busy={!!busy[it.hash]} act={act} onRemoved={flash} />)}
              </div>
            )
          ) : tab === "searching" ? (
            shownSearching.length === 0 ? <Empty>Nothing is being searched. Monitored, available titles that are missing a file show up here.</Empty> : (
              <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
                {shownSearching.map((s) => <AcqRow key={acqKey(s)} item={s} kind="searching" />)}
              </div>
            )
          ) : (
            shownUpcoming.length === 0 ? <Empty>Nothing upcoming. Unreleased films and unaired episodes you monitor land here.</Empty> : (
              <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
                {shownUpcoming.map((s) => <AcqRow key={acqKey(s)} item={s} kind="upcoming" />)}
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

function Empty({ children }: { children: React.ReactNode }) {
  return <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{children}</div>;
}

const acqKey = (s: SearchingItem) => (s.media_type === "series" ? `s${s.series_id}` : `m${s.movie_id}`);

function TypeChip({ mediaType }: { mediaType?: string }) {
  const label = mediaType === "series" ? "TV" : mediaType === "book" ? "Book" : mediaType === "music" ? "Music" : "Movie";
  return <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{label}</span>;
}

// DownloadCard is an in-flight (incomplete) transfer: progress bar, speed, ETA, queue controls.
function DownloadCard({ it, guard, busy, act, onRemoved }: { it: ActivityDownload; guard: DiskGuardHold | null; busy: boolean; act: (hash: string, fn: () => Promise<unknown>) => void; onRemoved: (m: string) => void }) {
  const paused = it.state === "paused";
  const [removing, setRemoving] = useState(false);
  const chip = phaseLabel(it);
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
        </div>
        <div className="flex flex-none items-center gap-1">
          <ResumeBtn it={it} guard={guard} busy={busy} act={act} />
          <IconBtn label="↑" title="Move up the queue" disabled={busy} onClick={() => act(it.hash, () => api.torrentAction(it.hash, "prio_up"))} />
          <IconBtn label="↓" title="Move down the queue" disabled={busy} onClick={() => act(it.hash, () => api.torrentAction(it.hash, "prio_down"))} />
          <IconBtn label="Block" tone="var(--avoid)" title="Blocklist this release and grab a different one" disabled={busy} onClick={() => act(it.hash, async () => { const r = await api.blockDownload(it.hash, it.name); onRemoved(`Blocked for ${r.blocked_for.title} — searching for another release.`); })} />
          <IconBtn label="Delete" tone="var(--reject)" title="Remove from the client — asks what to do with the files" disabled={busy} onClick={() => setRemoving(true)} />
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

// AcqRow is one Searching/Upcoming entry — a movie or a series, linking to its page.
function AcqRow({ item, kind }: { item: SearchingItem; kind: "searching" | "upcoming" }) {
  const isSeries = item.media_type === "series";
  const to = isSeries ? `/series/${item.series_id}` : `/movies/${item.movie_id}`;
  const right = kind === "searching"
    ? (isSeries ? `${item.episode_count ?? 0} episode${item.episode_count === 1 ? "" : "s"}` : "Searching…")
    : (item.available_at ? `${item.next_label ? item.next_label + " · " : ""}${fmtReleaseDate(item.available_at)}` : "Awaiting release");
  const dotColor = kind === "searching" ? "var(--avoid)" : "var(--ink-faint)";
  return (
    <Link to={to} className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-[var(--panel-2)]" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
      <span className={`inline-block h-2 w-2 flex-none rounded-full ${kind === "searching" ? "animate-pulse" : ""}`} style={{ background: dotColor }} />
      <div className="min-w-0 flex-1"><div className="truncate text-[12.5px] font-medium">{item.title} <span className="font-mono text-[10.5px] text-ink-faint">{item.year || ""}</span></div></div>
      <TypeChip mediaType={isSeries ? "series" : "movie"} />
      <ProfileChip profile={item.quality_profile} />
      <span className="w-[150px] text-right font-mono text-[10px] uppercase" style={{ color: kind === "searching" ? "var(--avoid)" : "var(--ink-faint)" }}>{right}</span>
    </Link>
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
