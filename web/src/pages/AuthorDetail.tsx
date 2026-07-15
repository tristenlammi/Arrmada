import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type Book, type BookAuthor, type BookDiscoverCard } from "../lib/api";

// AuthorDetail unifies an author's shelf: the books you already own (link through to the
// full book detail page for grab / auto-grab / etc.) plus the rest of their official
// catalogue (from Open Library, bundle-filtered) that you can add in a click.
export function AuthorDetail() {
  const { name: raw } = useParams();
  const name = decodeURIComponent(raw ?? "");

  const [library, setLibrary] = useState<Book[] | null>(null);
  const [author, setAuthor] = useState<BookAuthor | null>(null);
  const [works, setWorks] = useState<BookDiscoverCard[] | null>(null);
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [profile, setProfile] = useState("");
  const [addingKey, setAddingKey] = useState<string | null>(null);
  const [addingAll, setAddingAll] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };

  const loadLibrary = useCallback(() => api.books().then((r) => setLibrary(r.books)).catch(() => setLibrary([])), []);

  // Resolve the author's Open Library key from the name, then pull their catalogue.
  const loadCatalogue = useCallback(() => {
    setWorks(null);
    api.searchBookAuthors(name).then((authors) => {
      const exact = authors.find((a) => a.name.toLowerCase() === name.toLowerCase());
      const best = exact ?? authors.slice().sort((a, b) => b.work_count - a.work_count)[0];
      if (!best) { setAuthor(null); setWorks([]); return; }
      setAuthor(best);
      api.bookAuthorWorks(best.key).then(setWorks).catch(() => setWorks([]));
    }).catch(() => { setAuthor(null); setWorks([]); });
  }, [name]);

  useEffect(() => { loadLibrary(); loadCatalogue(); }, [loadLibrary, loadCatalogue]);
  useEffect(() => {
    api.qualityProfiles("book").then((r) => {
      setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
  }, []);

  const owned = useMemo(() => (library ?? []).filter((b) => (b.author || "Unknown author") === name), [library, name]);
  const ownedKeys = useMemo(() => new Set(owned.map((b) => b.ol_key)), [owned]);
  const missing = useMemo(() => (works ?? []).filter((w) => !w.in_library && !ownedKeys.has(w.key)), [works, ownedKeys]);

  const add = async (w: BookDiscoverCard) => {
    setAddingKey(w.key);
    try {
      await api.addBook({ ol_key: w.key, quality_profile: profile, monitored: true, title: w.title, author: name, year: w.year, cover_url: w.cover_url });
      await loadLibrary();
      flash(`Added “${w.title}”`);
    } catch (e) { flash((e as Error).message); } finally { setAddingKey(null); }
  };

  const addAll = async () => {
    if (!author) return;
    setAddingAll(true);
    try {
      const r = await api.addAuthor({ author_key: author.key, quality_profile: profile });
      await loadLibrary();
      flash(`Added ${r.added} book${r.added === 1 ? "" : "s"}${r.skipped ? ` · ${r.skipped} already in library` : ""}`);
    } catch (e) { flash((e as Error).message); } finally { setAddingAll(false); }
  };

  return (
    <>
      <PageHeader title={name} crumb="Library / Books / Author" />
      <div className="mx-auto w-full max-w-[1440px] px-4 py-6 sm:px-6">
        <Link to="/books" className="mb-4 inline-flex items-center gap-1 text-[12px] text-ink-dim hover:text-[var(--ink)]">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
          All books
        </Link>

        <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
          <div>
            <h1 className="m-0 text-[22px] font-bold">{name}</h1>
            <div className="mt-1 text-[12px] text-ink-faint">
              {owned.length} in library{author && author.work_count > 0 ? ` · ${author.work_count.toLocaleString()} works on Open Library` : ""}{author?.birth_date ? ` · b. ${author.birth_date}` : ""}
            </div>
          </div>
          <div className="flex items-center gap-2.5">
            <span className="font-mono text-[10px] uppercase text-ink-faint">Format</span>
            <select value={profile} onChange={(e) => setProfile(e.target.value)} className="rounded-lg px-2.5 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
              {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
            </select>
            {missing.length > 0 && (
              <button onClick={addAll} disabled={addingAll} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
                {addingAll ? "Adding…" : `+ Add all ${missing.length} missing`}
              </button>
            )}
          </div>
        </div>

        {/* In your library */}
        <SectionTitle count={owned.length}>In your library</SectionTitle>
        {owned.length === 0 ? (
          <Empty>No books by {name} in your library yet — add some from their catalogue below.</Empty>
        ) : (
          <div className="mb-8 grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
            {owned.map((b) => <OwnedCard key={b.id} b={b} />)}
          </div>
        )}

        {/* Rest of the catalogue */}
        <SectionTitle count={works === null ? undefined : missing.length}>More by {name}</SectionTitle>
        {works === null ? (
          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
            {Array.from({ length: 8 }).map((_, i) => <div key={i} className="rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />)}
          </div>
        ) : missing.length === 0 ? (
          <Empty>{owned.length > 0 ? "You have this author's whole catalogue. 🎉" : "Couldn't find more books for this author on Open Library."}</Empty>
        ) : (
          <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
            {missing.map((w) => <CatalogueCard key={w.key} w={w} onAdd={() => add(w)} busy={addingKey === w.key} disabled={addingKey !== null} />)}
          </div>
        )}
      </div>

      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </>
  );
}

function statusOf(b: Book): { label: string; tone: string } {
  if (b.has_file) {
    const tags = [b.ebook && "E", b.audiobook && "A"].filter(Boolean).join("+");
    return { label: tags ? `${tags} ✓` : "Downloaded", tone: "var(--good)" };
  }
  if (b.monitored) return { label: "Wanted", tone: "var(--avoid)" };
  return { label: "Unmonitored", tone: "var(--ink-faint)" };
}

function OwnedCard({ b }: { b: Book }) {
  const st = statusOf(b);
  return (
    <Link to={`/books/${b.id}`} className="group relative block overflow-hidden rounded-xl" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      {b.cover_url ? (
        <img src={b.cover_url} alt={b.title} className="h-full w-full object-cover" loading="lazy" />
      ) : (
        <div className="flex h-full w-full items-center justify-center p-2 text-center text-[12px] font-bold text-white" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>{b.title}</div>
      )}
      <span className="absolute left-1.5 top-1.5 rounded-full px-1.5 py-0.5 font-mono text-[8.5px] font-bold uppercase" style={{ background: "rgba(20,12,7,.72)", color: st.tone }}>{st.label}</span>
      <div className="absolute inset-x-0 bottom-0 p-2 opacity-0 transition-opacity group-hover:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.9), transparent)" }}>
        <div className="truncate text-[11.5px] font-semibold text-white">{b.title}</div>
        {b.year > 0 && <div className="text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>{b.year}</div>}
      </div>
    </Link>
  );
}

function CatalogueCard({ w, onAdd, busy, disabled }: { w: BookDiscoverCard; onAdd: () => void; busy: boolean; disabled: boolean }) {
  return (
    <div className="group relative overflow-hidden rounded-xl" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      {w.cover_url ? (
        <img src={w.cover_url} alt={w.title} className="h-full w-full object-cover" loading="lazy" />
      ) : (
        <div className="flex h-full w-full items-center justify-center p-2 text-center text-[12px] font-bold text-white" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>{w.title}</div>
      )}
      <div className="absolute inset-0 flex flex-col justify-end p-2" style={{ background: "linear-gradient(to top, rgba(0,0,0,.92), transparent 55%)" }}>
        <div className="truncate text-[11.5px] font-semibold text-white" title={w.title}>{w.title}</div>
        {w.year > 0 && <div className="mb-1.5 text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>{w.year}</div>}
        <button onClick={onAdd} disabled={disabled} className="rounded-lg py-1.5 text-[11.5px] font-semibold disabled:opacity-60" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
          {busy ? "Adding…" : "+ Add"}
        </button>
      </div>
    </div>
  );
}

function SectionTitle({ children, count }: { children: React.ReactNode; count?: number }) {
  return <h2 className="m-0 mb-3 text-[15px] font-bold">{children}{count !== undefined && <span className="ml-1.5 font-normal text-ink-faint">· {count}</span>}</h2>;
}

function Empty({ children }: { children: React.ReactNode }) {
  return <div className="mb-8 rounded-xl p-8 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{children}</div>;
}
