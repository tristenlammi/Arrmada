import { useEffect, useRef, useState } from "react";
import type { SeriesSeason } from "../lib/api";
import { latestSeason, missingSeasons, pickLabel, sameSeasons, seasonChip, seasonMeta, seasonSelectable, seasonTitle } from "../lib/seasons";
import { quotaLeft, quotaLine, useQuota } from "../lib/quota";
import { Button, StatusChip } from "../ui";

// SeasonPicker is "which seasons?" in the title sheet when someone asks for a show. It
// starts on All seasons (the whole show; the server skips what's already here or coming
// and follows what someone else asked for). Ticking or unticking a season switches to
// just those. A season already here, on the way, not out yet or already asked for by
// this viewer can't be ticked, and its chip says why. Loaded with the sheet's first
// series request, not with Discover.
export function SeasonPicker({ seasons, busy, onSubmit, onCancel }: {
  seasons: SeriesSeason[];
  busy: boolean;
  /** null asks for the whole show; otherwise the season numbers ticked. */
  onSubmit: (pick: number[] | null) => void;
  onCancel: () => void;
}) {
  const [pick, setPick] = useState<number[] | null>(null);
  // On a phone the picker opens below the fold of the sheet: bring it, and its button,
  // into view once it has rendered (it loads lazily, so the opener can't do it).
  const ref = useRef<HTMLElement>(null);
  useEffect(() => { ref.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" }); }, []);
  const selectable = seasons.filter(seasonSelectable).map((s) => s.number);
  const missing = missingSeasons(seasons);
  const latest = latestSeason(seasons);
  // "All missing" only says something when part of the show is already here or asked for.
  const someCovered = seasons.some((s) => s.state !== "requestable" && s.state !== "unaired");
  const follows = (pick ?? []).some((n) => seasons.some((s) => s.number === n && s.state === "requested"));
  const ticked = (n: number) => (pick === null ? selectable.includes(n) : pick.includes(n));
  // A season limit, when there is one. It counts what the server counts: every season
  // asked for that isn't already asked for by someone or complete on disk — so following a
  // request is free, and All seasons counts the not-yet-aired and on-the-way ones too.
  const quota = useQuota();
  const left = quotaLeft(quota, "season");
  const counts = (s: SeriesSeason) => s.state !== "requested" && s.state !== "in_library";
  const counted = pick === null ? seasons.filter(counts).length : seasons.filter((s) => pick.includes(s.number) && counts(s)).length;
  const overQuota = left !== null && counted > left;
  const toggle = (n: number) => {
    const base = pick ?? selectable;
    setPick(base.includes(n) ? base.filter((x) => x !== n) : [...base, n].sort((a, b) => a - b));
  };

  const chip = (label: string, value: number[] | null) => {
    const on = sameSeasons(pick, value);
    return (
      <button
        type="button"
        aria-pressed={on}
        onClick={() => setPick(value)}
        className="min-h-[36px] rounded-full px-3 text-[12px] font-semibold"
        style={{ border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`, background: on ? "var(--accent-soft)" : "var(--panel)", color: on ? "var(--accent)" : "var(--ink-dim)" }}
      >
        {label}
      </button>
    );
  };

  return (
    <section ref={ref} aria-label="Choose seasons" className="scroll-mb-4 rounded-xl p-3 sm:p-4" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      <div className="mb-2 flex flex-wrap items-center gap-1.5">
        {chip("All seasons", null)}
        {someCovered && missing.length > 0 && chip("All missing", missing)}
        {latest !== null && missing.length > 1 && chip("Latest season", [latest])}
      </div>
      {seasons.length === 0 ? (
        <p className="m-0 py-2 text-[12px] text-ink-faint">No season list for this show yet — All seasons asks for the whole show.</p>
      ) : (
        // A long-running show scrolls inside the sheet instead of pushing the button away.
        <ul role="group" aria-label="Seasons" className="thin-scroll m-0 max-h-[45vh] list-none overflow-y-auto rounded-lg p-0" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
          {seasons.map((s, i) => {
            const can = seasonSelectable(s);
            const st = seasonChip(s);
            const meta = seasonMeta(s);
            return (
              <li key={s.number} style={{ borderTop: i ? "1px solid var(--line-soft)" : undefined }}>
                <label className={`flex min-h-[48px] items-center gap-3 px-3 py-2 ${can ? "cursor-pointer" : "cursor-default"}`} style={{ opacity: can ? 1 : 0.62 }}>
                  <input
                    type="checkbox"
                    checked={can && ticked(s.number)}
                    disabled={!can || busy}
                    onChange={() => toggle(s.number)}
                    className="h-[18px] w-[18px] flex-none accent-[var(--accent)]"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[12.5px] font-semibold" style={{ color: "var(--ink)" }}>{seasonTitle(s)}</span>
                    {meta && <span className="block text-[10.5px] text-ink-faint">{meta}</span>}
                  </span>
                  {st && <StatusChip tone={st.tone} size="xs" className="max-w-[45%] flex-none truncate">{st.label}</StatusChip>}
                </label>
              </li>
            );
          })}
        </ul>
      )}
      <p className="m-0 mt-2 text-[11px] text-ink-faint">
        {pick === null ? "The whole show. Anything already here or on the way is skipped." : follows ? "Only the seasons ticked. Where someone already asked for one, you’ll follow their request." : "Only the seasons ticked."}
      </p>
      {left !== null && (
        <p className="m-0 mt-1 text-[11px]" style={{ color: overQuota ? "var(--avoid-text)" : "var(--ink-faint)" }}>
          {quotaLine(quota, "season")}{overQuota && left > 0 ? ` — tick ${left === 1 ? "one" : `at most ${left}`}.` : ""}
        </p>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button variant="primary" className="min-h-[40px]" onClick={() => onSubmit(pick)} disabled={(pick !== null && pick.length === 0) || overQuota} busy={busy} busyLabel="Requesting…">
          {pickLabel(pick)}
        </Button>
        <Button variant="ghost" className="min-h-[40px]" onClick={onCancel} disabled={busy}>Cancel</Button>
      </div>
    </section>
  );
}
