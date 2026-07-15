import { useCallback, useEffect, useRef, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { RequestsPanel } from "./Requests";
import { BooksDiscover } from "./BooksDiscover";
import { useMe } from "../lib/me";
import { api, type DiscoverCard, type Genre, type MediaDetail } from "../lib/api";

type Tab = "discover" | "movies" | "series" | "requests" | "books";
const BASE_TABS: { key: Tab; label: string }[] = [
  { key: "discover", label: "Discover" },
  { key: "movies", label: "Movies" },
  { key: "series", label: "Series" },
  { key: "requests", label: "Requests" },
];

export function Discover({ chrome = true }: { chrome?: boolean }) {
  const { booksEnabled } = useMe();
  // Books get their own tab at the end — a completely separate Open Library experience.
  const TABS = booksEnabled ? [...BASE_TABS, { key: "books" as Tab, label: "Books" }] : BASE_TABS;
  const [tab, setTabState] = useState<Tab>("discover");
  const [requested, setRequested] = useState<Set<string>>(new Set());
  const [toast, setToast] = useState<string | null>(null);
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3000); };

  // Live search — debounce the input; empty clears back to the browse tabs.
  useEffect(() => {
    const q = searchInput.trim();
    if (!q) { setSearch(""); return; }
    const t = setTimeout(() => setSearch(q), 350);
    return () => clearTimeout(t);
  }, [searchInput]);

  const setTab = (t: Tab) => { setTabState(t); setSearchInput(""); setSearch(""); };

  const doRequest = useCallback(async (c: DiscoverCard) => {
    const key = `${c.media_type}:${c.tmdb_id}`;
    try {
      await api.createRequest({ media_type: c.media_type, tmdb_id: c.tmdb_id, title: c.title, year: c.year, poster_url: c.poster_url, overview: c.overview });
      setRequested((s) => new Set(s).add(key));
      flash(`Requested “${c.title}”`);
    } catch (e) {
      const m = (e as Error).message;
      if (/already/i.test(m)) { setRequested((s) => new Set(s).add(key)); flash("Already requested."); }
      else flash(m);
    }
  }, []);
  const isRequested = useCallback((c: DiscoverCard) => requested.has(`${c.media_type}:${c.tmdb_id}`), [requested]);

  const ctx: RowCtx = { doRequest, isRequested };

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
          {/* Books have their own search inside BooksDiscover — hide the movie/TV one there. */}
          {tab !== "books" && (
            <div className="relative mb-2 sm:mb-0">
              <svg className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2" width="14" height="14" viewBox="0 0 24 24" fill="none" style={{ color: "var(--ink-faint)" }}>
                <circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" /><path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
              </svg>
              <input
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                placeholder="Search movies & TV…"
                className="w-[200px] rounded-lg py-1.5 pl-8 pr-7 text-[12.5px] transition-[width] focus:w-[260px]"
                style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
              />
              {searchInput && (
                <button onClick={() => setSearchInput("")} className="absolute right-2 top-1/2 -translate-y-1/2 text-ink-faint hover:text-[var(--ink)]" style={{ fontSize: "13px" }}>✕</button>
              )}
            </div>
          )}
        </div>

        {tab === "books" ? (
          <BooksDiscover flash={flash} />
        ) : search ? (
          <SearchResults query={search} ctx={ctx} />
        ) : (
          <>
            {tab === "discover" && <DiscoverTab ctx={ctx} />}
            {tab === "movies" && <BrowseTab media="movie" ctx={ctx} />}
            {tab === "series" && <BrowseTab media="series" ctx={ctx} />}
            {tab === "requests" && <RequestsPanel />}
          </>
        )}
      </div>

      {toast && (
        <div className="fixed bottom-5 left-1/2 z-50 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>
          {toast}
        </div>
      )}
    </>
  );
}

interface RowCtx {
  doRequest: (c: DiscoverCard) => Promise<void>;
  isRequested: (c: DiscoverCard) => boolean;
}

function SearchResults({ query, ctx }: { query: string; ctx: RowCtx }) {
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
  useEffect(() => {
    let alive = true;
    setItems(null);
    api.discoverSearch(query).then((r) => alive && setItems(r)).catch(() => alive && setItems([]));
    return () => { alive = false; };
  }, [query]);

  return (
    <div>
      <h2 className="m-0 mb-3 text-[15px] font-bold">
        Results for “{query}” {items && <span className="font-normal text-ink-faint">· {items.length}</span>}
      </h2>
      {items === null ? (
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
          {Array.from({ length: 12 }).map((_, i) => <div key={i} className="rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />)}
        </div>
      ) : items.length === 0 ? (
        <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
          No movies or shows match “{query}”.
        </div>
      ) : (
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
          {items.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} full />)}
        </div>
      )}
    </div>
  );
}

function DiscoverTab({ ctx }: { ctx: RowCtx }) {
  return (
    <div className="flex flex-col gap-7">
      <PosterRow title="Trending this week" load={() => api.discoverTrending("all")} ctx={ctx} />
      <PosterRow title="Popular movies" load={() => api.discoverPopular("movie")} ctx={ctx} />
      <PosterRow title="Popular series" load={() => api.discoverPopular("series")} ctx={ctx} />
      <PosterRow title="Upcoming — request ahead" load={() => api.discoverUpcoming()} ctx={ctx} />
      <GenreExplorer media="movie" ctx={ctx} />
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

function PosterRow({ title, load, ctx }: { title: string; load: () => Promise<DiscoverCard[]>; ctx: RowCtx }) {
  const [items, setItems] = useState<DiscoverCard[] | null>(null);
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
          ? Array.from({ length: 8 }).map((_, i) => <div key={i} className="w-[150px] flex-none rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />)
          : items.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} />)}
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

function GenreExplorer({ media, ctx }: { media: "movie" | "series"; ctx: RowCtx }) {
  const [genres, setGenres] = useState<Genre[]>([]);
  const [active, setActive] = useState<Genre | null>(null);
  const [items, setItems] = useState<DiscoverCard[] | null>(null);

  useEffect(() => {
    api.discoverGenres(media).then(setGenres).catch(() => {});
    setActive(null); setItems(null);
  }, [media]);

  const pick = (g: Genre) => {
    setActive(g); setItems(null);
    api.discoverByGenre(media, g.id).then(setItems).catch(() => setItems([]));
  };

  if (genres.length === 0) return null;
  return (
    <div>
      <h2 className="m-0 mb-2.5 text-[15px] font-bold">Browse by genre</h2>
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
        <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" }}>
          {items === null
            ? Array.from({ length: 10 }).map((_, i) => <div key={i} className="rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />)
            : items.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} full />)}
        </div>
      )}
    </div>
  );
}

function badgeFor(c: DiscoverCard, requested: boolean): { label: string; tone: string; bg: string } | null {
  if (c.has_file) return { label: "Available", tone: "var(--good)", bg: "var(--good-soft, rgba(90,140,90,.18))" };
  if (requested || c.request_status === "pending") return { label: "Pending", tone: "var(--avoid)", bg: "var(--avoid-soft)" };
  if (c.in_library || c.request_status === "approved") return { label: "Processing", tone: "var(--accent)", bg: "var(--accent-soft)" };
  return null;
}

function MediaCard({ c, ctx, full }: { c: DiscoverCard; ctx: RowCtx; full?: boolean }) {
  const [open, setOpen] = useState(false);
  const requested = ctx.isRequested(c);
  const badge = badgeFor(c, requested);
  const requestable = !badge; // no badge → nothing in library/queue yet

  return (
    <>
      <button
        onClick={() => setOpen(true)}
        className={`group relative overflow-hidden rounded-xl text-left ${full ? "w-full" : "w-[150px] flex-none"}`}
        style={{ aspectRatio: "2/3", border: "1px solid var(--line)", background: "var(--panel-2)", scrollSnapAlign: "start" }}
      >
        {c.poster_url ? (
          <img src={c.poster_url} alt={c.title} className="h-full w-full object-cover" loading="lazy" />
        ) : (
          <div className="flex h-full w-full items-end p-2" style={{ background: "linear-gradient(150deg, hsl(24 40% 30%), hsl(20 35% 16%))" }}><span className="text-[12px] font-bold text-white">{c.title}</span></div>
        )}
        <span className="absolute left-1.5 top-1.5 rounded px-1.5 py-0.5 font-mono text-[8px] font-bold uppercase" style={{ background: "rgba(20,12,7,.72)", color: "#fff" }}>{c.media_type === "series" ? "TV" : "Movie"}</span>
        {badge && (
          <span className="absolute right-1.5 top-1.5 rounded-full px-1.5 py-0.5 font-mono text-[8px] font-bold uppercase" style={{ background: badge.bg, color: badge.tone }}>{badge.label}</span>
        )}
        <div className="absolute inset-x-0 bottom-0 flex flex-col gap-1 p-2 opacity-0 transition-opacity group-hover:opacity-100" style={{ background: "linear-gradient(to top, rgba(0,0,0,.9), transparent)" }}>
          <div className="truncate text-[11.5px] font-semibold text-white">{c.title}</div>
          <div className="flex items-center gap-2 text-[10px]" style={{ color: "rgba(255,255,255,.7)" }}>
            <span>{c.year || "—"}</span>
            {c.vote_average > 0 && <span style={{ color: "var(--accent)" }}>★ {c.vote_average.toFixed(1)}</span>}
          </div>
        </div>
      </button>
      {open && <RequestDetailModal c={c} requested={requested} requestable={requestable} onRequest={() => ctx.doRequest(c)} onClose={() => setOpen(false)} />}
    </>
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

function RequestDetailModal({ c, requested, requestable, onRequest, onClose }: { c: DiscoverCard; requested: boolean; requestable: boolean; onRequest: () => Promise<void>; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(requested);
  const [d, setD] = useState<MediaDetail | null>(null);
  const badge = badgeFor(c, done);

  useEffect(() => {
    let alive = true;
    api.mediaDetail(c.media_type, c.tmdb_id).then((r) => alive && setD(r)).catch(() => {});
    return () => { alive = false; };
  }, [c.media_type, c.tmdb_id]);

  const request = async () => {
    setBusy(true);
    try { await onRequest(); setDone(true); } finally { setBusy(false); }
  };

  const overview = d?.overview || c.overview;
  const runtime = d?.runtime ? `${Math.floor(d.runtime / 60)}h ${d.runtime % 60}m` : "";
  const director = crewByJob(d?.crew, "Director");
  const writer = crewByJob(d?.crew, "Writer");
  const producer = crewByJob(d?.crew, "Producer");
  const creator = crewByJob(d?.crew, "Creator");
  const r = d?.ratings;

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-4 sm:p-6" style={{ background: "rgba(0,0,0,.65)" }} onClick={onClose}>
      <div className="mt-6 w-full max-w-[680px] overflow-hidden rounded-2xl" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        {/* Backdrop hero */}
        <div className="relative h-[210px]" style={{ background: "var(--panel-2)" }}>
          {(d?.backdrop_url || c.backdrop_url) && <img src={d?.backdrop_url || c.backdrop_url} alt="" className="h-full w-full object-cover opacity-55" />}
          <div className="absolute inset-0" style={{ background: "linear-gradient(to top, var(--panel), transparent 75%)" }} />
          <button onClick={onClose} className="absolute right-2.5 top-2.5 grid h-7 w-7 place-items-center rounded-full" style={{ background: "rgba(20,12,7,.7)", color: "#fff" }}>✕</button>
        </div>

        <div className="flex gap-4 px-5 pt-0">
          <div className="-mt-16 h-[168px] w-[112px] flex-none overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
            {c.poster_url && <img src={c.poster_url} alt="" className="h-full w-full object-cover" />}
          </div>
          <div className="min-w-0 flex-1 pt-4">
            <div className="flex flex-wrap items-center gap-2">
              <span className="rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{c.media_type === "series" ? "TV" : "Movie"}</span>
              <h2 className="m-0 text-[17px] font-bold leading-tight">{c.title}</h2>
              <span className="font-mono text-[11.5px] text-ink-faint">{c.year || ""}</span>
            </div>
            {/* Meta line */}
            <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-faint">
              {d?.certification && <span className="rounded border px-1.5 py-px font-mono text-[10px]" style={{ borderColor: "var(--line)", color: "var(--ink-dim)" }}>{d.certification}</span>}
              {runtime && <span>{runtime}</span>}
              {d?.network && <span>{d.network}</span>}
              {d?.status && <span>{d.status}</span>}
              {d?.genres && d.genres.length > 0 && <span className="text-ink-dim">{d.genres.slice(0, 3).join(" · ")}</span>}
            </div>
            {/* Ratings */}
            <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
              {r?.tmdb ? <RatingBadge label="TMDB" value={r.tmdb.toFixed(1)} bg="var(--accent-soft)" fg="var(--accent)" /> : null}
              {r?.imdb ? <RatingBadge label="IMDb" value={r.imdb} bg="#f5c518" fg="#000" /> : null}
              {r?.rotten_tomatoes ? <RatingBadge label="RT" value={r.rotten_tomatoes} bg="#fa320a" fg="#fff" /> : null}
              {r?.metacritic ? <RatingBadge label="MC" value={r.metacritic} bg="#00658f" fg="#fff" /> : null}
            </div>
            {c.release_date && new Date(c.release_date) > new Date() && (
              <div className="mt-2 font-mono text-[10.5px]" style={{ color: "var(--avoid)" }}>Releases {c.release_date} — request ahead</div>
            )}
            <div className="mt-3">
              {badge && !requestable ? (
                <span className="inline-block rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: badge.bg, color: badge.tone }}>{badge.label === "Available" ? "✓ In your library" : badge.label === "Pending" ? "Requested — pending approval" : "Processing"}</span>
              ) : (
                <button onClick={request} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
                  {busy ? "Requesting…" : "＋ Request"}
                </button>
              )}
            </div>
          </div>
        </div>

        <div className="px-5 pb-5 pt-4">
          {overview && <p className="m-0 text-[13px] leading-relaxed text-ink-dim">{overview}</p>}

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
                {d.cast.slice(0, 12).map((p, i) => (
                  <div key={i} className="w-[76px] flex-none text-center">
                    <div className="mb-1 overflow-hidden rounded-lg" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }}>
                      {p.profile_url ? <img src={p.profile_url} alt={p.name} className="h-full w-full object-cover" loading="lazy" /> : null}
                    </div>
                    <div className="truncate text-[10.5px] font-semibold" title={p.name}>{p.name}</div>
                    {p.character && <div className="truncate text-[9.5px] text-ink-faint" title={p.character}>{p.character}</div>}
                  </div>
                ))}
              </div>
            </div>
          )}
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
