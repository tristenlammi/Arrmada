import { useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import type { HistoryEntry, InsightsServer, UserEntry } from "../../lib/api";
import { insightsApi } from "../../lib/insightsApi";
import { jellyfinSlotIsEmby } from "../../components/brand";
import { useSearchParamState } from "../../lib/useSearchState";
import { IconButton, Modal } from "../../ui";
import { Field } from "../../ui/Field";
import { Input } from "../../ui/Input";
import { Select } from "../../ui/Select";
import { Switch } from "../../ui/Switch";
import { Chip, DECISION, FilterGroup, ImportedNotice, InsightsCfg, RowServer, SetupState, fmtDate, fmtDur, geoLabel } from "./shared";

// History: every recorded play, filterable by type, stream, person and date, optionally
// with a person's resumed sittings of one title folded into one row, and exportable as
// CSV. Every filter lives in the address, so a refresh or a shared link keeps the view.

const TYPE_FILTERS = [
  { key: "", label: "All" }, { key: "movie", label: "Movies" }, { key: "episode", label: "TV" }, { key: "track", label: "Music" },
] as const;
const DEC_FILTERS = [
  { key: "", label: "All" }, { key: "direct_play", label: "Direct Play" }, { key: "direct_stream", label: "Direct Stream" }, { key: "transcode", label: "Transcode" },
] as const;
const RANGES = ["all", "7d", "30d", "90d", "custom"] as const;
type Range = (typeof RANGES)[number];
const RANGE_LABEL: Record<Range, string> = { all: "All time", "7d": "7d", "30d": "30d", "90d": "90d", custom: "Custom" };
const PAGE_SIZE = 50;

// localMidnight is the epoch second of a local calendar day's start ("2026-10-09").
function localMidnight(day: string, plusDays = 0): number {
  const [y, m, d] = day.split("-").map(Number);
  if (!y || !m || !d) return 0;
  return Math.floor(new Date(y, m - 1, d + plusDays).getTime() / 1000);
}

// rangeBounds turns the date filter into epoch bounds (to exclusive). The presets run to
// local midnight, so they stay the same for the whole day instead of moving every render.
export function rangeBounds(range: Range, fromDay: string, toDay: string, now = new Date()): { from?: number; to?: number } {
  if (range === "custom") {
    const from = fromDay ? localMidnight(fromDay) : 0;
    // The "to" day is included: the bound is the next day's midnight.
    const to = toDay ? localMidnight(toDay, 1) : 0;
    return { from: from || undefined, to: to || undefined };
  }
  const days = range === "7d" ? 7 : range === "30d" ? 30 : range === "90d" ? 90 : 0;
  if (!days) return {};
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (days - 1));
  return { from: Math.floor(start.getTime() / 1000) };
}

// The watched/partial badge: Watched from 90% on, Partial nn% below, nothing when the
// runtime isn't known.
export function progressBadge(r: Pick<HistoryEntry, "duration_ms" | "progress_pct">): { label: string; done: boolean } | null {
  if (!r.duration_ms) return null;
  return r.progress_pct >= 90 ? { label: "Watched", done: true } : { label: `Partial ${r.progress_pct}%`, done: false };
}

// server narrows to one server's plays (the page's server chips; undefined = both).
export function HistoryView({ connected, server, userLink }: { connected: boolean; server?: InsightsServer; userLink?: (id: string, name: string) => ReactNode }) {
  const [type, setType] = useSearchParamState<string>("type", "", { allowed: TYPE_FILTERS.map((f) => f.key) });
  const [decision, setDecision] = useSearchParamState<string>("decision", "", { allowed: DEC_FILTERS.map((f) => f.key) });
  const [q, setQ] = useSearchParamState<string>("q", "", { debounceMs: 300 });
  const [user, setUser] = useSearchParamState<string>("user", "");
  const [range, setRange] = useSearchParamState<Range>("range", "all", { allowed: RANGES });
  const [fromDay, setFromDay] = useSearchParamState<string>("from", "");
  const [toDay, setToDay] = useSearchParamState<string>("to", "");
  const [groupParam, setGroup] = useSearchParamState<string>("group", "");
  const [pageParam, setPage] = useSearchParamState<string>("page", "1");
  const group = groupParam === "1";
  const page = Math.max(1, Number(pageParam) || 1);

  const [rows, setRows] = useState<HistoryEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [detail, setDetail] = useState<HistoryEntry | null>(null);
  const [people, setPeople] = useState<UserEntry[]>([]);
  useEffect(() => { insightsApi.insightsUsers(server).then(setPeople).catch(() => setPeople([])); }, [server]);

  const bounds = useMemo(() => rangeBounds(range, fromDay, toDay), [range, fromDay, toDay]);
  const filters = useMemo(() => ({ type, decision, q, user, group, from: bounds.from, to: bounds.to, server }), [type, decision, q, user, group, bounds, server]);

  // Changing a filter starts again at page 1. It runs after the filter's own address write
  // has landed: two writes in one click would each start from the same address, and the
  // second would undo the first. A deep link's page survives the first render.
  const filtersKey = JSON.stringify(filters);
  const lastFilters = useRef(filtersKey);
  useEffect(() => {
    if (lastFilters.current === filtersKey) return;
    lastFilters.current = filtersKey;
    if (page !== 1) setPage("1");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filtersKey]);

  // History is read from the database, not from Plex, so a Tautulli import is listed even
  // before a server is connected.
  useEffect(() => {
    let alive = true;
    setLoading(true);
    insightsApi.insightsHistory({ ...filters, page, page_size: PAGE_SIZE })
      .then((r) => { if (alive) { setRows(r.rows ?? []); setTotal(r.total); setFailed(false); } })
      .catch(() => { if (alive) { setRows([]); setFailed(true); } })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, [filters, page]);

  // Nothing recorded at all (not just nothing matching a filter) and no server to record
  // from: the setup state says more than an empty table.
  const unfiltered = !type && !decision && !q && !user && range === "all";
  if (!connected && !loading && total === 0 && unfiltered) return <SetupState tab="history" />;

  const from = total === 0 ? 0 : (page - 1) * PAGE_SIZE + 1;
  const to = Math.min(page * PAGE_SIZE, total);
  const maxPage = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const csvURL = insightsApi.insightsHistoryCSVURL(filters);
  const name = (r: HistoryEntry) => (userLink && r.user_id ? userLink(r.user_id, r.user_name) : r.user_name);

  return (
    <div className="flex flex-col gap-3">
      {!connected && (total > 0 || !unfiltered) && <ImportedNotice />}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <FilterGroup label="Type">{TYPE_FILTERS.map((f) => <Chip key={f.key} active={type === f.key} onClick={() => setType(f.key)}>{f.label}</Chip>)}</FilterGroup>
        <FilterGroup label="Stream">{DEC_FILTERS.map((f) => <Chip key={f.key} active={decision === f.key} onClick={() => setDecision(f.key)}>{f.label}</Chip>)}</FilterGroup>
        <FilterGroup label="When">{RANGES.map((r) => <Chip key={r} active={range === r} onClick={() => setRange(r)}>{RANGE_LABEL[r]}</Chip>)}</FilterGroup>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <Field label="Person" className="min-w-[160px]">
          <Select value={user} onChange={(e) => setUser(e.target.value)} className="px-3 py-1.5 text-sm">
            <option value="">Everyone</option>
            {people.map((p) => <option key={p.id} value={p.id}>{p.username || p.id}</option>)}
            {user && !people.some((p) => p.id === user) && <option value={user}>{user}</option>}
          </Select>
        </Field>
        {range === "custom" && (
          <>
            <Field label="From"><Input type="date" value={fromDay} max={toDay || undefined} onChange={(e) => setFromDay(e.target.value)} className="px-3 py-1.5 text-sm" /></Field>
            <Field label="To"><Input type="date" value={toDay} min={fromDay || undefined} onChange={(e) => setToDay(e.target.value)} className="px-3 py-1.5 text-sm" /></Field>
          </>
        )}
        <Field label="Search" className="min-w-[200px] flex-1">
          <Input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Title or person…" className="px-3 py-1.5 text-sm" />
        </Field>
        <Switch checked={group} onChange={(v) => setGroup(v ? "1" : "")} size="sm" className="pb-1.5">Group resumed plays</Switch>
        <a href={csvURL} download className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}
          title={`Download the plays these filters match as CSV (up to 50,000)`}>Export CSV</a>
      </div>

      {failed && <div role="status" className="rounded-lg px-3 py-2 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>Couldn't load the history — this is an error, not an empty history.</div>}

      {/* relative: the header cells' screen-reader text is positioned inside the scroller, not the page. */}
      <div className="relative overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
        <table className="w-full border-collapse text-base" style={{ minWidth: 880 }}>
          <thead><tr style={{ background: "var(--panel-2)" }}>
            {["When", "User", "", "Title", "Player", "Location", "Stream", "Watched"].map((h, i) => (
              <th key={i} scope="col" className="px-3 py-2 text-left font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">{h || <span className="sr-only">Poster</span>}</th>
            ))}
            <th scope="col"><span className="sr-only">Details</span></th>
          </tr></thead>
          <tbody>
            {loading && rows.length === 0 ? (
              <tr><td colSpan={9} className="px-3 py-10 text-center text-sm text-ink-dim">Loading history…</td></tr>
            ) : rows.length === 0 ? (
              <tr><td colSpan={9} className="px-3 py-10 text-center text-sm text-ink-faint">{unfiltered ? "No plays recorded yet. History fills in as people watch." : "No plays match these filters."}</td></tr>
            ) : rows.map((r, i) => {
              const d = DECISION[r.decision] ?? DECISION.direct_play;
              const badge = progressBadge(r);
              return (
                // A click anywhere on the row opens the details too (the › button is the
                // keyboard's way in).
                <tr key={r.id} onClick={() => setDetail(r)} className="cursor-pointer" style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)", opacity: loading ? 0.6 : 1 }}>
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-xs text-ink-dim">{fmtDate(r.started_at)}</td>
                  <td className="px-3 py-2"><span className="inline-flex flex-wrap items-center gap-1.5">{name(r)}<RowServer server={r.server} /></span></td>
                  <td className="py-1.5 pl-3">
                    <span className="block h-12 w-8 overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
                      {r.thumb_url ? <img src={r.thumb_url} alt="" loading="lazy" className="h-full w-full object-cover" /> : null}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <div className="font-semibold">{r.title}</div>
                    <div className="flex flex-wrap items-center gap-1.5 text-xs text-ink-faint">
                      {r.subtitle && <span>{r.subtitle}</span>}
                      {badge && <span className="rounded-full px-1.5 py-px font-semibold" style={badge.done ? { background: "var(--good-soft)", color: "var(--good-text)" } : { background: "var(--panel-2)", color: "var(--ink-dim)" }}>{badge.label}</span>}
                      {(r.sessions ?? 0) > 1 && <span className="rounded-full px-1.5 py-px font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent-text)" }}>{r.sessions} sessions</span>}
                    </div>
                  </td>
                  <td className="px-3 py-2 text-ink-dim">{r.player || r.platform}</td>
                  <td className="px-3 py-2 font-mono text-xs text-ink-dim">{geoLabel(r.geo)}</td>
                  <td className="px-3 py-2"><span className="rounded-full px-2 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink)" }}>{d.label}</span></td>
                  <td className="whitespace-nowrap px-3 py-2 font-mono tabular-nums text-ink-dim">{fmtDur(r.watched_secs)}{r.buffer_count > 0 && <span title={`${r.buffer_count} buffer event(s)`} style={{ color: "var(--avoid-text)" }}> · ⚠{r.buffer_count}</span>}</td>
                  <td className="px-2 py-2 text-right"><IconButton label="Open play details" onClick={(e) => { e.stopPropagation(); setDetail(r); }} className="text-ink-faint hover:text-[var(--ink)]">›</IconButton></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between text-sm text-ink-faint">
        <span>{total > 0 ? `${from}–${to} of ${total.toLocaleString()}${group ? " viewings" : ""}` : group ? "0 viewings" : "0 plays"}</span>
        <span className="flex items-center gap-2">
          <button type="button" disabled={page <= 1} onClick={() => setPage(String(page - 1))} className="rounded-lg px-2.5 py-1 font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Prev</button>
          <span className="font-mono">{page}/{maxPage}</span>
          <button type="button" disabled={page >= maxPage} onClick={() => setPage(String(page + 1))} className="rounded-lg px-2.5 py-1 font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Next</button>
        </span>
      </div>

      {detail && <HistoryDetail r={detail} onClose={() => setDetail(null)} />}
    </div>
  );
}

// HistoryHW is a recorded play's hardware badge. Plays now record a hardware encode Plex
// really used; older ones recorded "hardware was requested", which a CPU fallback also set.
// A Jellyfin play records the hardware acceleration Jellyfin had configured.
function HistoryHW({ startedAt, server }: { startedAt: number; server?: string }) {
  const since = useContext(InsightsCfg)?.hw_since ?? 0;
  if (server === "jellyfin") {
    return (
      <span title={jellyfinSlotIsEmby()
        ? "Emby ran this stream's video on hardware."
        : "Jellyfin's configured hardware acceleration — it doesn't say whether the GPU took this stream."}
        className="rounded-full px-2.5 py-0.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>HW</span>
    );
  }
  const old = !since || startedAt < since;
  return (
    <span title={old ? `Plays before ${since ? fmtDate(since) : "this update"} show HW as requested — Plex may have fallen back to CPU.` : undefined}
      className="rounded-full px-2.5 py-0.5 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
      {old ? "HW requested" : "HW enc"}
    </span>
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

function HistoryDetail({ r, onClose }: { r: HistoryEntry; onClose: () => void }) {
  const d = DECISION[r.decision] ?? DECISION.direct_play;
  const titleId = useId();
  const badge = progressBadge(r);
  const grouped = (r.sessions ?? 0) > 1;
  return (
    <Modal onClose={onClose} labelledBy={titleId} size="md">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 id={titleId} className="m-0 truncate text-lg font-bold">{r.title}</h2>
          <p className="m-0 mt-0.5 text-sm text-ink-dim">{r.subtitle}</p>
        </div>
        <IconButton label="Close" onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</IconButton>
      </div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="rounded-full px-2.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: d.color, color: "var(--accent-ink)" }}>{d.label}</span>
        <RowServer server={r.server} />
        {r.hw_transcode && <HistoryHW startedAt={r.started_at} server={r.server} />}
        {badge && <span className="rounded-full px-2.5 py-0.5 text-xs font-semibold" style={badge.done ? { background: "var(--good-soft)", color: "var(--good-text)" } : { border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{badge.label}</span>}
        {r.buffer_count > 0 && <span className="rounded-full px-2.5 py-0.5 text-xs font-semibold" style={{ background: "var(--avoid-soft)", color: "var(--avoid-text)" }}>{r.buffer_count} buffer event{r.buffer_count === 1 ? "" : "s"}</span>}
      </div>
      {grouped && <p className="mb-2 text-sm text-ink-dim">{r.sessions} sittings from {fmtDate(r.started_at)} to {fmtDate(r.stopped_at)}; the stream details are from the last one.</p>}
      <div className="grid gap-px overflow-hidden rounded-lg text-sm" style={{ background: "var(--line)" }}>
        <DDRow label="User" a={r.user_name} />
        <DDRow label="Player" a={`${r.player || "—"} · ${r.platform} · ${r.product}`} />
        <DDRow label="Location" a={`${geoLabel(r.geo)}${r.ip_address ? ` · ${r.ip_address}` : ""} · ${r.location?.toUpperCase() || "—"}`} />
        <DDRow label="Started" a={fmtDate(r.started_at)} />
        <DDRow label="Watched" a={`${fmtDur(r.watched_secs)}${r.paused_ms > 0 ? ` (paused ${fmtDur(Math.round(r.paused_ms / 1000))})` : ""}`} />
        <DDRow label="Video" a={r.video_src} b={r.video_stream} />
        <DDRow label="Audio" a={r.audio_src} b={r.audio_stream} />
        <DDRow label="Container" a={r.container_src} b={r.container_stream} />
      </div>
    </Modal>
  );
}
