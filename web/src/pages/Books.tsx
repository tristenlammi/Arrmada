import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type Book, type BookLookup, type BookAuthor, type BookDiscoverCard } from "../lib/api";
import { posterThumb } from "../lib/img";

const FILTERS = [
  { key: "all", label: "All" },
  { key: "monitored", label: "Monitored" },
  { key: "missing", label: "Missing" },
  { key: "downloaded", label: "Downloaded" },
] as const;
type FilterKey = (typeof FILTERS)[number]["key"];

function matches(b: Book, f: FilterKey): boolean {
  switch (f) {
    case "monitored": return b.monitored;
    case "missing": return !b.has_file;
    case "downloaded": return b.has_file;
    default: return true;
  }
}

function statusOf(b: Book): { label: string; tone: string } {
  if (b.has_file) {
    // Show which editions are present (E / A) so the grid conveys ebook vs audiobook.
    const tags = [b.ebook && "E", b.audiobook && "A"].filter(Boolean).join("+");
    return { label: tags ? `${tags} ✓` : "Downloaded", tone: "var(--good)" };
  }
  if (b.monitored) return { label: "Wanted", tone: "var(--avoid)" };
  return { label: "Unmonitored", tone: "var(--ink-faint)" };
}

export function Books() {
  const [list, setList] = useState<Book[]>([]);
  const [metaOK, setMetaOK] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<FilterKey>("all");
  const [query, setQuery] = useState("");
  const [adding, setAdding] = useState(false);
  const [addingAuthor, setAddingAuthor] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Book | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const [view, setView] = useState<"grid" | "table">("grid");
  const [mode, setMode] = useState<"book" | "author">("author");
  const [scanning, setScanning] = useState(false);
  const [multiSelect, setMultiSelect] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);

  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };
  const refresh = () => api.books().then((r) => { setList(r.books); setMetaOK(r.metadata_available); setError(null); }).catch((e: Error) => setError(e.message));
  useEffect(() => {
    refresh();
    api.qualityProfiles("book").then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })))).catch(() => {});
  }, []);

  const toggleSelect = (id: number) => setSelected((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const clearSelect = () => setSelected(new Set());
  const exitMultiSelect = () => { setMultiSelect(false); clearSelect(); };
  const enterSelect = () => { setMode("book"); setMultiSelect(true); }; // selection is per-book
  const bulkMonitor = async (mon: boolean) => {
    setBulkBusy(true);
    try {
      await Promise.all([...selected].map((id) => api.setBookMonitored(id, mon)));
      flash(`${selected.size} ${mon ? "monitored" : "unmonitored"}.`);
      clearSelect();
      refresh();
    } finally { setBulkBusy(false); }
  };
  const bulkProfile = async (profile: string) => {
    setBulkBusy(true);
    try {
      await Promise.all([...selected].map((id) => api.setBookProfile(id, profile)));
      flash(`Quality profile set on ${selected.size} books.`);
      clearSelect();
      refresh();
    } finally { setBulkBusy(false); }
  };

  const q = query.trim().toLowerCase();
  const filtered = useMemo(
    () =>
      list
        .filter((b) => matches(b, filter))
        .filter((b) => !q || b.title.toLowerCase().includes(q) || (b.author ?? "").toLowerCase().includes(q)),
    [list, filter, q],
  );
  const authors = useMemo(() => {
    const map = new Map<string, Book[]>();
    for (const b of filtered) {
      const name = b.author || "Unknown author";
      const arr = map.get(name);
      if (arr) arr.push(b); else map.set(name, [b]);
    }
    return [...map.entries()]
      .map(([name, books]) => ({ name, books: books.slice().sort((a, b) => a.title.localeCompare(b.title)) }))
      .sort((a, b) => a.name.localeCompare(b.name)); // authors A→Z, books A→Z within each
  }, [filtered]);

  const doDelete = async (id: number, deleteFiles: boolean) => { await api.deleteBook(id, deleteFiles); setConfirmDelete(null); refresh(); };
  const search = async (b: Book) => {
    try { await api.searchBook(b.id); flash(`Searching for “${b.title}”…`); } catch (e) { flash((e as Error).message); }
  };
  const scanLibrary = async () => {
    setScanning(true);
    try {
      await api.scanBooks();
      flash("Scanning your library — books will appear shortly.");
      let ticks = 0;
      const t = setInterval(() => { refresh(); if (++ticks >= 12) { clearInterval(t); setScanning(false); } }, 2500);
    } catch (e) { flash((e as Error).message); setScanning(false); }
  };

  return (
    <>
      <PageHeader title="Books" crumb="Library / Books" />
      <div className="mx-auto w-full max-w-[1440px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex items-center justify-between gap-3">
          <span className="font-mono text-[11px] text-ink-faint">{list.length} in library</span>
          <div className="flex items-center gap-2">
            {!multiSelect && (
              <>
                <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
                  {(["author", "book"] as const).map((m) => (
                    <button key={m} onClick={() => setMode(m)} className="rounded-md px-3 py-1.5 text-[11.5px] font-semibold capitalize" style={{ background: mode === m ? "var(--accent)" : "transparent", color: mode === m ? "var(--accent-ink)" : "var(--ink-faint)" }}>{m}</button>
                  ))}
                </div>
                <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
                  {(["grid", "table"] as const).map((v) => (
                    <button key={v} onClick={() => setView(v)} className="rounded-md px-3 py-1.5 text-[11.5px] font-semibold capitalize" style={{ background: view === v ? "var(--accent)" : "transparent", color: view === v ? "var(--accent-ink)" : "var(--ink-faint)" }}>{v}</button>
                  ))}
                </div>
              </>
            )}
            <button onClick={scanLibrary} disabled={scanning} title="Find books already in your library folder and catalog them" className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{scanning ? "Scanning…" : "Scan library"}</button>
            <button onClick={() => (multiSelect ? exitMultiSelect() : enterSelect())} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: `1px solid ${multiSelect ? "var(--accent)" : "var(--line)"}`, background: multiSelect ? "var(--accent-soft)" : "var(--panel-2)", color: multiSelect ? "var(--accent)" : "var(--ink)" }}>{multiSelect ? "Done" : "Select"}</button>
            <button onClick={() => setAddingAuthor(true)} disabled={!metaOK} title="Add an author's entire catalogue of official books" className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", background: "var(--panel-2)", color: "var(--accent)", opacity: metaOK ? 1 : 0.5 }}>+ Add author</button>
            <button onClick={() => setAdding(true)} disabled={!metaOK} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: metaOK ? 1 : 0.5 }}>+ Add book</button>
          </div>
        </div>

        <div className="mb-4 flex flex-wrap items-center gap-2">
          {FILTERS.map((f) => {
            const active = filter === f.key;
            const count = f.key === "all" ? list.length : list.filter((b) => matches(b, f.key)).length;
            return (
              <button key={f.key} onClick={() => setFilter(f.key)} className="rounded-full px-3 py-1 text-[12px] font-semibold" style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "var(--panel)", color: active ? "var(--accent)" : "var(--ink-faint)" }}>
                {f.label} <span className="font-mono text-[10.5px] opacity-70">{count}</span>
              </button>
            );
          })}
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search title or author…"
            className="ml-auto w-[240px] rounded-lg px-3 py-1.5 text-[12px]"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
          />
        </div>

        {multiSelect && (
          <div className="mb-4 flex flex-wrap items-center gap-2.5 rounded-xl p-3" style={{ background: "var(--panel)", border: "1px solid var(--accent-line)" }}>
            <span className="font-mono text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>{selected.size} selected</span>
            <button onClick={() => setSelected(new Set(filtered.map((b) => b.id)))} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Select all ({filtered.length})</button>
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

        {error && <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}

        {list.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No books yet. Click <b>Add book</b>, search by title or author, and Arrmada will monitor and grab it.
          </div>
        ) : filtered.length === 0 ? (
          <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{q ? <>No books match “<b>{query.trim()}</b>”.</> : <>No books match the <b>{FILTERS.find((f) => f.key === filter)?.label}</b> filter.</>}</div>
        ) : mode === "author" ? (
          view === "table" ? <AuthorTable authors={authors} /> : (
            <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))" }}>
              {authors.map((a) => <AuthorCard key={a.name} name={a.name} books={a.books} />)}
            </div>
          )
        ) : view === "table" ? (
          <BooksTable list={filtered} />
        ) : (
          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
            {filtered.map((b) => (
              <Card
                key={b.id}
                b={b}
                onDelete={() => setConfirmDelete(b)}
                onSearch={() => search(b)}
                selectable={multiSelect}
                selected={selected.has(b.id)}
                onToggleSelect={() => toggleSelect(b.id)}
              />
            ))}
          </div>
        )}

        {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
      </div>
      {adding && <AddBookModal onClose={() => setAdding(false)} onAdded={() => { setAdding(false); refresh(); }} />}
      {addingAuthor && <AddAuthorModal onClose={() => setAddingAuthor(false)} onAdded={(msg) => { setAddingAuthor(false); refresh(); flash(msg); }} />}
      {confirmDelete && <DeleteBookModal book={confirmDelete} onClose={() => setConfirmDelete(null)} onConfirm={(deleteFiles) => doDelete(confirmDelete.id, deleteFiles)} />}
    </>
  );
}

function gb(bytes?: number): string {
  if (!bytes) return "—";
  const mb = bytes / 1024 ** 2;
  return mb >= 1000 ? `${(mb / 1024).toFixed(1)} GB` : `${mb.toFixed(0)} MB`;
}

function editionTags(b: Book): string {
  const t: string[] = [];
  if (b.ebook) t.push(b.ebook.format);
  if (b.audiobook) t.push(b.audiobook.file_count > 1 ? `${b.audiobook.format}×${b.audiobook.file_count}` : b.audiobook.format);
  return t.join(" + ") || "—";
}

function BooksTable({ list }: { list: Book[] }) {
  const th = "px-2.5 py-2 text-left font-mono text-[9.5px] font-bold uppercase tracking-[0.06em] text-ink-faint";
  const td = "px-2.5 py-2 align-middle";
  return (
    <div className="thin-scroll overflow-x-auto rounded-xl" style={{ border: "1px solid var(--line)" }}>
      <table className="w-full border-collapse text-[12px]" style={{ minWidth: "780px" }}>
        <thead>
          <tr style={{ background: "var(--panel)", borderBottom: "1px solid var(--line)" }}>
            <th className={th}>Title</th>
            <th className={th}>Author</th>
            <th className={`${th} text-right`}>Year</th>
            <th className={th}>Status</th>
            <th className={th}>Editions</th>
            <th className={`${th} text-right`}>Size</th>
            <th className={th}>Monitored</th>
          </tr>
        </thead>
        <tbody>
          {list.map((b) => {
            const st = statusOf(b);
            const size = (b.ebook?.size_bytes ?? 0) + (b.audiobook?.size_bytes ?? 0);
            return (
              <tr key={b.id} className="transition-colors hover:bg-[var(--panel-2)]" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
                <td className={`${td} min-w-[200px]`}><Link to={`/books/${b.id}`} className="font-semibold hover:text-[var(--accent)]">{b.title}</Link></td>
                <td className={td}>{b.author || "—"}</td>
                <td className={`${td} text-right font-mono text-[11px] text-ink-dim`}>{b.year || "—"}</td>
                <td className={td}><span className="font-mono text-[10px] uppercase" style={{ color: st.tone }}>{st.label}</span></td>
                <td className={td}><span className="font-mono text-[11px] text-ink-dim">{editionTags(b)}</span></td>
                <td className={`${td} text-right font-mono text-[11px] text-ink-dim`}>{size > 0 ? gb(size) : "—"}</td>
                <td className={td}><span className="font-mono text-[10px] uppercase" style={{ color: b.monitored ? "var(--accent)" : "var(--ink-faint)" }}>{b.monitored ? "Yes" : "No"}</span></td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Cover({ url, title }: { url?: string; title: string }) {
  if (url) return <img src={posterThumb(url)} alt={title} className="h-full w-full object-cover" loading="lazy" decoding="async" />;
  return (
    <div className="flex h-full w-full items-center justify-center p-3 text-center" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>
      <span className="text-[12px] font-bold text-white">{title}</span>
    </div>
  );
}

function Card({ b, onDelete, onSearch, selectable, selected, onToggleSelect }: { b: Book; onDelete: () => void; onSearch: () => void; selectable?: boolean; selected?: boolean; onToggleSelect?: () => void }) {
  const st = statusOf(b);
  const [searching, setSearching] = useState(false);
  const doSearch = async () => { setSearching(true); try { await onSearch(); } finally { window.setTimeout(() => setSearching(false), 1500); } };
  const meta = (
    <>
      <div className="truncate text-[12.5px] font-semibold" title={b.title}>{b.title}</div>
      <div className="mt-1 flex items-center justify-between gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-ink-faint" title={b.author}>{b.author || "—"}</span>
        <span className="flex-none font-mono text-[9.5px] uppercase" style={{ color: st.tone }}>{st.label}</span>
      </div>
    </>
  );
  return (
    <div className="group relative overflow-hidden rounded-xl" style={{ border: `1px solid ${selected ? "var(--accent)" : "var(--line)"}`, background: "var(--panel)", boxShadow: selected ? "0 0 0 1px var(--accent)" : "none" }}>
      <div className="relative" style={{ aspectRatio: "2/3" }}>
        {selectable ? (
          <button onClick={onToggleSelect} className="block h-full w-full text-left">
            <Cover url={b.cover_url} title={b.title} />
            {selected && <div className="absolute inset-0" style={{ background: "var(--accent-soft)" }} />}
          </button>
        ) : (
          <Link to={`/books/${b.id}`} className="block h-full w-full"><Cover url={b.cover_url} title={b.title} /></Link>
        )}
        {selectable && (
          <span className="absolute left-1.5 top-1.5 grid h-6 w-6 place-items-center rounded-full" style={{ background: selected ? "var(--accent)" : "rgba(20,12,7,.65)", border: `1.5px solid ${selected ? "var(--accent)" : "rgba(255,255,255,.6)"}` }}>
            {selected && <svg width="13" height="13" viewBox="0 0 24 24" fill="none"><path d="M4 12l5 5L20 6" stroke="var(--accent-ink)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>}
          </span>
        )}
        {!selectable && (
          <button onClick={onDelete} title="Remove" className="absolute right-1.5 top-1.5 hidden h-6 w-6 place-items-center rounded-full group-hover:grid" style={{ background: "rgba(20,12,7,.75)", color: "#fff" }}>
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" /></svg>
          </button>
        )}
        {!selectable && b.monitored && !b.has_file && (
          <button onClick={doSearch} disabled={searching} title="Search now" className="absolute inset-x-1.5 bottom-1.5 hidden items-center justify-center gap-1.5 rounded-lg py-1.5 text-[11px] font-semibold group-hover:flex" style={{ background: "rgba(20,12,7,.82)", color: "#fff" }}>
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" /><path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" /></svg>
            {searching ? "Searching…" : "Search now"}
          </button>
        )}
      </div>
      {selectable ? (
        <button onClick={onToggleSelect} className="block w-full p-2.5 text-left">{meta}</button>
      ) : (
        <Link to={`/books/${b.id}`} className="block p-2.5">{meta}</Link>
      )}
    </div>
  );
}

function DeleteBookModal({ book, onClose, onConfirm }: { book: Book; onClose: () => void; onConfirm: (deleteFiles: boolean) => void }) {
  const [deleteFiles, setDeleteFiles] = useState(true);
  const [busy, setBusy] = useState(false);
  return (
    <div className="fixed inset-0 z-50 grid place-items-center p-6" style={{ background: "rgba(0,0,0,.6)" }} onClick={onClose}>
      <div className="w-full max-w-[440px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <h2 className="m-0 text-[15px] font-bold">Remove “{book.title}”?</h2>
        <p className="mt-1 text-[12px] text-ink-dim">It'll be removed from your library and Arrmada will stop monitoring it.</p>

        <label className="mt-4 flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)", cursor: book.has_file ? "pointer" : "default", opacity: book.has_file ? 1 : 0.6 }}>
          <input type="checkbox" checked={deleteFiles} disabled={!book.has_file} onChange={(e) => setDeleteFiles(e.target.checked)} className="mt-0.5" />
          <span>
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject)" : "var(--ink)" }}>Also delete files from disk</span>
            <span className="mt-0.5 block text-[11px] text-ink-faint">{book.has_file ? "Moves the ebook and/or audiobook file(s) to the recycle bin." : "This book has no files on disk."}</span>
          </span>
        </label>

        <div className="mt-4 flex justify-end gap-2.5">
          <button onClick={onClose} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          <button onClick={async () => { setBusy(true); try { await onConfirm(deleteFiles); } finally { setBusy(false); } }} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>{busy ? "Removing…" : deleteFiles ? "Remove + delete files" : "Remove"}</button>
        </div>
      </div>
    </div>
  );
}

function AddBookModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<BookLookup[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [profile, setProfile] = useState("");
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [addingKey, setAddingKey] = useState<string | null>(null);

  useEffect(() => {
    api.qualityProfiles("book").then((r) => {
      setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
  }, []);

  const search = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!q.trim()) return;
    setLoading(true); setError(null);
    try { setResults(await api.lookupBooks(q.trim())); } catch (e) { setError((e as Error).message); } finally { setLoading(false); }
  };

  const add = async (r: BookLookup) => {
    setAddingKey(r.key); setError(null);
    try { await api.addBook({ ol_key: r.key, quality_profile: profile, monitored: true, title: r.title, author: r.author, year: r.year, cover_url: r.cover_url }); onAdded(); }
    catch (e) { setError((e as Error).message); setAddingKey(null); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[640px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Add a book</h2>
          <div className="flex items-center gap-2">
            <span className="font-mono text-[10px] uppercase text-ink-faint">Format</span>
            <select value={profile} onChange={(e) => setProfile(e.target.value)} className="rounded-lg px-2 py-1.5 text-[12px]" style={inputStyle}>
              {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
            </select>
          </div>
        </div>
        <form onSubmit={search} className="mb-3 flex gap-2">
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search by title or author — e.g. Dune Herbert" className="flex-1 rounded-lg px-3 py-2 text-[13px]" style={inputStyle} />
          <button type="submit" disabled={loading} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{loading ? "…" : "Search"}</button>
        </form>
        {error && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="thin-scroll max-h-[56vh] overflow-y-auto">
          {results.map((r) => (
            <button key={r.key} onClick={() => add(r)} disabled={addingKey !== null} className="flex w-full items-center gap-3 rounded-lg p-2 text-left transition-colors hover:bg-[var(--panel-2)]">
              <div className="h-[66px] w-[44px] flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
                {r.cover_url && <img src={r.cover_url} alt="" className="h-full w-full object-cover" loading="lazy" />}
              </div>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[13px] font-semibold">{r.title} <span className="font-normal text-ink-faint">{r.year ? `(${r.year})` : ""}</span></div>
                <div className="truncate text-[11.5px] text-ink-dim">{r.author || "Unknown author"}</div>
              </div>
              <span className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{addingKey === r.key ? "Adding…" : "Add"}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

function authorStats(books: Book[]): { downloaded: number; wanted: number; cover?: string } {
  let downloaded = 0, wanted = 0;
  for (const b of books) {
    if (b.has_file) downloaded++;
    else if (b.monitored) wanted++;
  }
  return { downloaded, wanted, cover: books.find((b) => b.cover_url)?.cover_url };
}

function AuthorCard({ name, books }: { name: string; books: Book[] }) {
  const { downloaded, wanted, cover } = authorStats(books);
  return (
    <Link to={`/books/author/${encodeURIComponent(name)}`} className="group relative block overflow-hidden rounded-xl" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      {cover ? (
        <img src={posterThumb(cover)} alt={name} className="h-full w-full object-cover transition-transform group-hover:scale-[1.03]" loading="lazy" decoding="async" />
      ) : (
        <div className="flex h-full w-full items-center justify-center text-[34px] font-bold text-white" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>{name.charAt(0).toUpperCase()}</div>
      )}
      <div className="absolute inset-x-0 bottom-0 p-2.5" style={{ background: "linear-gradient(to top, rgba(0,0,0,.92), rgba(0,0,0,.35) 60%, transparent)" }}>
        <div className="truncate text-[12.5px] font-bold text-white" title={name}>{name}</div>
        <div className="mt-0.5 flex items-center gap-2 text-[10px]" style={{ color: "rgba(255,255,255,.8)" }}>
          <span>{books.length} book{books.length === 1 ? "" : "s"}</span>
          {downloaded > 0 && <span style={{ color: "var(--good)" }}>{downloaded} ✓</span>}
          {wanted > 0 && <span style={{ color: "var(--avoid)" }}>{wanted} wanted</span>}
        </div>
      </div>
    </Link>
  );
}

function AuthorTable({ authors }: { authors: { name: string; books: Book[] }[] }) {
  return (
    <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
      <table className="w-full border-collapse text-[12.5px]">
        <thead>
          <tr style={{ background: "var(--panel-2)" }}>
            <th className="px-3 py-2 text-left font-mono text-[10px] font-bold uppercase tracking-wide text-ink-faint">Author</th>
            <th className="px-3 py-2 text-right font-mono text-[10px] font-bold uppercase tracking-wide text-ink-faint">Books</th>
            <th className="px-3 py-2 text-right font-mono text-[10px] font-bold uppercase tracking-wide text-ink-faint">Downloaded</th>
            <th className="px-3 py-2 text-right font-mono text-[10px] font-bold uppercase tracking-wide text-ink-faint">Wanted</th>
          </tr>
        </thead>
        <tbody>
          {authors.map((a, i) => {
            const { downloaded, wanted } = authorStats(a.books);
            return (
              <tr key={a.name} style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                <td className="px-3 py-2">
                  <Link to={`/books/author/${encodeURIComponent(a.name)}`} className="font-semibold hover:underline" style={{ color: "var(--accent)" }}>{a.name}</Link>
                </td>
                <td className="px-3 py-2 text-right font-mono text-ink-dim">{a.books.length}</td>
                <td className="px-3 py-2 text-right font-mono" style={{ color: downloaded > 0 ? "var(--good)" : "var(--ink-faint)" }}>{downloaded}</td>
                <td className="px-3 py-2 text-right font-mono" style={{ color: wanted > 0 ? "var(--avoid)" : "var(--ink-faint)" }}>{wanted}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function AddAuthorModal({ onClose, onAdded }: { onClose: () => void; onAdded: (msg: string) => void }) {
  const [q, setQ] = useState("");
  const [authors, setAuthors] = useState<BookAuthor[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<BookAuthor | null>(null);
  const [works, setWorks] = useState<BookDiscoverCard[] | null>(null);
  const [profile, setProfile] = useState("");
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.qualityProfiles("book").then((r) => {
      setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
  }, []);

  const search = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!q.trim()) return;
    setLoading(true); setError(null);
    try { setAuthors(await api.searchBookAuthors(q.trim())); } catch (e) { setError((e as Error).message); } finally { setLoading(false); }
  };

  const pick = (a: BookAuthor) => {
    setSelected(a); setWorks(null); setError(null);
    api.bookAuthorWorks(a.key).then(setWorks).catch(() => setWorks([]));
  };

  const add = async () => {
    if (!selected) return;
    setBusy(true); setError(null);
    try {
      const r = await api.addAuthor({ author_key: selected.key, quality_profile: profile });
      const parts = [`Added ${r.added} book${r.added === 1 ? "" : "s"} by ${selected.name}`];
      if (r.skipped > 0) parts.push(`${r.skipped} already in library`);
      onAdded(parts.join(" · "));
    } catch (e) { setError((e as Error).message); setBusy(false); }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[640px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Add an author</h2>
          <div className="flex items-center gap-2">
            <span className="font-mono text-[10px] uppercase text-ink-faint">Format</span>
            <select value={profile} onChange={(e) => setProfile(e.target.value)} className="rounded-lg px-2 py-1.5 text-[12px]" style={inputStyle}>
              {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
            </select>
          </div>
        </div>

        {error && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}

        {!selected ? (
          <>
            <p className="mb-3 text-[12px] text-ink-dim">Search for an author and add their entire catalogue of official individual books (box sets and collections are excluded).</p>
            <form onSubmit={search} className="mb-3 flex gap-2">
              <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search by author — e.g. Brandon Sanderson" className="flex-1 rounded-lg px-3 py-2 text-[13px]" style={inputStyle} />
              <button type="submit" disabled={loading} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{loading ? "…" : "Search"}</button>
            </form>
            <div className="thin-scroll max-h-[52vh] overflow-y-auto">
              {authors === null ? null : authors.length === 0 ? (
                <div className="p-6 text-center text-[12.5px] text-ink-dim">No authors found.</div>
              ) : authors.map((a) => (
                <button key={a.key} onClick={() => pick(a)} className="flex w-full items-center gap-3 rounded-lg p-2.5 text-left transition-colors hover:bg-[var(--panel-2)]">
                  <span className="grid h-10 w-10 flex-none place-items-center rounded-full text-[14px] font-bold text-white" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))" }}>{a.name.charAt(0).toUpperCase()}</span>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px] font-semibold">{a.name}</div>
                    <div className="truncate text-[11.5px] text-ink-faint">{a.work_count > 0 ? `${a.work_count.toLocaleString()} works` : "Author"}{a.top_work ? ` · ${a.top_work}` : ""}</div>
                  </div>
                  <span className="flex-none text-ink-faint">›</span>
                </button>
              ))}
            </div>
          </>
        ) : (
          <>
            <button onClick={() => { setSelected(null); setWorks(null); }} className="mb-3 inline-flex items-center gap-1 text-[12px] text-ink-dim hover:text-[var(--ink)]">
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none"><path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
              Choose a different author
            </button>
            <div className="mb-3 flex items-center justify-between gap-3">
              <div className="min-w-0">
                <div className="text-[15px] font-bold">{selected.name}</div>
                <div className="text-[11.5px] text-ink-faint">{works === null ? "Loading catalogue…" : `${works.length} official book${works.length === 1 ? "" : "s"}`}</div>
              </div>
              <button onClick={add} disabled={busy || works === null || works.length === 0} className="flex-none rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
                {busy ? "Adding…" : works ? `Add all ${works.length} books` : "Add all"}
              </button>
            </div>
            <div className="thin-scroll max-h-[48vh] overflow-y-auto">
              {works === null ? (
                <div className="grid gap-2.5" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(90px, 1fr))" }}>
                  {Array.from({ length: 12 }).map((_, i) => <div key={i} className="rounded-lg" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />)}
                </div>
              ) : (
                <div className="grid gap-2.5" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(90px, 1fr))" }}>
                  {works.map((b) => (
                    <div key={b.key} className="overflow-hidden rounded-lg" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }} title={`${b.title}${b.year ? ` (${b.year})` : ""}`}>
                      {b.cover_url ? <img src={b.cover_url} alt={b.title} className="h-full w-full object-cover" loading="lazy" /> : <div className="flex h-full items-center justify-center p-1.5 text-center text-[9.5px] font-semibold text-white" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>{b.title}</div>}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

const inputStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
