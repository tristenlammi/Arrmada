import { useEffect, useRef, useState } from "react";
import { api, type ApiError, type PersonDetail } from "../lib/api";
import { useTitle } from "../lib/title";
import { Button, IconButton, Sheet } from "../ui";
import { CardSkeleton, GRID, MediaCard, useCloseOverlay, type RowCtx } from "./discover/shared";

// A person's page (REQ-20), at /discover/person/<id> over the page it was opened from: a
// cast tile, a director, or a name in search. Photo, department and biography, then what
// they're known for and everything they've made, each card with its library and request
// badge. The server keeps adult performers and adult titles out entirely. Its own chunk.

const sheetPanel = "flex h-full w-full flex-col overflow-hidden sm:h-auto sm:max-h-[92vh] sm:max-w-[920px] sm:rounded-2xl sm:shadow-panel";

export function PersonSheet({ id, ctx }: { id: number; ctx: RowCtx }) {
  const close = useCloseOverlay();
  const [p, setP] = useState<PersonDetail | null>(null);
  const [error, setError] = useState<{ status: number; message: string } | null>(null);
  const [bioOpen, setBioOpen] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  useTitle(p?.name);

  useEffect(() => {
    let alive = true;
    setP(null); setError(null); setBioOpen(false);
    scrollRef.current?.scrollTo({ top: 0 });
    api.discoverPerson(id)
      .then((r) => { if (alive) setP(r); })
      .catch((e) => { if (alive) setError({ status: (e as ApiError).status ?? 0, message: (e as Error).message }); });
    return () => { alive = false; };
  }, [id]);

  if (error) {
    return (
      <Sheet onClose={close} closeOnBack={false} ariaLabel="Not available" size="sm">
        <div className="p-6">
          <h2 className="m-0 text-[16px] font-bold">Not available</h2>
          <p className="m-0 mt-1.5 text-[12.5px] text-ink-dim">{error.status === 404 ? "This person can’t be shown here." : `Couldn’t load them — ${error.message}`}</p>
          <div className="mt-4 flex justify-end"><Button onClick={close}>Back</Button></div>
        </div>
      </Sheet>
    );
  }

  const credits = p?.credits ?? [];
  // Known for: their most popular work (the server's order). The filmography: newest first.
  const knownFor = credits.slice(0, 8);
  const byYear = [...credits].sort((a, b) => (b.year || 0) - (a.year || 0));
  const born = [p?.birthday, p?.place_of_birth].filter(Boolean).join(" · ");
  return (
    <Sheet onClose={close} closeOnBack={false} ariaLabel={p?.name || "Person"} handle={false} scrim={0.68} panelClassName={sheetPanel}>
      <IconButton label="Close" onClick={close} className="absolute right-3 z-20 h-8 w-8 rounded-full" style={{ top: "max(0.75rem, env(safe-area-inset-top, 0px))", background: "rgba(20,12,7,.7)", color: "#fff" }}>✕</IconButton>
      <div ref={scrollRef} className="flex-1 overflow-y-auto px-5 pb-6 pt-5 sm:px-6" style={{ paddingTop: "max(1.25rem, env(safe-area-inset-top, 0px))" }}>
        <div className="flex gap-4 pr-10">
          <div className="h-[150px] w-[100px] flex-none overflow-hidden rounded-xl sm:h-[186px] sm:w-[124px]" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
            {p?.profile_url ? <img src={p.profile_url} alt="" className="h-full w-full object-cover" /> : null}
          </div>
          <div className="min-w-0 flex-1 pt-1">
            <h2 className="m-0 text-[19px] font-bold leading-tight sm:text-[22px]">{p?.name || <span className="text-ink-faint">Loading…</span>}</h2>
            {p?.known_for_department && <div className="mt-1 font-mono text-[10.5px] uppercase text-ink-faint">{p.known_for_department}</div>}
            {born && <div className="mt-1 text-[11.5px] text-ink-dim">{born}</div>}
            {p?.biography && (
              <div className="mt-2.5">
                <p className={`m-0 whitespace-pre-line text-[12.5px] leading-relaxed text-ink-dim ${bioOpen ? "" : "line-clamp-4"}`}>{p.biography}</p>
                {!bioOpen && p.biography.length > 280 && (
                  <button onClick={() => setBioOpen(true)} className="mt-1 min-h-[28px] text-[12px] font-semibold" style={{ color: "var(--accent)" }}>More</button>
                )}
              </div>
            )}
          </div>
        </div>

        {p === null ? (
          <div className="mt-6 grid gap-x-3 gap-y-5" style={GRID}>{Array.from({ length: 8 }).map((_, i) => <CardSkeleton key={i} full />)}</div>
        ) : credits.length === 0 ? (
          <p className="mt-6 text-[12.5px] text-ink-dim">Nothing of theirs to show here yet.</p>
        ) : (
          <>
            <h3 className="m-0 mb-2.5 mt-6 text-[12px] font-bold uppercase tracking-wide text-ink-faint">Known for</h3>
            <div className="thin-scroll flex gap-3 overflow-x-auto pb-2">
              {knownFor.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} />)}
            </div>
            <h3 className="m-0 mb-2.5 mt-6 text-[12px] font-bold uppercase tracking-wide text-ink-faint">Filmography · {credits.length}</h3>
            <div className="grid gap-x-3 gap-y-5" style={GRID}>
              {byYear.map((c) => <MediaCard key={`${c.media_type}:${c.tmdb_id}`} c={c} ctx={ctx} full />)}
            </div>
          </>
        )}
      </div>
    </Sheet>
  );
}
