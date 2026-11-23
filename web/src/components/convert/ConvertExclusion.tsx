import { useState } from "react";
import { type ConvertTitleExclusion } from "../../lib/api";
import { convertApi } from "../../lib/convertApi";
import { invalidate, useQuery } from "../../lib/query";
import { Button, StatusChip, type MenuItem } from "../../ui";

// "Don't convert this": a film, a show or one season that Convert leaves alone. The same
// switch sits on the title's page (its menu, and a chip while it's on) and on Convert's plan
// card, so it can be seen and turned back off from either.

export const EXCLUSIONS_KEY = "convert-exclusions";

export type ExclusionTarget =
  | { kind: "movie"; id: number; name: string }
  | { kind: "series"; id: number; name: string; season?: number };

export function useConvertExclusions(enabled = true) {
  return useQuery<ConvertTitleExclusion[]>(EXCLUSIONS_KEY, () => convertApi.convertExclusions(), { enabled, staleMs: 30_000 });
}

/** isExcluded: the exact rule is on (a season's own rule, not its show's). */
export function isExcluded(list: ConvertTitleExclusion[] | undefined, t: ExclusionTarget): boolean {
  const season = t.kind === "series" && t.season !== undefined ? t.season : -1;
  return !!list?.some((x) => x.kind === t.kind && (t.kind === "movie" ? x.movie_id === t.id : x.series_id === t.id) && x.season === season);
}

function what(t: ExclusionTarget): string {
  if (t.kind === "movie") return "this film";
  return t.season !== undefined ? (t.season === 0 ? "the specials" : `season ${t.season}`) : "this show";
}

/** setExclusion turns the rule on or off and says what happened, in a sentence for a toast. */
export async function setExclusion(t: ExclusionTarget, excluded: boolean): Promise<string> {
  const r = await convertApi.setConvertExclusion({
    kind: t.kind,
    ...(t.kind === "movie" ? { movie_id: t.id } : { series_id: t.id, season: t.season }),
    excluded,
  });
  invalidate(EXCLUSIONS_KEY);
  invalidate("convert-plan:");
  if (!excluded) return `Convert can work on ${what(t)} again.`;
  let msg = `Convert will leave ${what(t)} alone.`;
  if (r.dropped > 0) msg += ` ${r.dropped} waiting file${r.dropped === 1 ? " was" : "s were"} taken off the queue.`;
  if (r.running) msg += ` “${r.running}” is already converting and will finish — cancel it under Convert if you'd rather it didn't.`;
  return msg;
}

/** exclusionMenuItem is the title menu's entry: "Don't convert this film" or "Convert this film again". */
export function exclusionMenuItem(t: ExclusionTarget, excluded: boolean, flash: (m: string, err?: boolean) => void): MenuItem {
  const noun = t.kind === "movie" ? "film" : "show";
  return {
    label: excluded ? `Let Convert work on this ${noun}` : `Don't convert this ${noun}`,
    title: excluded ? "Convert may re-encode and tidy this title's files again" : "Convert never re-encodes or tidies this title's files",
    busyLabel: "Saving…",
    onSelect: async () => {
      try { flash(await setExclusion(t, !excluded)); } catch (e) { flash((e as Error).message, true); }
    },
  };
}

/** LeftOutChip shows while a title (or season) is left out, with the way back. */
export function LeftOutChip({ target, excluded, flash, className = "mt-3" }: {
  target: ExclusionTarget; excluded: boolean; flash: (m: string, err?: boolean) => void; className?: string;
}) {
  const [busy, setBusy] = useState(false);
  if (!excluded) return null;
  const allow = async () => {
    setBusy(true);
    try { flash(await setExclusion(target, false)); } catch (e) { flash((e as Error).message, true); } finally { setBusy(false); }
  };
  return (
    <div className={`${className} flex flex-wrap items-center gap-2 text-sm text-ink-dim`} data-testid="convert-left-out">
      <StatusChip tone="faint">Not converted</StatusChip>
      <span>You told Convert to leave {what(target)} alone.</span>
      <Button size="sm" variant="secondary" onClick={allow} busy={busy} busyLabel="Saving…">Let Convert work on it</Button>
    </div>
  );
}
