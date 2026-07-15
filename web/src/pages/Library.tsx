import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type LibraryPaths, type BrowseResult, type UnmatchedFolder, type MatchCandidate } from "../lib/api";

// Library — System page for pointing each library at a folder, with an in-app folder picker.
// Mount your media into the container (see the installer), then browse + select here.
type PathKey = keyof LibraryPaths;
const ROWS: { key: PathKey; label: string; hint: string; scan?: () => Promise<unknown> }[] = [
  { key: "movies", label: "Movies", hint: "folder of movie subfolders", scan: () => api.scanLibrary() },
  { key: "tv", label: "TV Shows", hint: "folder of show subfolders", scan: () => api.scanSeries() },
  { key: "ebooks", label: "Ebooks", hint: "folder of book subfolders", scan: () => api.scanBooks() },
  { key: "audiobooks", label: "Audiobooks", hint: "may share the ebooks folder", scan: () => api.scanBooks() },
  { key: "downloads", label: "Downloads", hint: "where the download client saves completed files" },
];

export function Library() {
  const [paths, setPaths] = useState<LibraryPaths | null>(null);
  const [draft, setDraft] = useState<LibraryPaths | null>(null);
  const [picking, setPicking] = useState<PathKey | null>(null);
  const [busy, setBusy] = useState(false);
  const [reviewKey, setReviewKey] = useState(0); // bump to reload the unmatched lists
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  useEffect(() => { api.libraryPaths().then((p) => { setPaths(p); setDraft(p); }).catch(() => flash("Could not load library paths")); }, []);
  if (!draft) return <><PageHeader title="Library" crumb="System / Library" /><div className="mx-auto max-w-[820px] px-4 py-6 text-[12.5px] text-ink-dim">Loading…</div></>;

  const dirty = paths && (Object.keys(draft) as PathKey[]).some((k) => draft[k] !== paths[k]);
  const save = async () => {
    setBusy(true);
    try { const p = await api.setLibraryPaths(draft); setPaths(p); setDraft(p); flash("Saved"); }
    catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };
  const scan = async (row: typeof ROWS[number]) => {
    if (!row.scan) return;
    if (dirty) { flash("Save your folders first, then scan."); return; }
    try {
      await row.scan();
      flash(`Scanning ${row.label}… matches appear in the library; anything unsure lands in "needs review" below.`);
      window.setTimeout(() => setReviewKey((k) => k + 1), 5000); // reload review once the scan has had a moment
    } catch (e) { flash((e as Error).message); }
  };

  return (
    <>
      <PageHeader title="Library" crumb="System / Library" />
      <div className="mx-auto w-full max-w-[820px] px-4 py-6 sm:px-6">
        <p className="mb-5 max-w-[64ch] text-[12.5px] text-ink-dim">Point each library at a folder inside your mounted media. Use <b>Browse</b> to pick from the folders Arrmada can see, then <b>Scan</b> to catalog what's there. Ebooks and audiobooks can share one folder.</p>

        <div className="flex flex-col gap-2.5">
          {ROWS.map((row) => (
            <div key={row.key} className="rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
              <div className="flex items-center justify-between gap-3">
                <div>
                  <div className="text-[13px] font-semibold">{row.label}</div>
                  <div className="text-[10.5px] text-ink-faint">{row.hint}</div>
                </div>
                {row.scan && <button onClick={() => scan(row)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Scan</button>}
              </div>
              <div className="mt-2 flex gap-2">
                <input
                  value={draft[row.key]}
                  onChange={(e) => setDraft({ ...draft, [row.key]: e.target.value })}
                  placeholder="/storage/media/…"
                  className="flex-1 rounded-lg px-2.5 py-1.5 font-mono text-[11.5px]"
                  style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
                />
                <button onClick={() => setPicking(row.key)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Browse…</button>
              </div>
            </div>
          ))}
        </div>

        <div className="mt-4 flex items-center justify-end gap-3">
          {dirty && <span className="text-[11.5px] text-ink-faint">Unsaved changes</span>}
          <button onClick={save} disabled={!dirty || busy} className="rounded-lg px-4 py-2 text-[13px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Saving…" : "Save folders"}</button>
        </div>

        <UnmatchedReview media="movie" reloadKey={reviewKey} flash={flash} />
        <UnmatchedReview media="series" reloadKey={reviewKey} flash={flash} />
      </div>

      {picking && (
        <FolderPicker
          initial={draft[picking] || undefined}
          onClose={() => setPicking(null)}
          onSelect={(p) => { setDraft({ ...draft, [picking]: p }); setPicking(null); }}
        />
      )}
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

// UnmatchedReview lists folders the last scan couldn't confidently identify and
// lets the admin pick the right title (from candidates or a manual search), so a
// mis-titled folder never gets silently mis-filed.
function UnmatchedReview({ media, reloadKey, flash }: { media: "movie" | "series"; reloadKey: number; flash: (m: string) => void }) {
  const label = media === "movie" ? "Movies" : "TV Shows";
  const [items, setItems] = useState<UnmatchedFolder[] | null>(null);
  const [busy, setBusy] = useState(false);

  const load = () =>
    (media === "movie" ? api.moviesUnmatched() : api.seriesUnmatched()).then(setItems).catch(() => setItems([]));
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [reloadKey]);

  const pick = async (folder: string, tmdb_id: number) => {
    setBusy(true);
    try {
      await (media === "movie" ? api.importMovieFolder(folder, tmdb_id) : api.importSeriesFolder(folder, tmdb_id));
      setItems((xs) => (xs ?? []).filter((u) => u.folder !== folder));
      flash(`Imported “${folder}”.`);
    } catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };

  if (items === null || items.length === 0) return null;
  return (
    <div className="mt-4 rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-[13px] font-semibold">
            {label} — needs review
            <span className="ml-1.5 rounded-full px-1.5 py-0.5 text-[10px] font-bold" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>{items.length}</span>
          </div>
          <div className="text-[10.5px] text-ink-faint">Arrmada couldn't confidently identify these — pick the right title so nothing is mis-filed.</div>
        </div>
        <button onClick={load} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Refresh</button>
      </div>
      <div className="mt-3 flex flex-col gap-2.5">
        {items.map((u) => <UnmatchedRow key={u.folder} media={media} item={u} busy={busy} onPick={pick} />)}
      </div>
    </div>
  );
}

function UnmatchedRow({ media, item, busy, onPick }: { media: "movie" | "series"; item: UnmatchedFolder; busy: boolean; onPick: (folder: string, tmdb: number) => void }) {
  const [q, setQ] = useState(item.title);
  const [results, setResults] = useState<MatchCandidate[] | null>(null);
  const [searching, setSearching] = useState(false);

  const search = async () => {
    if (!q.trim()) return;
    setSearching(true);
    try {
      const r = media === "movie" ? await api.lookupMovies(q) : await api.lookupSeries(q);
      setResults(r.map((x) => ({ tmdb_id: x.tmdb_id, title: x.title, year: x.year, poster_url: x.poster_url })));
    } catch { setResults([]); } finally { setSearching(false); }
  };
  const shown = results ?? item.candidates;

  return (
    <div className="rounded-lg p-2.5" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex items-center gap-2 text-[12px]">
        <span style={{ color: "var(--accent)" }}>📁</span>
        <span className="font-mono font-semibold">{item.folder}</span>
        <span className="text-ink-faint">— looked for “{item.title}”{item.year ? ` (${item.year})` : ""}</span>
      </div>
      <div className="mt-2 flex flex-wrap gap-1.5">
        {shown.length === 0 && <span className="text-[11px] text-ink-faint">No candidates — search a different title below.</span>}
        {shown.map((c) => (
          <button
            key={c.tmdb_id}
            disabled={busy}
            onClick={() => onPick(item.folder, c.tmdb_id)}
            title={c.overview}
            className="flex items-center gap-2 rounded-lg px-2 py-1 text-left text-[11.5px] disabled:opacity-50"
            style={{ border: "1px solid var(--line)", background: "var(--panel)" }}
          >
            {c.poster_url
              ? <img src={c.poster_url} alt="" className="h-9 w-6 flex-none rounded object-cover" />
              : <span className="grid h-9 w-6 flex-none place-items-center rounded text-[9px] text-ink-faint" style={{ background: "var(--panel-2)" }}>?</span>}
            <span><span className="font-semibold">{c.title}</span>{c.year ? <span className="text-ink-faint"> ({c.year})</span> : ""}</span>
          </button>
        ))}
      </div>
      <div className="mt-2 flex gap-1.5">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && search()}
          placeholder="Search a different title…"
          className="flex-1 rounded-lg px-2 py-1 text-[11.5px]"
          style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
        />
        <button onClick={search} disabled={searching} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink)" }}>{searching ? "…" : "Search"}</button>
      </div>
    </div>
  );
}

function FolderPicker({ initial, onClose, onSelect }: { initial?: string; onClose: () => void; onSelect: (path: string) => void }) {
  const [data, setData] = useState<BrowseResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const go = (path?: string) => { setErr(null); api.browseFolders(path).then(setData).catch((e) => setErr((e as Error).message)); };
  useEffect(() => { go(initial); /* eslint-disable-next-line */ }, []);

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Select a folder</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>

        <div className="mb-2 flex items-center gap-2">
          <button onClick={() => data && go(data.parent)} disabled={!data || data.path === "/"} className="rounded-lg px-2.5 py-1.5 text-[12px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} title="Up one level">↑</button>
          <div className="min-w-0 flex-1 truncate rounded-lg px-2.5 py-1.5 font-mono text-[11.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{data?.path ?? "…"}</div>
        </div>

        {err && <div className="mb-2 rounded-lg p-2.5 text-[11.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{err}</div>}

        <div className="thin-scroll max-h-[46vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {!data ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">Loading…</div>
          ) : data.dirs.length === 0 ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">No sub-folders here.</div>
          ) : data.dirs.map((d) => (
            <button key={d.path} onClick={() => go(d.path)} className="flex w-full items-center gap-2 px-3 py-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <span style={{ color: "var(--accent)" }}>📁</span>
              <span className="truncate">{d.name}</span>
            </button>
          ))}
        </div>

        <div className="mt-3 flex items-center justify-between gap-2">
          <span className="text-[10.5px] text-ink-faint">Navigate into the folder you want, then select it.</span>
          <button onClick={() => data && onSelect(data.path)} disabled={!data} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Select this folder</button>
        </div>
      </div>
    </div>
  );
}
