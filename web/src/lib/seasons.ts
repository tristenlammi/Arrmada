import type { SeriesSeason } from "./api";
import type { Tone } from "../ui";

// The words and rules of the season picker (the title sheet's "which seasons?") and the
// staff trim on a series request. Kept apart from the components so the rules can be
// tested and both lazy chunks read them the same way.

// seasonSelectable is whether a season can be ticked in the picker: one the server says a
// request may ask for, or one someone else already asked for (asking again follows their
// request). A season already here, already coming, not out yet or asked for by this
// viewer can't be.
export function seasonSelectable(s: SeriesSeason): boolean {
  return s.requestable || (s.state === "requested" && !s.request?.mine);
}

// seasonChip is the state chip on a season row; null for a plain requestable season.
// requested_by_name only ever reaches staff, so a requester never sees anyone's name.
export function seasonChip(s: SeriesSeason): { label: string; tone: Tone } | null {
  switch (s.state) {
    case "in_library":
      return { label: "In library ✓", tone: "good" };
    case "partial":
      return { label: `${s.have} of ${s.aired}`, tone: "faint" };
    case "on_the_way":
      return { label: "On the way", tone: "accent" };
    case "unaired":
      return { label: "Not out yet", tone: "faint" };
    case "requested":
      if (s.request?.mine) return { label: "You asked for this", tone: "avoid" };
      return { label: s.request?.requested_by_name ? `Requested by ${s.request.requested_by_name}` : "Requested", tone: "avoid" };
  }
  return null;
}

// seasonTitle is "Season 2", or "Season 2 · The Return" when TMDB names it something
// more than its number.
export function seasonTitle(s: Pick<SeriesSeason, "number" | "name">): string {
  const plain = `Season ${s.number}`;
  const name = s.name?.trim();
  return name && name.toLowerCase() !== plain.toLowerCase() ? `${plain} · ${name}` : plain;
}

// seasonMeta is the small line under a season: episode count and the year it aired.
export function seasonMeta(s: Pick<SeriesSeason, "episode_count" | "air_date">): string {
  const parts: string[] = [];
  if (s.episode_count > 0) parts.push(`${s.episode_count} episode${s.episode_count === 1 ? "" : "s"}`);
  if (s.air_date && /^\d{4}/.test(s.air_date)) parts.push(s.air_date.slice(0, 4));
  return parts.join(" · ");
}

// missingSeasons are the seasons nobody has asked for and the library doesn't have in
// full: what "All missing" ticks.
export function missingSeasons(seasons: SeriesSeason[]): number[] {
  return seasons.filter((s) => s.requestable).map((s) => s.number);
}

// latestSeason is the newest season that can still be asked for, or null.
export function latestSeason(seasons: SeriesSeason[]): number | null {
  const ns = missingSeasons(seasons);
  return ns.length ? Math.max(...ns) : null;
}

// pickLabel is the picker's submit button. null is the whole show.
export function pickLabel(pick: number[] | null): string {
  if (pick === null) return "Request all seasons";
  if (pick.length === 0) return "Tick a season";
  return `Request ${pick.length} season${pick.length === 1 ? "" : "s"}`;
}

// sameSeasons compares two season lists regardless of order.
export function sameSeasons(a: number[] | null, b: number[] | null): boolean {
  if (a === null || b === null) return a === b;
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((n) => set.has(n));
}
