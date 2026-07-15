import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type Movie, type MovieLookup } from "../lib/api";


type FilterKey = "all" | "monitored" | "unmonitored" | "missing" | "available";

const FILTERS: { key: FilterKey; label: string }[] = [
  { key: "all", label: "All" },
  { key: "monitored", label: "Monitored" },
  { key: "unmonitored", label: "Unmonitored" },
  { key: "missing", label: "Missing" },
  { key: "available", label: "Available" },
];

function matchesFilter(m: Movie, f: FilterKey): boolean {
  switch (f) {
    case "monitored":
      return m.monitored;
    case "unmonitored":
      return !m.monitored;
    case "missing":
      return !m.has_file;
    case "available":
      return m.has_file;
    default:
      return true;
  }
}

export function Movies() {
  const [movies, setMovies] = useState<Movie[]>([]);
  const [metaOK, setMetaOK] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Movie | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const [filter, setFilter] = useState<FilterKey>("all");
  const [multiSelect, setMultiSelect] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [view, setView] = useState<"grid" | "table">("grid");

  const scanLibrary = async () => {
    setScanning(true);
    try {
      await api.scanLibrary();
      flash("Scanning your library — existing movies will appear shortly.");
      // The scan runs in the background; poll the grid for a while as entries land.
      let ticks = 0;
      const t = setInterval(() => {
        refresh();
        if (++ticks >= 12) {
          clearInterval(t);
          setScanning(false);
        }
      }, 2500);
    } catch (e) {
      flash((e as Error).message);
      setScanning(false);
    }
  };

  const flash = (msg: string) => {
    setToast(msg);
    window.setTimeout(() => setToast(null), 3500);
  };

  const refresh = () =>
    api
      .movies()
      .then((r) => {
        setMovies(r.movies);
        setMetaOK(r.metadata_available);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));

  useEffect(() => {
    refresh();
    api.qualityProfiles("movie").then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })))).catch(() => {});
  }, []);

  const filtered = movies
    .filter((m) => matchesFilter(m, filter))
    .sort((a, b) => a.title.localeCompare(b.title)); // default: alphabetical by title

  const toggleSelect = (id: number) =>
    setSelected((s) => {
      const n = new Set(s);
      n.has(id) ? n.delete(id) : n.add(id);
      return n;
    });
  const clearSelect = () => setSelected(new Set());
  const exitMultiSelect = () => {
    setMultiSelect(false);
    clearSelect();
  };

  const bulkMonitor = async (mon: boolean) => {
    setBulkBusy(true);
    try {
      await Promise.all([...selected].map((id) => api.setMonitored(id, mon)));
      flash(`${selected.size} ${mon ? "monitored" : "unmonitored"}.`);
      clearSelect();
      refresh();
    } finally {
      setBulkBusy(false);
    }
  };
  const bulkProfile = async (profile: string) => {
    setBulkBusy(true);
    try {
      await Promise.all([...selected].map((id) => api.setQualityProfile(id, profile)));
      flash(`Quality profile set on ${selected.size} movies.`);
      clearSelect();
      refresh();
    } finally {
      setBulkBusy(false);
    }
  };

  // Poll while any movie is downloading so the grid indicators advance.
  const anyDownloading = movies.some((m) => m.download);
  useEffect(() => {
    if (!anyDownloading) return;
    const t = setInterval(refresh, 4000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [anyDownloading]);

  const doDelete = async (id: number, deleteFiles: boolean) => {
    await api.deleteMovie(id, deleteFiles);
    setConfirmDelete(null);
    refresh();
  };

  const search = async (m: Movie) => {
    try {
      await api.searchMovie(m.id);
      flash(`Searching for “${m.title}” — it'll appear in Activity once grabbed.`);
    } catch (e) {
      flash((e as Error).message);
    }
  };

  return (
    <>
      <PageHeader title="Movies" crumb="Library / Movies" />
      <div className="mx-auto w-full max-w-[1440px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex items-center justify-between gap-3">
          <span className="font-mono text-[11px] text-ink-faint">{movies.length} in library</span>
          <div className="flex items-center gap-2">
            <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
              {(["grid", "table"] as const).map((v) => (
                <button key={v} onClick={() => setView(v)} className="rounded-md px-3 py-1.5 text-[11.5px] font-semibold capitalize" style={{ background: view === v ? "var(--accent)" : "transparent", color: view === v ? "var(--accent-ink)" : "var(--ink-faint)" }}>{v}</button>
              ))}
            </div>
            <button
              onClick={scanLibrary}
              disabled={scanning}
              title="Find movies already in your library folder and catalog them"
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
              onClick={() => setShowAdd(true)}
              disabled={!metaOK}
              className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold"
              style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: metaOK ? 1 : 0.5 }}
            >
              + Add movie
            </button>
          </div>
        </div>

        {/* Filters */}
        <div className="mb-4 flex flex-wrap gap-2">
          {FILTERS.map((f) => {
            const active = filter === f.key;
            const count = f.key === "all" ? movies.length : movies.filter((m) => matchesFilter(m, f.key)).length;
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

        {/* Bulk actions */}
        {multiSelect && (
          <div className="mb-4 flex flex-wrap items-center gap-2.5 rounded-xl p-3" style={{ background: "var(--panel)", border: "1px solid var(--accent-line)" }}>
            <span className="font-mono text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>{selected.size} selected</span>
            <button onClick={() => setSelected(new Set(filtered.map((m) => m.id)))} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Select all ({filtered.length})</button>
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
              {profiles.map((p) => (
                <option key={p.key} value={p.key}>{p.name}</option>
              ))}
            </select>
          </div>
        )}

        {!metaOK && (
          <div className="mb-4 rounded-lg p-3.5 text-[12.5px]" style={{ border: "1px solid var(--avoid)", background: "var(--avoid-soft)", color: "var(--avoid)" }}>
            <b>Metadata not configured.</b> To add movies, set <span className="font-mono">ARRMADA_TMDB_API_KEY</span> (a free key from themoviedb.org) and restart.
          </div>
        )}
        {error && (
          <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>
            {error}
          </div>
        )}

        {movies.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No movies yet. Click <b>Add movie</b>, search for a film, and Arrmada will monitor and grab it.
          </div>
        ) : filtered.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No movies match the <b>{FILTERS.find((f) => f.key === filter)?.label}</b> filter.
          </div>
        ) : view === "table" ? (
          <MovieTable movies={filtered} multiSelect={multiSelect} selected={selected} onToggleSelect={toggleSelect} />
        ) : (
          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))" }}>
            {filtered.map((m) => (
              <MovieCard
                key={m.id}
                m={m}
                onDelete={() => setConfirmDelete(m)}
                onSearch={() => search(m)}
                selectable={multiSelect}
                selected={selected.has(m.id)}
                onToggleSelect={() => toggleSelect(m.id)}
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

      {showAdd && <AddMovieModal onClose={() => setShowAdd(false)} onAdded={refresh} />}
      {confirmDelete && <DeleteMovieModal movie={confirmDelete} onClose={() => setConfirmDelete(null)} onConfirm={(df) => doDelete(confirmDelete.id, df)} />}
    </>
  );
}

function statusOf(m: Movie): { label: string; tone: string } {
  if (m.has_file) return { label: "Downloaded", tone: "var(--good)" };
  if (m.monitored) return { label: "Wanted", tone: "var(--avoid)" };
  return { label: "Unmonitored", tone: "var(--ink-faint)" };
}

function Poster({ url, title }: { url?: string; title: string }) {
  if (url) {
    return <img src={url} alt={title} className="h-full w-full object-cover" loading="lazy" />;
  }
  return (
    <div className="flex h-full w-full items-end p-2.5" style={{ background: "linear-gradient(150deg, hsl(24 40% 30%), hsl(20 35% 16%))" }}>
      <span className="text-[13px] font-bold text-white" style={{ textShadow: "0 1px 4px rgba(0,0,0,.6)" }}>{title}</span>
    </div>
  );
}

function gb(bytes?: number): string {
  if (!bytes) return "—";
  return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
}
function bitrateNum(f?: Movie["file"]): number | undefined {
  if (!f?.size_bytes || !f.duration_min) return undefined;
  return (f.size_bytes * 8) / (f.duration_min * 60) / 1e6;
}
function bitrateMbps(f?: Movie["file"]): string {
  const n = bitrateNum(f);
  return n === undefined ? "—" : `${n.toFixed(1)} Mbps`;
}
function resValue(f?: Movie["file"]): number | undefined {
  const r = f?.resolution || (f?.quality ? f.quality.split(" ")[0] : "");
  if (!r) return undefined;
  const s = r.toLowerCase();
  if (s.includes("4k") || s.includes("2160")) return 2160;
  if (s.includes("1440")) return 1440;
  if (s.includes("1080")) return 1080;
  if (s.includes("720")) return 720;
  if (s.includes("480") || s === "sd") return 480;
  const n = parseInt(s.replace(/\D/g, ""), 10);
  return isNaN(n) ? undefined : n;
}
function fileExt(f?: Movie["file"]): string {
  if (!f?.filename) return "—";
  const i = f.filename.lastIndexOf(".");
  return i >= 0 ? f.filename.slice(i + 1).toLowerCase() : "—";
}
function hasAtmos(f?: Movie["file"]): boolean {
  return !!f?.audio?.some((a) => /atmos/i.test(a));
}

// YesNo renders a clear yes/no so gaps (missing HDR/Atmos) are easy to scan for.
function YesNo({ on, label }: { on: boolean; label?: string }) {
  return on ? (
    <span className="rounded px-1.5 py-0.5 text-[10px] font-bold" style={{ background: "var(--good-soft, rgba(90,140,90,.14))", color: "var(--good)" }}>{label ?? "Yes"}</span>
  ) : (
    <span className="text-[11px] text-ink-faint">—</span>
  );
}

type SortKey = "title" | "status" | "resolution" | "codec" | "audio" | "atmos" | "hdr" | "type" | "size" | "bitrate";

// sortValue returns a comparable value per column (undefined = empty → sorted last regardless of dir).
function sortValue(m: Movie, key: SortKey): number | string | undefined {
  const f = m.file;
  switch (key) {
    case "title": return m.title.toLowerCase();
    case "status": return m.has_file ? 0 : m.monitored ? 1 : 2;
    case "resolution": return resValue(f);
    case "codec": return f?.codec || undefined;
    case "audio": return f?.audio?.length ? f.audio.join(", ").toLowerCase() : undefined;
    case "atmos": return hasAtmos(f) ? 1 : 0;
    case "hdr": return f?.hdr?.length ? 1 : 0;
    case "type": { const e = fileExt(f); return e === "—" ? undefined : e; }
    case "size": return f?.size_bytes || undefined;
    case "bitrate": return bitrateNum(f);
  }
}

function MovieTable({ movies, multiSelect, selected, onToggleSelect }: { movies: Movie[]; multiSelect: boolean; selected: Set<number>; onToggleSelect: (id: number) => void }) {
  const th = "px-2.5 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-[0.06em] text-ink-faint";
  const td = "px-2.5 py-2 align-middle";
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "title", dir: "asc" });
  const onSort = (key: SortKey) => setSort((s) => (s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: "asc" }));

  const sorted = useMemo(() => {
    const arr = [...movies];
    arr.sort((a, b) => {
      const av = sortValue(a, sort.key), bv = sortValue(b, sort.key);
      const aE = av === undefined, bE = bv === undefined;
      if (aE && bE) return 0;
      if (aE) return 1; // empties always last
      if (bE) return -1;
      let r: number;
      if (typeof av === "number" && typeof bv === "number") r = av - bv;
      else r = String(av).localeCompare(String(bv));
      return sort.dir === "asc" ? r : -r;
    });
    return arr;
  }, [movies, sort]);

  const Th = ({ label, k, align }: { label: string; k: SortKey; align?: "right" }) => {
    const active = sort.key === k;
    return (
      <th className={`${th}${align === "right" ? " text-right" : ""}`}>
        <button onClick={() => onSort(k)} className={`inline-flex items-center gap-1 hover:text-[var(--ink)] ${align === "right" ? "flex-row-reverse" : ""}`} style={{ color: active ? "var(--accent)" : undefined }}>
          {label}
          <span className="text-[8px]" style={{ opacity: active ? 1 : 0.3 }}>{active ? (sort.dir === "asc" ? "▲" : "▼") : "▲"}</span>
        </button>
      </th>
    );
  };

  return (
    <div className="overflow-x-auto thin-scroll rounded-xl" style={{ border: "1px solid var(--line)" }}>
      <table className="w-full border-collapse text-[12px]" style={{ minWidth: "900px" }}>
        <thead>
          <tr style={{ background: "var(--panel)", borderBottom: "1px solid var(--line)" }}>
            {multiSelect && <th className={th}></th>}
            <Th label="Title" k="title" />
            <Th label="Status" k="status" />
            <Th label="Resolution" k="resolution" />
            <Th label="Codec" k="codec" />
            <Th label="Audio" k="audio" />
            <Th label="Atmos" k="atmos" />
            <Th label="HDR" k="hdr" />
            <Th label="Type" k="type" />
            <Th label="Size" k="size" align="right" />
            <Th label="Bitrate" k="bitrate" align="right" />
          </tr>
        </thead>
        <tbody>
          {sorted.map((m) => {
            const f = m.file;
            const st = statusOf(m);
            return (
              <tr key={m.id} className="transition-colors hover:bg-[var(--panel-2)]" style={{ background: selected.has(m.id) ? "var(--accent-soft)" : "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
                {multiSelect && (
                  <td className={td}>
                    <input type="checkbox" checked={selected.has(m.id)} onChange={() => onToggleSelect(m.id)} />
                  </td>
                )}
                <td className={`${td} min-w-[220px]`}>
                  {multiSelect ? (
                    <button onClick={() => onToggleSelect(m.id)} className="text-left font-semibold">{m.title} <span className="font-normal text-ink-faint">{m.year || ""}</span></button>
                  ) : (
                    <Link to={`/movies/${m.id}`} className="font-semibold hover:text-[var(--accent)]">{m.title} <span className="font-normal text-ink-faint">{m.year || ""}</span></Link>
                  )}
                </td>
                <td className={td}><span className="font-mono text-[10px] uppercase" style={{ color: st.tone }}>{st.label}</span></td>
                <td className={td}>{f?.resolution || (f?.quality ? f.quality.split(" ")[0] : "—")}</td>
                <td className={td}>{f?.codec || "—"}</td>
                <td className={td}>{f?.audio?.length ? f.audio.join(", ") : "—"}</td>
                <td className={td}><YesNo on={hasAtmos(f)} /></td>
                <td className={td}>{f?.hdr?.length ? <YesNo on label={f.hdr.join("/")} /> : <YesNo on={false} />}</td>
                <td className={td}><span className="font-mono text-[11px] text-ink-dim">{fileExt(f)}</span></td>
                <td className={`${td} text-right font-mono text-[11px] text-ink-dim`}>{gb(f?.size_bytes)}</td>
                <td className={`${td} text-right font-mono text-[11px] text-ink-dim`}>{bitrateMbps(f)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function MovieCard({ m, onDelete, onSearch, selectable, selected, onToggleSelect }: { m: Movie; onDelete: () => void; onSearch: () => void; selectable?: boolean; selected?: boolean; onToggleSelect?: () => void }) {
  const st = statusOf(m);
  const [searching, setSearching] = useState(false);
  const doSearch = async () => {
    setSearching(true);
    try {
      await onSearch();
    } finally {
      window.setTimeout(() => setSearching(false), 1500);
    }
  };
  return (
    <div className="group relative overflow-hidden rounded-xl" style={{ border: `1px solid ${selected ? "var(--accent)" : "var(--line)"}`, background: "var(--panel)", boxShadow: selected ? "0 0 0 1px var(--accent)" : "none" }}>
      <div className="relative" style={{ aspectRatio: "2/3" }}>
        {selectable ? (
          <button onClick={onToggleSelect} className="block h-full w-full text-left">
            <Poster url={m.poster_url} title={m.title} />
            {selected && <div className="absolute inset-0" style={{ background: "var(--accent-soft)" }} />}
          </button>
        ) : (
          <Link to={`/movies/${m.id}`} className="block h-full w-full">
            <Poster url={m.poster_url} title={m.title} />
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
        {!selectable && !m.has_file && !m.download && (
          <button
            onClick={doSearch}
            disabled={searching}
            title="Search now"
            className="absolute inset-x-1.5 bottom-1.5 hidden items-center justify-center gap-1.5 rounded-lg py-1.5 text-[11px] font-semibold group-hover:flex"
            style={{ background: "rgba(20,12,7,.82)", color: "#fff" }}
          >
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" /><path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" /></svg>
            {searching ? "Searching…" : "Search now"}
          </button>
        )}
        {m.download && (
          <div className="absolute inset-x-0 bottom-0 p-1.5">
            <div className="flex items-center gap-1.5 rounded-md px-1.5 py-1" style={{ background: "rgba(20,12,7,.82)" }}>
              <div className="h-1 flex-1 overflow-hidden rounded-full" style={{ background: "rgba(255,255,255,.25)" }}>
                <div className="h-full rounded-full" style={{ width: `${Math.round(m.download.progress * 100)}%`, background: "var(--accent)" }} />
              </div>
              <span className="font-mono text-[9px] font-bold" style={{ color: "#fff" }}>{Math.round(m.download.progress * 100)}%</span>
            </div>
          </div>
        )}
      </div>
      {selectable ? (
        <button onClick={onToggleSelect} className="block w-full p-2.5 text-left">
          <div className="truncate text-[12.5px] font-semibold" title={m.title}>{m.title}</div>
          <div className="mt-1 flex items-center justify-between">
            <span className="font-mono text-[10.5px] text-ink-faint">{m.year || "—"}</span>
            <span className="font-mono text-[9.5px] uppercase" style={{ color: st.tone }}>{st.label}</span>
          </div>
        </button>
      ) : (
      <Link to={`/movies/${m.id}`} className="block p-2.5">
        <div className="truncate text-[12.5px] font-semibold" title={m.title}>{m.title}</div>
        <div className="mt-1 flex items-center justify-between">
          <span className="font-mono text-[10.5px] text-ink-faint">{m.year || "—"}</span>
          <span className="font-mono text-[9.5px] uppercase" style={{ color: st.tone }}>{st.label}</span>
        </div>
      </Link>
      )}
    </div>
  );
}

function DeleteMovieModal({ movie, onClose, onConfirm }: { movie: Movie; onClose: () => void; onConfirm: (deleteFiles: boolean) => void }) {
  const [deleteFiles, setDeleteFiles] = useState(true);
  const [busy, setBusy] = useState(false);
  const confirm = async () => {
    setBusy(true);
    try {
      await onConfirm(deleteFiles);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="fixed inset-0 z-50 grid place-items-center p-6" style={{ background: "rgba(0,0,0,.6)" }} onClick={onClose}>
      <div className="w-full max-w-[440px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="flex items-start gap-3">
          <div className="h-[84px] w-[56px] flex-none overflow-hidden rounded-lg" style={{ background: "var(--panel-2)" }}>
            {movie.poster_url && <img src={movie.poster_url} alt="" className="h-full w-full object-cover" />}
          </div>
          <div className="min-w-0">
            <h2 className="m-0 text-[15px] font-bold">Remove “{movie.title}”?</h2>
            <p className="mt-1 text-[12px] text-ink-dim">It'll be removed from your library and Arrmada will stop monitoring it.</p>
          </div>
        </div>

        <label className="mt-4 flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)" }}>
          <input type="checkbox" checked={deleteFiles} onChange={(e) => setDeleteFiles(e.target.checked)} className="mt-0.5" />
          <span>
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject)" : "var(--ink)" }}>Also delete files from disk</span>
            <span className="mt-0.5 block text-[11px] text-ink-faint">{movie.has_file ? "Moves the movie's file(s) to the recycle bin." : "This movie has no files on disk."}</span>
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

function AddMovieModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<MovieLookup[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [profile, setProfile] = useState("");
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [searchOnAdd, setSearchOnAdd] = useState(true);
  const [addingId, setAddingId] = useState<number | null>(null);

  useEffect(() => {
    api
      .qualityProfiles("movie")
      .then((r) => {
        setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
        const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
        if (def) setProfile(def.key);
      })
      .catch(() => {});
    api.settings().then((s) => setSearchOnAdd(s.search_on_add)).catch(() => {});
  }, []);

  // Persist the preference so it sticks across sessions and devices.
  const toggleSearchOnAdd = () => {
    const next = !searchOnAdd;
    setSearchOnAdd(next);
    api.updateSettings({ search_on_add: next }).catch(() => {});
  };

  const search = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!q.trim()) return;
    setLoading(true);
    setError(null);
    try {
      setResults(await api.lookupMovies(q.trim()));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  const add = async (r: MovieLookup) => {
    setAddingId(r.tmdb_id);
    setError(null);
    try {
      await api.addMovie({ tmdb_id: r.tmdb_id, quality_profile: profile, monitored: true, search_on_add: searchOnAdd });
      onAdded();
      onClose();
    } catch (err) {
      setError((err as Error).message);
      setAddingId(null);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[640px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Add a movie</h2>
          <div className="flex items-center gap-4">
            <button type="button" onClick={toggleSearchOnAdd} title="On: monitor and search for a release right away. Off: add unmonitored — nothing is searched or grabbed until you monitor it." className="flex items-center gap-2">
              <span className="relative inline-block h-[20px] w-[34px] rounded-full transition-colors" style={{ background: searchOnAdd ? "var(--accent)" : "var(--line)" }}>
                <span className="absolute top-[3px] h-[14px] w-[14px] rounded-full bg-white transition-all" style={{ left: searchOnAdd ? "17px" : "3px" }} />
              </span>
              <span className="text-[11.5px] font-medium" style={{ color: searchOnAdd ? "var(--ink)" : "var(--ink-dim)" }}>{searchOnAdd ? "Search on add" : "Add unmonitored"}</span>
            </button>
            <div className="flex items-center gap-2">
              <span className="font-mono text-[10px] uppercase text-ink-faint">Quality</span>
              <select value={profile} onChange={(e) => setProfile(e.target.value)} className="rounded-lg px-2 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                {profiles.map((p) => (
                  <option key={p.key} value={p.key}>{p.name}</option>
                ))}
              </select>
            </div>
          </div>
        </div>

        <form onSubmit={search} className="mb-3 flex gap-2">
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search movies — e.g. Dune Part Two" className="flex-1 rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <button type="submit" disabled={loading} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            {loading ? "…" : "Search"}
          </button>
        </form>

        {error && <div className="mb-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}

        <div className="max-h-[52vh] overflow-y-auto">
          {results.map((r) => (
            <button
              key={r.tmdb_id}
              onClick={() => add(r)}
              disabled={addingId !== null}
              className="flex w-full items-center gap-3 rounded-lg p-2 text-left transition-colors hover:bg-[var(--panel-2)]"
            >
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
