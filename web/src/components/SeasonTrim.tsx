import { useEffect, useState } from "react";
import { api, type MediaRequest, type SeriesSeason } from "../lib/api";
import { seasonChip, seasonTitle } from "../lib/seasons";
import { StatusChip } from "../ui";

// SeasonTrim is the staff "which seasons to approve" on a pending series request: the
// seasons it asked for (every season the show has, for a whole-show request), all ticked.
// Unticking one leaves it out of the approval. onChange gets null while everything is
// ticked (approve as asked, so a whole-show request stays whole) and the ticked seasons
// otherwise. A season already here, on the way or not out yet says so.
export function SeasonTrim({ rq, disabled, onChange }: {
  rq: MediaRequest;
  disabled?: boolean;
  onChange: (keep: number[] | null) => void;
}) {
  const [states, setStates] = useState<SeriesSeason[] | null>(null);
  const [off, setOff] = useState<Set<number>>(new Set());

  useEffect(() => {
    let alive = true;
    api.seriesSeasons(rq.tmdb_id).then((r) => { if (alive) setStates(r); }).catch(() => { if (alive) setStates([]); });
    return () => { alive = false; };
  }, [rq.tmdb_id]);

  const asked = rq.seasons?.length ? rq.seasons : (states ?? []).map((s) => s.number);
  // A whole-show request with no season list to show can only be approved as asked.
  if (asked.length === 0) return null;
  const byNumber = new Map((states ?? []).map((s) => [s.number, s]));

  const toggle = (n: number) => {
    const next = new Set(off);
    if (next.has(n)) next.delete(n); else next.add(n);
    setOff(next);
    onChange(next.size === 0 ? null : asked.filter((x) => !next.has(x)));
  };

  return (
    <fieldset className="m-0 flex flex-col gap-1 border-0 p-0 text-[12px]">
      <legend className="mb-1 p-0 font-mono text-[9.5px] uppercase text-ink-faint">
        Seasons to approve{!rq.seasons?.length && " · the whole show was asked for"}
      </legend>
      <div className="thin-scroll max-h-[40vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
        {asked.map((n, i) => {
          const s = byNumber.get(n);
          // "Requested" is this request itself: only the other states say anything here.
          const chip = s && s.state !== "requested" && s.state !== "requestable" ? seasonChip(s) : null;
          return (
            <label key={n} className="flex min-h-[44px] cursor-pointer items-center gap-3 px-3 py-1.5" style={{ borderTop: i ? "1px solid var(--line-soft)" : undefined }}>
              <input type="checkbox" checked={!off.has(n)} disabled={disabled} onChange={() => toggle(n)} className="h-[18px] w-[18px] flex-none accent-[var(--accent)]" />
              <span className="min-w-0 flex-1 truncate font-semibold">{seasonTitle({ number: n, name: s?.name })}</span>
              {chip && <StatusChip tone={chip.tone} size="xs" className="flex-none">{chip.label}</StatusChip>}
            </label>
          );
        })}
      </div>
      {off.size > 0 && off.size < asked.length && <span className="text-[11px] text-ink-faint">The rest won’t be approved; the requester is told which.</span>}
      {off.size === asked.length && <span className="text-[11px] font-medium" style={{ color: "var(--avoid-text)" }}>Tick at least one season, or decline the request.</span>}
    </fieldset>
  );
}
