import { useEffect, useState } from "react";
import { api, type ApiError, type CollectionDetail } from "../lib/api";
import { useMe, isStaff } from "../lib/me";
import { useTitle } from "../lib/title";
import { Button, IconButton, Sheet } from "../ui";
import { badgeFor, CardSkeleton, GRID, MediaCard, useCloseOverlay, type RowCtx } from "./discover/shared";

// A movie collection's page (REQ-21), at /discover/collection/<id> over the page it was
// opened from: the backdrop, what the franchise is, how much of it the library has, and
// every member with its badge. Staff can ask for every missing released film at once;
// each still goes through the normal request (quota, approvals, notifications). Its own
// chunk.

const sheetPanel = "flex h-full w-full flex-col overflow-hidden sm:h-auto sm:max-h-[92vh] sm:max-w-[920px] sm:rounded-2xl sm:shadow-panel";
const today = () => new Date().toISOString().slice(0, 10);

export function CollectionSheet({ id, ctx }: { id: number; ctx: RowCtx }) {
  const close = useCloseOverlay();
  const { user } = useMe();
  const [c, setC] = useState<CollectionDetail | null>(null);
  const [error, setError] = useState<{ status: number; message: string } | null>(null);
  const [busy, setBusy] = useState(false);
  useTitle(c?.name);

  useEffect(() => {
    let alive = true;
    setC(null); setError(null);
    api.discoverCollection(id)
      .then((r) => { if (alive) setC(r); })
      .catch((e) => { if (alive) setError({ status: (e as ApiError).status ?? 0, message: (e as Error).message }); });
    return () => { alive = false; };
  }, [id]);

  if (error) {
    return (
      <Sheet onClose={close} closeOnBack={false} ariaLabel="Not available" size="sm">
        <div className="p-6">
          <h2 className="m-0 text-[16px] font-bold">Not available</h2>
          <p className="m-0 mt-1.5 text-[12.5px] text-ink-dim">{error.status === 404 ? "This collection can’t be shown here." : `Couldn’t load it — ${error.message}`}</p>
          <div className="mt-4 flex justify-end"><Button onClick={close}>Back</Button></div>
        </div>
      </Sheet>
    );
  }

  const items = c?.items ?? [];
  // What's out and nobody has or has asked for: what "Request the rest" would ask for.
  const missing = items.filter((m) => !badgeFor(m, ctx.isRequested(m)) && !!m.release_date && m.release_date <= today());
  const requestRest = async () => {
    setBusy(true);
    let asked = 0;
    for (const m of missing) {
      try { await ctx.doRequest(m, undefined, null, true); asked++; } catch { /* the next one may still go */ }
    }
    setBusy(false);
    ctx.flash(asked === missing.length ? `Requested ${asked} ${asked === 1 ? "film" : "films"}` : `Requested ${asked} of ${missing.length} — some couldn’t be asked for`, { tone: asked === missing.length ? undefined : "error" });
  };

  return (
    <Sheet onClose={close} closeOnBack={false} ariaLabel={c?.name || "Collection"} handle={false} scrim={0.68} panelClassName={sheetPanel}>
      <IconButton label="Close" onClick={close} className="absolute right-3 z-20 h-8 w-8 rounded-full" style={{ top: "max(0.75rem, env(safe-area-inset-top, 0px))", background: "rgba(20,12,7,.7)", color: "#fff" }}>✕</IconButton>
      <div className="flex-1 overflow-y-auto">
        <div className="relative h-[170px] sm:h-[240px]" style={{ background: "var(--panel-2)" }}>
          {c?.backdrop_url && <img src={c.backdrop_url} alt="" className="h-full w-full object-cover opacity-60" />}
          <div className="absolute inset-0" style={{ background: "linear-gradient(to top, var(--panel) 4%, transparent 78%)" }} />
          <div className="absolute inset-x-5 bottom-3 sm:inset-x-6">
            <h2 className="m-0 text-[20px] font-bold leading-tight sm:text-[24px]">{c?.name || <span className="text-ink-faint">Loading…</span>}</h2>
            {c && <div className="mt-1 text-[12px] font-semibold" style={{ color: "var(--accent)" }}>{c.owned} of {c.total} in the library</div>}
          </div>
        </div>
        <div className="px-5 pb-6 pt-3 sm:px-6">
          {c?.overview && <p className="m-0 text-[12.5px] leading-relaxed text-ink-dim">{c.overview}</p>}
          {isStaff(user) && ctx.canRequest && missing.length > 1 && (
            <div className="mt-3"><Button variant="primary" onClick={() => { void requestRest(); }} busy={busy} busyLabel="Requesting…">Request the rest ({missing.length})</Button></div>
          )}
          <div className="mt-5 grid gap-x-3 gap-y-5" style={GRID}>
            {c === null
              ? Array.from({ length: 6 }).map((_, i) => <CardSkeleton key={i} full />)
              : items.map((m) => <MediaCard key={m.tmdb_id} c={m} ctx={ctx} full />)}
          </div>
        </div>
      </div>
    </Sheet>
  );
}
