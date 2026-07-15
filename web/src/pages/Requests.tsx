import { useEffect, useMemo, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type MediaRequest } from "../lib/api";
import { useMe, isStaff } from "../lib/me";

const FILTERS = [
  { key: "all", label: "All" },
  { key: "pending", label: "Pending" },
  { key: "approved", label: "Approved" },
  { key: "declined", label: "Declined" },
] as const;
type FilterKey = (typeof FILTERS)[number]["key"];

function matches(r: MediaRequest, f: FilterKey): boolean {
  return f === "all" ? true : r.status === f;
}

const STATUS_STYLE: Record<string, { label: string; tone: string; soft: string }> = {
  pending: { label: "Pending", tone: "var(--avoid)", soft: "var(--avoid-soft)" },
  approved: { label: "Approved", tone: "var(--accent)", soft: "var(--accent-soft)" },
  declined: { label: "Declined", tone: "var(--reject)", soft: "var(--reject-soft)" },
};

export function Requests() {
  return (
    <>
      <PageHeader title="Requests" crumb="Services / Requests" />
      <div className="mx-auto w-full max-w-[1440px] px-4 py-6 sm:px-6">
        <RequestsPanel />
      </div>
    </>
  );
}

export function RequestsPanel() {
  const { user } = useMe();
  const canManage = isStaff(user);
  const [list, setList] = useState<MediaRequest[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<FilterKey>("all");
  const [requesting, setRequesting] = useState(false);
  const [autoApprove, setAutoApprove] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const [busy, setBusy] = useState<number | null>(null);

  const flash = (msg: string) => { setToast(msg); window.setTimeout(() => setToast(null), 3500); };

  const refresh = () =>
    api.requests().then((r) => { setList(r.requests); setAutoApprove(r.auto_approve); setError(null); }).catch((e: Error) => setError(e.message));

  useEffect(() => { refresh(); }, []);

  const filtered = useMemo(() => list.filter((r) => matches(r, filter)), [list, filter]);

  const act = async (id: number, fn: () => Promise<unknown>, msg: string) => {
    setBusy(id);
    try { await fn(); flash(msg); refresh(); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(null); }
  };

  return (
    <>
      <div>
        <div className="mb-4 flex items-center justify-between gap-3">
          <span className="font-mono text-[11px] text-ink-faint">{list.length} request{list.length === 1 ? "" : "s"}{autoApprove ? " · auto-approve on" : ""}</span>
          <button
            onClick={() => setRequesting(true)}
            className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            + Request media
          </button>
        </div>

        <div className="mb-4 flex flex-wrap gap-2">
          {FILTERS.map((f) => {
            const active = filter === f.key;
            const count = f.key === "all" ? list.length : list.filter((r) => matches(r, f.key)).length;
            return (
              <button
                key={f.key}
                onClick={() => setFilter(f.key)}
                className="rounded-full px-3 py-1 text-[12px] font-semibold"
                style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "var(--panel)", color: active ? "var(--accent)" : "var(--ink-faint)" }}
              >
                {f.label} <span className="font-mono text-[10.5px] opacity-70">{count}</span>
              </button>
            );
          })}
        </div>

        {error && <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}

        {list.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No requests yet. Click <b>Request media</b> to search for a movie or show and add it to the queue.
          </div>
        ) : filtered.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No <b>{FILTERS.find((f) => f.key === filter)?.label}</b> requests.
          </div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {filtered.map((r) => (
              <RequestRow
                key={r.id}
                r={r}
                busy={busy === r.id}
                canManage={canManage}
                onApprove={() => act(r.id, () => api.approveRequest(r.id), `Approved “${r.title}” — searching now.`)}
                onDecline={() => act(r.id, () => api.declineRequest(r.id), `Declined “${r.title}”.`)}
                onDelete={() => act(r.id, () => api.deleteRequest(r.id), `Removed request.`)}
              />
            ))}
          </div>
        )}

        {toast && (
          <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>
            {toast}
          </div>
        )}
      </div>
      {requesting && <RequestModal onClose={() => setRequesting(false)} onDone={() => refresh()} flash={flash} />}
    </>
  );
}

function RequestRow({ r, busy, canManage, onApprove, onDecline, onDelete }: { r: MediaRequest; busy: boolean; canManage: boolean; onApprove: () => void; onDecline: () => void; onDelete: () => void }) {
  const st = STATUS_STYLE[r.status] ?? STATUS_STYLE.pending;
  return (
    <div className="flex items-center gap-3 rounded-xl p-3" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="h-[72px] w-[48px] flex-none overflow-hidden rounded-lg" style={{ background: "var(--panel-2)" }}>
        {r.poster_url && <img src={r.poster_url} alt="" className="h-full w-full object-cover" loading="lazy" />}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{r.media_type === "series" ? "TV" : r.media_type === "book" ? "Book" : "Movie"}</span>
          <span className="text-[13.5px] font-semibold">{r.title}</span>
          {r.media_type === "book" && r.author && <span className="text-[11.5px] text-ink-dim">{r.author}</span>}
          <span className="font-mono text-[10.5px] text-ink-faint">{r.year || ""}</span>
          <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: st.soft, color: st.tone }}>{st.label}</span>
          {r.available && <span className="rounded-full px-2 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--good-soft, rgba(90,140,90,.14))", color: "var(--good)" }}>Available</span>}
        </div>
        {r.overview && <div className="mt-1 line-clamp-1 text-[11.5px] text-ink-dim">{r.overview}</div>}
        <div className="mt-1 font-mono text-[10px] text-ink-faint">{r.requested_by_name ? `by ${r.requested_by_name}` : ""}{r.requested_by_name ? " · " : ""}{fmtTime(r.created_at)}</div>
      </div>
      {canManage && (
        <div className="flex flex-none items-center gap-1.5">
          {r.status === "pending" && (
            <>
              <button onClick={onApprove} disabled={busy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{busy ? "…" : "Approve"}</button>
              <button onClick={onDecline} disabled={busy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Decline</button>
            </>
          )}
          <button onClick={onDelete} disabled={busy} title="Remove request" className="grid h-8 w-8 place-items-center rounded-lg" style={{ border: "1px solid var(--line)", color: "var(--ink-faint)" }}>
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" /></svg>
          </button>
        </div>
      )}
    </div>
  );
}

type SearchHit = { media_type: "movie" | "series"; tmdb_id: number; title: string; year: number; poster_url?: string; overview?: string };

function RequestModal({ onClose, onDone, flash }: { onClose: () => void; onDone: () => void; flash: (m: string) => void }) {
  const [q, setQ] = useState("");
  const [type, setType] = useState<"all" | "movie" | "series">("all");
  const [results, setResults] = useState<SearchHit[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [requestedIds, setRequestedIds] = useState<Set<string>>(new Set());
  const [busyKey, setBusyKey] = useState<string | null>(null);

  const search = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!q.trim()) return;
    setLoading(true); setError(null);
    try {
      const [movies, series] = await Promise.all([
        type === "series" ? Promise.resolve([]) : api.lookupMovies(q.trim()).catch(() => []),
        type === "movie" ? Promise.resolve([]) : api.lookupSeries(q.trim()).catch(() => []),
      ]);
      const hits: SearchHit[] = [
        ...movies.map((m) => ({ media_type: "movie" as const, tmdb_id: m.tmdb_id, title: m.title, year: m.year, poster_url: m.poster_url, overview: m.overview })),
        ...series.map((s) => ({ media_type: "series" as const, tmdb_id: s.tmdb_id, title: s.title, year: s.year, poster_url: s.poster_url, overview: s.overview })),
      ].sort((a, b) => (b.year || 0) - (a.year || 0));
      setResults(hits);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  const request = async (h: SearchHit) => {
    const key = `${h.media_type}:${h.tmdb_id}`;
    setBusyKey(key);
    try {
      await api.createRequest({ media_type: h.media_type, tmdb_id: h.tmdb_id, title: h.title, year: h.year, poster_url: h.poster_url, overview: h.overview });
      setRequestedIds((s) => new Set(s).add(key));
      onDone();
      flash(`Requested “${h.title}”.`);
    } catch (e) {
      const msg = (e as Error).message;
      if (/already/i.test(msg)) { setRequestedIds((s) => new Set(s).add(key)); flash("Already requested."); onDone(); }
      else setError(msg);
    } finally {
      setBusyKey(null);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[640px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Request media</h2>
          <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
            {(["all", "movie", "series"] as const).map((t) => (
              <button key={t} onClick={() => setType(t)} className="rounded-md px-2.5 py-1 text-[11px] font-semibold capitalize" style={{ background: type === t ? "var(--accent)" : "transparent", color: type === t ? "var(--accent-ink)" : "var(--ink-faint)" }}>{t === "all" ? "All" : t === "movie" ? "Movies" : "TV"}</button>
            ))}
          </div>
        </div>
        <form onSubmit={search} className="mb-3 flex gap-2">
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search movies & TV — e.g. Andor" className="flex-1 rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <button type="submit" disabled={loading} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{loading ? "…" : "Search"}</button>
        </form>
        {error && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="thin-scroll max-h-[56vh] overflow-y-auto">
          {results.map((h) => {
            const key = `${h.media_type}:${h.tmdb_id}`;
            const done = requestedIds.has(key);
            return (
              <div key={key} className="flex items-center gap-3 rounded-lg p-2">
                <div className="h-[68px] w-[46px] flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
                  {h.poster_url && <img src={h.poster_url} alt="" className="h-full w-full object-cover" loading="lazy" />}
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="rounded px-1.5 py-0.5 font-mono text-[8.5px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{h.media_type === "series" ? "TV" : "Movie"}</span>
                    <span className="text-[13px] font-semibold">{h.title}</span>
                    <span className="font-mono text-[10.5px] text-ink-faint">{h.year ? `(${h.year})` : ""}</span>
                  </div>
                  <div className="mt-0.5 line-clamp-2 text-[11.5px] text-ink-dim">{h.overview || "No overview."}</div>
                </div>
                <button onClick={() => request(h)} disabled={busyKey === key || done} className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: done ? "var(--panel-2)" : "var(--accent-soft)", color: done ? "var(--ink-dim)" : "var(--accent)" }}>
                  {done ? "Requested ✓" : busyKey === key ? "…" : "Request"}
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function fmtTime(s: string): string {
  const d = new Date(s.includes("T") ? s : s.replace(" ", "T") + "Z");
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
