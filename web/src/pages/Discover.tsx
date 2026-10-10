import { createContext, Suspense, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { Link, Outlet, useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { MetadataMissing } from "../components/MetadataMissing";
import { lazyPage } from "../lib/lazyPage";
import { pickTab, withTab } from "../lib/useTabParam";
import { TabPanel, Tabs } from "../ui/Tabs";
import { useMe, isStaff } from "../lib/me";
import { api, type DiscoverRow, type WatchProvider, type DiscoverCard, type Genre, type MediaRequest } from "../lib/api";
import { posterThumb } from "../lib/img";
import { formatSeasons, MOVING_STAGES, requestStage, sortForRequester } from "../lib/requestStage";
import { usePoll } from "../lib/usePoll";
import { announceRequested } from "../lib/pushPrompt";
import { IconButton, StatusChip, POSTER_CHIP_BG, TONE_HUE, useToast } from "../ui";
import { badgeFor, CardSkeleton, discoverPlace, FOLLOWING, GRID, LoadError, MediaCard, PosterPlaceholder, useOpenTitle, type ReqStatus, type RowCtx } from "./discover/shared";

// The Books tab is a separate Open Library experience; its code loads only when chosen.
const BooksDiscover = lazyPage(() => import("./BooksDiscover"), "BooksDiscover");
// The request sheet loads when a request is first opened, not with Discover.
const RequestSheet = lazyPage(() => import("../components/RequestSheet"), "RequestSheet");
// A title's sheet is its own chunk, fetched when the first title opens.
const TitleSheet = lazyPage(() => import("./discover/TitleSheet"), "TitleSheet");
// The browse grid behind "See all" and the paged search results: one chunk, fetched when
// either first shows.
const DiscoverBrowse = lazyPage(() => import("./DiscoverBrowse"), "DiscoverBrowse");
const SearchResults = lazyPage(() => import("./DiscoverBrowse"), "SearchResults");

type Tab = "discover" | "movies" | "series" | "books";
const BASE_TABS: { key: Tab; label: string }[] = [
  { key: "discover", label: "Discover" },
  { key: "movies", label: "Movies" },
  { key: "series", label: "Series" },
];

export function Discover({ chrome = true }: { chrome?: boolean }) {
  const { user, booksEnabled, metadataReady } = useMe();
  // Books get their own tab at the end — a completely separate Open Library experience.
  const TABS = booksEnabled ? [...BASE_TABS, { key: "books" as Tab, label: "Books" }] : BASE_TABS;
  // What this session asked for, by card key, with the status the server answered: an
  // auto-approved request is already approved and searching, not waiting.
  const [requested, setRequested] = useState<Map<string, ReqStatus>>(new Map());
  // The tab and the committed search live in the address (?tab=, ?q=), so Back steps
  // through them, a reload keeps the results, and a notification can link straight to a
  // search. On the Books tab ?q= seeds the book search instead.
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  // Where in Discover this is: the rows or a browse grid, and what's open over it.
  const { pathname } = useLocation();
  const place = discoverPlace(pathname);
  const overlay = place.overlay && place.overlay.id > 0 ? place.overlay : null;
  const tab = pickTab(params.get("tab"), TABS.map((t) => t.key), "discover");
  const q = params.get("q") ?? "";
  // `search` is the committed query that swaps the page to the full results grid (only via
  // "See all" / Enter, not per keystroke); `searchInput` is what's typed in the omnibox.
  const search = tab === "books" ? "" : q;
  const bookSeed = tab === "books" ? q : "";
  // ?work=<key>: one book's sheet on the Books tab (a notification's link); closing it drops the key.
  const work = tab === "books" ? params.get("work") ?? "" : "";
  const closeWork = () => setParams((p) => { const next = new URLSearchParams(p); next.delete("work"); return next; }, { replace: true });
  const [searchInput, setSearchInput] = useState(search);
  // Back, Forward or a notification link changed the committed search: show it in the box.
  useEffect(() => { setSearchInput(search); }, [search]);
  const flash = useToast();
  // Readonly users can browse but never request.
  const canRequest = !!user && user.role !== "readonly";

  // Choosing a tab (even the current one, while results are showing) leaves the search.
  const setTab = (t: Tab) => {
    setSearchInput("");
    // From a browse grid, a tab goes back to the rows (the grid's filters stay behind).
    if (place.browse) { navigate(t === "discover" ? "/discover" : `/discover?tab=${t}`); return; }
    if (t === tab && !q) return;
    setParams((p) => { const next = withTab(p, "tab", t, "discover"); next.delete("q"); next.delete("work"); return next; });
  };
  // Committing a search is a new history entry; emptying the box just drops it.
  const commitSearch = (query: string) => {
    setSearchInput(query);
    if (place.browse) { navigate(`/discover?q=${encodeURIComponent(query)}`); return; }
    if (query === q) return;
    setParams((p) => { const next = new URLSearchParams(p); next.set("q", query); return next; });
  };
  const onSearchChange = (v: string) => {
    setSearchInput(v);
    if (!v.trim() && q) setParams((p) => { const next = new URLSearchParams(p); next.delete("q"); return next; }, { replace: true });
  };

  // Rethrows on failure so callers (modal, quick-request) only flip to their success
  // state on an actual success. subscribed=true → you joined an existing request.
  // seasons (a show): the season numbers asked for; absent or null is the whole show.
  const doRequest = useCallback(async (c: DiscoverCard, note?: string, seasons?: number[] | null): Promise<{ subscribed: boolean; status: ReqStatus }> => {
    const key = `${c.media_type}:${c.tmdb_id}`;
    try {
      const res = await api.createRequest({ media_type: c.media_type, tmdb_id: c.tmdb_id, title: c.title, year: c.year, poster_url: c.poster_url, overview: c.overview, note: note?.trim() || undefined, seasons: seasons?.length ? seasons : undefined });
      const status = res.request.status;
      setRequested((m) => new Map(m).set(key, status));
      flash(res.subscribed ? FOLLOWING : requestedMessage(c.title, status, res.request.seasons));
      announceRequested();
      return { subscribed: res.subscribed, status };
    } catch (e) {
      flash((e as Error).message, { tone: "error" });
      throw e;
    }
  }, [flash]);
  const isRequested = useCallback((c: DiscoverCard) => requested.get(`${c.media_type}:${c.tmdb_id}`), [requested]);

  const ctx: RowCtx = { doRequest, isRequested, canRequest, flash };

  // A title's own address (/discover/movie/603) opens its sheet over the page. Discover
  // stays mounted underneath, so its rows, what loaded and the scroll position survive,
  // and Back from the title lands right where you were.

  return (
    <>
      {chrome && <PageHeader title="Discover" />}
      <div className="mx-auto w-full max-w-[1600px] px-4 py-5 sm:px-6">
        {/* Tabs + search. On a phone they stack, search on top so the tabs still sit on the
            underline, and the tabs scroll sideways rather than widening the page. Search comes
            first in the DOM as well, so focus order matches what a phone shows; sm:order-last
            puts it back after the tabs on wider screens. */}
        <div className="mb-5 flex flex-col gap-2 border-b sm:flex-row sm:flex-wrap sm:items-center sm:justify-between sm:gap-3" style={{ borderColor: "var(--line)" }}>
          {/* Books have their own search inside BooksDiscover — hide the movie/TV one there.
              The bell lives in the layout's top bar, on every page. */}
          {tab !== "books" && (
            <div className="flex w-full items-center justify-end gap-2 sm:order-last sm:w-auto sm:justify-start">
              {metadataReady ? (
                <SearchBox value={searchInput} onChange={onSearchChange} onSeeAll={commitSearch} />
              ) : (
                <input disabled placeholder="Search isn't available yet" aria-label="Search movies and TV (not available yet)" className="min-w-0 flex-1 rounded-lg px-3 py-2 text-[12.5px] opacity-60 sm:w-[210px] sm:flex-initial" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
              )}
            </div>
          )}
          {/* No tab is current while search results are showing. */}
          <Tabs tabs={TABS} value={search || place.browse ? null : tab} onChange={setTab} idPrefix="discover" label="Discover sections" className="-mb-px" />
        </div>

        <TabPanel idPrefix="discover" value={tab}>
          {tab === "books" ? (
            <Suspense fallback={<div className="py-10 text-center text-[12.5px] text-ink-dim">Loading…</div>}>
              <BooksDiscover flash={flash} canRequest={canRequest} initialQuery={bookSeed} initialWork={work || undefined} onWorkClosed={closeWork} />
            </Suspense>
          ) : !metadataReady ? (
            // No TMDB key: every movie/TV feed would fail on its own and repeat the same error
            // row after row. Show the viewer's requests (they don't need TMDB) and one message
            // worded for their role instead. Books use Open Library and are unaffected.
            <div className="flex flex-col gap-7">
              <MyRequestsRow />
              <MetadataMissing variant="empty" />
            </div>
          ) : place.browse ? (
            <Suspense fallback={gridLoading}><DiscoverBrowse ctx={ctx} /></Suspense>
          ) : search ? (
            <Suspense fallback={gridLoading}><SearchResults query={search} ctx={ctx} /></Suspense>
          ) : (
            <>
              {tab === "discover" && <DiscoverTab ctx={ctx} />}
              {tab === "movies" && <BrowseTab media="movie" ctx={ctx} />}
              {tab === "series" && <BrowseTab media="series" ctx={ctx} />}
            </>
          )}
        </TabPanel>
      </div>
      {(overlay?.kind === "movie" || overlay?.kind === "series") && (
        <Suspense fallback={null}><TitleSheet media={overlay.kind} tmdbId={overlay.id} ctx={ctx} /></Suspense>
      )}
      {/* The old /discover/tv/<id> address redirects from here (lib/routes). */}
      <Outlet />
    </>
  );
}

const gridLoading = <div className="py-10 text-center text-[12.5px] text-ink-dim">Loading…</div>;

// requestedMessage is what asking for a title says: an auto-approved request is already
// being searched for, anything else waits for someone to approve it.
// A show asked for by season names them: Requested “Severance” S2 — waiting for approval.
function requestedMessage(title: string, status: ReqStatus, seasons?: number[]): string {
  const what = `Requested “${title}”${seasons?.length ? ` ${formatSeasons(seasons)}` : ""}`;
  return status === "approved" ? `${what} — searching now` : `${what} — waiting for approval`;
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

function SearchBox({ value, onChange, onSeeAll }: { value: string; onChange: (v: string) => void; onSeeAll: (q: string) => void }) {
  const [focused, setFocused] = useState(false);
  const [results, setResults] = useState<DiscoverCard[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [highlight, setHighlight] = useState(-1);
  const [recent, setRecent] = useState<string[]>(loadRecent);
  const openTitle = useOpenTitle();
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
    setFocused(false);
    openTitle(c);
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
        const badge = badgeFor(c);
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
            {badge && <StatusChip tone={badge.tone} surface="poster" size="xs" className="flex-none">{badge.label}</StatusChip>}
          </button>
        );
      })}
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
        <MyRequestsRow />
        {/* What the viewer asked for that has arrived: the payoff, with Watch on Plex. */}
        <ReadyForYouRow />
        <Hero ctx={ctx} />
        {/* What just arrived in the library, for everyone. It doesn't claim cards from the
            rows below: those are things to request, this is what's already here. */}
        <PosterRow hideUntilLoaded hideOnError title="Recently added" load={() => api.discoverRecentlyAdded()} ctx={ctx} />
        {/* Personalized to the viewer's watch history/requests. Hidden entirely (no header,
            no skeleton) when the backend returns nothing to recommend, or on error. At
            order 0 it claims the top slot so the rows below dedupe against it. */}
        <PosterRow order={0} hideUntilLoaded hideOnError title="Recommended for you" load={() => api.discoverRecommended()} ctx={ctx} />
        <BecauseRows ctx={ctx} firstOrder={1} />
        <PosterRow order={3} title="Trending this week" seeAll={browseLink("trending", "all")} load={() => api.discoverTrending("all")} ctx={ctx} />
        <PosterRow order={4} title="Popular movies" seeAll={browseLink("popular", "movie")} load={() => api.discoverPopular("movie")} ctx={ctx} />
        <PosterRow order={5} title="Popular series" seeAll={browseLink("popular", "series")} load={() => api.discoverPopular("series")} ctx={ctx} />
        <PosterRow order={6} hideOnError title="In cinemas now" seeAll={browseLink("now_playing", "movie")} load={() => api.discoverRow("now_playing")} ctx={ctx} />
        <StreamingRow media="movie" switchable ctx={ctx} />
        <PosterRow order={7} hideUntilLoaded hideOnError excludeOwned title="Finish your collections" load={() => api.discoverCollections()} ctx={ctx} />
        <PosterRow order={8} hideOnError title="Hidden gems" seeAll={browseLink("hidden_gems", "movie")} load={() => api.discoverRow("hidden_gems", "movie")} ctx={ctx} />
        <PosterRow order={9} excludeOwned title="Upcoming — request ahead" seeAll={browseLink("upcoming", "movie")} load={() => api.discoverUpcoming()} ctx={ctx} />
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
  const [quickBusy, setQuickBusy] = useState(false);
  const openTitle = useOpenTitle();

  useEffect(() => {
    let alive = true;
    api.discoverTrending("all")
      .then((r) => { if (alive) setItems(r.filter((x) => !!x.backdrop_url).slice(0, 5)); })
      .catch(() => { if (alive) setItems([]); });
    return () => { alive = false; };
  }, []);

  const count = items?.length ?? 0;
  usePoll(() => setIdx((i) => (i + 1) % count), paused || count <= 1 ? null : 7000, { immediate: false });
  useEffect(() => { if (count > 0 && idx >= count) setIdx(0); }, [idx, count]);

  if (items === null) return <div className="w-full animate-pulse rounded-2xl" style={{ height: "clamp(340px, 46vh, 560px)", background: "var(--panel-2)", border: "1px solid var(--line)" }} />;
  if (count === 0) return null;

  const cur = items[Math.min(idx, count - 1)];
  const requested = ctx.isRequested(cur);
  const badge = badgeFor(cur, requested);
  const requestable = !badge;

  const quick = async () => {
    // A show is never asked for whole in one click: the sheet asks which seasons.
    if (cur.media_type === "series") { openTitle(cur, true); return; }
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
          <StatusChip tone={badge.tone} surface="poster" size="md" className="w-fit">{badge.label}</StatusChip>
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
            onClick={() => openTitle(cur)}
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
    </div>
  );
}

// MyRequestsRow is the strip of requests, first thing on Discover: a capped view of the
// Requests page. Staff see what's waiting for them first (oldest first), then what's on its
// way; everyone else their own and followed requests, what's moving first. Each poster is
// one button that opens the RequestSheet — no action happens from a tap on the strip.
function MyRequestsRow() {
  const { user } = useMe();
  const staff = isStaff(user);
  const [items, setItems] = useState<MediaRequest[] | null>(null);
  const [waiting, setWaiting] = useState(0);

  // false while downloads can't be checked: a request's progress is then unknown.
  const [queueKnown, setQueueKnown] = useState(true);
  const load = useCallback(() => api.requests({ section: "strip" }).then((r) => {
    setItems(r.requests);
    setWaiting(r.counts?.needs_approval ?? 0);
    setQueueKnown(r.client_health?.ok ?? true);
  }).catch(() => setItems([])), []);
  // Every 8 s while something is downloading or importing, else once a minute; a hidden
  // tab makes no calls (usePoll).
  const moving = (items ?? []).filter((rq) => MOVING_STAGES.has(rq.tracking?.stage ?? "")).length;
  usePoll(load, moving > 0 ? 8000 : 60000);

  if (!items || items.length === 0) return null;
  // Staff keep the server's order: waiting (oldest first), then in progress.
  const sorted = staff ? items : sortForRequester(items);
  const heading = (
    <>
      {staff ? "Requests" : "Your requests"}
      {staff && waiting > 0 && <span className="ml-2 text-[11.5px] font-medium" style={{ color: "var(--avoid-text)" }}>{waiting} waiting</span>}
      {moving > 0 && <span className="ml-2 text-[11.5px] font-medium" style={{ color: "var(--accent)" }}>{moving} on the way</span>}
    </>
  );
  return <RequestRail heading={heading} seeAll={staff && waiting > 0 ? "/requests?tab=needs" : "/requests"} items={sorted} staff={staff} queueKnown={queueKnown} onChanged={load} />;
}

// ReadyForYouRow is what the viewer asked for (or follows) that arrived in the last month,
// newest first, each with Watch on Plex when the owner's Plex has it. Staff see their own
// here too, not everyone's. Nothing renders when nothing is ready.
function ReadyForYouRow() {
  const [items, setItems] = useState<MediaRequest[] | null>(null);
  const load = useCallback(() => api.requests({ section: "ready", ready_within_days: 30, mine: 1, limit: 20 })
    .then((r) => setItems(r.requests)).catch(() => setItems([])), []);
  usePoll(load, 60000);
  if (!items || items.length === 0) return null;
  return <RequestRail heading="Ready for you" seeAll="/requests?tab=ready" seeAllLabel="See all ready requests" items={items} staff={false} onChanged={load} />;
}

// RequestRail is a strip of request posters, each opening its RequestSheet.
function RequestRail({ heading, seeAll, seeAllLabel, items, staff, queueKnown = true, onChanged }: { heading: React.ReactNode; seeAll: string; seeAllLabel?: string; items: MediaRequest[]; staff: boolean; queueKnown?: boolean; onChanged: () => void }) {
  const [openID, setOpenID] = useState<number | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });
  const open = openID ? items.find((rq) => rq.id === openID) : undefined;
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between gap-2">
        <h2 className="m-0 min-w-0 text-[15px] font-bold">{heading}</h2>
        <div className="flex flex-none items-center gap-2">
          <Link to={seeAll} aria-label={seeAllLabel} className="text-[12px] font-semibold" style={{ color: "var(--accent)" }}>See all →</Link>
          <div className="hidden gap-1 sm:flex"><ArrowBtn dir={-1} onClick={() => scroll(-1)} /><ArrowBtn dir={1} onClick={() => scroll(1)} /></div>
        </div>
      </div>
      <div ref={scroller} className="thin-scroll flex gap-3 overflow-x-auto pb-2" style={{ scrollSnapType: "x proximity" }}>
        {items.map((rq) => <RequestPoster key={rq.id} rq={rq} staff={staff} queueKnown={queueKnown} onOpen={() => setOpenID(rq.id)} />)}
      </div>
      {openID !== null && (
        <Suspense fallback={null}>
          <RequestSheet key={openID} requestId={openID} initial={open} onChanged={onChanged} onClose={() => setOpenID(null)} />
        </Suspense>
      )}
    </div>
  );
}

function RequestPoster({ rq, staff, queueKnown = true, onOpen }: { rq: MediaRequest; staff: boolean; queueKnown?: boolean; onOpen: () => void }) {
  const tr = rq.tracking;
  const stage = requestStage(rq, queueKnown);
  const pct = tr && (tr.stage === "downloading" || tr.stage === "paused") && tr.progress != null ? Math.round(tr.progress * 100) : 0;
  const settling = tr?.stage === "importing" || tr?.stage === "adding"; // on disk, nearly there: a full, pulsing bar
  const showBar = !stage.unknown && (tr?.stage === "downloading" || tr?.stage === "paused" || settling);
  const following = rq.relation === "subscriber";
  return (
    <div className="w-[150px] flex-none" style={{ scrollSnapAlign: "start" }}>
      {/* The whole poster is one button: a tap anywhere opens the sheet, where every
          action is a visible, labelled button. Nothing on the strip acts by itself. */}
      <button
        onClick={onOpen}
        aria-label={`Open the request for ${rq.title}`}
        className="relative block w-full overflow-hidden rounded-xl text-left"
        style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}
      >
        {rq.poster_url ? (
          <img src={posterThumb(rq.poster_url)} alt="" className="h-full w-full object-cover" loading="lazy" decoding="async" />
        ) : (
          <PosterPlaceholder title={rq.title} year={rq.year} />
        )}
        {/* One flex row, not two independent absolutes: a long username shrinks and
            truncates while the status badge always keeps its full width. */}
        <span className="absolute inset-x-1.5 top-1.5 z-10 flex items-start justify-between gap-1">
          {/* Who asked for it — staff only, since staff see everyone's requests and
              "whose is this?" is the first question. */}
          {staff && rq.requested_by_name ? (
            <span
              className="flex min-w-0 items-center gap-1 rounded-full py-[2px] pl-[2px] pr-1.5"
              style={{ background: POSTER_CHIP_BG, border: "1px solid rgba(255,255,255,.18)" }}
              title={`Requested by ${rq.requested_by_name}`}
            >
              <span className="grid h-[14px] w-[14px] flex-none place-items-center rounded-full text-[8px] font-bold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
                {rq.requested_by_name[0]?.toUpperCase()}
              </span>
              <span className="truncate text-[9px] font-semibold text-white">{rq.requested_by_name}</span>
            </span>
          ) : following ? (
            <StatusChip tone="faint" surface="poster" className="min-w-0">Following</StatusChip>
          ) : (
            <span />
          )}
          <StatusChip tone={stage.tone} surface="poster" className="flex-none">{stage.badge}</StatusChip>
        </span>
        {/* Progress along the bottom while it's on its way; importing fills the bar. */}
        {showBar && (
          <span className="absolute inset-x-0 bottom-0 z-10 block h-1.5" style={{ background: "rgba(20,12,7,.55)" }}>
            <span
              className={`block h-full ${settling ? "animate-pulse" : ""}`}
              style={{ width: `${settling ? 100 : Math.max(2, pct)}%`, background: tr?.stage === "paused" ? "var(--ink-faint)" : TONE_HUE[stage.tone] }}
            />
          </span>
        )}
      </button>
      {/* Always-visible caption: the title, and where it's got to in plain words. */}
      <div className="px-0.5 pt-2">
        <div className="truncate text-[12px] font-semibold" style={{ color: "var(--ink)" }} title={rq.title}>{rq.title}</div>
        <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[11px]" style={{ color: stage.detailTone ?? "var(--ink-faint)" }} title={stage.detail}>
          {/* A book request says what was asked for: Read, Listen or Both. */}
          {rq.media_type === "book" && rq.formats && (
            <span className="flex-none rounded px-1.5 py-px font-mono text-[9px] font-bold uppercase" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>
              {BOOK_FORMAT_BADGE[rq.formats]}
            </span>
          )}
          {/* A show asked for by season says which: S1–3, S5. */}
          {rq.media_type === "series" && rq.seasons && rq.seasons.length > 0 && (
            <span className="flex-none font-mono text-[9.5px] font-bold" style={{ color: "var(--ink-dim)" }}>{formatSeasons(rq.seasons)}</span>
          )}
          <span className="truncate">{stage.detail}</span>
        </div>
        {/* Delivered and in the owner's Plex: one tap to watch it there. */}
        {rq.plex_url && (
          <a href={rq.plex_url} target="_blank" rel="noopener noreferrer" aria-label={`Watch ${rq.title} on Plex`} className="mt-1.5 inline-flex min-h-[28px] items-center rounded-md bg-accent-grad px-2.5 text-[11px] font-semibold text-accent-ink">▶ Watch</a>
        )}
      </div>
    </div>
  );
}

// BOOK_FORMAT_BADGE is the "what was asked for" tag on a book request. The same words as
// lib/bookFormats' FORMAT_BADGE, repeated rather than imported: a module this page and the
// Books tab (which it loads lazily) both import gets folded into this page's chunk.
const BOOK_FORMAT_BADGE = { ebook: "Read", audiobook: "Listen", both: "Both" } as const;

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
        <PosterRow key={`${m}:${active.id}`} hideOnError title="" seeAll={`/discover/browse?media=${m}&provider=${active.id}`} load={() => api.discoverProviderNew(m, active.id)} ctx={ctx} />
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
        <PosterRow order={0} title={`Trending ${media === "movie" ? "movies" : "series"}`} seeAll={browseLink("trending", media)} load={() => api.discoverTrending(media)} ctx={ctx} />
        <PosterRow order={1} title={`Popular ${media === "movie" ? "movies" : "series"}`} seeAll={browseLink("popular", media)} load={() => api.discoverPopular(media)} ctx={ctx} />
        <PosterRow order={2} hideOnError title={`Top rated ${media === "movie" ? "movies" : "series"}`} seeAll={browseLink("top_rated", media)} load={() => api.discoverRow("top_rated", media)} ctx={ctx} />
        {media === "movie" ? (
          <PosterRow order={3} hideOnError title="In cinemas now" seeAll={browseLink("now_playing", "movie")} load={() => api.discoverRow("now_playing")} ctx={ctx} />
        ) : (
          <PosterRow order={3} hideOnError title="Anime" load={() => api.discoverRow("anime")} ctx={ctx} />
        )}
        <StreamingRow media={media} ctx={ctx} />
        <PosterRow order={4} hideOnError title="Hidden gems" seeAll={browseLink("hidden_gems", media)} load={() => api.discoverRow("hidden_gems", media)} ctx={ctx} />
        <PosterRow order={5} hideUntilLoaded hideOnError title="From your region" load={() => api.discoverRow("region", media)} ctx={ctx} />
        {media === "movie" ? (
          <PosterRow order={6} excludeOwned title="Upcoming — request ahead" seeAll={browseLink("upcoming", "movie")} load={() => api.discoverUpcoming()} ctx={ctx} />
        ) : (
          // Series upcoming ships behind a backend change; if it isn't live the row
          // hides itself rather than showing an error on an otherwise healthy tab.
          <PosterRow order={6} excludeOwned hideOnError title="Airing soon" seeAll={browseLink("upcoming", "series")} load={() => api.discoverUpcoming("series")} ctx={ctx} />
        )}
        <GenreExplorer media={media} ctx={ctx} />
      </div>
    </RowRegistryCtx.Provider>
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

// browseLink is a row's "See all": the browse grid for its list.
function browseLink(list: string, media: string): string {
  return `/discover/browse?list=${list}&media=${media}`;
}

function PosterRow({ title, load, ctx, order, excludeOwned, hideOnError, hideUntilLoaded, seeAll }: {
  title: string;
  /** "See all →": where the row's whole list opens (a browse grid, a collection). */
  seeAll?: string | (() => void);
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
  const mounted = useRef(false);

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

  // The mounted check matters: a late answer must not re-claim cards for a row that
  // has already gone.
  const pull = (first: boolean) => loadRef.current()
    .then((r) => { if (!mounted.current) return; raw.current = r; setError(null); recomputeRef.current(); })
    .catch((e) => { if (!mounted.current || !first) return; raw.current = []; setItems([]); setError((e as Error).message); });
  useEffect(() => {
    mounted.current = true;
    pull(true);
    return () => { mounted.current = false; };
  }, []);
  // Re-pull enrichment (badges, download progress) every 30s while the tab is
  // visible; refresh failures keep the last good data.
  usePoll(() => pull(false), 30000, { immediate: false });

  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });

  if (error && hideOnError) return null;
  if (hideUntilLoaded && items === null) return null;
  if (items && items.length === 0 && !error) return null;
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between">
        {title ? <h2 className="m-0 min-w-0 text-[15px] font-bold">{title}</h2> : <span />}
        {items && items.length > 0 && (
          <div className="flex flex-none items-center gap-2">
            {seeAll && <SeeAll to={seeAll} label={title} />}
            <div className="hidden gap-1 sm:flex">
              <ArrowBtn dir={-1} onClick={() => scroll(-1)} />
              <ArrowBtn dir={1} onClick={() => scroll(1)} />
            </div>
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

// SeeAll is a row's "See all →": a link to an address, or an action (opening a collection
// over the page goes through the overlay history).
function SeeAll({ to, label }: { to: string | (() => void); label: string }) {
  const aria = label ? `See all: ${label}` : "See all";
  const style = { color: "var(--accent)" };
  return typeof to === "string"
    ? <Link to={to} aria-label={aria} className="text-[12px] font-semibold" style={style}>See all →</Link>
    : <button onClick={to} aria-label={aria} className="min-h-[28px] text-[12px] font-semibold" style={style}>See all →</button>;
}

function ArrowBtn({ dir, onClick }: { dir: -1 | 1; onClick: () => void }) {
  return (
    <IconButton label={dir === -1 ? "Scroll left" : "Scroll right"} onClick={onClick} className="h-7 w-7 rounded-full" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink-dim)" }}>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden style={{ transform: dir === -1 ? "rotate(180deg)" : "none" }}><path d="M9 6l6 6-6 6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
    </IconButton>
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
          {active && !itemsError && items && items.length > 0 && (
            <div className="-mt-2 mb-3 flex justify-end"><SeeAll to={`/discover/browse?media=${m}&genre=${active.id}`} label={active.name} /></div>
          )}
          {active && (
            itemsError ? (
              <LoadError message={itemsError} />
            ) : (
              <div className="grid gap-x-3 gap-y-5" style={GRID}>
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
