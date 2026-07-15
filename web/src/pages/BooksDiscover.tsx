import { useCallback, useEffect, useRef, useState } from "react";
import { api, type BookAuthor, type BookDiscoverCard, type BookMeta } from "../lib/api";

// BooksDiscover is the Books area of Discover — deliberately separate from the movie/TV
// experience: its own search (titles + authors), Open Library browse rows, author
// catalogue pages, and a book request modal. Books run on Open Library and go through the
// same request → approve pipeline as movies/series (auto-approved for auto-approve users).
const SUBJECTS = ["Fantasy", "Science Fiction", "Mystery", "Thriller", "Romance", "History"];

export function BooksDiscover({ flash }: { flash: (m: string) => void }) {
  const [input, setInput] = useState("");
  const [query, setQuery] = useState("");
  const [author, setAuthor] = useState<BookAuthor | null>(null);
  // ol_keys the viewer has just requested this session (optimistic badge).
  const [requested, setRequested] = useState<Set<string>>(new Set());

  useEffect(() => {
    const q = input.trim();
    if (!q) { setQuery(""); return; }
    const t = setTimeout(() => setQuery(q), 350);
    return () => clearTimeout(t);
  }, [input]);

  const request = useCallback(async (b: BookDiscoverCard | BookMeta, authorName?: string) => {
    try {
      const res = await api.createRequest({
        media_type: "book", ol_key: b.key, title: b.title,
        author: ("author" in b && b.author) ? b.author : (authorName || ""),
        year: b.year || 0, poster_url: b.cover_url,
        overview: "description" in b ? b.description : undefined,
      });
      setRequested((s) => new Set(s).add(b.key));
      flash(res.status === "approved" ? `Added “${b.title}” to your library` : `Requested “${b.title}”`);
    } catch (e) {
      const m = (e as Error).message;
      if (/already/i.test(m)) { setRequested((s) => new Set(s).add(b.key)); flash("Already requested."); }
      else flash(m);
    }
  }, [flash]);

  const ctx: BookCtx = { request, isRequested: (k) => requested.has(k) };

  return (
    <div>
      {/* Books' own search bar — separate from the movie/TV one. */}
      <div className="mb-5 flex items-center gap-3">
        <div className="relative flex-1 sm:max-w-[420px]">
          <svg className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2" width="15" height="15" viewBox="0 0 24 24" fill="none" style={{ color: "var(--ink-faint)" }}>
            <circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" /><path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
          </svg>
          <input
            value={input}
            onChange={(e) => { setInput(e.target.value); setAuthor(null); }}
            placeholder="Search books & authors…"
            className="w-full rounded-lg py-2 pl-9 pr-8 text-[13px]"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
          />
          {input && <button onClick={() => setInput("")} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-ink-faint hover:text-[var(--ink)]" style={{ fontSize: "13px" }}>✕</button>}
        </div>
      </div>

      {author ? (
        <AuthorView author={author} ctx={ctx} onBack={() => setAuthor(null)} />
      ) : query ? (
        <SearchView query={query} ctx={ctx} onPickAuthor={setAuthor} />
      ) : (
        <BrowseView ctx={ctx} />
      )}
    </div>
  );
}

interface BookCtx {
  request: (b: BookDiscoverCard | BookMeta, authorName?: string) => Promise<void>;
  isRequested: (key: string) => boolean;
}

function BrowseView({ ctx }: { ctx: BookCtx }) {
  return (
    <div className="flex flex-col gap-7">
      <BookRow title="Trending this week" load={() => api.bookDiscoverTrending()} ctx={ctx} />
      {SUBJECTS.map((s) => (
        <BookRow key={s} title={s} load={() => api.bookDiscoverSubject(s)} ctx={ctx} />
      ))}
    </div>
  );
}

function SearchView({ query, ctx, onPickAuthor }: { query: string; ctx: BookCtx; onPickAuthor: (a: BookAuthor) => void }) {
  const [authors, setAuthors] = useState<BookAuthor[] | null>(null);
  const [books, setBooks] = useState<BookDiscoverCard[] | null>(null);
  useEffect(() => {
    let alive = true;
    setAuthors(null); setBooks(null);
    api.bookDiscoverSearch(query).then((r) => { if (alive) { setAuthors(r.authors); setBooks(r.books); } }).catch(() => { if (alive) { setAuthors([]); setBooks([]); } });
    return () => { alive = false; };
  }, [query]);

  return (
    <div className="flex flex-col gap-7">
      {authors && authors.length > 0 && (
        <div>
          <h2 className="m-0 mb-2.5 text-[15px] font-bold">Authors</h2>
          <div className="thin-scroll flex gap-2.5 overflow-x-auto pb-2">
            {authors.map((a) => <AuthorCard key={a.key} author={a} onClick={() => onPickAuthor(a)} />)}
          </div>
        </div>
      )}
      <div>
        <h2 className="m-0 mb-3 text-[15px] font-bold">Books {books && <span className="font-normal text-ink-faint">· {books.length}</span>}</h2>
        <BookGrid books={books} ctx={ctx} emptyLabel={`No books match “${query}”.`} />
      </div>
    </div>
  );
}

function AuthorView({ author, ctx, onBack }: { author: BookAuthor; ctx: BookCtx; onBack: () => void }) {
  const [books, setBooks] = useState<BookDiscoverCard[] | null>(null);
  useEffect(() => {
    let alive = true;
    setBooks(null);
    api.bookAuthorWorks(author.key).then((r) => alive && setBooks(r)).catch(() => alive && setBooks([]));
    return () => { alive = false; };
  }, [author.key]);

  return (
    <div>
      <button onClick={onBack} className="mb-4 inline-flex items-center gap-1 text-[12px] text-ink-dim hover:text-[var(--ink)]">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
        Back to results
      </button>
      <div className="mb-4">
        <h2 className="m-0 text-[19px] font-bold">{author.name}</h2>
        <div className="mt-0.5 text-[12px] text-ink-faint">
          {author.work_count > 0 ? `${author.work_count.toLocaleString()} works` : "Author"}{author.birth_date ? ` · b. ${author.birth_date}` : ""}
        </div>
      </div>
      <BookGrid books={books} ctx={ctx} emptyLabel="No works found for this author." authorName={author.name} />
    </div>
  );
}

function BookRow({ title, load, ctx }: { title: string; load: () => Promise<BookDiscoverCard[]>; ctx: BookCtx }) {
  const [items, setItems] = useState<BookDiscoverCard[] | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let alive = true;
    load().then((r) => alive && setItems(r)).catch(() => alive && setItems([]));
    return () => { alive = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });

  if (items && items.length === 0) return null;
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between">
        <h2 className="m-0 text-[15px] font-bold">{title}</h2>
        {items && items.length > 0 && (
          <div className="flex gap-1">
            <ArrowBtn dir={-1} onClick={() => scroll(-1)} />
            <ArrowBtn dir={1} onClick={() => scroll(1)} />
          </div>
        )}
      </div>
      <div ref={scroller} className="thin-scroll flex gap-3 overflow-x-auto pb-2" style={{ scrollSnapType: "x proximity" }}>
        {items === null
          ? Array.from({ length: 8 }).map((_, i) => <div key={i} className="w-[140px] flex-none rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />)
          : items.map((b) => <BookCard key={b.key} b={b} ctx={ctx} />)}
      </div>
    </div>
  );
}

function BookGrid({ books, ctx, emptyLabel, authorName }: { books: BookDiscoverCard[] | null; ctx: BookCtx; emptyLabel: string; authorName?: string }) {
  if (books === null) {
    return (
      <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(130px, 1fr))" }}>
        {Array.from({ length: 12 }).map((_, i) => <div key={i} className="rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />)}
      </div>
    );
  }
  if (books.length === 0) {
    return <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{emptyLabel}</div>;
  }
  return (
    <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(130px, 1fr))" }}>
      {books.map((b) => <BookCard key={b.key} b={b} ctx={ctx} authorName={authorName} full />)}
    </div>
  );
}

function badgeFor(b: BookDiscoverCard, requested: boolean): { label: string; tone: string; bg: string } | null {
  if (b.has_file) return { label: "Available", tone: "var(--good)", bg: "var(--good-soft, rgba(90,140,90,.18))" };
  if (requested || b.requested) return { label: "Requested", tone: "var(--avoid)", bg: "var(--avoid-soft)" };
  if (b.in_library) return { label: "Processing", tone: "var(--accent)", bg: "var(--accent-soft)" };
  return null;
}

function BookCard({ b, ctx, authorName, full }: { b: BookDiscoverCard; ctx: BookCtx; authorName?: string; full?: boolean }) {
  const [open, setOpen] = useState(false);
  const requested = ctx.isRequested(b.key);
  const badge = badgeFor(b, requested);
  return (
    <>
      <button
        onClick={() => setOpen(true)}
        className={`group relative overflow-hidden rounded-xl text-left ${full ? "w-full" : "w-[140px] flex-none"}`}
        style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)", scrollSnapAlign: "start" }}
      >
        {b.cover_url ? (
          <img src={b.cover_url} alt={b.title} className="h-full w-full object-cover" loading="lazy" onError={(e) => { e.currentTarget.style.visibility = "hidden"; }} />
        ) : (
          <div className="flex h-full w-full items-center justify-center p-2 text-center" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}><span className="text-[11.5px] font-bold text-white">{b.title}</span></div>
        )}
        {badge && <span className="absolute right-1.5 top-1.5 rounded-full px-1.5 py-0.5 font-mono text-[8px] font-bold uppercase" style={{ background: badge.bg, color: badge.tone }}>{badge.label}</span>}
        <div className="absolute inset-x-0 bottom-0 flex flex-col gap-0.5 p-2 opacity-0 transition-opacity group-hover:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.9), transparent)" }}>
          <div className="truncate text-[11.5px] font-semibold text-white">{b.title}</div>
          <div className="truncate text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>{b.author || authorName || ""}{b.year ? ` · ${b.year}` : ""}</div>
        </div>
      </button>
      {open && <BookRequestModal b={b} ctx={ctx} authorName={authorName} onClose={() => setOpen(false)} />}
    </>
  );
}

function AuthorCard({ author, onClick }: { author: BookAuthor; onClick: () => void }) {
  return (
    <button onClick={onClick} className="flex w-[210px] flex-none items-center gap-3 rounded-xl p-3 text-left transition-colors hover:bg-[var(--panel-2)]" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <span className="grid h-11 w-11 flex-none place-items-center rounded-full text-[15px] font-bold text-white" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))" }}>
        {author.name.charAt(0).toUpperCase()}
      </span>
      <div className="min-w-0">
        <div className="truncate text-[13px] font-semibold">{author.name}</div>
        <div className="truncate text-[11px] text-ink-faint">{author.work_count > 0 ? `${author.work_count.toLocaleString()} works` : "Author"}{author.top_work ? ` · ${author.top_work}` : ""}</div>
      </div>
    </button>
  );
}

function BookRequestModal({ b, ctx, authorName, onClose }: { b: BookDiscoverCard; ctx: BookCtx; authorName?: string; onClose: () => void }) {
  const [detail, setDetail] = useState<BookMeta | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(ctx.isRequested(b.key) || b.requested);
  const badge = badgeFor(b, done);

  useEffect(() => {
    let alive = true;
    api.bookDiscoverDetail(b.key).then((d) => alive && setDetail(d)).catch(() => {});
    return () => { alive = false; };
  }, [b.key]);

  const doRequest = async () => {
    setBusy(true);
    try { await ctx.request(detail ?? b, authorName); setDone(true); } finally { setBusy(false); }
  };

  const author = detail?.author || b.author || authorName || "Unknown author";
  const description = detail?.description || "";
  const subjects = detail?.subjects || [];

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-4 sm:p-6" style={{ background: "rgba(0,0,0,.65)" }} onClick={onClose}>
      <div className="mt-8 w-full max-w-[620px] overflow-hidden rounded-2xl" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="relative">
          {b.cover_url && <div className="pointer-events-none absolute inset-0 bg-cover bg-center opacity-[0.08] blur-2xl" style={{ backgroundImage: `url(${b.cover_url})` }} />}
          <div className="pointer-events-none absolute inset-0" style={{ background: "linear-gradient(180deg, transparent, var(--panel))" }} />
          <button onClick={onClose} className="absolute right-2.5 top-2.5 z-10 grid h-7 w-7 place-items-center rounded-full" style={{ background: "rgba(20,12,7,.7)", color: "#fff" }}>✕</button>
          <div className="relative flex gap-4 p-5">
            <div className="h-[168px] w-[112px] flex-none overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
              {b.cover_url ? <img src={b.cover_url} alt={b.title} className="h-full w-full object-cover" /> : <div className="flex h-full items-center justify-center p-2 text-center text-[11px] font-bold text-white" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}>{b.title}</div>}
            </div>
            <div className="min-w-0 flex-1">
              <h2 className="m-0 text-[17px] font-bold leading-tight">{b.title}</h2>
              <div className="mt-1 text-[13px] font-semibold text-ink-dim">{author}</div>
              {b.year > 0 && <div className="mt-0.5 font-mono text-[11px] text-ink-faint">{b.year}</div>}
              <div className="mt-3">
                {badge ? (
                  <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: badge.bg, color: badge.tone }}>{badge.label === "Available" ? "✓ In your library" : badge.label === "Requested" ? "Requested" : "Processing"}</span>
                ) : (
                  <button onClick={doRequest} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
                    {busy ? "Requesting…" : "＋ Request"}
                  </button>
                )}
              </div>
            </div>
          </div>
        </div>
        <div className="px-5 pb-5">
          {description && <p className="m-0 max-h-[220px] overflow-y-auto text-[13px] leading-relaxed text-ink-dim">{description}</p>}
          {subjects.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-1.5">
              {subjects.slice(0, 8).map((s) => <span key={s} className="rounded-full px-2 py-0.5 text-[11px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{s}</span>)}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function ArrowBtn({ dir, onClick }: { dir: -1 | 1; onClick: () => void }) {
  return (
    <button onClick={onClick} className="grid h-7 w-7 place-items-center rounded-full" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink-dim)" }}>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" style={{ transform: dir === -1 ? "rotate(180deg)" : "none" }}><path d="M9 6l6 6-6 6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
    </button>
  );
}
