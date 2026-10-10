import { createContext, Suspense, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { Link, Outlet, useLocation, useMatch, useNavigate, useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { MetadataMissing } from "../components/MetadataMissing";
import { lazyPage } from "../lib/lazyPage";
import { pickTab, withTab } from "../lib/useTabParam";
import { titlePath } from "../lib/refLink";
import { useTitle } from "../lib/title";
import { TabPanel, Tabs } from "../ui/Tabs";
import { useMe, isStaff } from "../lib/me";
import { api, type ApiError, type DiscoverRow, type WatchProvider, type DiscoverCard, type Genre, type MediaDetail, type MediaRequest, type SeriesSeason } from "../lib/api";
import { posterThumb } from "../lib/img";
import { useCanHover } from "../lib/useCanHover";
import { formatSeasons, MOVING_STAGES, requestStage, sortForRequester } from "../lib/requestStage";
import { usePoll } from "../lib/usePoll";
import { Button, IconButton, Sheet, StatusChip, POSTER_CHIP_BG, TONE_HUE, useToast, type Tone, type ToastFn } from "../ui";

// The Books tab is a separate Open Library experience; its code loads only when chosen.
const BooksDiscover = lazyPage(() => import("./BooksDiscover"), "BooksDiscover");
// The request sheet loads when a request is first opened, not with Discover.
const RequestSheet = lazyPage(() => import("../components/RequestSheet"), "RequestSheet");
// "Which seasons?" loads when someone first asks for a show.
const SeasonPicker = lazyPage(() => import("../components/SeasonPicker"), "SeasonPicker");
// "3 movie requests left this week": its own chunk, only fetched when a sheet opens.
const QuotaHint = lazyPage(() => import("../components/QuotaHint"), "QuotaHint");

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
  const tab = pickTab(params.get("tab"), TABS.map((t) => t.key), "discover");
  const q = params.get("q") ?? "";
  // `search` is the committed query that swaps the page to the full results grid (only via
  // "See all" / Enter, not per keystroke); `searchInput` is what's typed in the omnibox.
  const search = tab === "books" ? "" : q;
  const bookSeed = tab === "books" ? q : "";
  const [searchInput, setSearchInput] = useState(search);
  // Back, Forward or a notification link changed the committed search: show it in the box.
  useEffect(() => { setSearchInput(search); }, [search]);
  const flash = useToast();
  // Readonly users can browse but never request.
  const canRequest = !!user && user.role !== "readonly";

  // Choosing a tab (even the current one, while results are showing) leaves the search.
  const setTab = (t: Tab) => {
    if (t === tab && !q) return;
    setSearchInput("");
    setParams((p) => { const next = withTab(p, "tab", t, "discover"); next.delete("q"); return next; });
  };
  // Committing a search is a new history entry; emptying the box just drops it.
  const commitSearch = (query: string) => {
    setSearchInput(query);
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
  const movieMatch = useMatch("/discover/movie/:tmdbId");
  const seriesMatch = useMatch("/discover/series/:tmdbId");
  const titleMatch = movieMatch ? { media: "movie" as const, id: Number(movieMatch.params.tmdbId) } : seriesMatch ? { media: "series" as const, id: Number(seriesMatch.params.tmdbId) } : null;

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
          <Tabs tabs={TABS} value={search ? null : tab} onChange={setTab} idPrefix="discover" label="Discover sections" className="-mb-px" />
        </div>

        <TabPanel idPrefix="discover" value={tab}>
          {tab === "books" ? (
            <Suspense fallback={<div className="py-10 text-center text-[12.5px] text-ink-dim">Loading…</div>}>
              <BooksDiscover flash={flash} canRequest={canRequest} initialQuery={bookSeed} />
            </Suspense>
          ) : !metadataReady ? (
            // No TMDB key: every movie/TV feed would fail on its own and repeat the same error
            // row after row. Show the viewer's requests (they don't need TMDB) and one message
            // worded for their role instead. Books use Open Library and are unaffected.
            <div className="flex flex-col gap-7">
              <MyRequestsRow />
              <MetadataMissing variant="empty" />
            </div>
          ) : search ? (
            <SearchResults query={search} ctx={ctx} />
          ) : (
            <>
              {tab === "discover" && <DiscoverTab ctx={ctx} />}
              {tab === "movies" && <BrowseTab media="movie" ctx={ctx} />}
              {tab === "series" && <BrowseTab media="series" ctx={ctx} />}
            </>
          )}
        </TabPanel>
      </div>
      {titleMatch && titleMatch.id > 0 && <DiscoverTitle media={titleMatch.media} tmdbId={titleMatch.id} ctx={ctx} />}
      {/* The old /discover/tv/<id> address redirects from here (lib/routes). */}
      <Outlet />
    </>
  );
}

// What a title's history entry carries when it was opened from the page: the card it was
// opened from (shown at once, before the detail arrives), how many title entries are
// stacked above the list (so closing steps back over all of them), whether the first
// one was opened cold from a link (then there's no list underneath to step back to), and
// whether to start on "which seasons?".
interface TitleState {
  card?: DiscoverCard;
  depth?: number;
  cold?: boolean;
  pick?: boolean;
}

// useOpenTitle opens a title's sheet by going to its address, keeping the tab and search
// (?tab=, ?q=) so closing it returns to the same view. Opening one from inside a sheet
// ("More like this") stacks another entry, so Back returns to the previous title.
function useOpenTitle(): (c: DiscoverCard, pick?: boolean) => void {
  const navigate = useNavigate();
  const location = useLocation();
  return (c, pick = false) => {
    const onTitle = /^\/discover\/(movie|series)\//.test(location.pathname);
    const st = (onTitle ? location.state : null) as TitleState | null;
    const state: TitleState = { card: c, depth: (st?.depth ?? 0) + 1, cold: onTitle ? (st?.cold ?? !st?.depth) : false, pick };
    navigate(titlePath(c.media_type, c.tmdb_id) + location.search, { state });
  };
}

// DiscoverTitle is the sheet for the title in the address. Opened from a poster it starts
// from that card; opened cold (a shared link, a notification, a reload) it starts from a
// bare id and fills in from the detail answer, which carries the card's badge state.
function DiscoverTitle({ media, tmdbId, ctx }: { media: "movie" | "series"; tmdbId: number; ctx: RowCtx }) {
  const location = useLocation();
  const navigate = useNavigate();
  const st = location.state as TitleState | null;
  const card = st?.card && st.card.media_type === media && st.card.tmdb_id === tmdbId ? st.card : stubCard(media, tmdbId);
  const close = () => {
    const depth = st?.depth ?? 0;
    if (depth > 0 && !st?.cold) navigate(-depth);
    else navigate({ pathname: "/discover", search: location.search }, { replace: true });
  };
  return <RequestDetailModal card={card} ctx={ctx} pick={!!st?.pick} onClose={close} />;
}

// shareTitle hands the title's address to the phone's share sheet, or copies it where
// there is none (most desktops). A share the person cancels is not an error.
async function shareTitle(c: DiscoverCard, flash: ToastFn) {
  const url = window.location.origin + titlePath(c.media_type, c.tmdb_id);
  if (typeof navigator.share === "function") {
    try { await navigator.share({ title: c.title, url }); } catch { /* cancelled */ }
    return;
  }
  try {
    await navigator.clipboard.writeText(url);
    flash("Link copied");
  } catch {
    flash("Couldn’t copy the link", { tone: "error" });
  }
}

function stubCard(media: "movie" | "series", tmdbId: number): DiscoverCard {
  return { media_type: media, tmdb_id: tmdbId, title: "", year: 0, vote_average: 0, in_library: false, has_file: false };
}

type ReqStatus = MediaRequest["status"];

// requestedMessage is what asking for a title says: an auto-approved request is already
// being searched for, anything else waits for someone to approve it.
// A show asked for by season names them: Requested “Severance” S2 — waiting for approval.
function requestedMessage(title: string, status: ReqStatus, seasons?: number[]): string {
  const what = `Requested “${title}”${seasons?.length ? ` ${formatSeasons(seasons)}` : ""}`;
  return status === "approved" ? `${what} — searching now` : `${what} — waiting for approval`;
}

// What asking for a title someone already asked for says: you joined their request.
const FOLLOWING = "You’re following this request — it’s in your requests now.";

// The longest note a requester can leave for the admin (the server refuses longer).
const NOTE_MAX = 500;

interface RowCtx {
  doRequest: (c: DiscoverCard, note?: string, seasons?: number[] | null) => Promise<{ subscribed: boolean; status: ReqStatus }>;
  /** What this session asked for the card, if anything: the status the server answered. */
  isRequested: (c: DiscoverCard) => ReqStatus | undefined;
  canRequest: boolean;
  flash: ToastFn;
}

// Compact inline error line for a failed fetch — distinct from a genuine empty result.
// The backend's message is surfaced verbatim. A missing TMDB key never gets here: the page
// shows one MetadataMissing message instead of an error per row.
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
        <MyRequestsRow />
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
  const [openID, setOpenID] = useState<number | null>(null);
  const scroller = useRef<HTMLDivElement>(null);

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
  const scroll = (dir: -1 | 1) => scroller.current?.scrollBy({ left: dir * Math.max(600, scroller.current.clientWidth * 0.8), behavior: "smooth" });
  const seeAll = staff && waiting > 0 ? "/requests?tab=needs" : "/requests";
  const open = openID ? items.find((rq) => rq.id === openID) : undefined;
  return (
    <div>
      <div className="mb-2.5 flex items-center justify-between gap-2">
        <h2 className="m-0 min-w-0 text-[15px] font-bold">
          {staff ? "Requests" : "Your requests"}
          {staff && waiting > 0 && <span className="ml-2 text-[11.5px] font-medium" style={{ color: "var(--avoid-text)" }}>{waiting} waiting</span>}
          {moving > 0 && <span className="ml-2 text-[11.5px] font-medium" style={{ color: "var(--accent)" }}>{moving} on the way</span>}
        </h2>
        <div className="flex flex-none items-center gap-2">
          <Link to={seeAll} className="text-[12px] font-semibold" style={{ color: "var(--accent)" }}>See all →</Link>
          <div className="hidden gap-1 sm:flex"><ArrowBtn dir={-1} onClick={() => scroll(-1)} /><ArrowBtn dir={1} onClick={() => scroll(1)} /></div>
        </div>
      </div>
      <div ref={scroller} className="thin-scroll flex gap-3 overflow-x-auto pb-2" style={{ scrollSnapType: "x proximity" }}>
        {sorted.map((rq) => <RequestPoster key={rq.id} rq={rq} staff={staff} queueKnown={queueKnown} onOpen={() => setOpenID(rq.id)} />)}
      </div>
      {openID !== null && (
        <Suspense fallback={null}>
          <RequestSheet key={openID} requestId={openID} initial={open} onChanged={() => { void load(); }} onClose={() => setOpenID(null)} />
        </Suspense>
      )}
    </div>
  );
}

function RequestPoster({ rq, staff, queueKnown = true, onOpen }: { rq: MediaRequest; staff: boolean; queueKnown?: boolean; onOpen: () => void }) {
  const tr = rq.tracking;
  const stage = requestStage(rq, queueKnown);
  const pct = tr && (tr.stage === "downloading" || tr.stage === "paused") && tr.progress != null ? Math.round(tr.progress * 100) : 0;
  const showBar = !stage.unknown && (tr?.stage === "downloading" || tr?.stage === "paused" || tr?.stage === "importing");
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
              className={`block h-full ${tr?.stage === "importing" ? "animate-pulse" : ""}`}
              style={{ width: `${tr?.stage === "importing" ? 100 : Math.max(2, pct)}%`, background: tr?.stage === "paused" ? "var(--ink-faint)" : TONE_HUE[stage.tone] }}
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

function badgeFor(c: DiscoverCard, requested?: ReqStatus): { label: string; tone: Tone } | null {
  if (c.has_file) return { label: "In library", tone: "good" };
  if ((c.download_progress ?? 0) > 0) return { label: "Downloading", tone: "accent" };
  // Asked for this session and auto-approved: it's on its way, not waiting.
  if (requested === "approved" || c.request_status === "approved") return { label: "Requested", tone: "accent" };
  // `requested` (this session) beats a stale "declined" — a re-request goes pending.
  if (requested || c.request_status === "pending") return { label: "Pending", tone: "avoid" };
  if (c.request_status === "declined") return { label: "Declined", tone: "faint" };
  // In the library, being fetched, no file yet and no request in flight → it's wanted, not
  // "requested". One nobody monitors (a library scan found it) gets no badge, so it keeps
  // its Request button.
  if (c.wanted) return { label: "Wanted", tone: "faint" };
  return null;
}

function MediaCard({ c, ctx, full }: { c: DiscoverCard; ctx: RowCtx; full?: boolean }) {
  const openTitle = useOpenTitle();
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
  // A show opens the sheet on its season picker instead: never the whole show in one click.
  const quick = async () => {
    if (c.media_type === "series") { openTitle(c, true); return; }
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
          onClick={() => openTitle(c)}
          aria-label={`View details for ${c.title}`}
          className="absolute inset-0 block h-full w-full text-left"
        >
          {c.poster_url ? (
            <img src={posterThumb(c.poster_url)} alt={c.title} className="h-full w-full object-cover" loading="lazy" decoding="async" />
          ) : (
            <PosterPlaceholder title={c.title} year={c.year} />
          )}
          {badge && <StatusChip tone={badge.tone} surface="poster" className="absolute right-1.5 top-1.5">{badge.label}</StatusChip>}
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
            className="pointer-events-none absolute bottom-2 left-2 z-20 rounded-md px-2.5 py-1 text-[10.5px] font-semibold opacity-0 transition-opacity group-hover:pointer-events-auto group-hover:opacity-100 focus-visible:pointer-events-auto focus-visible:opacity-100 disabled:cursor-default"
            // Dimmed with a filter, not opacity, so the busy look doesn't fight the hover reveal.
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", filter: quickBusy ? "brightness(.8)" : undefined }}
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

// The sheet behind a title's address (DiscoverTitle). card is what to show until the
// detail arrives; "More like this" goes to that title's address, and the same sheet
// re-fetches for it rather than stacking another one.
// pick: open a show's sheet straight on "which seasons?" (its "＋ Request" was tapped).
function RequestDetailModal({ card, ctx, pick, onClose }: { card: DiscoverCard; ctx: RowCtx; pick?: boolean; onClose: () => void }) {
  const current = card;
  const titleKey = `${card.media_type}:${card.tmdb_id}`;
  const openTitle = useOpenTitle();
  // A show's seasons and what asking for each would mean; "error" when they couldn't be
  // read, which falls back to asking for the whole show.
  const [seasons, setSeasons] = useState<SeriesSeason[] | "error" | null>(null);
  const [picking, setPicking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<ReqStatus | undefined>(ctx.isRequested(card));
  const [subscribed, setSubscribed] = useState(false);
  const [note, setNote] = useState("");
  const [noteOpen, setNoteOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The requester has no movie requests left (QuotaHint says when the next frees up).
  const [quotaOut, setQuotaOut] = useState(false);
  const [d, setD] = useState<MediaDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(true);
  // The detail couldn't be had: 404 is a title that doesn't exist or isn't allowed (the
  // adult filter); anything else is a failed fetch.
  const [detailError, setDetailError] = useState<{ status: number; message: string } | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  // A stable ref so the detail effect doesn't re-run when the parent re-renders
  // (ctx is a fresh object on every Discover render).
  const ctxRef = useRef(ctx); ctxRef.current = ctx;
  // The title on show now, so a late seasons answer for another one is dropped.
  const currentRef = useRef(titleKey); currentRef.current = titleKey;

  // The detail answer's card wins once it's here: the freshest badge state, and all of a
  // title opened cold from its address.
  const c: DiscoverCard = d?.card ? { ...current, ...d.card } : current;
  useTitle(c.title || undefined);
  const badge = badgeFor(c, done);
  const declined = badge?.label === "Declined";
  const isShow = c.media_type === "series";
  // A show partly here or partly asked for still offers the seasons nobody has asked for.
  const moreSeasons = isShow && ctx.canRequest && Array.isArray(seasons) && seasons.some((s) => s.requestable);

  // Fetch detail whenever the shown card changes; reset per-card request state.
  useEffect(() => {
    let alive = true;
    setD(null); setDetailLoading(true); setDetailError(null); setError(null);
    setDone(ctxRef.current.isRequested(current)); setSubscribed(false); setNote(""); setNoteOpen(false);
    setSeasons(null); setPicking(!!pick);
    scrollRef.current?.scrollTo({ top: 0 });
    api.mediaDetail(current.media_type, current.tmdb_id)
      .then((r) => { if (alive) { setD(r); setDetailLoading(false); } })
      .catch((e) => { if (alive) { setDetailLoading(false); setDetailError({ status: (e as ApiError).status ?? 0, message: (e as Error).message }); } });
    // One seasons call per show sheet, and only for someone who can ask for it.
    if (current.media_type === "series" && ctxRef.current.canRequest) {
      api.seriesSeasons(current.tmdb_id)
        .then((r) => { if (alive) setSeasons(r); })
        .catch(() => { if (alive) setSeasons("error"); });
    }
    return () => { alive = false; };
    // Only a change of title resets the sheet; the card object itself is rebuilt often.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [titleKey]);

  // Only flip to the success state when the request actually succeeded; on failure
  // the error shows inline (plus the toast) and the button stays.
  // picked (a show): the seasons chosen; null or absent is the whole show.
  const request = async (picked?: number[] | null) => {
    setBusy(true); setError(null);
    try {
      const r = await ctx.doRequest(c, note, picked);
      setSubscribed(r.subscribed);
      setDone(r.status);
      if (isShow) {
        // The season states moved on: what's asked for now, and whether any is left.
        setPicking(false); setNote(""); setNoteOpen(false);
        const asked = titleKey;
        api.seriesSeasons(c.tmdb_id)
          .then((r) => { if (currentRef.current === asked) setSeasons(r); })
          .catch(() => { if (currentRef.current === asked) setSeasons("error"); });
      }
    } catch (e) {
      // Declined before: asking again needs a note, and says why it was turned down.
      const b = (e as ApiError).body;
      if (b?.code === "needs_note") {
        setNoteOpen(true);
        setError(`Declined before${b.decline_reason ? `: ${String(b.decline_reason)}` : ""}. Say why you’d still like it, then ask again.`);
      } else setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  // A show's Request opens the season picker; without a season list it asks for the
  // whole show, as before seasons could be picked.
  const ask = () => (isShow && seasons !== "error" ? setPicking(true) : request());

  const overview = d?.overview || c.overview;
  const runtime = d?.runtime ? `${Math.floor(d.runtime / 60)}h ${d.runtime % 60}m` : "";
  const director = crewByJob(d?.crew, "Director");
  const writer = crewByJob(d?.crew, "Writer");
  const producer = crewByJob(d?.crew, "Producer");
  const creator = crewByJob(d?.crew, "Creator");
  const r = d?.ratings;
  const similar = (d?.similar ?? []).filter((s) => s.tmdb_id !== c.tmdb_id).slice(0, 12);

  // Opened cold from an address that leads nowhere: a title TMDB doesn't know, or one the
  // always-on adult filter keeps out (the server answers 404 for both).
  if (!c.title && detailError) {
    return (
      <Sheet onClose={onClose} closeOnBack={false} ariaLabel="Not available" size="sm">
        <div className="p-6">
          <h2 className="m-0 text-[16px] font-bold">Not available</h2>
          <p className="m-0 mt-1.5 text-[12.5px] text-ink-dim">
            {detailError.status === 404 ? "This title can’t be shown here." : `Couldn’t load it — ${detailError.message}`}
          </p>
          <div className="mt-4 flex justify-end"><Button onClick={onClose}>Back to Discover</Button></div>
        </div>
      </Sheet>
    );
  }

  // The kit Sheet owns the dialog plumbing (portal, focus trap and restore, Esc, scroll
  // lock). Back needs no help: the title is its own address, so Back leaves it. The panel
  // keeps its own layout: full screen on a phone, a centred card with an inner scroller
  // above sm, and a slightly darker scrim than a plain dialog because it sits over artwork.
  return (
    <Sheet
      onClose={onClose}
      closeOnBack={false}
      ariaLabel={c.title || "Title"}
      handle={false}
      scrim={0.68}
      panelClassName="flex h-full w-full flex-col overflow-hidden sm:h-auto sm:max-h-[92vh] sm:max-w-[820px] sm:rounded-2xl sm:shadow-panel"
    >
      {/* Close sits on the dialog, not the backdrop, so it stays put while the body scrolls.
          Full screen in the installed iPhone app, it stays clear of the status bar. */}
      <IconButton label="Close" onClick={onClose} className="absolute right-3 z-20 h-8 w-8 rounded-full" style={{ top: "max(0.75rem, env(safe-area-inset-top, 0px))", background: "rgba(20,12,7,.7)", color: "#fff" }}>✕</IconButton>
      {/* Every title has an address now, so it can be sent to someone. */}
      {c.title && (
        <IconButton label="Share" onClick={() => { void shareTitle(c, ctx.flash); }} className="absolute right-[3.25rem] z-20 h-8 w-8 rounded-full" style={{ top: "max(0.75rem, env(safe-area-inset-top, 0px))", background: "rgba(20,12,7,.7)", color: "#fff" }}>
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden><path d="M12 3v12M7 8l5-5 5 5" /><path d="M5 13v6a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-6" /></svg>
        </IconButton>
      )}

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
              <h2 className="m-0 text-[18px] font-bold leading-tight sm:text-[21px]">{c.title || <span className="text-ink-faint">Loading…</span>}</h2>
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
              <div className="mt-2 font-mono text-[10.5px]" style={{ color: "var(--avoid-text)" }}>Releases {c.release_date} — request ahead</div>
            )}
            <div className="mt-3 flex flex-wrap items-center gap-2">
              {/* Opened cold, nothing is offered until the title is known. */}
              {!c.title ? null : done && subscribed ? (
                <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{FOLLOWING}</span>
              ) : badge && !declined ? (
                <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: POSTER_CHIP_BG, color: TONE_HUE[badge.tone], border: `1px solid ${TONE_HUE[badge.tone]}` }}>
                  {badge.label === "In library" ? (moreSeasons ? "✓ Partly in your library" : "✓ In your library") : badge.label === "Pending" ? "Requested — waiting for approval" : badge.label === "Downloading" ? "Downloading…" : badge.label === "Wanted" ? "In library — waiting for a file" : done === "approved" ? "Requested — searching now" : "Requested"}
                </span>
              ) : !ctx.canRequest ? (
                <span className="text-[12px] text-ink-faint">Ask your admin for request access.</span>
              ) : (
                <>
                  {declined && badge && (
                    <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: POSTER_CHIP_BG, color: TONE_HUE[badge.tone], border: `1px solid ${TONE_HUE[badge.tone]}` }}>Declined</span>
                  )}
                  {!picking && (
                    <Button variant="primary" onClick={ask} busy={busy} busyLabel="Requesting…" disabled={quotaOut && !isShow}>
                      {declined ? "Request again" : "＋ Request"}
                    </Button>
                  )}
                </>
              )}
              {/* Already here, coming or asked for, but some seasons aren't: ask for those. */}
              {moreSeasons && ((done && subscribed) || (badge && !declined)) && !picking && (
                <Button variant="primary" onClick={() => setPicking(true)}>Request more seasons</Button>
              )}
              {d?.trailer_url && (
                <a href={d.trailer_url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1.5 rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                  <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z" /></svg>
                  Trailer
                </a>
              )}
            </div>
            {/* An optional note for whoever approves it, folded away until asked for. */}
            {ctx.canRequest && !!c.title && (picking || (!(done && subscribed) && (!badge || declined))) && (
              noteOpen || declined ? (
                <label className="mt-2.5 flex max-w-[460px] flex-col gap-1 text-[11.5px] text-ink-dim">
                  {declined ? "Tell them why you’d still like it" : "Note for the admin (optional)"}
                  <textarea
                    value={note}
                    onChange={(e) => setNote(e.target.value.slice(0, NOTE_MAX))}
                    maxLength={NOTE_MAX}
                    rows={3}
                    placeholder="Anything they should know — a particular version, why you'd like it…"
                    className="rounded-lg px-3 py-2 text-[12.5px]"
                    style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
                  />
                  <span className="self-end font-mono text-[10px] text-ink-faint">{note.length}/{NOTE_MAX}</span>
                </label>
              ) : (
                <button onClick={() => setNoteOpen(true)} className="mt-2 min-h-[32px] text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>
                  ＋ Add a note for the admin (optional)
                </button>
              )
            )}
            {/* A movie's request limit, when there is one (a show's is in the season picker). */}
            {ctx.canRequest && !isShow && !(done && subscribed) && (!badge || declined) && (
              <Suspense fallback={null}><QuotaHint kind="movie" onOut={setQuotaOut} /></Suspense>
            )}
            {error && <div className="mt-1.5 text-[11.5px] font-medium" style={{ color: "var(--reject)" }}>{error}</div>}
          </div>
        </div>

        {/* Which seasons: full width under the title, where a phone has room for it. */}
        {isShow && picking && (
          <div className="px-5 pt-4 sm:px-6">
            {Array.isArray(seasons) ? (
              <Suspense fallback={<div className="py-3 text-[12px] text-ink-faint">Loading seasons…</div>}>
                <SeasonPicker seasons={seasons} busy={busy} onSubmit={(p) => { void request(p); }} onCancel={() => setPicking(false)} />
              </Suspense>
            ) : seasons === "error" ? (
              <div className="flex flex-wrap items-center gap-2 text-[12px] text-ink-dim">
                Couldn’t load the seasons.
                <Button variant="primary" onClick={() => { void request(); }} busy={busy} busyLabel="Requesting…">Request the whole show</Button>
              </div>
            ) : (
              <div className="py-3 text-[12px] text-ink-faint">Loading seasons…</div>
            )}
          </div>
        )}

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
                      onClick={() => openTitle(s)}
                      className="group w-[112px] flex-none text-left"
                      aria-label={`View ${s.title}`}
                    >
                      <div className="relative overflow-hidden rounded-lg transition-transform duration-200 group-hover:scale-[1.04]" style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)" }}>
                        {s.poster_url ? <img src={posterThumb(s.poster_url)} alt={s.title} className="h-full w-full object-cover" loading="lazy" /> : <PosterPlaceholder title={s.title} year={s.year} />}
                        {sBadge && <StatusChip tone={sBadge.tone} surface="poster" className="absolute right-1 top-1">{sBadge.label}</StatusChip>}
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
    </Sheet>
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
