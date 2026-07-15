import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type Series as SeriesT, type SeriesLookup } from "../lib/api";

const FILTERS = [
  { key: "all", label: "All" },
  { key: "monitored", label: "Monitored" },
  { key: "continuing", label: "Continuing" },
  { key: "ended", label: "Ended" },
  { key: "missing", label: "Missing" },
] as const;

type FilterKey = (typeof FILTERS)[number]["key"];

function statsOf(s: SeriesT) {
  return s.stats ?? { episodes: 0, have_files: 0, size_bytes: 0, seasons: 0 };
}

function matches(s: SeriesT, f: FilterKey): boolean {
  const st = statsOf(s);
  switch (f) {
    case "monitored": return s.monitored;
    case "continuing": return /return|continu/i.test(s.status ?? "");
    case "ended": return /end|cancel/i.test(s.status ?? "");
    case "missing": return st.have_files < st.episodes;
    default: return true;
  }
}

export function Series() {
  const [list, setList] = useState<SeriesT[]>([]);
  const [metaOK, setMetaOK] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<FilterKey>("all");
  const [adding, setAdding] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<SeriesT | null>(null);
  const [scanning, setScanning] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const [view, setView] = useState<"grid" | "table">("grid");
  const [multiSelect, setMultiSelect] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [bulkBusy, setBulkBusy] = useState(false);

  const flash = (msg: string) => { setToast(msg); window.setTimeout(() => setToast(null), 3500); };

  const refresh = () =>
    api.series().then((r) => { setList(r.series); setMetaOK(r.metadata_available); setError(null); }).catch((e: Error) => setError(e.message));

  useEffect(() => {
    refresh();
    api.qualityProfiles("series").then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })))).catch(() => {});
  }, []);

  const filtered = useMemo(() => list.filter((s) => matches(s, filter)), [list, filter]);

  const toggleSelect = (id: number) =>
    setSelected((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const clearSelect = () => setSelected(new Set());
  const exitMultiSelect = () => { setMultiSelect(false); clearSelect(); };

  const bulkMonitor = async (mon: boolean) => {
    setBulkBusy(true);
    try {
      await Promise.all([...selected].map((id) => api.setSeriesMonitored(id, mon)));
      flash(`${selected.size} ${mon ? "monitored" : "unmonitored"}.`);
      clearSelect();
      refresh();
    } finally { setBulkBusy(false); }
  };
  const bulkProfile = async (profile: string) => {
    setBulkBusy(true);
    try {
      await Promise.all([...selected].map((id) => api.setSeriesProfile(id, profile)));
      flash(`Quality profile set on ${selected.size} series.`);
      clearSelect();
      refresh();
    } finally { setBulkBusy(false); }
  };

  const scanLibrary = async () => {
    setScanning(true);
    try {
      await api.scanSeries();
      flash("Scanning your library — existing series will appear shortly.");
      let ticks = 0;
      const t = setInterval(() => {
        refresh();
        if (++ticks >= 12) { clearInterval(t); setScanning(false); }
      }, 2500);
    } catch (e) {
      flash((e as Error).message);
      setScanning(false);
    }
  };

  const doDelete = async (id: number, deleteFiles: boolean) => {
    await api.deleteSeries(id, deleteFiles);
    setConfirmDelete(null);
    refresh();
  };

  const search = async (s: SeriesT) => {
    try {
      await api.searchSeries(s.id);
      flash(`Searching for “${s.title}” — packs/episodes appear in Downloads once grabbed.`);
    } catch (e) {
      flash((e as Error).message);
    }
  };

  return (
    <>
      <PageHeader title="Series" crumb="Library / Series" />
      <div className="mx-auto w-full max-w-[1440px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex items-center justify-between gap-3">
          <span className="font-mono text-[11px] text-ink-faint">{list.length} in library</span>
          <div className="flex items-center gap-2">
            <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
              {(["grid", "table"] as const).map((v) => (
                <button key={v} onClick={() => setView(v)} className="rounded-md px-3 py-1.5 text-[11.5px] font-semibold capitalize" style={{ background: view === v ? "var(--accent)" : "transparent", color: view === v ? "var(--accent-ink)" : "var(--ink-faint)" }}>{v}</button>
              ))}
            </div>
            <button
              onClick={scanLibrary}
              disabled={scanning}
              title="Find series already in your library folder and catalog them"
              className="rounded-lg px-3 py-2 text-[12.5px] font-semibold"
              style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}
            >
              {scanning ? "Scanning…" : "Scan library"}
            </button>
            <button
              onClick={() => (multiSelect ? exitMultiSelect() : setMultiSelect(true))}
              className="rounded-lg px-3 py-2 text-[12.5px] font-semibold"
              style={{ border: `1px solid ${multiSelect ? "var(--accent)" : "var(--line)"}`, background: multiSelect ? "var(--accent-soft)" : "var(--panel-2)", color: multiSelect ? "var(--accent)" : "var(--ink)" }}
            >
              {multiSelect ? "Done" : "Select"}
            </button>
            <button
              onClick={() => setAdding(true)}
              disabled={!metaOK}
              className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold"
              style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: metaOK ? 1 : 0.5 }}
            >
              + Add series
            </button>
          </div>
        </div>

        {/* Filters */}
        <div className="mb-4 flex flex-wrap gap-2">
          {FILTERS.map((f) => {
            const active = filter === f.key;
            const count = f.key === "all" ? list.length : list.filter((s) => matches(s, f.key)).length;
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

        {multiSelect && (
          <div className="mb-4 flex flex-wrap items-center gap-2.5 rounded-xl p-3" style={{ background: "var(--panel)", border: "1px solid var(--accent-line)" }}>
            <span className="font-mono text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>{selected.size} selected</span>
            <button onClick={() => setSelected(new Set(filtered.map((s) => s.id)))} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Select all ({filtered.length})</button>
            <button onClick={clearSelect} disabled={selected.size === 0} className="rounded-lg px-2.5 py-1.5 text-[11.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Clear</button>
            <span className="mx-1 h-5 w-px" style={{ background: "var(--line)" }} />
            <button onClick={() => bulkMonitor(true)} disabled={selected.size === 0 || bulkBusy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>Monitor</button>
            <button onClick={() => bulkMonitor(false)} disabled={selected.size === 0 || bulkBusy} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Unmonitor</button>
            <span className="mx-1 h-5 w-px" style={{ background: "var(--line)" }} />
            <select
              defaultValue=""
              disabled={selected.size === 0 || bulkBusy}
              onChange={(e) => e.target.value && (bulkProfile(e.target.value), (e.target.value = ""))}
              className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-medium"
              style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
            >
              <option value="">Set quality profile…</option>
              {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
            </select>
          </div>
        )}

        {!metaOK && (
          <div className="mb-4 rounded-lg p-3.5 text-[12.5px]" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
            <b>Metadata not configured.</b> To add series, set <span className="font-mono">ARRMADA_TMDB_API_KEY</span> (a free key from themoviedb.org) and restart.
          </div>
        )}
        {error && <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}

        {list.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No series yet. Click <b>Add series</b>, search for a show, and Arrmada will monitor and grab it.
          </div>
        ) : filtered.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No series match the <b>{FILTERS.find((f) => f.key === filter)?.label}</b> filter.
          </div>
        ) : view === "table" ? (
          <SeriesTable list={filtered} multiSelect={multiSelect} selected={selected} onToggleSelect={toggleSelect} />
        ) : (
          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))" }}>
            {filtered.map((s) => (
              <Card
                key={s.id}
                s={s}
                onDelete={() => setConfirmDelete(s)}
                onSearch={() => search(s)}
                selectable={multiSelect}
                selected={selected.has(s.id)}
                onToggleSelect={() => toggleSelect(s.id)}
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
      {adding && <AddSeriesModal onClose={() => setAdding(false)} onAdded={() => { setAdding(false); refresh(); }} />}
      {confirmDelete && <DeleteSeriesModal series={confirmDelete} onClose={() => setConfirmDelete(null)} onConfirm={(df) => doDelete(confirmDelete.id, df)} />}
    </>
  );
}

function statusOf(s: SeriesT): { label: string; tone: string } {
  const st = statsOf(s);
  if (st.episodes > 0 && st.have_files >= st.episodes) return { label: "Complete", tone: "var(--good)" };
  if (s.monitored) return { label: st.have_files > 0 ? "Partial" : "Wanted", tone: "var(--avoid)" };
  return { label: "Unmonitored", tone: "var(--ink-faint)" };
}

function Poster({ url, title }: { url?: string; title: string }) {
  if (url) return <img src={url} alt={title} className="h-full w-full object-cover" loading="lazy" />;
  return (
    <div className="flex h-full w-full items-end p-2.5" style={{ background: "linear-gradient(150deg, hsl(24 40% 30%), hsl(20 35% 16%))" }}>
      <span className="text-[13px] font-bold text-white" style={{ textShadow: "0 1px 4px rgba(0,0,0,.6)" }}>{title}</span>
    </div>
  );
}

function Card({ s, onDelete, onSearch, selectable, selected, onToggleSelect }: { s: SeriesT; onDelete: () => void; onSearch: () => void; selectable?: boolean; selected?: boolean; onToggleSelect?: () => void }) {
  const st = statsOf(s);
  const status = statusOf(s);
  const pct = st.episodes > 0 ? Math.round((st.have_files / st.episodes) * 100) : 0;
  const complete = st.episodes > 0 && st.have_files >= st.episodes;
  const missing = st.have_files < st.episodes;
  const [searching, setSearching] = useState(false);
  const doSearch = async () => {
    setSearching(true);
    try { await onSearch(); } finally { window.setTimeout(() => setSearching(false), 1500); }
  };
  return (
    <div className="group relative overflow-hidden rounded-xl" style={{ border: `1px solid ${selected ? "var(--accent)" : "var(--line)"}`, background: "var(--panel)", boxShadow: selected ? "0 0 0 1px var(--accent)" : "none", opacity: s.monitored ? 1 : 0.7 }}>
      <div className="relative" style={{ aspectRatio: "2/3" }}>
        {selectable ? (
          <button onClick={onToggleSelect} className="block h-full w-full text-left">
            <Poster url={s.poster_url} title={s.title} />
            {selected && <div className="absolute inset-0" style={{ background: "var(--accent-soft)" }} />}
          </button>
        ) : (
          <Link to={`/series/${s.id}`} className="block h-full w-full">
            <Poster url={s.poster_url} title={s.title} />
          </Link>
        )}
        {selectable && (
          <span className="absolute left-1.5 top-1.5 grid h-6 w-6 place-items-center rounded-full" style={{ background: selected ? "var(--accent)" : "rgba(20,12,7,.65)", border: `1.5px solid ${selected ? "var(--accent)" : "rgba(255,255,255,.6)"}` }}>
            {selected && <svg width="13" height="13" viewBox="0 0 24 24" fill="none"><path d="M4 12l5 5L20 6" stroke="var(--accent-ink)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>}
          </span>
        )}
        {!selectable && (
          <button
            onClick={onDelete}
            title="Remove"
            className="absolute right-1.5 top-1.5 hidden h-6 w-6 place-items-center rounded-full group-hover:grid"
            style={{ background: "rgba(20,12,7,.75)", color: "#fff" }}
          >
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" /></svg>
          </button>
        )}
        {!selectable && s.monitored && missing && (
          <button
            onClick={doSearch}
            disabled={searching}
            title="Search now"
            className="absolute inset-x-1.5 bottom-6 hidden items-center justify-center gap-1.5 rounded-lg py-1.5 text-[11px] font-semibold group-hover:flex"
            style={{ background: "rgba(20,12,7,.82)", color: "#fff" }}
          >
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" /><path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" /></svg>
            {searching ? "Searching…" : "Search now"}
          </button>
        )}
        {/* Episode progress overlay — series-specific, kept from before */}
        <div className="absolute inset-x-0 bottom-0 flex items-center gap-1.5 px-2 py-1.5" style={{ background: "linear-gradient(to top, rgba(0,0,0,.78), transparent)" }}>
          <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "rgba(255,255,255,.25)" }}>
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: complete ? "var(--good)" : "var(--accent)" }} />
          </div>
          <span className="font-mono text-[9.5px] text-white">{st.have_files}/{st.episodes}</span>
        </div>
      </div>
      {selectable ? (
        <button onClick={onToggleSelect} className="block w-full p-2.5 text-left">
          <div className="truncate text-[12.5px] font-semibold" title={s.title}>{s.title}</div>
          <div className="mt-1 flex items-center justify-between">
            <span className="font-mono text-[10.5px] text-ink-faint">{s.year || "—"} · {st.seasons} {st.seasons === 1 ? "sn" : "sns"}</span>
            <span className="font-mono text-[9.5px] uppercase" style={{ color: status.tone }}>{status.label}</span>
          </div>
        </button>
      ) : (
        <Link to={`/series/${s.id}`} className="block p-2.5">
          <div className="truncate text-[12.5px] font-semibold" title={s.title}>{s.title}</div>
          <div className="mt-1 flex items-center justify-between">
            <span className="font-mono text-[10.5px] text-ink-faint">{s.year || "—"} · {st.seasons} {st.seasons === 1 ? "sn" : "sns"}</span>
            <span className="font-mono text-[9.5px] uppercase" style={{ color: status.tone }}>{status.label}</span>
          </div>
        </Link>
      )}
    </div>
  );
}

function gb(bytes?: number): string {
  if (!bytes) return "—";
  return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
}

function SeriesTable({ list, multiSelect, selected, onToggleSelect }: { list: SeriesT[]; multiSelect: boolean; selected: Set<number>; onToggleSelect: (id: number) => void }) {
  const th = "px-2.5 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-[0.06em] text-ink-faint";
  const td = "px-2.5 py-2 align-middle";
  return (
    <div className="thin-scroll overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
      <table className="w-full border-collapse text-[12px]" style={{ minWidth: "820px" }}>
        <thead>
          <tr style={{ background: "var(--panel)", borderBottom: "1px solid var(--line)" }}>
            {multiSelect && <th className={th}></th>}
            <th className={th}>Title</th>
            <th className={th}>Status</th>
            <th className={th}>Network</th>
            <th className={`${th} text-right`}>Seasons</th>
            <th className={`${th} text-right`}>Episodes</th>
            <th className={`${th} text-right`}>Size</th>
            <th className={th}>Monitored</th>
          </tr>
        </thead>
        <tbody>
          {list.map((s) => {
            const st = statsOf(s);
            const status = statusOf(s);
            return (
              <tr key={s.id} className="transition-colors hover:bg-[var(--panel-2)]" style={{ background: selected.has(s.id) ? "var(--accent-soft)" : "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
                {multiSelect && (
                  <td className={td}><input type="checkbox" checked={selected.has(s.id)} onChange={() => onToggleSelect(s.id)} /></td>
                )}
                <td className={`${td} min-w-[220px]`}>
                  {multiSelect ? (
                    <button onClick={() => onToggleSelect(s.id)} className="text-left font-semibold">{s.title} <span className="font-normal text-ink-faint">{s.year || ""}</span></button>
                  ) : (
                    <Link to={`/series/${s.id}`} className="font-semibold hover:text-[var(--accent)]">{s.title} <span className="font-normal text-ink-faint">{s.year || ""}</span></Link>
                  )}
                </td>
                <td className={td}><span className="font-mono text-[10px] uppercase" style={{ color: status.tone }}>{status.label}</span></td>
                <td className={td}>{s.network || "—"}</td>
                <td className={`${td} text-right font-mono text-[11px] text-ink-dim`}>{st.seasons}</td>
                <td className={`${td} text-right font-mono text-[11px]`}><span style={{ color: st.have_files >= st.episodes && st.episodes > 0 ? "var(--good)" : "var(--ink-dim)" }}>{st.have_files}/{st.episodes}</span></td>
                <td className={`${td} text-right font-mono text-[11px] text-ink-dim`}>{gb(st.size_bytes)}</td>
                <td className={td}><span className="font-mono text-[10px] uppercase" style={{ color: s.monitored ? "var(--accent)" : "var(--ink-faint)" }}>{s.monitored ? "Yes" : "No"}</span></td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function DeleteSeriesModal({ series, onClose, onConfirm }: { series: SeriesT; onClose: () => void; onConfirm: (deleteFiles: boolean) => void }) {
  const [deleteFiles, setDeleteFiles] = useState(true);
  const [busy, setBusy] = useState(false);
  const hasFiles = (series.stats?.have_files ?? 0) > 0;
  const confirm = async () => {
    setBusy(true);
    try { await onConfirm(deleteFiles); } finally { setBusy(false); }
  };
  return (
    <div className="fixed inset-0 z-50 grid place-items-center p-6" style={{ background: "rgba(0,0,0,.6)" }} onClick={onClose}>
      <div className="w-full max-w-[440px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="flex items-start gap-3">
          <div className="h-[84px] w-[56px] flex-none overflow-hidden rounded-lg" style={{ background: "var(--panel-2)" }}>
            {series.poster_url && <img src={series.poster_url} alt="" className="h-full w-full object-cover" />}
          </div>
          <div className="min-w-0">
            <h2 className="m-0 text-[15px] font-bold">Remove “{series.title}”?</h2>
            <p className="mt-1 text-[12px] text-ink-dim">It'll be removed from your library and Arrmada will stop monitoring it.</p>
          </div>
        </div>

        <label className="mt-4 flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)" }}>
          <input type="checkbox" checked={deleteFiles} onChange={(e) => setDeleteFiles(e.target.checked)} className="mt-0.5" />
          <span>
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject)" : "var(--ink)" }}>Also delete files from disk</span>
            <span className="mt-0.5 block text-[11px] text-ink-faint">{hasFiles ? "Deletes every episode file and the show folder." : "This series has no files on disk."}</span>
          </span>
        </label>

        <div className="mt-4 flex justify-end gap-2.5">
          <button onClick={onClose} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          <button onClick={confirm} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>
            {busy ? "Removing…" : deleteFiles ? "Remove + delete files" : "Remove"}
          </button>
        </div>
      </div>
    </div>
  );
}

function AddSeriesModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<SeriesLookup[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [profile, setProfile] = useState("");
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [searchOnAdd, setSearchOnAdd] = useState(true);
  const [addingId, setAddingId] = useState<number | null>(null);

  useEffect(() => {
    api.qualityProfiles("series").then((r) => {
      setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
    api.settings().then((s) => setSearchOnAdd(s.search_on_add)).catch(() => {});
  }, []);

  const toggleSearchOnAdd = () => {
    const next = !searchOnAdd;
    setSearchOnAdd(next);
    api.updateSettings({ search_on_add: next }).catch(() => {});
  };

  const search = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!q.trim()) return;
    setLoading(true); setError(null);
    try { setResults(await api.lookupSeries(q.trim())); } catch (e) { setError((e as Error).message); } finally { setLoading(false); }
  };

  const add = async (r: SeriesLookup) => {
    setAddingId(r.tmdb_id); setError(null);
    try { await api.addSeries({ tmdb_id: r.tmdb_id, quality_profile: profile, monitored: true, search_on_add: searchOnAdd }); onAdded(); }
    catch (e) { setError((e as Error).message); setAddingId(null); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[640px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Add a series</h2>
          <div className="flex items-center gap-4">
            <button type="button" onClick={toggleSearchOnAdd} title="On: monitor and search right away. Off: add unmonitored — nothing is searched until you monitor it." className="flex items-center gap-2">
              <span className="relative inline-block h-[20px] w-[34px] rounded-full transition-colors" style={{ background: searchOnAdd ? "var(--accent)" : "var(--line)" }}>
                <span className="absolute top-[3px] h-[14px] w-[14px] rounded-full bg-white transition-all" style={{ left: searchOnAdd ? "17px" : "3px" }} />
              </span>
              <span className="text-[11.5px] font-medium" style={{ color: searchOnAdd ? "var(--ink)" : "var(--ink-dim)" }}>{searchOnAdd ? "Search on add" : "Add unmonitored"}</span>
            </button>
            <div className="flex items-center gap-2">
              <span className="font-mono text-[10px] uppercase text-ink-faint">Quality</span>
              <select value={profile} onChange={(e) => setProfile(e.target.value)} className="rounded-lg px-2 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
              </select>
            </div>
          </div>
        </div>
        <form onSubmit={search} className="mb-3 flex gap-2">
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search TV series — e.g. Severance" className="flex-1 rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <button type="submit" disabled={loading} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{loading ? "…" : "Search"}</button>
        </form>
        {error && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="max-h-[52vh] overflow-y-auto">
          {results.map((r) => (
            <button key={r.tmdb_id} onClick={() => add(r)} disabled={addingId !== null} className="flex w-full items-center gap-3 rounded-lg p-2 text-left transition-colors hover:bg-[var(--panel-2)]">
              <div className="h-[68px] w-[46px] flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
                {r.poster_url && <img src={r.poster_url} alt="" className="h-full w-full object-cover" loading="lazy" />}
              </div>
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-semibold">{r.title} <span className="font-normal text-ink-faint">{r.year ? `(${r.year})` : ""}</span></div>
                <div className="mt-0.5 line-clamp-2 text-[11.5px] text-ink-dim">{r.overview || "No overview."}</div>
              </div>
              <span className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
                {addingId === r.tmdb_id ? "Adding…" : "Add"}
              </span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
