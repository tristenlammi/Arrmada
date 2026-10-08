import { useCallback, useEffect, useRef, useState } from "react";
import { api, type BookSource, type BookAuthor, type BookDiscoverCard, type BookMeta, type BookRecommendedRow } from "../lib/api";
import { useCanHover } from "../lib/useCanHover";

// BooksDiscover is the Books area of Discover — deliberately separate from the movie/TV
// experience: its own search (titles + authors), Open Library browse rows, author
// catalogue pages, and a book request modal. Books run on Open Library and go through the
// same request → approve pipeline as movies/series (auto-approved for auto-approve users).
// Genres for the explorer. Hardcover tags books with these names; on Open Library they
// go through its subject search, which knows most of them too.
const GENRES = ["Fantasy", "Science Fiction", "Mystery", "Thriller", "Romance", "Horror", "Historical Fiction", "Literary Fiction", "Young Adult", "Nonfiction", "Biography", "History", "Science", "Self Help", "Business", "Classics", "Humor", "Graphic Novels", "Poetry", "Children's"];

// fmtCount renders 12345 as "12.3k".
function fmtCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}m`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1).replace(/\.0$/, "")}k`;
  return String(n);
}

// Shared with the movie/TV tab's design system. Duplicated (not imported) because
// Discover.tsx imports this file — importing back would be a circular import.
const BADGE_BG = "rgba(14,10,7,.92)";

function LoadError({ message }: { message: string }) {
  return (
    <div className="rounded-lg px-3 py-2 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-faint)" }}>
      Couldn’t load — {message}
    </div>
  );
}

// Cover + caption skeleton, matched to a real card so loading → loaded has no shift.
function BookCardSkeleton({ full }: { full?: boolean }) {
  return (
    <div className={full ? "w-full" : "w-[150px] flex-none"}>
      <div className="rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />
      <div className="mt-2 h-3 w-4/5 rounded" style={{ background: "var(--panel-2)" }} />
      <div className="mt-1.5 h-2.5 w-2/5 rounded" style={{ background: "var(--panel-2)" }} />
    </div>
  );
}

export function BooksDiscover({ flash, canRequest, initialQuery }: { flash: (m: string) => void; canRequest: boolean; initialQuery?: string }) {
  const [input, setInput] = useState(initialQuery ?? "");
  const [query, setQuery] = useState("");
  const [author, setAuthor] = useState<BookAuthor | null>(null);
  const [focused, setFocused] = useState(false);
  // ol_keys the viewer has just requested this session (optimistic badge).
  const [requested, setRequested] = useState<Set<string>>(new Set());

  // A seeded search (e.g. from a notification click) lands here after mount too.
  useEffect(() => {
    if (initialQuery) { setInput(initialQuery); setAuthor(null); }
  }, [initialQuery]);

  useEffect(() => {
    const q = input.trim();
    if (!q) { setQuery(""); return; }
    const t = setTimeout(() => setQuery(q), 350);
    return () => clearTimeout(t);
  }, [input]);

  // Rethrows on failure so the modal only flips to its success state on real success.
  const request = useCallback(async (b: BookDiscoverCard | BookMeta, authorName?: string): Promise<{ subscribed: boolean }> => {
    try {
      const res = await api.createRequest({
        media_type: "book", ol_key: b.key, title: b.title,
        author: ("author" in b && b.author) ? b.author : (authorName || ""),
        year: b.year || 0, poster_url: b.cover_url,
        overview: "description" in b ? b.description : undefined,
      });
      setRequested((s) => new Set(s).add(b.key));
      flash(res.subscribed ? "You’re on the list — we’ll notify you when it’s ready"
        : res.request.status === "approved" ? `Added “${b.title}” to your library` : `Requested “${b.title}”`);
      return { subscribed: res.subscribed };
    } catch (e) {
      flash((e as Error).message);
      throw e;
    }
  }, [flash]);

  const ctx: BookCtx = { request, isRequested: (k) => requested.has(k), canRequest };

  return (
    <div>
      {/* Books' own search bar — separate from the movie/TV one. */}
      <div className="mb-5 flex items-center gap-3">
        <div className="relative w-full sm:w-[320px]">
          <svg className="pointer-events-none absolute left-2.5 top-1/2 z-10 -translate-y-1/2" width="14" height="14" viewBox="0 0 24 24" fill="none" style={{ color: "var(--ink-faint)" }}>
            <circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" /><path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
          </svg>
          <input
            value={input}
            onChange={(e) => { setInput(e.target.value); setAuthor(null); }}
            onFocus={() => setFocused(true)}
            onBlur={() => setFocused(false)}
            placeholder="Search books & authors…"
            aria-label="Search books and authors"
            className="w-full rounded-lg py-1.5 pl-8 pr-7 text-[12.5px]"
            style={{ background: "var(--panel-2)", border: `1px solid ${focused ? "var(--accent-line)" : "var(--line)"}`, color: "var(--ink)" }}
          />
          {input && (
            <button onClick={() => setInput("")} aria-label="Clear search" className="absolute right-2 top-1/2 z-10 -translate-y-1/2 text-ink-faint hover:text-[var(--ink)]" style={{ fontSize: "13px" }}>✕</button>
          )}
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
  request: (b: BookDiscoverCard | BookMeta, authorName?: string) => Promise<{ subscribed: boolean }>;
  isRequested: (key: string) => boolean;
  canRequest: boolean;
}

function BrowseView({ ctx }: { ctx: BookCtx }) {
  return (
    <div className="flex flex-col gap-7">
      <BookHero ctx={ctx} />
      <RecommendedRows ctx={ctx} />
      <BookRow title="Trending this week" load={() => api.bookDiscoverBrowse("trending")} ctx={ctx} />
      <BookRow title="New releases" load={() => api.bookDiscoverBrowse("new_releases")} ctx={ctx} hideOnError />
      <BookRow title="Top rated" load={() => api.bookDiscoverBrowse("top_rated")} ctx={ctx} hideOnError />
      <BookRow title="Popular all time" load={() => api.bookDiscoverBrowse("popular")} ctx={ctx} hideOnError />
      <GenreExplorer ctx={ctx} />
    </div>
  );
}

// BookHero is the page's opener: the trending books, one at a time, cover art and all.
// Books have no backdrops, so the cover itself — blurred — is the backdrop, with the real
// cover standing in front of it. Details (description, genres) load per slide and are
// cached server-side, so the strip costs a handful of requests a day.
function BookHero({ ctx }: { ctx: BookCtx }) {
  const [items, setItems] = useState<BookDiscoverCard[] | null>(null);
  const [idx, setIdx] = useState(0);
  const [paused, setPaused] = useState(false);
  const [details, setDetails] = useState<Record<string, BookMeta>>({});
  const [open, setOpen] = useState(false);
  const [quickBusy, setQuickBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    api.bookDiscoverBrowse("trending")
      .then((r) => { if (alive) setItems(r.filter((b) => !!b.cover_url).slice(0, 6)); })
      .catch(() => { if (alive) setItems([]); });
    return () => { alive = false; };
  }, []);
  const count = items?.length ?? 0;
  useEffect(() => {
    if (paused || count <= 1 || open) return;
    const t = setInterval(() => setIdx((i) => (i + 1) % count), 8000);
    return () => clearInterval(t);
  }, [paused, count, open]);
  useEffect(() => { if (count > 0 && idx >= count) setIdx(0); }, [idx, count]);
  const cur = items && count > 0 ? items[Math.min(idx, count - 1)] : null;
  useEffect(() => {
    if (!cur || details[cur.key]) return;
    let alive = true;
    api.bookDiscoverDetail(cur.key).then((d) => { if (alive) setDetails((m) => ({ ...m, [cur.key]: d })); }).catch(() => {});
    return () => { alive = false; };
  }, [cur?.key]); // eslint-disable-line react-hooks/exhaustive-deps

  if (items === null) return <div className="w-full animate-pulse rounded-2xl" style={{ height: "clamp(300px, 40vh, 460px)", background: "var(--panel-2)", border: "1px solid var(--line)" }} />;
  if (!cur) return null;
  const d = details[cur.key];
  const requested = ctx.isRequested(cur.key);
  const badge = badgeFor(cur, requested);
  const genres = (cur.genres && cur.genres.length > 0 ? cur.genres : d?.subjects ?? []).slice(0, 3);
  const quick = async () => {
    if (quickBusy) return;
    setQuickBusy(true);
    try { await ctx.request(d ?? cur); } catch { /* toast shown */ } finally { setQuickBusy(false); }
  };

  return (
    <div className="relative overflow-hidden rounded-2xl" style={{ height: "clamp(300px, 40vh, 460px)", border: "1px solid var(--line)", background: "var(--panel-2)" }}
      onMouseEnter={() => setPaused(true)} onMouseLeave={() => setPaused(false)}>
      {items.map((it, i) => (
        <img key={it.key} src={it.cover_url} alt="" aria-hidden className="absolute inset-0 h-full w-full object-cover blur-2xl transition-opacity duration-700" style={{ opacity: i === idx ? 0.55 : 0, transform: "scale(1.15)" }} loading={i === 0 ? "eager" : "lazy"} decoding="async" />
      ))}
      <div className="absolute inset-0" style={{ background: "linear-gradient(to right, rgba(0,0,0,.86) 0%, rgba(0,0,0,.6) 45%, rgba(0,0,0,.25) 100%)" }} />
      <div className="absolute inset-x-0 bottom-0 h-1/2" style={{ background: "linear-gradient(to top, var(--bg) 2%, transparent)" }} />

      <div className="relative z-10 flex h-full items-end gap-5 p-5 sm:items-center sm:gap-8 sm:p-8">
        <button onClick={() => setOpen(true)} aria-label={`View details for ${cur.title}`} className="hidden flex-none overflow-hidden rounded-xl transition-transform hover:scale-[1.02] sm:block" style={{ width: 168, aspectRatio: "2/3", border: "1px solid rgba(255,255,255,.2)", boxShadow: "0 18px 40px rgba(0,0,0,.55)" }}>
          <img src={cur.cover_url} alt={cur.title} className="h-full w-full object-cover" decoding="async" />
        </button>
        <div className="flex min-w-0 max-w-[640px] flex-col gap-2.5">
          {badge && <span className="w-fit rounded-full px-2 py-0.5 text-[9.5px] font-bold uppercase tracking-wide" style={{ background: BADGE_BG, color: badge.tone, border: `1px solid ${badge.tone}` }}>{badge.label}</span>}
          <h2 className="m-0 line-clamp-2 text-[24px] font-extrabold leading-[1.08] sm:text-[34px]" style={{ color: "#fff", textShadow: "0 2px 18px rgba(0,0,0,.55)" }}>{cur.title}</h2>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[12.5px] font-medium" style={{ color: "rgba(255,255,255,.85)" }}>
            {cur.author && <span>{cur.author}</span>}
            {cur.year ? <span>{cur.year}</span> : null}
            {!!cur.rating && <span style={{ color: "var(--accent)" }}>★ {cur.rating.toFixed(1)}{cur.ratings ? <span style={{ color: "rgba(255,255,255,.6)" }}> · {fmtCount(cur.ratings)} ratings</span> : null}</span>}
            {d?.series_name && <span className="rounded px-1.5 py-px text-[10px] uppercase" style={{ background: "rgba(255,255,255,.14)" }}>{d.series_name}{d.series_position ? ` #${d.series_position}` : ""}</span>}
          </div>
          {genres.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {genres.map((g) => <span key={g} className="rounded-full px-2 py-0.5 text-[10.5px] font-semibold" style={{ background: "rgba(255,255,255,.14)", color: "rgba(255,255,255,.9)" }}>{g}</span>)}
            </div>
          )}
          {d?.description && <p className="m-0 line-clamp-2 text-[13px] leading-relaxed sm:line-clamp-3" style={{ color: "rgba(255,255,255,.78)" }}>{d.description}</p>}
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <button onClick={() => setOpen(true)} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold backdrop-blur-sm" style={{ background: "rgba(255,255,255,.16)", border: "1px solid rgba(255,255,255,.28)", color: "#fff" }}>View details</button>
            {ctx.canRequest && !badge && (
              <button onClick={quick} disabled={quickBusy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: quickBusy ? 0.65 : 1 }}>
                {quickBusy ? "Requesting…" : "＋ Request"}
              </button>
            )}
          </div>
        </div>
      </div>
      {count > 1 && (
        <div className="absolute bottom-4 right-5 z-10 flex gap-1.5">
          {items.map((it, i) => (
            <button key={it.key} onClick={() => setIdx(i)} aria-label={`Show slide ${i + 1}`} className="h-1.5 rounded-full transition-all" style={{ width: i === idx ? 20 : 6, background: i === idx ? "#fff" : "rgba(255,255,255,.45)" }} />
          ))}
        </div>
      )}
      {open && <BookRequestModal b={cur} ctx={ctx} onClose={() => setOpen(false)} />}
    </div>
  );
}

// RecommendedRows are the "Because you own …" strips: the catalogue's similar-books
// lists seeded from the library, rotating daily. Nothing shows until there's a library
// on Hardcover to seed from.
function RecommendedRows({ ctx }: { ctx: BookCtx }) {
  const [rows, setRows] = useState<BookRecommendedRow[] | null>(null);
  useEffect(() => {
    let alive = true;
    api.bookDiscoverRecommended().then((r) => { if (alive) setRows(r); }).catch(() => { if (alive) setRows([]); });
    return () => { alive = false; };
  }, []);
  if (!rows || rows.length === 0) return null;
  return (
    <>
      {rows.map((r) => <BookRow key={r.seed_id} title={r.title} load={() => Promise.resolve(r.books)} ctx={ctx} />)}
    </>
  );
}

// GenreExplorer: pick a genre, see a grid. One request per pick (cached six hours),
// where six always-on genre rows cost six on every visit.
function GenreExplorer({ ctx }: { ctx: BookCtx }) {
  const [active, setActive] = useState<string | null>(null);
  const [items, setItems] = useState<BookDiscoverCard[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const seq = useRef(0);
  const pick = (g: string) => {
    const my = ++seq.current;
    setActive(g); setItems(null); setError(null);
    api.bookDiscoverSubject(g)
      .then((r) => { if (seq.current === my) setItems(r); })
      .catch((e) => { if (seq.current === my) { setItems([]); setError((e as Error).message); } });
  };
  return (
    <div>
      <h2 className="m-0 mb-2.5 text-[15px] font-bold">Browse by genre</h2>
      <div className="mb-4 flex flex-wrap gap-2">
        {GENRES.map((g) => {
          const on = active === g;
          return (
            <button key={g} onClick={() => pick(g)} className="rounded-full px-3 py-1 text-[12px] font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>
              {g}
            </button>
          );
        })}
      </div>
      {active && <BookGrid books={items} ctx={ctx} error={error} emptyLabel={`Nothing found for ${active}.`} />}
    </div>
  );
}

function SearchView({ query, ctx, onPickAuthor }: { query: string; ctx: BookCtx; onPickAuthor: (a: BookAuthor) => void }) {
  const [authors, setAuthors] = useState<BookAuthor[] | null>(null);
  const [books, setBooks] = useState<BookDiscoverCard[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Which catalogue answered, and the optional second opinion from Open Library when
  // Hardcover is primary (it hasn't got everything, and you may know the title is there).
  const [source, setSource] = useState<BookSource>("openlibrary");
  const [olBooks, setOlBooks] = useState<BookDiscoverCard[] | null | "loading">(null);
  useEffect(() => {
    let alive = true;
    setAuthors(null); setBooks(null); setError(null); setOlBooks(null);
    api.bookDiscoverSearch(query)
      .then((r) => { if (alive) { setAuthors(r.authors); setBooks(r.books); setSource(r.source); } })
      .catch((e) => { if (alive) { setAuthors([]); setBooks([]); setError((e as Error).message); } });
    return () => { alive = false; };
  }, [query]);
  const showOpenLibrary = async () => {
    setOlBooks("loading");
    try {
      const r = await api.bookDiscoverSearch(query, "openlibrary");
      const seen = new Set((books ?? []).map((b) => b.key));
      setOlBooks(r.books.filter((b) => !seen.has(b.key)));
    } catch { setOlBooks([]); }
  };

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
        <div className="mb-3 flex items-center gap-3">
          <h2 className="m-0 text-[15px] font-bold">Books {books && !error && <span className="font-normal text-ink-faint">· {books.length}</span>}</h2>
          <div className="flex-1" />
          {source === "hardcover" && books && olBooks === null && (
            <button type="button" onClick={showOpenLibrary} className="text-[11.5px] font-semibold underline-offset-2 hover:underline" style={{ color: "var(--ink-dim)" }}>
              Show Open Library results too
            </button>
          )}
          {source === "hardcover" && books && <span className="font-mono text-[10px] uppercase tracking-wide text-ink-faint">via Hardcover</span>}
        </div>
        {/* A failed search is an error, not "no books match". */}
        <BookGrid books={books} ctx={ctx} error={error} emptyLabel={`No books match “${query}”.`} />
      </div>
      {olBooks !== null && (
        <div>
          <h2 className="m-0 mb-3 text-[15px] font-bold">Open Library {Array.isArray(olBooks) && <span className="font-normal text-ink-faint">· {olBooks.length}</span>}</h2>
          <BookGrid books={olBooks === "loading" ? null : olBooks} ctx={ctx} error={null} emptyLabel="Open Library has nothing more for this search." />
        </div>
      )}
    </div>
  );
}

function AuthorView({ author, ctx, onBack }: { author: BookAuthor; ctx: BookCtx; onBack: () => void }) {
  const [books, setBooks] = useState<BookDiscoverCard[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setBooks(null); setError(null);
    api.bookAuthorWorks(author.key)
      .then((r) => { if (alive) { setBooks(r); setError(null); } })
      .catch((e) => { if (alive) { setBooks([]); setError((e as Error).message); } });
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
      <BookGrid books={books} ctx={ctx} error={error} emptyLabel="No works found for this author." authorName={author.name} />
    </div>
  );
}

function BookRow({ title, load, ctx, hideOnError }: { title: string; load: () => Promise<BookDiscoverCard[]>; ctx: BookCtx; hideOnError?: boolean }) {
  const [items, setItems] = useState<BookDiscoverCard[] | null>(null);
  // A failed row stays visible with an inline error — silently vanishing (or
  // skeleton-ing forever) hides real Open Library outages from the viewer.
  const [error, setError] = useState<string | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let alive = true;
    load()
      .then((r) => { if (alive) { setItems(r); setError(null); } })
      .catch((e) => { if (alive) { setItems([]); setError((e as Error).message); } });
    return () => { alive = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });

  // Genuinely empty (but successful) rows still collapse; errored ones do not —
  // unless the row is one the catalogue may simply not offer (hideOnError).
  if (items && items.length === 0 && !error) return null;
  if (error && hideOnError) return null;
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
      {error ? (
        <LoadError message={error} />
      ) : (
        <div ref={scroller} className="thin-scroll flex gap-3 overflow-x-auto pb-2" style={{ scrollSnapType: "x proximity" }}>
          {items === null
            ? Array.from({ length: 8 }).map((_, i) => <BookCardSkeleton key={i} />)
            : items.map((b) => <BookCard key={b.key} b={b} ctx={ctx} />)}
        </div>
      )}
    </div>
  );
}

const GRID_COLS = "repeat(auto-fill, minmax(140px, 1fr))";

function BookGrid({ books, ctx, emptyLabel, authorName, error }: { books: BookDiscoverCard[] | null; ctx: BookCtx; emptyLabel: string; authorName?: string; error?: string | null }) {
  // An error is not "no results" — say so instead of showing an empty state.
  if (error) return <LoadError message={error} />;
  if (books === null) {
    return (
      <div className="grid gap-x-3 gap-y-5" style={{ gridTemplateColumns: GRID_COLS }}>
        {Array.from({ length: 12 }).map((_, i) => <BookCardSkeleton key={i} full />)}
      </div>
    );
  }
  if (books.length === 0) {
    return <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{emptyLabel}</div>;
  }
  return (
    <div className="grid gap-x-3 gap-y-5" style={{ gridTemplateColumns: GRID_COLS }}>
      {books.map((b) => <BookCard key={b.key} b={b} ctx={ctx} authorName={authorName} full />)}
    </div>
  );
}

function badgeFor(b: BookDiscoverCard, requested: boolean): { label: string; tone: string; bg: string } | null {
  if (b.has_file) return { label: "In library", tone: "var(--good)", bg: "var(--good-soft, rgba(90,140,90,.18))" };
  if (b.request_status === "approved") return { label: "Requested", tone: "var(--accent)", bg: "var(--accent-soft)" };
  // `requested` (this session) beats a stale "declined" — a re-request goes pending.
  if (requested || b.request_status === "pending" || (b.requested && b.request_status !== "declined")) return { label: "Pending", tone: "var(--avoid)", bg: "var(--avoid-soft)" };
  if (b.request_status === "declined") return { label: "Declined", tone: "var(--ink-faint)", bg: "var(--panel-2)" };
  // In the library but no file yet and no request in flight.
  if (b.in_library) return { label: "Wanted", tone: "var(--ink-faint)", bg: "var(--panel-2)" };
  return null;
}

function BookCard({ b, ctx, authorName, full }: { b: BookDiscoverCard; ctx: BookCtx; authorName?: string; full?: boolean }) {
  const [open, setOpen] = useState(false);
  const [quickBusy, setQuickBusy] = useState(false);
  const canHover = useCanHover();
  const requested = ctx.isRequested(b.key);
  const badge = badgeFor(b, requested);
  const requestable = !badge;
  // Mouse only: on touch the whole cover opens the sheet, which has a visible Request.
  const showQuick = canHover && ctx.canRequest && requestable;

  const quick = async () => {
    if (quickBusy) return;
    setQuickBusy(true);
    try { await ctx.request(b, authorName); } catch { /* toast already shown */ }
    finally { setQuickBusy(false); }
  };

  const byline = b.author || authorName || "";

  return (
    <div className={`group ${full ? "w-full" : "w-[150px] flex-none"}`} style={{ scrollSnapAlign: "start" }}>
      <div
        className="relative overflow-hidden rounded-xl transition-[transform,box-shadow] duration-200 will-change-transform group-hover:-translate-y-1 group-hover:scale-[1.03] group-hover:shadow-[0_12px_30px_rgba(0,0,0,0.45)]"
        style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}
      >
        <button
          onClick={() => setOpen(true)}
          aria-label={`View details for ${b.title}`}
          className="absolute inset-0 block h-full w-full text-left"
        >
          {b.cover_url ? (
            <img src={b.cover_url} alt={b.title} className="h-full w-full object-cover" loading="lazy" decoding="async" onError={(e) => { e.currentTarget.style.visibility = "hidden"; }} />
          ) : (
            <div className="flex h-full w-full items-center justify-center p-2 text-center" style={{ background: "linear-gradient(150deg, hsl(28 30% 26%), hsl(24 28% 16%))" }}><span className="text-[11.5px] font-bold text-white">{b.title}</span></div>
          )}
          {badge && (
            <span className="absolute right-1.5 top-1.5 rounded-full px-2 py-0.5 text-[9px] font-bold uppercase tracking-wide" style={{ background: BADGE_BG, color: badge.tone, border: `1px solid ${badge.tone}` }}>{badge.label}</span>
          )}
          {/* Hover scrim, purely decorative: pointer-events-none so a tap anywhere on the
              cover opens the sheet, never something the user couldn't see. */}
          <div className="pointer-events-none absolute inset-0 flex flex-col justify-end p-2 opacity-0 transition-opacity group-hover:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.82) 0%, rgba(0,0,0,.15) 42%, transparent 70%)" }}>
            {!showQuick && (
              <span className="self-start rounded-md px-2.5 py-1 text-[10.5px] font-semibold" style={{ background: "rgba(255,255,255,.16)", border: "1px solid rgba(255,255,255,.26)", color: "#fff" }}>Details</span>
            )}
          </div>
        </button>
        {/* A sibling of the details button, not nested in it (invalid HTML, and its invisible
            hit area filed requests from stray taps). Clickable only once revealed. */}
        {showQuick && (
          <button
            onClick={quick}
            disabled={quickBusy}
            className="pointer-events-none absolute bottom-2 left-2 z-20 rounded-md px-2.5 py-1 text-[10.5px] font-semibold opacity-0 transition-opacity group-hover:pointer-events-auto group-hover:opacity-100 focus-visible:pointer-events-auto focus-visible:opacity-100"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            {quickBusy ? "Requesting…" : "＋ Request"}
          </button>
        )}
      </div>
      {/* Always-visible caption strip — many Open Library covers are blank leather
          with no printed title, so the cover alone can't identify the book. */}
      <div className="px-0.5 pt-2">
        <div className="truncate text-[12px] font-semibold" style={{ color: "var(--ink)" }} title={b.title}>{b.title}</div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[11px]" style={{ color: "var(--ink-faint)" }}>
          <span className="flex-none">{b.year || "—"}</span>
          {!!b.rating && <><span className="flex-none">·</span><span className="flex-none" style={{ color: "var(--accent)" }} title={b.ratings ? `${b.ratings.toLocaleString()} ratings` : undefined}>★ {b.rating.toFixed(1)}</span></>}
          {byline && <><span className="flex-none">·</span><span className="truncate" title={byline}>{byline}</span></>}
        </div>
        {b.genres && b.genres.length > 0 && (
          <div className="mt-1 truncate text-[10px]" style={{ color: "var(--ink-faint)" }} title={b.genres.join(" · ")}>{b.genres.slice(0, 2).join(" · ")}</div>
        )}
      </div>
      {open && <BookRequestModal b={b} ctx={ctx} authorName={authorName} onClose={() => setOpen(false)} />}
    </div>
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
  const [done, setDone] = useState(ctx.isRequested(b.key));
  const [subscribed, setSubscribed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const badge = badgeFor(b, done);
  const declined = badge?.label === "Declined";

  const [similar, setSimilar] = useState<BookDiscoverCard[] | null>(null);
  useEffect(() => {
    let alive = true;
    api.bookDiscoverDetail(b.key).then((d) => alive && setDetail(d)).catch(() => {});
    // The catalogue's "readers also liked" — Hardcover only; empty elsewhere.
    if (b.key.startsWith("hc:")) api.bookDiscoverSimilar(b.key).then((s) => alive && setSimilar(s)).catch(() => alive && setSimilar([]));
    return () => { alive = false; };
  }, [b.key]);

  // Only flip to the success state on real success; failures show inline + toast.
  const doRequest = async () => {
    setBusy(true); setError(null);
    try {
      const r = await ctx.request(detail ?? b, authorName);
      setSubscribed(r.subscribed);
      setDone(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
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
              <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-[11px] text-ink-faint">
                {b.year > 0 && <span>{b.year}</span>}
                {(detail?.series_name || b.series_name) && <span>· {detail?.series_name || b.series_name}{(detail?.series_position || b.series_position) ? ` #${detail?.series_position || b.series_position}` : ""}</span>}
                {!!detail?.pages && <span>· {detail.pages} pages</span>}
              </div>
              {(!!(detail?.rating ?? b.rating)) && (
                <div className="mt-2 flex flex-wrap items-center gap-2 text-[12px]">
                  <span className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11.5px] font-bold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>★ {(detail?.rating ?? b.rating ?? 0).toFixed(2)}</span>
                  {!!(detail?.ratings ?? b.ratings) && <span className="text-ink-faint">{fmtCount(detail?.ratings ?? b.ratings ?? 0)} ratings</span>}
                  {!!(detail?.readers ?? b.readers) && <span className="text-ink-faint">· {fmtCount(detail?.readers ?? b.readers ?? 0)} readers</span>}
                </div>
              )}
              <div className="mt-3">
                {done && subscribed ? (
                  <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>You’re on the list — we’ll notify you when it’s ready</span>
                ) : badge && !declined ? (
                  <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: badge.bg, color: badge.tone }}>
                    {badge.label === "In library" ? "✓ In your library" : badge.label === "Pending" ? "Requested — pending approval" : badge.label === "Wanted" ? "In library — waiting for a copy" : "Requested"}
                  </span>
                ) : !ctx.canRequest ? (
                  <span className="text-[12px] text-ink-faint">Ask your admin for request access.</span>
                ) : (
                  <div className="flex flex-wrap items-center gap-2">
                    {declined && badge && (
                      <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: badge.bg, color: badge.tone }}>Declined</span>
                    )}
                    <button onClick={doRequest} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
                      {busy ? "Requesting…" : declined ? "Request again" : "＋ Request"}
                    </button>
                  </div>
                )}
                {error && <div className="mt-1.5 text-[11.5px] font-medium" style={{ color: "var(--reject)" }}>{error}</div>}
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
          {similar && similar.length > 0 && (
            <div className="mt-4">
              <div className="mb-2 font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">Readers also liked</div>
              <div className="thin-scroll flex gap-2.5 overflow-x-auto pb-1">
                {similar.slice(0, 10).map((s) => <div key={s.key} className="w-[96px] flex-none"><BookCard b={s} ctx={ctx} /></div>)}
              </div>
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
