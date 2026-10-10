import { useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import type { DiscoverCard, MediaRequest } from "../../lib/api";
import { posterThumb } from "../../lib/img";
import { useCanHover } from "../../lib/useCanHover";
import { StatusChip, type Tone, type ToastFn } from "../../ui";

// What Discover's pages share: the poster card and its badge, the row context requests go
// through, and the addresses of what opens over the page. Kept out of Discover.tsx so the
// lazily loaded pages (title sheet, browse grid, person, collection) can use them without
// importing the page itself.

// Discover's overlays: a title, a person or a collection opened over the page, each at its
// own address under the view it was opened from: /discover/movie/603 over the rows,
// /discover/browse/person/287 over a browse grid, which stays mounted (with what it
// loaded and where it was scrolled) underneath.
export type OverlayKind = "movie" | "series" | "person" | "collection";
const DISCOVER_PATH = /^\/discover(\/browse)?(?:\/(movie|series|person|collection)\/(\d+))?\/?$/;

export interface DiscoverPlace {
  browse: boolean;
  overlay: { kind: OverlayKind; id: number } | null;
}

export function discoverPlace(pathname: string): DiscoverPlace {
  const m = DISCOVER_PATH.exec(pathname);
  return { browse: !!m?.[1], overlay: m?.[2] ? { kind: m[2] as OverlayKind, id: Number(m[3]) } : null };
}

// The view under the overlays: the rows, or the browse grid.
export const baseOf = (pathname: string) => (discoverPlace(pathname).browse ? "/discover/browse" : "/discover");

// What an overlay's history entry carries when it was opened from the page: the card a
// title was opened from (shown at once, before the detail arrives), how many overlay
// entries are stacked above the page (so closing steps back over all of them), whether
// the first one was opened cold from a link (then there's no page underneath to step back
// to), and whether to start on "which seasons?".
export interface OverlayState {
  card?: DiscoverCard;
  depth?: number;
  cold?: boolean;
  pick?: boolean;
}

// useOpenOverlay opens a title, person or collection by going to its address, keeping the
// view's search (?tab=, ?q=, the browse filters) so closing it returns to the same view.
// Opening one from inside another (a cast member, "More like this") stacks another entry,
// so Back returns to the previous one.
export function useOpenOverlay(): (kind: OverlayKind, id: number, extra?: { card?: DiscoverCard; pick?: boolean }) => void {
  const navigate = useNavigate();
  const location = useLocation();
  return (kind, id, extra = {}) => {
    const on = !!discoverPlace(location.pathname).overlay;
    const st = (on ? location.state : null) as OverlayState | null;
    const state: OverlayState = { ...extra, depth: (st?.depth ?? 0) + 1, cold: on ? (st?.cold ?? !st?.depth) : false };
    navigate(`${baseOf(location.pathname)}/${kind}/${id}${location.search}`, { state });
  };
}

// useOpenTitle is useOpenOverlay for a card.
export function useOpenTitle(): (c: DiscoverCard, pick?: boolean) => void {
  const open = useOpenOverlay();
  return (c, pick = false) => open(c.media_type, c.tmdb_id, { card: c, pick });
}

// useCloseOverlay steps back over every overlay opened from the page, or, for one opened
// cold, replaces it with the view underneath.
export function useCloseOverlay(): () => void {
  const navigate = useNavigate();
  const location = useLocation();
  return () => {
    const st = location.state as OverlayState | null;
    const depth = st?.depth ?? 0;
    if (depth > 0 && !st?.cold) navigate(-depth);
    else navigate({ pathname: baseOf(location.pathname), search: location.search }, { replace: true });
  };
}

// The poster grid's columns: two on a phone, as many as fit above that.
export const GRID = { gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))" };

export type ReqStatus = MediaRequest["status"];

// What asking for a title someone already asked for says: you joined their request.
export const FOLLOWING = "You’re following this request — it’s in your requests now.";

export interface RowCtx {
  /** quiet: no toast for this one (a batch says how it went once, at the end). */
  doRequest: (c: DiscoverCard, note?: string, seasons?: number[] | null, quiet?: boolean) => Promise<{ subscribed: boolean; status: ReqStatus }>;
  /** What this session asked for the card, if anything: the status the server answered. */
  isRequested: (c: DiscoverCard) => ReqStatus | undefined;
  canRequest: boolean;
  flash: ToastFn;
}

// Compact inline error line for a failed fetch — distinct from a genuine empty result.
// The backend's message is surfaced verbatim. A missing TMDB key never gets here: the page
// shows one MetadataMissing message instead of an error per row.
export function LoadError({ message }: { message: string }) {
  return (
    <div className="rounded-lg px-3 py-2 text-[12px] font-medium" style={{ border: "1px solid var(--line)", color: "var(--reject)", background: "var(--panel)" }}>
      Couldn’t load — {message}
    </div>
  );
}

// A film/TV glyph placeholder for cards without artwork — reads as intentional, not a
// broken image. Neutral panel + centered icon + title, consistent everywhere.
export function PosterPlaceholder({ title, year }: { title: string; year?: number }) {
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

// Poster + caption skeleton, matched to a real card so loading → loaded has no shift.
export function CardSkeleton({ full }: { full?: boolean }) {
  return (
    <div className={full ? "w-full" : "w-[150px] flex-none"}>
      <div className="rounded-xl" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }} />
      <div className="mt-2 h-3 w-4/5 rounded" style={{ background: "var(--panel-2)" }} />
      <div className="mt-1.5 h-2.5 w-2/5 rounded" style={{ background: "var(--panel-2)" }} />
    </div>
  );
}

export function badgeFor(c: DiscoverCard, requested?: ReqStatus): { label: string; tone: Tone } | null {
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

export function MediaCard({ c, ctx, full }: { c: DiscoverCard; ctx: RowCtx; full?: boolean }) {
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
