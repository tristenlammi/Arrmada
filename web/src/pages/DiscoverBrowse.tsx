import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { api, type DiscoverCard, type DiscoverPage, type Genre, type WatchProvider } from "../lib/api";
import { CardSkeleton, GRID, LoadError, MediaCard, type RowCtx } from "./discover/shared";

// The deeper Discover grids (REQ-19): a filterable browse grid behind every row's
// "See all" (/discover/browse?list=popular&media=movie), and search results past the first
// page. Both load one page of 20 at a time as the end of the grid scrolls into view; the
// server whitelists every filter, so the address is the whole state and a reload or a
// shared link reproduces the grid. Its own chunk: Discover's first load doesn't carry it.

// The fixed lists a row's "See all" opens, and what each is called.
const LISTS: Record<string, { movie: string; series: string }> = {
  trending: { movie: "Trending movies", series: "Trending series" },
  popular: { movie: "Popular movies", series: "Popular series" },
  top_rated: { movie: "Top rated movies", series: "Top rated series" },
  now_playing: { movie: "In cinemas now", series: "In cinemas now" },
  upcoming: { movie: "Upcoming — request ahead", series: "Airing soon" },
  hidden_gems: { movie: "Hidden gems", series: "Hidden gems" },
};

const SORTS: { value: string; label: string; movieOnly?: boolean }[] = [
  { value: "popularity.desc", label: "Most popular" },
  { value: "vote_average.desc", label: "Highest rated" },
  { value: "primary_release_date.desc", label: "Newest" },
  { value: "revenue.desc", label: "Highest grossing", movieOnly: true },
];

const RATINGS = ["", "5", "6", "7", "8"];
// Runtime presets: [label, min, max] in minutes ("" = no bound).
const RUNTIMES: [string, string, string][] = [["Any length", "", ""], ["Under 90 min", "", "89"], ["90 min – 2 h", "90", "120"], ["Over 2 h", "121", ""]];
const LANGUAGES: [string, string][] = [["", "Any language"], ["en", "English"], ["ja", "Japanese"], ["ko", "Korean"], ["fr", "French"], ["es", "Spanish"], ["de", "German"], ["it", "Italian"], ["hi", "Hindi"], ["zh", "Chinese"]];

// The address's filter keys, passed through to /api/v1/discover/browse as they are.
const FILTER_KEYS = ["list", "media", "genre", "year_from", "year_to", "rating", "runtime_min", "runtime_max", "provider", "lang", "sort"];

const cardKey = (c: DiscoverCard) => `${c.media_type}:${c.tmdb_id}`;

// useInfinite pages through a list: page 1 when key changes, then the next page each time
// the sentinel at the end of the grid comes near the screen. Nothing is fetched ahead.
function useInfinite<P extends DiscoverPage>(key: string, fetchPage: (page: number) => Promise<P>, onFirst?: (p: P) => void) {
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(1);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fetchRef = useRef(fetchPage); fetchRef.current = fetchPage;
  const firstRef = useRef(onFirst); firstRef.current = onFirst;
  // Bumped on every new key, so an answer for the old one is dropped.
  const gen = useRef(0);
  const sentinel = useRef<HTMLDivElement>(null);

  const load = useCallback((p: number) => {
    const my = gen.current;
    setLoading(true); setError(null);
    fetchRef.current(p).then((r) => {
      if (gen.current !== my) return;
      // TMDB's pages shift as popularity moves, so a title can turn up twice: keep the first.
      setItems((cur) => {
        const base = p === 1 ? [] : cur ?? [];
        const seen = new Set(base.map(cardKey));
        return [...base, ...r.items.filter((c) => !seen.has(cardKey(c)))];
      });
      setPage(p); setTotal(r.total_pages); setLoading(false);
      if (p === 1) firstRef.current?.(r);
    }).catch((e) => {
      if (gen.current !== my) return;
      setLoading(false); setError((e as Error).message);
      if (p === 1) setItems([]);
    });
  }, []);

  useEffect(() => {
    gen.current++;
    setItems(null); setPage(0); setTotal(1);
    load(1);
  }, [key, load]);

  const more = page > 0 && page < total && !loading && !error;
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !more || typeof IntersectionObserver === "undefined") return;
    // The page scrolls inside the layout's <main>, not the window: watching from the
    // scroller itself lets the margin reach below its visible edge.
    const io = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) load(page + 1); }, { root: scrollParent(el), rootMargin: "0px 0px 400px 0px" });
    io.observe(el);
    return () => io.disconnect();
  }, [more, page, load]);

  return { items, loading, error, sentinel, retry: () => load(Math.max(1, page + 1)), hasMore: page < total };
}

// scrollParent is the nearest ancestor that scrolls vertically, or null for the window.
function scrollParent(el: HTMLElement): HTMLElement | null {
  for (let n = el.parentElement; n; n = n.parentElement) {
    const oy = getComputedStyle(n).overflowY;
    if (oy === "auto" || oy === "scroll") return n;
  }
  return null;
}

// Grid is the infinite poster grid: skeletons first, the error line on a failure (with a
// retry for a later page), and the sentinel that loads the next page.
function Grid({ state, ctx, empty }: { state: ReturnType<typeof useInfinite>; ctx: RowCtx; empty: string }) {
  const { items, loading, error, sentinel, retry, hasMore } = state;
  if (items === null) {
    return <div className="grid gap-x-3 gap-y-5" style={GRID}>{Array.from({ length: 12 }).map((_, i) => <CardSkeleton key={i} full />)}</div>;
  }
  if (items.length === 0) {
    return error ? <LoadError message={error} /> : <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>{empty}</div>;
  }
  return (
    <>
      <div className="grid gap-x-3 gap-y-5" style={GRID}>
        {items.map((c) => <MediaCard key={cardKey(c)} c={c} ctx={ctx} full />)}
        {loading && Array.from({ length: 6 }).map((_, i) => <CardSkeleton key={`s${i}`} full />)}
      </div>
      {error && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <LoadError message={error} />
          <button onClick={retry} className="min-h-[32px] text-[12px] font-semibold" style={{ color: "var(--accent)" }}>Try again</button>
        </div>
      )}
      {!hasMore && items.length > 20 && <div className="mt-6 text-center text-[11.5px] text-ink-faint">That’s everything.</div>}
      <div ref={sentinel} aria-hidden className="h-px" />
    </>
  );
}

// SearchResults is a committed search (?q=), every page of it.
export function SearchResults({ query, ctx }: { query: string; ctx: RowCtx }) {
  const state = useInfinite(query, (page) => api.discoverSearchPage(query, page));
  return (
    <div>
      <h2 className="m-0 mb-3 text-[15px] font-bold">Results for “{query}”</h2>
      <Grid state={state} ctx={ctx} empty={`No movies or shows match “${query}”.`} />
    </div>
  );
}

const chip = (on: boolean) => ({ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-faint)" });
const field = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" };

// DiscoverBrowse is /discover/browse: a filter bar over an infinite grid, all its state
// in the address.
export function DiscoverBrowse({ ctx }: { ctx: RowCtx }) {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const location = useLocation();
  const list = params.get("list") ?? "";
  const media = params.get("media") === "series" ? "series" : params.get("media") === "all" && list === "trending" ? "all" : "movie";
  const genres = (params.get("genre") ?? "").split(",").filter(Boolean).map(Number);
  const provider = Number(params.get("provider") ?? 0);
  const sort = params.get("sort") ?? "";

  const [genreList, setGenreList] = useState<Genre[]>([]);
  const [providers, setProviders] = useState<WatchProvider[]>([]);
  useEffect(() => {
    if (media === "all") { setGenreList([]); setProviders([]); return; }
    let alive = true;
    api.discoverGenres(media).then((g) => { if (alive) setGenreList(g); }).catch(() => { if (alive) setGenreList([]); });
    api.discoverProviders(media).then((p) => { if (alive) setProviders(p); }).catch(() => { if (alive) setProviders([]); });
    return () => { alive = false; };
  }, [media]);

  // The query the server sees: only the filter keys, in a stable order (the grid's key).
  const qs = new URLSearchParams();
  for (const k of FILTER_KEYS) { const v = params.get(k); if (v) qs.set(k, v); }
  const key = qs.toString();
  const state = useInfinite(key, (page) => api.discoverBrowse(key, page));

  // Any filter turns a fixed list into a filtered browse of the same media. Replacing the
  // entry keeps Back for leaving the grid, not undoing each filter.
  const set = (changes: Record<string, string | null>) => setParams((p) => {
    const next = new URLSearchParams(p);
    next.delete("list");
    for (const [k, v] of Object.entries(changes)) { if (v) next.set(k, v); else next.delete(k); }
    if (!next.get("media") || next.get("media") === "all") next.set("media", "movie");
    return next;
  }, { replace: true });
  // Switching media keeps a fixed list (Popular movies → Popular series); genre ids differ
  // between movies and series, so the genres are dropped, and series have no box office.
  const setMedia = (m: "movie" | "series") => setParams((p) => {
    const next = new URLSearchParams(p);
    next.set("media", m);
    next.delete("genre");
    if (m === "series" && next.get("sort") === "revenue.desc") next.delete("sort");
    return next;
  }, { replace: true });
  const toggleGenre = (id: number) => {
    const next = genres.includes(id) ? genres.filter((g) => g !== id) : [...genres, id];
    set({ genre: next.join(",") || null });
  };
  const runtime = RUNTIMES.findIndex(([, lo, hi]) => lo === (params.get("runtime_min") ?? "") && hi === (params.get("runtime_max") ?? ""));
  const filtered = FILTER_KEYS.some((k) => k !== "list" && k !== "media" && params.get(k));
  const providerName = providers.find((p) => p.id === provider)?.name;
  const noun = media === "series" ? "series" : media === "all" ? "titles" : "movies";
  const title = list && LISTS[list] ? (media === "all" ? "Trending this week" : LISTS[list][media]) : providerName ? `${providerName} ${noun}` : `Browse ${noun}`;
  // Back leaves the grid for wherever it was opened from; a grid opened cold goes to Discover.
  const back = () => (location.key !== "default" ? navigate(-1) : navigate("/discover", { replace: true }));

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2">
        <button onClick={back} className="min-h-[32px] text-[12px] font-semibold" style={{ color: "var(--accent)" }}>← Discover</button>
        <h2 className="m-0 min-w-0 flex-1 text-[17px] font-bold">{title}</h2>
      </div>

      <div className="mb-5 flex flex-col gap-2.5 rounded-xl p-3" style={{ background: "var(--panel)", border: "1px solid var(--line)" }} role="group" aria-label="Filters">
        <div className="flex flex-wrap items-center gap-2">
          {(["movie", "series"] as const).map((k) => (
            <button key={k} onClick={() => setMedia(k)} aria-pressed={media === k} className="min-h-[32px] rounded-full px-3 text-[12px] font-semibold" style={chip(media === k)}>
              {k === "movie" ? "Movies" : "Series"}
            </button>
          ))}
          <select aria-label="Sort by" value={sort} onChange={(e) => set({ sort: e.target.value || null })} className="min-h-[32px] rounded-lg px-2 text-[12px]" style={field}>
            <option value="">Sort: default</option>
            {SORTS.filter((s) => !s.movieOnly || media !== "series").map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
          </select>
          <select aria-label="Minimum rating" value={params.get("rating") ?? ""} onChange={(e) => set({ rating: e.target.value || null })} className="min-h-[32px] rounded-lg px-2 text-[12px]" style={field}>
            {RATINGS.map((r) => <option key={r} value={r}>{r ? `★ ${r}+` : "Any rating"}</option>)}
          </select>
          <select aria-label="Length" value={Math.max(0, runtime)} onChange={(e) => { const [, lo, hi] = RUNTIMES[Number(e.target.value)]; set({ runtime_min: lo || null, runtime_max: hi || null }); }} className="min-h-[32px] rounded-lg px-2 text-[12px]" style={field}>
            {RUNTIMES.map(([label], i) => <option key={label} value={i}>{label}</option>)}
          </select>
          <select aria-label="Original language" value={params.get("lang") ?? ""} onChange={(e) => set({ lang: e.target.value || null })} className="min-h-[32px] rounded-lg px-2 text-[12px]" style={field}>
            {LANGUAGES.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
          </select>
          <YearRange from={params.get("year_from") ?? ""} to={params.get("year_to") ?? ""} onChange={(from, to) => set({ year_from: from || null, year_to: to || null })} />
          {filtered && (
            <button onClick={() => setParams({ media: media === "all" ? "movie" : media }, { replace: true })} className="min-h-[32px] text-[12px] font-semibold" style={{ color: "var(--accent)" }}>Clear filters</button>
          )}
        </div>
        {genreList.length > 0 && (
          <div className="thin-scroll -mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1" role="group" aria-label="Genres">
            {genreList.map((g) => (
              <button key={g.id} onClick={() => toggleGenre(g.id)} aria-pressed={genres.includes(g.id)} className="min-h-[30px] flex-none rounded-full px-3 text-[12px] font-semibold" style={chip(genres.includes(g.id))}>{g.name}</button>
            ))}
          </div>
        )}
        {providers.length > 0 && (
          <div className="thin-scroll -mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1" role="group" aria-label="Streaming services">
            {providers.map((p) => (
              <button key={p.id} onClick={() => set({ provider: provider === p.id ? null : String(p.id) })} aria-pressed={provider === p.id} title={p.name} className="flex min-h-[30px] flex-none items-center gap-1.5 rounded-full py-0.5 pl-1 pr-2.5 text-[12px] font-semibold" style={chip(provider === p.id)}>
                {p.logo_url ? <img src={p.logo_url} alt="" className="h-5 w-5 rounded-md" /> : null}
                <span>{p.name}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      <Grid state={state} ctx={ctx} empty="Nothing matches these filters." />
    </div>
  );
}

// YearRange is two year boxes, applied when one is left or Enter is pressed (not on every
// keystroke, which would fetch a grid per digit).
function YearRange({ from, to, onChange }: { from: string; to: string; onChange: (from: string, to: string) => void }) {
  const [f, setF] = useState(from);
  const [t, setT] = useState(to);
  // Back, Forward or Clear changed the address: show it. Each box follows only its own
  // value, so applying one never wipes what's being typed in the other.
  useEffect(() => { setF(from); }, [from]);
  useEffect(() => { setT(to); }, [to]);
  const clean = (v: string) => (/^\d{4}$/.test(v.trim()) ? v.trim() : "");
  const apply = () => { if (clean(f) !== from || clean(t) !== to) onChange(clean(f), clean(t)); };
  const box = (label: string, v: string, setV: (v: string) => void) => (
    <input
      aria-label={label}
      inputMode="numeric"
      placeholder={label === "From year" ? "From" : "To"}
      value={v}
      maxLength={4}
      onChange={(e) => setV(e.target.value.replace(/\D/g, ""))}
      onBlur={apply}
      onKeyDown={(e) => { if (e.key === "Enter") apply(); }}
      className="min-h-[32px] w-[64px] rounded-lg px-2 text-[12px]"
      style={field}
    />
  );
  return (
    <span className="flex items-center gap-1 text-[12px] text-ink-faint">
      {box("From year", f, setF)}–{box("To year", t, setT)}
    </span>
  );
}
