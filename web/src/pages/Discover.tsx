import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { NotificationBell } from "../components/NotificationBell";
import { BooksDiscover } from "./BooksDiscover";
import { useMe, isStaff } from "../lib/me";
import { api, type DiscoverRow, type WatchProvider, type DiscoverCard, type Genre, type MediaDetail, type MediaRequest } from "../lib/api";
import { posterThumb } from "../lib/img";
import { useCanHover } from "../lib/useCanHover";

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
        {/* Tabs + search. On a phone they stack, search on top so the tabs still sit on the
            underline, and the tabs scroll sideways rather than widening the page. */}
        <div className="mb-5 flex flex-col gap-2 border-b sm:flex-row sm:flex-wrap sm:items-center sm:justify-between sm:gap-3" style={{ borderColor: "var(--line)" }}>
          <div className="thin-scroll -mb-px flex min-w-0 gap-1 overflow-x-auto sm:mb-0 sm:overflow-visible">
            {TABS.map((t) => {
              const active = tab === t.key && !search;
              return (
                <button
                  key={t.key}
                  onClick={() => setTab(t.key)}
                  className="relative flex-none px-3 py-2.5 text-[13.5px] font-semibold transition-colors sm:px-4"
                  style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}
                >
                  {t.label}
                  {active && <span className="absolute inset-x-2 bottom-0 h-[2px] rounded-full sm:-bottom-px" style={{ background: "var(--accent)" }} />}
                </button>
              );
            })}
          </div>
          <div className="order-first flex w-full items-center justify-end gap-2 sm:order-none sm:w-auto sm:justify-start">
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
    <div ref={wrap} className="relative min-w-0 flex-1 sm:flex-initial">
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
        className="w-full rounded-lg py-2 pl-8 pr-7 text-[12.5px] transition-[width,box-shadow] sm:w-[210px] sm:focus:w-[300px]"
        style={{ background: "var(--panel-2)", border: `1px solid ${focused ? "var(--accent-line)" : "var(--line)"}`, color: "var(--ink)" }}
      />
      {value && (
        <button onClick={() => { onChange(""); inputRef.current?.focus(); }} className="absolute right-2 top-1/2 z-10 -translate-y-1/2 text-ink-faint hover:text-[var(--ink)]" style={{ fontSize: "13px" }} aria-label="Clear search">✕</button>
      )}

      {open && (
        /* Phone: pinned to the viewport edges (fixed, auto top), the same fix as the
           notification bell, since a 340px panel right-anchored to the box ran off the left
           edge. sm+ keeps the box-anchored dropdown. */
        <div
          className="thin-scroll fixed inset-x-3 z-50 mt-2 max-h-[70vh] overflow-y-auto rounded-xl py-1.5 sm:absolute sm:inset-x-auto sm:right-0 sm:w-[340px]"
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
  // One registry per mount of the tab — switching tabs or entering/clearing a search
  // unmounts this subtree, so the "already shown" set resets with it.
  const registry = useMemo(createRowRegistry, []);
  return (
    <RowRegistryCtx.Provider value={registry}>
      <div className="flex flex-col gap-7">
        {/* Requests first, above everything: admins see everyone's (to act on), everyone
            else their own, each with how far along it is. */}
        <MyRequestsRow flash={ctx.flash} />
        <Hero ctx={ctx} />
        {/* Personalized to the viewer's watch history/requests. Hidden entirely (no header,
            no skeleton) when the backend returns nothing to recommend, or on error. At
            order 0 it claims the top slot so the rows below dedupe against it. */}
        <PosterRow order={0} hideUntilLoaded hideOnError title="Recommended for you" load={() => api.discoverRecommended()} ctx={ctx} />
        <BecauseRows ctx={ctx} firstOrder={1} />
        <PosterRow order={3} title="Trending this week" load={() => api.discoverTrending("all")} ctx={ctx} />
        <PosterRow order={4} title="Popular movies" load={() => api.discoverPopular("movie")} ctx={ctx} />
        <PosterRow order={5} title="Popular series" load={() => api.discoverPopular("series")} ctx={ctx} />
        <PosterRow order={6} hideOnError title="In cinemas now" load={() => api.discoverRow("now_playing")} ctx={ctx} />
        <StreamingRow media="movie" switchable ctx={ctx} />
        <PosterRow order={7} hideUntilLoaded hideOnError excludeOwned title="Finish your collections" load={() => api.discoverCollections()} ctx={ctx} />
        <PosterRow order={8} hideOnError title="Hidden gems" load={() => api.discoverRow("hidden_gems", "movie")} ctx={ctx} />
        <PosterRow order={9} excludeOwned title="Upcoming — request ahead" load={() => api.discoverUpcoming()} ctx={ctx} />
        <GenreExplorer media="movie" switchable ctx={ctx} />
      </div>
    </RowRegistryCtx.Provider>
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

// Order in the requests row: what's moving first, then what's waiting, then what's done.
const STAGE_ORDER: Record<string, number> = {
  downloading: 0, importing: 1, queued: 2, paused: 3, failed: 4, searching: 5, pending: 6,
  partial: 7, available: 8, declined: 9,
};

// MyRequestsRow is the strip of requests, first thing on Discover. Admins see every
// request and approve or decline inline; everyone else sees their own (the server scopes
// the list), each showing how far along it is — searching, downloading with progress,
// importing, ready.
function MyRequestsRow({ flash }: { flash: (m: string) => void }) {
  const { user } = useMe();
  const staff = isStaff(user);
  const admin = !!user && user.role === "admin";
  const [items, setItems] = useState<MediaRequest[] | null>(null);
  const scroller = useRef<HTMLDivElement>(null);

  const load = useCallback(() => api.requests().then((r) => setItems(r.requests)).catch(() => setItems([])), []);
  useEffect(() => {
    load();
    const t = setInterval(load, 8000); // refresh so status/progress advance
    return () => clearInterval(t);
  }, [load]);

  if (!items || items.length === 0) return null;
  const sorted = [...items].sort(
    (a, b) =>
      (STAGE_ORDER[a.tracking?.stage ?? ""] ?? 6) - (STAGE_ORDER[b.tracking?.stage ?? ""] ?? 6) ||
      b.updated_at.localeCompare(a.updated_at),
  );
  const moving = items.filter((rq) => ["downloading", "importing", "queued"].includes(rq.tracking?.stage ?? "")).length;
  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between">
        <h2 className="m-0 text-[15px] font-bold">
          {admin ? "Requests" : "Your requests"}
          {moving > 0 && <span className="ml-2 text-[11.5px] font-medium" style={{ color: "var(--accent)" }}>{moving} on the way</span>}
        </h2>
        <div className="flex gap-1"><ArrowBtn dir={-1} onClick={() => scroll(-1)} /><ArrowBtn dir={1} onClick={() => scroll(1)} /></div>
      </div>
      <div ref={scroller} className="thin-scroll flex gap-3 overflow-x-auto pb-2" style={{ scrollSnapType: "x proximity" }}>
        {sorted.map((rq) => <RequestPoster key={rq.id} rq={rq} staff={staff} own={!!user && rq.requested_by === user.id} onChanged={load} flash={flash} />)}
      </div>
    </div>
  );
}

function RequestPoster({ rq, staff, own, onChanged, flash }: { rq: MediaRequest; staff: boolean; own: boolean; onChanged: () => void; flash: (m: string) => void }) {
  const [busy, setBusy] = useState(false);
  const canHover = useCanHover();
  const tr = rq.tracking;
  const stage = requestStage(rq);
  const status = { label: stage.badge, tone: stage.tone };
  const pct = tr && (tr.stage === "downloading" || tr.stage === "paused") && tr.progress != null ? Math.round(tr.progress * 100) : 0;
  const showBar = tr?.stage === "downloading" || tr?.stage === "paused" || tr?.stage === "importing";
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
  const approve = () => act(() => api.approveRequest(rq.id), "Approved — searching now");
  // Declining notifies the requester and can't be undone from here, so it always asks first.
  const decline = () => {
    if (!window.confirm(`Decline “${rq.title}”${rq.requested_by_name ? ` requested by ${rq.requested_by_name}` : ""}? They’ll be told.`)) return;
    act(() => api.declineRequest(rq.id), "Declined");
  };
  const pending = rq.status === "pending";
  return (
    <div className="w-[150px] flex-none" style={{ scrollSnapAlign: "start" }}>
      <div className="group relative overflow-hidden rounded-xl" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}>
        {rq.poster_url ? (
          <img src={posterThumb(rq.poster_url)} alt={rq.title} className="h-full w-full object-cover" loading="lazy" decoding="async" />
        ) : (
          <PosterPlaceholder title={rq.title} year={rq.year} />
        )}
        {/* One flex row, not two independent absolutes: the requester chip and the status
            badge used to be positioned left and right on a 150px card, so a long username
            painted straight over the badge ("AVAILABLE" read as "ABLE"). Now the name
            shrinks and truncates while the badge always keeps its full width. */}
        <div className="absolute inset-x-1.5 top-1.5 z-10 flex items-start justify-between gap-1">
          {/* Who asked for it — staff only, and always visible (not hover-gated), since staff
              see everyone's requests and "whose is this?" is the first question. */}
          {staff && rq.requested_by_name ? (
            <span
              className="flex min-w-0 items-center gap-1 rounded-full py-[2px] pl-[2px] pr-1.5"
              style={{ background: BADGE_BG, border: "1px solid rgba(255,255,255,.18)" }}
              title={`Requested by ${rq.requested_by_name}`}
            >
              <span className="grid h-[14px] w-[14px] flex-none place-items-center rounded-full text-[8px] font-bold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
                {rq.requested_by_name[0]?.toUpperCase()}
              </span>
              <span className="truncate text-[9px] font-semibold text-white">{rq.requested_by_name}</span>
            </span>
          ) : (
            <span />
          )}
          <span className="flex-none rounded-full px-2 py-0.5 text-[9px] font-bold uppercase tracking-wide" style={{ background: BADGE_BG, color: status.tone, border: `1px solid ${status.tone}` }}>{status.label}</span>
        </div>
        {/* Progress along the bottom while it's on its way; importing fills the bar. */}
        {showBar && (
          <div className="absolute inset-x-0 bottom-0 z-10 h-1.5" style={{ background: "rgba(20,12,7,.55)" }}>
            <div
              className={`h-full ${tr?.stage === "importing" ? "animate-pulse" : ""}`}
              style={{ width: `${tr?.stage === "importing" ? 100 : Math.max(2, pct)}%`, background: tr?.stage === "paused" ? "var(--ink-faint)" : stage.tone }}
            />
          </div>
        )}
        {/* Hover strip with the actions, mouse only. It ignores the pointer until hover or
            keyboard focus reveals it: an invisible strip that still took taps let a phone
            approve or decline by accident. Touch gets the visible row under the caption. */}
        {canHover && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 flex flex-col gap-1.5 p-2 opacity-0 transition-opacity group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.92), transparent)" }}>
            <div className="truncate text-[11.5px] font-semibold text-white">{rq.title}</div>
            {staff && pending ? (
              <div className="flex gap-1.5">
                <button disabled={busy} onClick={approve} className="flex-1 rounded px-2 py-1 text-[10px] font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Approve</button>
                <button disabled={busy} onClick={decline} className="flex-1 rounded px-2 py-1 text-[10px] font-semibold" style={{ background: "rgba(255,255,255,.15)", color: "#fff" }}>Decline</button>
                {own && <button disabled={busy} onClick={withdraw} title="Withdraw your request" className="w-6 flex-none rounded px-0 py-1 text-[10px] font-semibold" style={{ background: "rgba(255,255,255,.15)", color: "#fff" }}>✕</button>}
              </div>
            ) : own && pending ? (
              <div className="flex items-center justify-between gap-1.5">
                <span className="text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>{rq.year || ""}</span>
                <button disabled={busy} onClick={withdraw} className="rounded px-2 py-1 text-[10px] font-semibold" style={{ background: "rgba(255,255,255,.15)", color: "#fff" }}>✕ Withdraw</button>
              </div>
            ) : (
              <div className="text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>{rq.year || ""}</div>
            )}
          </div>
        )}
      </div>
      {/* Always-visible caption: the title, and where it's got to in plain words. */}
      <div className="px-0.5 pt-2">
        <div className="truncate text-[12px] font-semibold" style={{ color: "var(--ink)" }} title={rq.title}>{rq.title}</div>
        <div className="mt-0.5 truncate text-[11px]" style={{ color: stage.detailTone ?? "var(--ink-faint)" }} title={stage.detail}>
          {stage.detail}
        </div>
        {/* Touch has no hover, so the same actions sit here in plain sight. */}
        {!canHover && pending && (staff || own) && (
          <div className="mt-1.5 flex gap-1.5">
            {staff && <button disabled={busy} onClick={approve} className="min-h-[32px] flex-1 rounded-md px-2 text-[11px] font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Approve</button>}
            {staff && <button disabled={busy} onClick={decline} className="min-h-[32px] flex-1 rounded-md px-2 text-[11px] font-semibold" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Decline</button>}
            {own && (
              <button disabled={busy} onClick={withdraw} title="Withdraw your request" className={`min-h-[32px] rounded-md px-2 text-[11px] font-semibold ${staff ? "w-8 flex-none" : "flex-1"}`} style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
                {staff ? "✕" : "Withdraw"}
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// requestStage turns a request's tracking into its badge and a one-line detail.
function requestStage(rq: MediaRequest): { badge: string; tone: string; detail: string; detailTone?: string } {
  const tr = rq.tracking;
  const ready = rq.media_type === "book" ? "Ready" : "Ready to watch";
  const eps = tr?.total ? `${tr.have ?? 0} of ${tr.total} episodes` : "";
  const pct = Math.round((tr?.progress ?? 0) * 100);
  switch (tr?.stage) {
    case "available":
      return { badge: "Ready", tone: "var(--good)", detail: ready, detailTone: "var(--good)" };
    case "partial":
      return { badge: "Partly ready", tone: "var(--good)", detail: `${eps} ready`, detailTone: "var(--good)" };
    case "downloading": {
      const parts = [`${pct}%`];
      if (tr.eta_seconds) parts.push(`${etaText(tr.eta_seconds)} left`);
      else if (tr.note) parts.push(tr.note.toLowerCase());
      if (eps) parts.push(eps);
      return { badge: "Downloading", tone: "var(--accent)", detail: parts.join(" · "), detailTone: "var(--accent)" };
    }
    case "importing":
      return { badge: "Importing", tone: "var(--accent)", detail: "Adding to the library…", detailTone: "var(--accent)" };
    case "queued":
      return { badge: "Starting", tone: "var(--accent)", detail: tr.note || "Starting the download" };
    case "paused":
      return { badge: "Paused", tone: "var(--ink-faint)", detail: `Paused at ${pct}%` };
    case "failed":
      return { badge: "Retrying", tone: "var(--avoid)", detail: tr.note || "The download failed" };
    case "searching":
      return { badge: "Searching", tone: "var(--accent)", detail: tr.note || (eps ? `${eps} · looking for more` : "Looking for a release") };
    case "declined":
      return { badge: "Declined", tone: "var(--reject)", detail: "Declined" };
    case "pending":
      return { badge: "Pending", tone: "var(--avoid)", detail: "Waiting for approval" };
  }
  // No tracking (an older server): fall back to the plain status.
  return rq.available ? { badge: "Ready", tone: "var(--good)", detail: ready }
    : rq.status === "declined" ? { badge: "Declined", tone: "var(--reject)", detail: "Declined" }
    : rq.status === "approved" ? { badge: "Requested", tone: "var(--accent)", detail: "Looking for a release" }
    : { badge: "Pending", tone: "var(--avoid)", detail: "Waiting for approval" };
}

function etaText(sec: number): string {
  if (sec < 60) return "under a minute";
  const m = Math.round(sec / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return h < 24 ? `${h}h ${m % 60}m` : `${Math.floor(h / 24)}d ${h % 24}h`;
}

// BecauseRows are the per-title strips ("Because you watched Silo"): the viewer's two
// most recent titles, each with its own recommendations. Nothing renders until the
// rows arrive, and none when there's no history to build them from.
function BecauseRows({ ctx, firstOrder }: { ctx: RowCtx; firstOrder: number }) {
  const [rows, setRows] = useState<DiscoverRow[] | null>(null);
  useEffect(() => {
    let alive = true;
    api.discoverBecause().then((r) => { if (alive) setRows(r); }).catch(() => { if (alive) setRows([]); });
    return () => { alive = false; };
  }, []);
  if (!rows || rows.length === 0) return null;
  return (
    <>
      {rows.map((r, i) => (
        <PosterRow key={r.seed} order={firstOrder + i} title={r.title} load={() => Promise.resolve(r.items)} ctx={ctx} />
      ))}
    </>
  );
}

// StreamingRow: "New on <service>", one strip with a chip per streaming service in the
// region (from TMDB's provider list). One request per chip, cached like every row.
function StreamingRow({ media, switchable, ctx }: { media: "movie" | "series"; switchable?: boolean; ctx: RowCtx }) {
  const [m, setM] = useState<"movie" | "series">(media);
  const [providers, setProviders] = useState<WatchProvider[] | null>(null);
  const [active, setActive] = useState<WatchProvider | null>(null);
  useEffect(() => { setM(media); }, [media]);
  useEffect(() => {
    let alive = true;
    setProviders(null); setActive(null);
    api.discoverProviders(m).then((p) => { if (alive) { setProviders(p); setActive(p[0] ?? null); } }).catch(() => { if (alive) setProviders([]); });
    return () => { alive = false; };
  }, [m]);
  if (providers && providers.length === 0) return null;
  return (
    <div>
      <div className="mb-2.5 flex flex-wrap items-center justify-between gap-2">
        <h2 className="m-0 text-[15px] font-bold">New on streaming</h2>
        <div className="flex flex-wrap items-center gap-1.5">
          {switchable && (
            <div className="mr-2 flex gap-1">
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
          {(providers ?? []).map((p) => {
            const on = active?.id === p.id;
            return (
              <button key={p.id} onClick={() => setActive(p)} title={p.name} className="flex items-center gap-1.5 rounded-full py-1 pl-1 pr-2.5 text-[12px] font-semibold" style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-faint)" }}>
                {p.logo_url ? <img src={p.logo_url} alt="" className="h-5 w-5 rounded-md" /> : null}
                <span>{p.name}</span>
              </button>
            );
          })}
        </div>
      </div>
      {active ? (
        <PosterRow key={`${m}:${active.id}`} hideOnError title="" load={() => api.discoverProviderNew(m, active.id)} ctx={ctx} />
      ) : (
        <div className="flex gap-3 overflow-hidden pb-2">{Array.from({ length: 8 }).map((_, i) => <CardSkeleton key={i} />)}</div>
      )}
    </div>
  );
}

function BrowseTab({ media, ctx }: { media: "movie" | "series"; ctx: RowCtx }) {
  // Keyed on media so Movies and Series each dedupe against their own rows only.
  const registry = useMemo(createRowRegistry, [media]);
  return (
    <RowRegistryCtx.Provider value={registry}>
      <div key={media} className="flex flex-col gap-7">
        <PosterRow order={0} title={`Trending ${media === "movie" ? "movies" : "series"}`} load={() => api.discoverTrending(media)} ctx={ctx} />
        <PosterRow order={1} title={`Popular ${media === "movie" ? "movies" : "series"}`} load={() => api.discoverPopular(media)} ctx={ctx} />
        <PosterRow order={2} hideOnError title={`Top rated ${media === "movie" ? "movies" : "series"}`} load={() => api.discoverRow("top_rated", media)} ctx={ctx} />
        {media === "movie" ? (
          <PosterRow order={3} hideOnError title="In cinemas now" load={() => api.discoverRow("now_playing")} ctx={ctx} />
        ) : (
          <PosterRow order={3} hideOnError title="Anime" load={() => api.discoverRow("anime")} ctx={ctx} />
        )}
        <StreamingRow media={media} ctx={ctx} />
        <PosterRow order={4} hideOnError title="Hidden gems" load={() => api.discoverRow("hidden_gems", media)} ctx={ctx} />
        <PosterRow order={5} hideUntilLoaded hideOnError title="From your region" load={() => api.discoverRow("region", media)} ctx={ctx} />
        {media === "movie" ? (
          <PosterRow order={6} excludeOwned title="Upcoming — request ahead" load={() => api.discoverUpcoming()} ctx={ctx} />
        ) : (
          // Series upcoming ships behind a backend change; if it isn't live the row
          // hides itself rather than showing an error on an otherwise healthy tab.
          <PosterRow order={6} excludeOwned hideOnError title="Airing soon" load={() => api.discoverUpcoming("series")} ctx={ctx} />
        )}
        <GenreExplorer media={media} ctx={ctx} />
      </div>
    </RowRegistryCtx.Provider>
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

// ---- Cross-row de-duplication ----------------------------------------------
//
// TMDB's trending/popular/upcoming lists overlap heavily, so the same four titles
// used to fill every row on a tab. Each row is given an `order` (its render
// position); a row drops anything already claimed by a row ABOVE it and claims
// what it keeps.
//
// Why this can't loop: claims only ever propagate DOWNWARD. `claim(order, …)`
// notifies subscribers with a strictly greater order, so the notification chain
// is a DAG over a finite, strictly increasing ordinal — it terminates after at
// most one pass per row. A row's own state update never re-runs its own load or
// subscribe effects (their deps are `[order, registry]`, both stable for the life
// of the tab), so a re-filter can't retrigger a fetch or a sibling's effect. The
// notify is additionally skipped when a row's claim set is unchanged, so the 30s
// enrichment refresh normally causes no downstream work at all.
const MIN_ROW_ITEMS = 4; // below this, a repeated title beats a dead row
const cardKey = (c: DiscoverCard) => `${c.media_type}:${c.tmdb_id}`;

interface RowRegistry {
  claim: (order: number, items: DiscoverCard[]) => DiscoverCard[];
  subscribe: (order: number, fn: () => void) => () => void;
  release: (order: number) => void;
}

function createRowRegistry(): RowRegistry {
  const claims = new Map<number, Set<string>>();
  const subs = new Map<number, () => void>();
  return {
    claim(order, items) {
      const taken = new Set<string>();
      claims.forEach((keys, o) => { if (o < order) keys.forEach((k) => taken.add(k)); });
      const filtered = items.filter((c) => !taken.has(cardKey(c)));
      const kept = filtered.length >= MIN_ROW_ITEMS ? filtered : items;
      const next = new Set(kept.map(cardKey));
      const prev = claims.get(order);
      const changed = !prev || prev.size !== next.size || [...next].some((k) => !prev.has(k));
      claims.set(order, next);
      if (changed) subs.forEach((fn, o) => { if (o > order) fn(); });
      return kept;
    },
    subscribe(order, fn) { subs.set(order, fn); return () => { subs.delete(order); }; },
    release(order) { claims.delete(order); subs.delete(order); },
  };
}

// Default is a no-op registry so a PosterRow rendered outside a provider (or with
// no `order`) behaves exactly as before.
const RowRegistryCtx = createContext<RowRegistry>({ claim: (_o, items) => items, subscribe: () => () => {}, release: () => {} });

function PosterRow({ title, load, ctx, order, excludeOwned, hideOnError, hideUntilLoaded }: {
  title: string;
  load: () => Promise<DiscoverCard[]>;
  ctx: RowCtx;
  /** Render position on the tab — rows dedupe against every lower order. Omit to opt out. */
  order?: number;
  /** Upcoming rows: don't invite a request for something already in the library. */
  excludeOwned?: boolean;
  /** Row quietly disappears if the endpoint isn't available (e.g. not deployed yet). */
  hideOnError?: boolean;
  /** Render nothing (no header, no skeleton) until the first load resolves — then show
      the row only if it has items. Prevents a header flashing in for a row that ends up
      empty (e.g. "Recommended for you" for a user with no history). */
  hideUntilLoaded?: boolean;
}) {
  const registry = useContext(RowRegistryCtx);
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  // Last raw server payload, kept so the row can re-filter without re-fetching.
  const raw = useRef<DiscoverCard[] | null>(null);
  const loadRef = useRef(load); loadRef.current = load;

  const recompute = useCallback(() => {
    const r = raw.current;
    if (!r) return;
    let pool = r;
    if (excludeOwned) {
      const fresh = r.filter((c) => !c.has_file && !c.in_library);
      pool = fresh.length >= MIN_ROW_ITEMS ? fresh : r;
    }
    setItems(order == null ? pool : registry.claim(order, pool));
  }, [excludeOwned, order, registry]);
  const recomputeRef = useRef(recompute); recomputeRef.current = recompute;

  useEffect(() => {
    if (order == null) return;
    const off = registry.subscribe(order, () => recomputeRef.current());
    return () => { off(); registry.release(order); };
  }, [order, registry]);

  useEffect(() => {
    let alive = true;
    const pull = (first: boolean) => loadRef.current()
      .then((r) => { if (!alive) return; raw.current = r; setError(null); recomputeRef.current(); })
      .catch((e) => { if (!alive || !first) return; raw.current = []; setItems([]); setError((e as Error).message); });
    pull(true);
    // Re-pull enrichment (badges, download progress) every 30s while the tab is
    // visible; refresh failures keep the last good data.
    const t = setInterval(() => {
      if (document.visibilityState !== "visible") return;
      pull(false);
    }, 30000);
    return () => { alive = false; clearInterval(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });

  if (error && hideOnError) return null;
  if (hideUntilLoaded && items === null) return null;
  if (items && items.length === 0 && !error) return null;
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between">
        {title ? <h2 className="m-0 text-[15px] font-bold">{title}</h2> : <span />}
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
  const canHover = useCanHover();
  const requested = ctx.isRequested(c);
  const badge = badgeFor(c, requested);
  const requestable = !badge; // no badge → nothing in library/queue yet
  // The quick-request button only exists where a mouse can reveal it first. On touch
  // the whole poster opens the sheet, which has its own visible Request button.
  const showQuick = canHover && ctx.canRequest && requestable;

  // Quick-request straight from the hover overlay — same honest flow as the modal:
  // doRequest toasts success/failure and only marks requested on success.
  const quick = async () => {
    if (quickBusy) return;
    setQuickBusy(true);
    try { await ctx.doRequest(c); } catch { /* toast already shown */ }
    finally { setQuickBusy(false); }
  };

  return (
    <div className={`group ${full ? "w-full" : "w-[150px] flex-none"}`} style={{ scrollSnapAlign: "start" }}>
      <div
        className="relative overflow-hidden rounded-xl transition-[transform,box-shadow] duration-200 will-change-transform group-hover:-translate-y-1 group-hover:scale-[1.03] group-hover:shadow-[0_12px_30px_rgba(0,0,0,0.45)]"
        style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}
      >
        <button
          onClick={() => setOpen(true)}
          aria-label={`View details for ${c.title}`}
          className="absolute inset-0 block h-full w-full text-left"
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
          {/* Hover scrim, purely decorative: pointer-events-none so a tap anywhere on the
              poster opens the sheet, never something the user couldn't see. */}
          <div className="pointer-events-none absolute inset-0 flex flex-col justify-end p-2 opacity-0 transition-opacity group-hover:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.82) 0%, rgba(0,0,0,.15) 42%, transparent 70%)" }}>
            {!showQuick && (
              <span className="self-start rounded-md px-2.5 py-1 text-[10.5px] font-semibold" style={{ background: "rgba(255,255,255,.16)", border: "1px solid rgba(255,255,255,.26)", color: "#fff" }}>Details</span>
            )}
          </div>
        </button>
        {/* A sibling of the details button, not nested in it: a control inside a button is
            invalid HTML, and its invisible hit area filed requests from stray taps. It only
            takes clicks once hover or keyboard focus has revealed it. */}
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
      {/* Always-visible caption strip so cards read before hover (Plex style). */}
      <div className="px-0.5 pt-2">
        <div className="truncate text-[12px] font-semibold" style={{ color: "var(--ink)" }} title={c.title}>{c.title}</div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[11px]" style={{ color: "var(--ink-faint)" }}>
          <span>{c.year || "—"}</span>
          {c.vote_average > 0 && <><span>·</span><span style={{ color: "var(--accent)" }}>★ {c.vote_average.toFixed(1)}</span></>}
        </div>
        {c.genres && c.genres.length > 0 && (
          <div className="mt-0.5 truncate text-[10px]" style={{ color: "var(--ink-faint)" }} title={c.genres.join(" · ")}>{c.genres.slice(0, 2).join(" · ")}</div>
        )}
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
      className="fixed inset-0 z-50 flex justify-center overflow-hidden sm:items-center sm:p-6"
      style={{ background: "rgba(0,0,0,.68)" }}
      onClick={onClose}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={c.title}
        tabIndex={-1}
        className="relative flex h-full w-full flex-col overflow-hidden outline-none sm:h-auto sm:max-h-[92vh] sm:max-w-[820px] sm:rounded-2xl sm:shadow-2xl"
        style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Close sits on the dialog, not the backdrop, so it stays put while the body scrolls. */}
        <button onClick={onClose} aria-label="Close" className="absolute right-3 top-3 z-20 grid h-8 w-8 place-items-center rounded-full" style={{ background: "rgba(20,12,7,.7)", color: "#fff" }}>✕</button>

        {/* The backdrop lives INSIDE the scroller: the poster below insets over it with a
            negative margin, and an overflow container clips negative margins — with the
            backdrop outside, the poster's overlap was sliced off and it butted against the
            image edge instead of floating over it. */}
        <div ref={scrollRef} className="flex-1 overflow-y-auto">
          <div className="relative h-[190px] sm:h-[260px]" style={{ background: "var(--panel-2)" }}>
            {(d?.backdrop_url || c.backdrop_url) && <img src={d?.backdrop_url || c.backdrop_url} alt="" className="h-full w-full object-cover opacity-60" />}
            <div className="absolute inset-0" style={{ background: "linear-gradient(to top, var(--panel) 4%, transparent 78%)" }} />
          </div>

          <div className="relative flex gap-4 px-5 pt-0 sm:px-6">
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
                    <div key={i} className="w-[96px] flex-none text-center">
                      <div className="mb-1 overflow-hidden rounded-lg" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }}>
                        {p.profile_url ? <img src={p.profile_url} alt={p.name} className="h-full w-full object-cover" loading="lazy" /> : (
                          <div className="grid h-full w-full place-items-center" style={{ color: "var(--ink-faint)" }}>
                            <svg width="20" height="20" viewBox="0 0 24 24" fill="none"><circle cx="12" cy="8" r="4" stroke="currentColor" strokeWidth="1.5" /><path d="M4 20a8 8 0 0 1 16 0" stroke="currentColor" strokeWidth="1.5" /></svg>
                          </div>
                        )}
                      </div>
                      {/* Two lines instead of a hard truncate: "Arnold Schwa…" and
                          "James Earl Jo…" cut mid-word at the old width. */}
                      <div className="line-clamp-2 text-[10.5px] font-semibold leading-tight" title={p.name}>{p.name}</div>
                      {p.character && <div className="mt-0.5 line-clamp-1 text-[9.5px] text-ink-faint" title={p.character}>{p.character}</div>}
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
