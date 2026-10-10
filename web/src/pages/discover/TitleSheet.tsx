import { Suspense, useEffect, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import { api, type ApiError, type CrewMember, type DiscoverCard, type MediaDetail, type SeriesSeason } from "../../lib/api";
import { lazyPage } from "../../lib/lazyPage";
import { posterThumb } from "../../lib/img";
import { titlePath } from "../../lib/refLink";
import { useTitle } from "../../lib/title";
import { Button, IconButton, Sheet, StatusChip, POSTER_CHIP_BG, TONE_HUE, type ToastFn } from "../../ui";
import { badgeFor, FOLLOWING, PosterPlaceholder, useCloseOverlay, useOpenOverlay, useOpenTitle, type OverlayState, type ReqStatus, type RowCtx } from "./shared";

// "Which seasons?" loads when someone first asks for a show.
const SeasonPicker = lazyPage(() => import("../../components/SeasonPicker"), "SeasonPicker");
// "3 movie requests left this week": its own chunk, only fetched when a sheet opens.
const QuotaHint = lazyPage(() => import("../../components/QuotaHint"), "QuotaHint");

// TitleSheet is the sheet for the title in the address. Opened from a poster it starts
// from that card; opened cold (a shared link, a notification, a reload) it starts from a
// bare id and fills in from the detail answer, which carries the card's badge state. It
// is its own chunk: Discover's first load doesn't carry the sheet until a title is opened.
export function TitleSheet({ media, tmdbId, ctx }: { media: "movie" | "series"; tmdbId: number; ctx: RowCtx }) {
  const location = useLocation();
  const close = useCloseOverlay();
  const st = location.state as OverlayState | null;
  const card = st?.card && st.card.media_type === media && st.card.tmdb_id === tmdbId ? st.card : stubCard(media, tmdbId);
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

// The longest note a requester can leave for the admin (the server refuses longer).
const NOTE_MAX = 500;

function RatingBadge({ label, value, bg, fg }: { label: string; value: string; bg: string; fg: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-bold" style={{ background: bg, color: fg }}>
      <span className="font-mono text-[8.5px] uppercase opacity-80">{label}</span>{value}
    </span>
  );
}

function crewByJob(crew: MediaDetail["crew"], job: string): CrewMember[] {
  return (crew ?? []).filter((c) => c.job === job);
}

// ---- Detail sheet -----------------------------------------------------------

// The sheet behind a title's address (TitleSheet). card is what to show until the
// detail arrives; "More like this" goes to that title's address, and the same sheet
// re-fetches for it rather than stacking another one.
// pick: open a show's sheet straight on "which seasons?" (its "＋ Request" was tapped).
function RequestDetailModal({ card, ctx, pick, onClose }: { card: DiscoverCard; ctx: RowCtx; pick?: boolean; onClose: () => void }) {
  const current = card;
  const titleKey = `${card.media_type}:${card.tmdb_id}`;
  const openTitle = useOpenTitle();
  // A cast or crew member opens their page over this one; Back returns here.
  const openOverlay = useOpenOverlay();
  const openPerson = (id: number) => openOverlay("person", id);
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
  const collection = d?.collection;
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
              {d?.plex_url && (
                <a href={d.plex_url} target="_blank" rel="noopener noreferrer" className="inline-block rounded-lg bg-accent-grad px-3.5 py-2 text-[12.5px] font-semibold text-accent-ink">▶ Watch on Plex</a>
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

          {/* The franchise it belongs to opens over this sheet; Back returns here. */}
          {collection && (
            <button onClick={() => openOverlay("collection", collection.id)} className="mt-3 min-h-[32px] text-[12.5px] font-semibold" style={{ color: "var(--accent)" }}>
              Part of the {collection.name} →
            </button>
          )}

          {/* Crew */}
          {(director.length > 0 || writer.length > 0 || producer.length > 0 || creator.length > 0) && (
            <div className="mt-4 grid grid-cols-2 gap-x-6 gap-y-2 text-[12px] sm:grid-cols-3">
              {creator.length > 0 && <CrewFact label="Creator" people={creator} onOpen={openPerson} />}
              {director.length > 0 && <CrewFact label="Director" people={director} onOpen={openPerson} />}
              {writer.length > 0 && <CrewFact label="Writer" people={writer} />}
              {producer.length > 0 && <CrewFact label="Producer" people={producer} />}
            </div>
          )}

          {/* Cast */}
          {d?.cast && d.cast.length > 0 && (
            <div className="mt-5">
              <h3 className="m-0 mb-2 text-[12px] font-bold uppercase tracking-wide text-ink-faint">Cast</h3>
              <div className="thin-scroll flex gap-3 overflow-x-auto pb-1">
                {d.cast.slice(0, 10).map((p, i) => (
                  // A tile with a person id opens their page; an old record without one stays a tile.
                  <CastTile key={i} id={p.id} name={p.name} onOpen={openPerson}>
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
                  </CastTile>
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

// CrewFact is one crew role and who held it; with onOpen, each name that has a person id
// opens their page.
function CrewFact({ label, people, onOpen }: { label: string; people: CrewMember[]; onOpen?: (id: number) => void }) {
  const names = people.map((p) => p.name).join(", ");
  return (
    <div className="min-w-0">
      <div className="font-mono text-[9.5px] uppercase text-ink-faint">{label}</div>
      <div className="truncate text-ink-dim" title={names}>
        {people.map((p, i) => (
          <span key={`${p.id ?? p.name}`}>
            {i > 0 && ", "}
            {onOpen && p.id ? (
              <button onClick={() => onOpen(p.id as number)} className="font-semibold underline-offset-2 hover:underline" style={{ color: "var(--accent)" }}>{p.name}</button>
            ) : p.name}
          </span>
        ))}
      </div>
    </div>
  );
}

// CastTile is one cast member: a button to their page when TMDB gave an id.
function CastTile({ id, name, onOpen, children }: { id?: number; name: string; onOpen: (id: number) => void; children: React.ReactNode }) {
  const cls = "w-[96px] flex-none text-center";
  return id ? <button onClick={() => onOpen(id)} aria-label={`Open ${name}`} className={cls}>{children}</button> : <div className={cls}>{children}</div>;
}
