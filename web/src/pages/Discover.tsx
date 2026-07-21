import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { NotificationBell } from "../components/NotificationBell";
import { BooksDiscover } from "./BooksDiscover";
import { useMe, isStaff } from "../lib/me";
import { api, type DiscoverCard, type Genre, type MediaDetail, type MediaRequest } from "../lib/api";
import { posterThumb } from "../lib/img";

type Tab = "discover" | "movies" | "series" | "books";
const BASE_TABS: { key: Tab; label: string }[] = [
  { key: "discover", label: "Discover" },
  { key: "movies", label: "Movies" },
  { key: "series", label: "Series" },
];

export function Discover({ chrome = true }: { chrome?: boolean }) {
  const { user, booksEnabled } = useMe();
  // Books get their own tab at the end — a completely separate Open Library experience.
  const TABS = booksEnabled ? [...BASE_TABS, { key: "books" as Tab, label: "Books" }] : BASE_TABS;
  const [tab, setTabState] = useState<Tab>("discover");
  const [requested, setRequested] = useState<Set<string>>(new Set());
  const [toast, setToast] = useState<string | null>(null);
  // `searchInput` is what's typed in the omnibox; `search` is the committed query that
  // swaps the page to the full results grid (only via "See all" / Enter, not per keystroke).
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  // Search seeded into the Books tab (e.g. arriving from a notification click).
  const [bookSeed, setBookSeed] = useState("");
  const [params, setParams] = useSearchParams();
  const flash = useCallback((m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3000); }, []);
  // Readonly users can browse but never request.
  const canRequest = !!user && user.role !== "readonly";

  // ?q=…&tab=… (e.g. from a notification click) prefill the search, then the params
  // are consumed so the URL stays clean.
  useEffect(() => {
    const q = params.get("q");
    const t = params.get("tab");
    if (q == null && t == null) return;
    if (t === "books" && booksEnabled) {
      setTabState("books");
      if (q) setBookSeed(q);
    } else if (q) {
      setTabState("discover");
      setSearchInput(q);
      setSearch(q); // show the full grid straight away when arriving with a query
    }
    setParams({}, { replace: true });
  }, [params, setParams, booksEnabled]);

  const setTab = (t: Tab) => { setTabState(t); setSearchInput(""); setSearch(""); };
  const onSearchChange = (v: string) => { setSearchInput(v); if (!v.trim()) setSearch(""); };

  // Rethrows on failure so callers (modal, quick-request) only flip to their success
  // state on an actual success. subscribed=true → you joined an existing request.
  const doRequest = useCallback(async (c: DiscoverCard): Promise<{ subscribed: boolean }> => {
    const key = `${c.media_type}:${c.tmdb_id}`;
    try {
      const res = await api.createRequest({ media_type: c.media_type, tmdb_id: c.tmdb_id, title: c.title, year: c.year, poster_url: c.poster_url, overview: c.overview });
      setRequested((s) => new Set(s).add(key));
      flash(res.subscribed ? "You’re on the list — we’ll notify you when it’s ready" : `Requested “${c.title}”`);
      return { subscribed: res.subscribed };
    } catch (e) {
      flash((e as Error).message);
      throw e;
    }
  }, [flash]);
  const isRequested = useCallback((c: DiscoverCard) => requested.has(`${c.media_type}:${c.tmdb_id}`), [requested]);

  const ctx: RowCtx = { doRequest, isRequested, canRequest, flash };

  return (
    <>
      {chrome && <PageHeader title="Discover" crumb="Services / Discover" />}
      <div className="mx-auto w-full max-w-[1600px] px-4 py-5 sm:px-6">
        {/* Tabs + search */}
        <div className="mb-5 flex flex-wrap items-center justify-between gap-3 border-b" style={{ borderColor: "var(--line)" }}>
          <div className="flex gap-1">
            {TABS.map((t) => {
              const active = tab === t.key && !search;
              return (
                <button
                  key={t.key}
                  onClick={() => setTab(t.key)}
                  className="relative px-4 py-2.5 text-[13.5px] font-semibold transition-colors"
                  style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}
                >
                  {t.label}
                  {active && <span className="absolute inset-x-2 -bottom-px h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
                </button>
              );
            })}
          </div>
          <div className="mb-2 flex items-center gap-2 sm:mb-0">
            {/* Books have their own search inside BooksDiscover — hide the movie/TV one there. */}
            {tab !== "books" && (
              <SearchBox value={searchInput} onChange={onSearchChange} onSeeAll={(q) => { setSearchInput(q); setSearch(q); }} ctx={ctx} />
            )}
            <NotificationBell />
          </div>
        </div>

        {tab === "books" ? (
          <BooksDiscover flash={flash} canRequest={canRequest} initialQuery={bookSeed} />
        ) : search ? (
          <SearchResults query={search} ctx={ctx} />
        ) : (
          <>
            {tab === "discover" && <DiscoverTab ctx={ctx} />}
            {tab === "movies" && <BrowseTab media="movie" ctx={ctx} />}
            {tab === "series" && <BrowseTab media="series" ctx={ctx} />}
          </>
        )}
      </div>

      {toast && (
        <div className="fixed bottom-5 left-1/2 z-[60] -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>
          {toast}
        </div>
      )}
    </>
  );
}

interface RowCtx {
  doRequest: (c: DiscoverCard) => Promise<{ subscribed: boolean }>;
  isRequested: (c: DiscoverCard) => boolean;
  canRequest: boolean;
  flash: (m: string) => void;
}

// Compact inline error line for a failed fetch — distinct from a genuine empty result.
// The backend's message is surfaced verbatim (e.g. "TMDB not configured — set …").
function LoadError({ message }: { message: string }) {
  return (
    <div className="rounded-lg px-3 py-2 text-[12px] font-medium" style={{ border: "1px solid var(--line)", color: "var(--reject)", background: "var(--panel)" }}>
      Couldn’t load — {message}
    </div>
  );
}

// A film/TV glyph placeholder for cards without artwork — reads as intentional, not a
// broken image. Neutral panel + centered icon + title, consistent everywhere.
function PosterPlaceholder({ title, year }: { title: string; year?: number }) {
  return (
    <div className="flex h-full w-full flex-col items-center justify-center gap-2 p-3 text-center" style={{ background: "var(--panel-2)" }}>
      <svg width="34" height="34" viewBox="0 0 24 24" fill="none" style={{ color: "var(--ink-faint)", opacity: 0.75 }} aria-hidden>
        <rect x="3" y="4" width="18" height="16" rx="2" stroke="currentColor" strokeWidth="1.6" />
        <path d="M7 4v16M17 4v16M3 9h4M17 9h4M3 15h4M17 15h4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
      </svg>
      <div className="line-clamp-3 text-[11.5px] font-semibold leading-snug" style={{ color: "var(--ink-dim)" }}>{title}</div>
      {year ? <div className="text-[10px]" style={{ color: "var(--ink-faint)" }}>{year}</div> : null}
    </div>
  );
}

// ---- Instant search omnibox -------------------------------------------------

const RECENT_KEY = "arrmada.recentSearches";
function loadRecent(): string[] {
  try { const v = JSON.parse(localStorage.getItem(RECENT_KEY) || "[]"); return Array.isArray(v) ? v.filter((x) => typeof x === "string").slice(0, 6) : []; }
  catch { return []; }
}
function pushRecent(q: string): string[] {
  const next = [q, ...loadRecent().filter((x) => x.toLowerCase() !== q.toLowerCase())].slice(0, 6);
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(next)); } catch { /* ignore quota */ }
  return next;
}

function SearchBox({ value, onChange, onSeeAll, ctx }: { value: string; onChange: (v: string) => void; onSeeAll: (q: string) => void; ctx: RowCtx }) {
  const [focused, setFocused] = useState(false);
  const [results, setResults] = useState<DiscoverCard[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [highlight, setHighlight] = useState(-1);
  const [recent, setRecent] = useState<string[]>(loadRecent);
  const [openCard, setOpenCard] = useState<DiscoverCard | null>(null);
  const wrap = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const q = value.trim();

  // Debounced live lookup for the dropdown (min 2 chars) — does NOT swap the page.
  useEffect(() => {
    if (q.length < 2) { setResults(null); setLoading(false); return; }
    setLoading(true);
    let alive = true;
    const t = setTimeout(() => {
      api.discoverSearch(q)
        .then((r) => { if (alive) { setResults(r); setLoading(false); setHighlight(-1); } })
        .catch(() => { if (alive) { setResults([]); setLoading(false); } });
    }, 250);
    return () => { alive = false; clearTimeout(t); };
  }, [q]);

  // Close the dropdown on any click outside the widget.
  useEffect(() => {
    const onDown = (e: MouseEvent) => { if (wrap.current && !wrap.current.contains(e.target as Node)) setFocused(false); };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, []);

  const movies = useMemo(() => (results ?? []).filter((c) => c.media_type === "movie").slice(0, 6), [results]);
  const series = useMemo(() => (results ?? []).filter((c) => c.media_type === "series").slice(0, 6), [results]);
  // Flat, keyboard-navigable order: movies, then series. Index === flat.length is "See all".
  const flat = useMemo(() => [...movies, ...series], [movies, series]);
  const showResults = q.length >= 2;
  const open = focused && (showResults || recent.length > 0);

  const commit = (query: string) => {
    const s = query.trim();
    if (!s) return;
    setRecent(pushRecent(s));
    onSeeAll(s);
    setFocused(false);
    inputRef.current?.blur();
  };
  const pick = (c: DiscoverCard) => {
    if (q) setRecent(pushRecent(q));
    setOpenCard(c);
    setFocused(false);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") { setFocused(false); inputRef.current?.blur(); return; }
    if (!open) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHighlight((h) => (showResults ? Math.min(flat.length, h + 1) : h)); // flat.length == See all
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlight((h) => Math.max(-1, h - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (showResults && highlight >= 0 && highlight < flat.length) pick(flat[highlight]);
      else commit(q);
    }
  };

  return (
    <div ref={wrap} className="relative">
      <svg className="pointer-events-none absolute left-2.5 top-1/2 z-10 -translate-y-1/2" width="14" height="14" viewBox="0 0 24 24" fill="none" style={{ color: "var(--ink-faint)" }}>
        <circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" /><path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
      </svg>
      <input
        ref={inputRef}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onFocus={() => setFocused(true)}
        onKeyDown={onKeyDown}
        placeholder="Search movies & TV…"
        aria-label="Search movies and TV"
        className="w-[210px] rounded-lg py-2 pl-8 pr-7 text-[12.5px] transition-[width,box-shadow] focus:w-[300px]"
        style={{ background: "var(--panel-2)", border: `1px solid ${focused ? "var(--accent-line)" : "var(--line)"}`, color: "var(--ink)" }}
      />
      {value && (
        <button onClick={() => { onChange(""); inputRef.current?.focus(); }} className="absolute right-2 top-1/2 z-10 -translate-y-1/2 text-ink-faint hover:text-[var(--ink)]" style={{ fontSize: "13px" }} aria-label="Clear search">✕</button>
      )}

      {open && (
        <div
          className="thin-scroll absolute right-0 z-40 mt-2 max-h-[70vh] w-[340px] overflow-y-auto rounded-xl py-1.5"
          style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "0 16px 40px rgba(0,0,0,.45)" }}
        >
          {!showResults ? (
            <div className="px-3 py-2">
              <div className="mb-2 font-mono text-[9.5px] uppercase tracking-wide" style={{ color: "var(--ink-faint)" }}>Recent searches</div>
              <div className="flex flex-wrap gap-1.5">
                {recent.map((rq) => (
                  <button
                    key={rq}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => { onChange(rq); inputRef.current?.focus(); }}
                    className="rounded-full px-2.5 py-1 text-[11.5px] font-medium"
                    style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}
                  >
                    {rq}
                  </button>
                ))}
              </div>
            </div>
          ) : loading && !results ? (
            <div className="px-3 py-6 text-center text-[12px]" style={{ color: "var(--ink-faint)" }}>Searching…</div>
          ) : flat.length === 0 ? (
            <button onMouseDown={(e) => e.preventDefault()} onClick={() => commit(q)} className="block w-full px-3 py-6 text-center text-[12px]" style={{ color: "var(--ink-faint)" }}>
              No quick matches — see all results for “{q}”
            </button>
          ) : (
            <>
              {movies.length > 0 && <SearchGroup label="Movies" items={movies} flatBase={0} highlight={highlight} onPick={pick} />}
              {series.length > 0 && <SearchGroup label="Series" items={series} flatBase={movies.length} highlight={highlight} onPick={pick} />}
              <button
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => commit(q)}
                className="mt-1 flex w-full items-center gap-2 border-t px-3 py-2.5 text-left text-[12px] font-semibold"
                style={{ borderColor: "var(--line)", background: highlight === flat.length ? "var(--accent-soft)" : "transparent", color: "var(--accent)" }}
              >
                See all results for “{q}”
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" className="ml-auto"><path d="M9 6l6 6-6 6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
              </button>
            </>
          )}
        </div>
      )}

      {openCard && <RequestDetailModal card={openCard} ctx={ctx} onClose={() => setOpenCard(null)} />}
    </div>
  );
}

function SearchGroup({ label, items, flatBase, highlight, onPick }: { label: string; items: DiscoverCard[]; flatBase: number; highlight: number; onPick: (c: DiscoverCard) => void }) {
  return (
    <div className="py-1">
      <div className="px-3 py-1 font-mono text-[9.5px] uppercase tracking-wide" style={{ color: "var(--ink-faint)" }}>{label}</div>
      {items.map((c, i) => {
        const idx = flatBase + i;
        const on = highlight === idx;
        const badge = badgeFor(c, false);
        return (
          <button
            key={`${c.media_type}:${c.tmdb_id}`}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => onPick(c)}
            className="flex w-full items-center gap-2.5 px-2.5 py-1.5 text-left"
            style={{ background: on ? "var(--accent-soft)" : "transparent" }}
          >
            <div className="h-[54px] w-[36px] flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
              {c.poster_url ? <img src={posterThumb(c.poster_url)} alt="" className="h-full w-full object-cover" loading="lazy" /> : (
                <div className="grid h-full w-full place-items-center" style={{ color: "var(--ink-faint)" }}>
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><rect x="3" y="4" width="18" height="16" rx="2" stroke="currentColor" strokeWidth="1.6" /></svg>
                </div>
              )}
            </div>
            <div className="min-w-0 flex-1">
              <div className="truncate text-[12.5px] font-semibold" style={{ color: "var(--ink)" }}>{c.title}</div>
              <div className="flex items-center gap-1.5 text-[10.5px]" style={{ color: "var(--ink-faint)" }}>
                <span>{c.year || "—"}</span>
                <span>·</span>
                <span>{c.media_type === "series" ? "TV" : "Movie"}</span>
                {c.vote_average > 0 && <span style={{ color: "var(--accent)" }}>★ {c.vote_average.toFixed(1)}</span>}
              </div>
            </div>
            {badge && <span className="flex-none rounded-full px-1.5 py-0.5 text-[8.5px] font-bold uppercase" style={{ background: BADGE_BG, color: badge.tone, border: `1px solid ${badge.tone}` }}>{badge.label}</span>}
          </button>
        );
      })}
    </div>
  );
}

function SearchResults({ query, ctx }: { query: string; ctx: RowCtx }) {
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setItems(null); setError(null);
    api.discoverSearch(query)
      .then((r) => { if (alive) setItems(r); })
      .catch((e) => { if (alive) { setItems([]); setError((e as Error).message); } });
    return () => { alive = false; };
  }, [query]);

  return (
    <div>
      <h2 className="m-0 mb-3 text-[15px] font-bold">
        Results for “{query}” {items && !error && <span className="font-normal text-ink-faint">· {items.length}</span>}
      </h2>
      {items === null ? (
        <div className="grid gap-x-3 gap-y-5" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
          {Array.from({ length: 12 }).map((_, i) => <CardSkeleton key={i} full />)}
        </div>
      ) : error ? (
        <LoadError message={error} />
      ) : items.length === 0 ? (
        <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
          No movies or shows match “{query}”.
        </div>
      ) : (
        <div className="grid gap-x-3 gap-y-5" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
          {items.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} full />)}
        </div>
      )}
    </div>
  );
}

function DiscoverTab({ ctx }: { ctx: RowCtx }) {
  return (
    <div className="flex flex-col gap-7">
      <Hero ctx={ctx} />
      <MyRequestsRow flash={ctx.flash} />
      <PosterRow title="Trending this week" load={() => api.discoverTrending("all")} ctx={ctx} />
      <PosterRow title="Popular movies" load={() => api.discoverPopular("movie")} ctx={ctx} />
      <PosterRow title="Popular series" load={() => api.discoverPopular("series")} ctx={ctx} />
      <PosterRow title="Upcoming — request ahead" load={() => api.discoverUpcoming()} ctx={ctx} />
      <GenreExplorer media="movie" switchable ctx={ctx} />
    </div>
  );
}

// ---- Cinematic hero ---------------------------------------------------------

function Hero({ ctx }: { ctx: RowCtx }) {
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
  const [idx, setIdx] = useState(0);
  const [paused, setPaused] = useState(false);
  const [openCard, setOpenCard] = useState<DiscoverCard | null>(null);
  const [quickBusy, setQuickBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    api.discoverTrending("all")
      .then((r) => { if (alive) setItems(r.filter((x) => !!x.backdrop_url).slice(0, 5)); })
      .catch(() => { if (alive) setItems([]); });
    return () => { alive = false; };
  }, []);

  const count = items?.length ?? 0;
  useEffect(() => {
    if (paused || count <= 1) return;
    const t = setInterval(() => setIdx((i) => (i + 1) % count), 7000);
    return () => clearInterval(t);
  }, [paused, count]);
  useEffect(() => { if (count > 0 && idx >= count) setIdx(0); }, [idx, count]);

  if (items === null) return <div className="w-full animate-pulse rounded-2xl" style={{ height: "clamp(340px, 46vh, 560px)", background: "var(--panel-2)", border: "1px solid var(--line)" }} />;
  if (count === 0) return null;

  const cur = items[Math.min(idx, count - 1)];
  const requested = ctx.isRequested(cur);
  const badge = badgeFor(cur, requested);
  const requestable = !badge;

  const quick = async () => {
    if (quickBusy) return;
    setQuickBusy(true);
    try { await ctx.doRequest(cur); } catch { /* toast shown */ } finally { setQuickBusy(false); }
  };

  return (
    <div
      className="relative overflow-hidden rounded-2xl"
      style={{ height: "clamp(340px, 46vh, 560px)", border: "1px solid var(--line)" }}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
    >
      {/* Stacked backdrops crossfade; rendering all 5 also preloads them. */}
      {items.map((it, i) => (
        <img
          key={it.tmdb_id}
          src={it.backdrop_url}
          alt=""
          aria-hidden
          className="absolute inset-0 h-full w-full object-cover transition-opacity duration-700"
          style={{ opacity: i === idx ? 1 : 0 }}
          loading={i === 0 ? "eager" : "lazy"}
          decoding="async"
        />
      ))}
      {/* Scrims: left→right for text legibility, bottom melts into the page. */}
      <div className="absolute inset-0" style={{ background: "linear-gradient(to right, rgba(0,0,0,.85) 0%, rgba(0,0,0,.55) 38%, rgba(0,0,0,0) 72%)" }} />
      <div className="absolute inset-x-0 bottom-0 h-2/3" style={{ background: "linear-gradient(to top, var(--bg) 2%, transparent)" }} />

      <div className="relative z-10 flex h-full max-w-[640px] flex-col justify-end gap-2.5 p-5 sm:p-8">
        {badge && (
          <span className="w-fit rounded-full px-2 py-0.5 text-[9.5px] font-bold uppercase tracking-wide" style={{ background: BADGE_BG, color: badge.tone, border: `1px solid ${badge.tone}` }}>{badge.label}</span>
        )}
        <h2 className="m-0 line-clamp-2 text-[26px] font-extrabold leading-[1.08] sm:text-[38px]" style={{ color: "#fff", textShadow: "0 2px 18px rgba(0,0,0,.55)" }}>{cur.title}</h2>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] font-medium" style={{ color: "rgba(255,255,255,.82)" }}>
          {cur.year ? <span>{cur.year}</span> : null}
          {cur.vote_average > 0 && <span style={{ color: "var(--accent)" }}>★ {cur.vote_average.toFixed(1)}</span>}
          <span className="rounded px-1.5 py-px text-[10px] uppercase" style={{ background: "rgba(255,255,255,.14)" }}>{cur.media_type === "series" ? "TV" : "Movie"}</span>
        </div>
        {cur.overview && <p className="m-0 line-clamp-2 max-w-[560px] text-[13px] leading-relaxed sm:line-clamp-3" style={{ color: "rgba(255,255,255,.78)" }}>{cur.overview}</p>}
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <button
            onClick={() => setOpenCard(cur)}
            className="rounded-lg px-4 py-2 text-[12.5px] font-semibold backdrop-blur-sm"
            style={{ background: "rgba(255,255,255,.16)", border: "1px solid rgba(255,255,255,.28)", color: "#fff" }}
          >
            View details
          </button>
          {ctx.canRequest && requestable && (
            <button
              onClick={quick}
              disabled={quickBusy}
              className="rounded-lg px-4 py-2 text-[12.5px] font-semibold"
              style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: quickBusy ? 0.65 : 1 }}
            >
              {quickBusy ? "Requesting…" : "＋ Request"}
            </button>
          )}
        </div>
      </div>

      {count > 1 && (
        <div className="absolute bottom-4 right-5 z-10 flex gap-1.5">
          {items.map((it, i) => (
            <button
              key={it.tmdb_id}
              onClick={() => setIdx(i)}
              aria-label={`Show slide ${i + 1}`}
              className="h-1.5 rounded-full transition-all"
              style={{ width: i === idx ? 20 : 6, background: i === idx ? "#fff" : "rgba(255,255,255,.45)" }}
            />
          ))}
        </div>
      )}

      {openCard && <RequestDetailModal card={openCard} ctx={ctx} onClose={() => setOpenCard(null)} />}
    </div>
  );
}

// MyRequestsRow is the horizontal strip of requests at the top of Discover. A
// requester sees their own requests (status + download progress); staff see every
// request and can approve/decline pending ones inline (no dedicated page needed).
function MyRequestsRow({ flash }: { flash: (m: string) => void }) {
  const { user } = useMe();
  const staff = isStaff(user);
  const [items, setItems] = useState<MediaRequest[] | null>(null);
  const scroller = useRef<HTMLDivElement>(null);

  const load = useCallback(() => api.requests().then((r) => setItems(r.requests)).catch(() => setItems([])), []);
  useEffect(() => {
    load();
    const t = setInterval(load, 8000); // refresh so status/progress advance
    return () => clearInterval(t);
  }, [load]);

  if (!items || items.length === 0) return null;
  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between">
        <h2 className="m-0 text-[15px] font-bold">{staff ? "Requests" : "Your requests"}</h2>
        <div className="flex gap-1"><ArrowBtn dir={-1} onClick={() => scroll(-1)} /><ArrowBtn dir={1} onClick={() => scroll(1)} /></div>
      </div>
      <div ref={scroller} className="thin-scroll flex gap-3 overflow-x-auto pb-2" style={{ scrollSnapType: "x proximity" }}>
        {items.map((rq) => <RequestPoster key={rq.id} rq={rq} staff={staff} own={!!user && rq.requested_by === user.id} onChanged={load} flash={flash} />)}
      </div>
    </div>
  );
}

function RequestPoster({ rq, staff, own, onChanged, flash }: { rq: MediaRequest; staff: boolean; own: boolean; onChanged: () => void; flash: (m: string) => void }) {
  const [busy, setBusy] = useState(false);
  const pct = rq.download_progress != null ? Math.round(rq.download_progress * 100) : 0;
  // "Downloading" only when something is actually downloading — an approved-but-not-
  // -yet-released title stays "Requested".
  const status = rq.available ? { label: "Available", tone: "var(--good)" }
    : rq.status === "declined" ? { label: "Declined", tone: "var(--reject)" }
    : pct > 0 ? { label: "Downloading", tone: "var(--accent)" }
    : rq.status === "approved" ? { label: "Requested", tone: "var(--accent)" }
    : { label: "Pending", tone: "var(--avoid)" };
  // Staff/owner actions get honest feedback: a success toast on success, the server's
  // message on failure — never a silent no-op.
  const act = async (fn: () => Promise<unknown>, okMsg: string) => {
    setBusy(true);
    try { await fn(); flash(okMsg); onChanged(); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(false); }
  };
  const withdraw = () => {
    if (!window.confirm(`Withdraw your request for “${rq.title}”?`)) return;
    act(() => api.deleteRequest(rq.id), `Withdrew “${rq.title}”`);
  };
  return (
    <div className="group relative w-[150px] flex-none overflow-hidden rounded-xl" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)", scrollSnapAlign: "start" }}>
      {rq.poster_url ? (
        <img src={posterThumb(rq.poster_url)} alt={rq.title} className="h-full w-full object-cover" loading="lazy" decoding="async" />
      ) : (
        <PosterPlaceholder title={rq.title} year={rq.year} />
      )}
      <span className="absolute right-1.5 top-1.5 rounded-full px-2 py-0.5 text-[9px] font-bold uppercase tracking-wide" style={{ background: BADGE_BG, color: status.tone, border: `1px solid ${status.tone}` }}>{status.label}</span>
      {/* Who asked for it — staff only, and always visible (not hover-gated), since staff
          see everyone's requests and "whose is this?" is the first question. */}
      {staff && rq.requested_by_name && (
        <span
          className="absolute left-1.5 top-1.5 flex max-w-[70%] items-center gap-1 rounded-full py-[2px] pl-[2px] pr-1.5"
          style={{ background: BADGE_BG, border: "1px solid rgba(255,255,255,.18)" }}
          title={`Requested by ${rq.requested_by_name}`}
        >
          <span className="grid h-[14px] w-[14px] flex-none place-items-center rounded-full text-[8px] font-bold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
            {rq.requested_by_name[0]?.toUpperCase()}
          </span>
          <span className="truncate text-[9px] font-semibold text-white">{rq.requested_by_name}</span>
        </span>
      )}
      {pct > 0 && pct < 100 && (
        <div className="absolute inset-x-0 bottom-0 z-10 h-1.5" style={{ background: "rgba(20,12,7,.55)" }}>
          <div className="h-full" style={{ width: `${pct}%`, background: "var(--accent)" }} />
        </div>
      )}
      <div className="absolute inset-x-0 bottom-0 flex flex-col gap-1.5 p-2 opacity-0 transition-opacity group-hover:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.92), transparent)" }}>
        <div className="truncate text-[11.5px] font-semibold text-white">{rq.title}</div>
        {staff && rq.status === "pending" ? (
          <div className="flex gap-1.5">
            <button disabled={busy} onClick={() => act(() => api.approveRequest(rq.id), "Approved — searching now")} className="flex-1 rounded px-2 py-1 text-[10px] font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Approve</button>
            <button disabled={busy} onClick={() => act(() => api.declineRequest(rq.id), "Declined")} className="flex-1 rounded px-2 py-1 text-[10px] font-semibold" style={{ background: "rgba(255,255,255,.15)", color: "#fff" }}>Decline</button>
            {own && <button disabled={busy} onClick={withdraw} title="Withdraw your request" className="w-6 flex-none rounded px-0 py-1 text-[10px] font-semibold" style={{ background: "rgba(255,255,255,.15)", color: "#fff" }}>✕</button>}
          </div>
        ) : own && rq.status === "pending" ? (
          <div className="flex items-center justify-between gap-1.5">
            <span className="text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>{rq.year || ""}</span>
            <button disabled={busy} onClick={withdraw} className="rounded px-2 py-1 text-[10px] font-semibold" style={{ background: "rgba(255,255,255,.15)", color: "#fff" }}>✕ Withdraw</button>
          </div>
        ) : (
          <div className="text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>{rq.year || ""}</div>
        )}
      </div>
    </div>
  );
}

function BrowseTab({ media, ctx }: { media: "movie" | "series"; ctx: RowCtx }) {
  return (
    <div className="flex flex-col gap-7">
      <PosterRow title={`Trending ${media === "movie" ? "movies" : "series"}`} load={() => api.discoverTrending(media)} ctx={ctx} />
      <PosterRow title={`Popular ${media === "movie" ? "movies" : "series"}`} load={() => api.discoverPopular(media)} ctx={ctx} />
      {media === "movie" && <PosterRow title="Upcoming — request ahead" load={() => api.discoverUpcoming()} ctx={ctx} />}
      <GenreExplorer media={media} ctx={ctx} />
    </div>
  );
}

// Poster + caption skeleton, matched to a real card so loading → loaded has no shift.
function CardSkeleton({ full }: { full?: boolean }) {
  return (
    <div className={full ? "w-full" : "w-[150px] flex-none"}>
      <div className="rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />
      <div className="mt-2 h-3 w-4/5 rounded" style={{ background: "var(--panel-2)" }} />
      <div className="mt-1.5 h-2.5 w-2/5 rounded" style={{ background: "var(--panel-2)" }} />
    </div>
  );
}

function PosterRow({ title, load, ctx }: { title: string; load: () => Promise<DiscoverCard[]>; ctx: RowCtx }) {
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const scroller = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let alive = true;
    load()
      .then((r) => { if (alive) { setItems(r); setError(null); } })
      .catch((e) => { if (alive) { setItems([]); setError((e as Error).message); } });
    // Re-pull enrichment (badges, download progress) every 30s while the tab is
    // visible; refresh failures keep the last good data.
    const t = setInterval(() => {
      if (document.visibilityState !== "visible") return;
      load().then((r) => { if (alive) { setItems(r); setError(null); } }).catch(() => {});
    }, 30000);
    return () => { alive = false; clearInterval(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });

  if (items && items.length === 0 && !error) return null;
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
            ? Array.from({ length: 8 }).map((_, i) => <CardSkeleton key={i} />)
            : items.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} />)}
        </div>
      )}
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

function GenreExplorer({ media, switchable, ctx }: { media: "movie" | "series"; switchable?: boolean; ctx: RowCtx }) {
  // On the main Discover tab (switchable) the explorer can flip between movies and
  // series; on the per-media tabs it stays locked to that tab's media.
  const [m, setM] = useState<"movie" | "series">(media);
  const [genres, setGenres] = useState<Genre[]>([]);
  const [genresError, setGenresError] = useState<string | null>(null);
  const [active, setActive] = useState<Genre | null>(null);
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
  const [itemsError, setItemsError] = useState<string | null>(null);
  // Monotonic sequence so a slow, earlier genre response can never overwrite the
  // results of a later selection.
  const seq = useRef(0);

  useEffect(() => { setM(media); }, [media]);

  useEffect(() => {
    let alive = true;
    seq.current++; // invalidate any in-flight genre-item load
    setActive(null); setItems(null); setItemsError(null); setGenresError(null);
    api.discoverGenres(m)
      .then((g) => { if (alive) setGenres(g); })
      .catch((e) => { if (alive) { setGenres([]); setGenresError((e as Error).message); } });
    return () => { alive = false; };
  }, [m]);

  const pick = (g: Genre) => {
    const my = ++seq.current;
    setActive(g); setItems(null); setItemsError(null);
    api.discoverByGenre(m, g.id)
      .then((r) => { if (seq.current === my) setItems(r); })
      .catch((e) => { if (seq.current === my) { setItems([]); setItemsError((e as Error).message); } });
  };

  if (!switchable && genres.length === 0 && !genresError) return null;
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between">
        <h2 className="m-0 text-[15px] font-bold">Browse by genre</h2>
        {switchable && (
          <div className="flex gap-1">
            {(["movie", "series"] as const).map((k) => {
              const on = m === k;
              return (
                <button key={k} onClick={() => setM(k)} className="rounded-full px-3 py-1 text-[12px] font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>
                  {k === "movie" ? "Movies" : "Series"}
                </button>
              );
            })}
          </div>
        )}
      </div>
      {genresError ? (
        <LoadError message={genresError} />
      ) : (
        <>
          <div className="mb-4 flex flex-wrap gap-2">
            {genres.map((g) => {
              const on = active?.id === g.id;
              return (
                <button key={g.id} onClick={() => pick(g)} className="rounded-full px-3 py-1 text-[12px] font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>
                  {g.name}
                </button>
              );
            })}
          </div>
          {active && (
            itemsError ? (
              <LoadError message={itemsError} />
            ) : (
              <div className="grid gap-x-3 gap-y-5" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
                {items === null
                  ? Array.from({ length: 10 }).map((_, i) => <CardSkeleton key={i} full />)
                  : items.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} full />)}
              </div>
            )
          )}
        </>
      )}
    </div>
  );
}

// A near-opaque chip so status stays legible over any poster.
const BADGE_BG = "rgba(14,10,7,.92)";

function badgeFor(c: DiscoverCard, requested: boolean): { label: string; tone: string } | null {
  if (c.has_file) return { label: "In library", tone: "var(--good)" };
  if ((c.download_progress ?? 0) > 0) return { label: "Downloading", tone: "var(--accent)" };
  if (c.request_status === "approved") return { label: "Requested", tone: "var(--accent)" };
  // `requested` (this session) beats a stale "declined" — a re-request goes pending.
  if (requested || c.request_status === "pending") return { label: "Pending", tone: "var(--avoid)" };
  if (c.request_status === "declined") return { label: "Declined", tone: "var(--ink-faint)" };
  // In the library but no file yet and no request in flight → it's wanted, not "requested".
  if (c.in_library) return { label: "Wanted", tone: "var(--ink-faint)" };
  return null;
}

// A restyled status chip — semi-opaque background, rounded, tone-coloured border.
function StatusChip({ badge, className }: { badge: { label: string; tone: string }; className?: string }) {
  return (
    <span className={`rounded-full px-2 py-0.5 text-[9px] font-bold uppercase tracking-wide ${className ?? ""}`} style={{ background: BADGE_BG, color: badge.tone, border: `1px solid ${badge.tone}` }}>{badge.label}</span>
  );
}

function MediaCard({ c, ctx, full }: { c: DiscoverCard; ctx: RowCtx; full?: boolean }) {
  const [open, setOpen] = useState(false);
  const [quickBusy, setQuickBusy] = useState(false);
  const requested = ctx.isRequested(c);
  const badge = badgeFor(c, requested);
  const requestable = !badge; // no badge → nothing in library/queue yet

  // Quick-request straight from the hover overlay — same honest flow as the modal:
  // doRequest toasts success/failure and only marks requested on success.
  const quick = async (e: React.MouseEvent | React.KeyboardEvent) => {
    e.stopPropagation();
    if (quickBusy) return;
    setQuickBusy(true);
    try { await ctx.doRequest(c); } catch { /* toast already shown */ }
    finally { setQuickBusy(false); }
  };

  return (
    <div className={`group ${full ? "w-full" : "w-[150px] flex-none"}`} style={{ scrollSnapAlign: "start" }}>
      <button
        onClick={() => setOpen(true)}
        aria-label={`View details for ${c.title}`}
        className="relative block w-full overflow-hidden rounded-xl text-left transition-[transform,box-shadow] duration-200 will-change-transform group-hover:-translate-y-1 group-hover:scale-[1.03] group-hover:shadow-[0_12px_30px_rgba(0,0,0,0.45)]"
        style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}
      >
        {c.poster_url ? (
          <img src={posterThumb(c.poster_url)} alt={c.title} className="h-full w-full object-cover" loading="lazy" decoding="async" />
        ) : (
          <PosterPlaceholder title={c.title} year={c.year} />
        )}
        {badge && <StatusChip badge={badge} className="absolute right-1.5 top-1.5" />}
        {/* Terracotta download bar along the bottom of the poster while it's grabbing. */}
        {c.download_progress != null && c.download_progress > 0 && c.download_progress < 1 && (
          <div className="absolute inset-x-0 bottom-0 z-10 h-1.5" style={{ background: "rgba(20,12,7,.55)" }}>
            <div className="h-full" style={{ width: `${Math.round(c.download_progress * 100)}%`, background: "var(--accent)" }} />
          </div>
        )}
        {/* Hover overlay: quick-request + a details affordance (touch users tap the card). */}
        <div className="absolute inset-0 flex flex-col justify-end gap-1.5 p-2 opacity-0 transition-opacity group-hover:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.82) 0%, rgba(0,0,0,.15) 42%, transparent 70%)" }}>
          {ctx.canRequest && requestable ? (
            <span
              role="button"
              tabIndex={0}
              onClick={quick}
              onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") quick(e); }}
              className="self-start rounded-md px-2.5 py-1 text-[10.5px] font-semibold"
              style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: quickBusy ? 0.6 : 1 }}
            >
              {quickBusy ? "Requesting…" : "＋ Request"}
            </span>
          ) : (
            <span className="self-start rounded-md px-2.5 py-1 text-[10.5px] font-semibold" style={{ background: "rgba(255,255,255,.16)", border: "1px solid rgba(255,255,255,.26)", color: "#fff" }}>Details</span>
          )}
        </div>
      </button>
      {/* Always-visible caption strip so cards read before hover (Plex style). */}
      <div className="px-0.5 pt-2">
        <div className="truncate text-[12px] font-semibold" style={{ color: "var(--ink)" }} title={c.title}>{c.title}</div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[11px]" style={{ color: "var(--ink-faint)" }}>
          <span>{c.year || "—"}</span>
          {c.vote_average > 0 && <><span>·</span><span style={{ color: "var(--accent)" }}>★ {c.vote_average.toFixed(1)}</span></>}
        </div>
      </div>
      {open && <RequestDetailModal card={c} ctx={ctx} onClose={() => setOpen(false)} />}
    </div>
  );
}

function RatingBadge({ label, value, bg, fg }: { label: string; value: string; bg: string; fg: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-bold" style={{ background: bg, color: fg }}>
      <span className="font-mono text-[8.5px] uppercase opacity-80">{label}</span>{value}
    </span>
  );
}

function crewByJob(crew: MediaDetail["crew"], job: string): string {
  return (crew ?? []).filter((c) => c.job === job).map((c) => c.name).join(", ");
}

// ---- Detail sheet -----------------------------------------------------------

function RequestDetailModal({ card, ctx, onClose }: { card: DiscoverCard; ctx: RowCtx; onClose: () => void }) {
  // `current` is swappable — clicking a "More like this" card replaces the sheet's
  // contents in place (one modal, no stacking) while keeping the honest request flow.
  const [current, setCurrent] = useState<DiscoverCard>(card);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(ctx.isRequested(card));
  const [subscribed, setSubscribed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [d, setD] = useState<MediaDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(true);
  const dialogRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  // Stable refs so the mount/detail effects don't re-run when the parent re-renders
  // (ctx and onClose are fresh object/arrow identities on every Discover render).
  const ctxRef = useRef(ctx); ctxRef.current = ctx;
  const closeRef = useRef(onClose); closeRef.current = onClose;

  const c = current;
  const badge = badgeFor(c, done);
  const declined = badge?.label === "Declined";

  // Fetch detail whenever the shown card changes; reset per-card request state.
  useEffect(() => {
    let alive = true;
    setD(null); setDetailLoading(true); setError(null);
    setDone(ctxRef.current.isRequested(current)); setSubscribed(false);
    scrollRef.current?.scrollTo({ top: 0 });
    api.mediaDetail(current.media_type, current.tmdb_id)
      .then((r) => { if (alive) { setD(r); setDetailLoading(false); } })
      .catch(() => { if (alive) setDetailLoading(false); });
    return () => { alive = false; };
  }, [current]);

  // A11y: trap focus, move focus in, restore on close, Esc to close, lock body scroll.
  useEffect(() => {
    const prevFocus = document.activeElement as HTMLElement | null;
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    dialogRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.stopPropagation(); closeRef.current(); return; }
      if (e.key === "Tab" && dialogRef.current) {
        const nodes = dialogRef.current.querySelectorAll<HTMLElement>('a[href],button:not([disabled]),input,select,textarea,[tabindex]:not([tabindex="-1"])');
        if (nodes.length === 0) return;
        const first = nodes[0], last = nodes[nodes.length - 1];
        const act = document.activeElement;
        if (e.shiftKey && (act === first || act === dialogRef.current)) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && act === last) { e.preventDefault(); first.focus(); }
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.body.style.overflow = prevOverflow;
      prevFocus?.focus?.();
    };
  }, []);

  // Only flip to the success state when the request actually succeeded; on failure
  // the error shows inline (plus the toast) and the button stays.
  const request = async () => {
    setBusy(true); setError(null);
    try {
      const r = await ctx.doRequest(c);
      setSubscribed(r.subscribed);
      setDone(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const overview = d?.overview || c.overview;
  const runtime = d?.runtime ? `${Math.floor(d.runtime / 60)}h ${d.runtime % 60}m` : "";
  const director = crewByJob(d?.crew, "Director");
  const writer = crewByJob(d?.crew, "Writer");
  const producer = crewByJob(d?.crew, "Producer");
  const creator = crewByJob(d?.crew, "Creator");
  const r = d?.ratings;
  const similar = (d?.similar ?? []).filter((s) => s.tmdb_id !== c.tmdb_id).slice(0, 12);

  return (
    <div
      className="fixed inset-0 z-50 flex justify-center overflow-y-auto sm:items-start sm:p-6"
      style={{ background: "rgba(0,0,0,.68)" }}
      onClick={onClose}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={c.title}
        tabIndex={-1}
        className="flex min-h-full w-full flex-col outline-none sm:my-0 sm:min-h-0 sm:max-w-[820px] sm:rounded-2xl sm:shadow-2xl"
        style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Backdrop hero */}
        <div className="relative h-[190px] flex-none sm:h-[260px] sm:rounded-t-2xl sm:overflow-hidden" style={{ background: "var(--panel-2)" }}>
          {(d?.backdrop_url || c.backdrop_url) && <img src={d?.backdrop_url || c.backdrop_url} alt="" className="h-full w-full object-cover opacity-60" />}
          <div className="absolute inset-0" style={{ background: "linear-gradient(to top, var(--panel) 4%, transparent 78%)" }} />
          <button onClick={onClose} aria-label="Close" className="absolute right-3 top-3 grid h-8 w-8 place-items-center rounded-full" style={{ background: "rgba(20,12,7,.7)", color: "#fff" }}>✕</button>
        </div>

        <div ref={scrollRef} className="flex-1 overflow-y-auto">
          <div className="flex gap-4 px-5 pt-0 sm:px-6">
            <div className="-mt-20 h-[186px] w-[124px] flex-none overflow-hidden rounded-xl sm:-mt-24 sm:h-[210px] sm:w-[140px]" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", boxShadow: "0 10px 26px rgba(0,0,0,.4)" }}>
              {c.poster_url ? <img src={c.poster_url} alt="" className="h-full w-full object-cover" /> : <PosterPlaceholder title={c.title} year={c.year} />}
            </div>
            <div className="min-w-0 flex-1 pt-4">
              <div className="flex flex-wrap items-center gap-2">
                <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{c.media_type === "series" ? "TV" : "Movie"}</span>
                <h2 className="m-0 text-[18px] font-bold leading-tight sm:text-[21px]">{c.title}</h2>
                <span className="font-mono text-[11.5px] text-ink-faint">{c.year || ""}</span>
              </div>
              {/* Meta line */}
              <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-faint">
                {d?.certification && <span className="rounded border px-1.5 py-px font-mono text-[10px]" style={{ borderColor: "var(--line)", color: "var(--ink-dim)" }}>{d.certification}</span>}
                {runtime && <span>{runtime}</span>}
                {d?.network && <span>{d.network}</span>}
                {d?.status && <span>{d.status}</span>}
              </div>
              {/* Genre pills */}
              {d?.genres && d.genres.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {d.genres.slice(0, 4).map((g) => (
                    <span key={g} className="rounded-full px-2 py-0.5 text-[10.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{g}</span>
                  ))}
                </div>
              )}
              {/* Ratings */}
              {(r?.tmdb || r?.imdb || r?.rotten_tomatoes || r?.metacritic) && (
                <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
                  {r?.tmdb ? <RatingBadge label="TMDB" value={r.tmdb.toFixed(1)} bg="var(--accent-soft)" fg="var(--accent)" /> : null}
                  {r?.imdb ? <RatingBadge label="IMDb" value={r.imdb} bg="#f5c518" fg="#000" /> : null}
                  {r?.rotten_tomatoes ? <RatingBadge label="RT" value={r.rotten_tomatoes} bg="#fa320a" fg="#fff" /> : null}
                  {r?.metacritic ? <RatingBadge label="MC" value={r.metacritic} bg="#00658f" fg="#fff" /> : null}
                </div>
              )}
              {c.release_date && new Date(c.release_date) > new Date() && (
                <div className="mt-2 font-mono text-[10.5px]" style={{ color: "var(--avoid)" }}>Releases {c.release_date} — request ahead</div>
              )}
              <div className="mt-3 flex flex-wrap items-center gap-2">
                {done && subscribed ? (
                  <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>You’re on the list — we’ll notify you when it’s ready</span>
                ) : badge && !declined ? (
                  <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: BADGE_BG, color: badge.tone, border: `1px solid ${badge.tone}` }}>
                    {badge.label === "In library" ? "✓ In your library" : badge.label === "Pending" ? "Requested — pending approval" : badge.label === "Downloading" ? "Downloading…" : badge.label === "Wanted" ? "In library — waiting for a file" : "Requested"}
                  </span>
                ) : !ctx.canRequest ? (
                  <span className="text-[12px] text-ink-faint">Ask your admin for request access.</span>
                ) : (
                  <>
                    {declined && badge && (
                      <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: BADGE_BG, color: badge.tone, border: `1px solid ${badge.tone}` }}>Declined</span>
                    )}
                    <button onClick={request} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
                      {busy ? "Requesting…" : declined ? "Request again" : "＋ Request"}
                    </button>
                  </>
                )}
                {d?.trailer_url && (
                  <a href={d.trailer_url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1.5 rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z" /></svg>
                    Trailer
                  </a>
                )}
              </div>
              {error && <div className="mt-1.5 text-[11.5px] font-medium" style={{ color: "var(--reject)" }}>{error}</div>}
            </div>
          </div>

          <div className="px-5 pb-6 pt-4 sm:px-6">
            {overview ? (
              <p className="m-0 text-[13px] leading-relaxed text-ink-dim">{overview}</p>
            ) : detailLoading ? (
              <div className="space-y-2">
                <div className="h-3 w-full rounded" style={{ background: "var(--panel-2)" }} />
                <div className="h-3 w-11/12 rounded" style={{ background: "var(--panel-2)" }} />
                <div className="h-3 w-3/4 rounded" style={{ background: "var(--panel-2)" }} />
              </div>
            ) : null}

            {/* Crew */}
            {(director || writer || producer || creator) && (
              <div className="mt-4 grid grid-cols-2 gap-x-6 gap-y-2 text-[12px] sm:grid-cols-3">
                {creator && <CrewFact label="Creator" value={creator} />}
                {director && <CrewFact label="Director" value={director} />}
                {writer && <CrewFact label="Writer" value={writer} />}
                {producer && <CrewFact label="Producer" value={producer} />}
              </div>
            )}

            {/* Cast */}
            {d?.cast && d.cast.length > 0 && (
              <div className="mt-5">
                <h3 className="m-0 mb-2 text-[12px] font-bold uppercase tracking-wide text-ink-faint">Cast</h3>
                <div className="thin-scroll flex gap-3 overflow-x-auto pb-1">
                  {d.cast.slice(0, 10).map((p, i) => (
                    <div key={i} className="w-[80px] flex-none text-center">
                      <div className="mb-1 overflow-hidden rounded-lg" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }}>
                        {p.profile_url ? <img src={p.profile_url} alt={p.name} className="h-full w-full object-cover" loading="lazy" /> : (
                          <div className="grid h-full w-full place-items-center" style={{ color: "var(--ink-faint)" }}>
                            <svg width="20" height="20" viewBox="0 0 24 24" fill="none"><circle cx="12" cy="8" r="4" stroke="currentColor" strokeWidth="1.5" /><path d="M4 20a8 8 0 0 1 16 0" stroke="currentColor" strokeWidth="1.5" /></svg>
                          </div>
                        )}
                      </div>
                      <div className="truncate text-[10.5px] font-semibold" title={p.name}>{p.name}</div>
                      {p.character && <div className="truncate text-[9.5px] text-ink-faint" title={p.character}>{p.character}</div>}
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* More like this */}
            {similar.length > 0 && (
              <div className="mt-6">
                <h3 className="m-0 mb-2.5 text-[12px] font-bold uppercase tracking-wide text-ink-faint">More like this</h3>
                <div className="thin-scroll flex gap-3 overflow-x-auto pb-2">
                  {similar.map((s) => {
                    const sBadge = badgeFor(s, ctx.isRequested(s));
                    return (
                      <button
                        key={`${s.media_type}:${s.tmdb_id}`}
                        onClick={() => setCurrent(s)}
                        className="group w-[112px] flex-none text-left"
                        aria-label={`View ${s.title}`}
                      >
                        <div className="relative overflow-hidden rounded-lg transition-transform duration-200 group-hover:scale-[1.04]" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}>
                          {s.poster_url ? <img src={posterThumb(s.poster_url)} alt={s.title} className="h-full w-full object-cover" loading="lazy" /> : <PosterPlaceholder title={s.title} year={s.year} />}
                          {sBadge && <StatusChip badge={sBadge} className="absolute right-1 top-1" />}
                        </div>
                        <div className="mt-1.5 truncate text-[11px] font-semibold" style={{ color: "var(--ink)" }} title={s.title}>{s.title}</div>
                        <div className="text-[10px]" style={{ color: "var(--ink-faint)" }}>{s.year || "—"}</div>
                      </button>
                    );
                  })}
                </div>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function CrewFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <div className="font-mono text-[9.5px] uppercase text-ink-faint">{label}</div>
      <div className="truncate text-ink-dim" title={value}>{value}</div>
    </div>
  );
}
