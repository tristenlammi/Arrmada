import type { StoppedTitle } from "./api";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// Why a title was left as it was, in the toast's words.
const KEPT: Record<NonNullable<StoppedTitle["reason"]>, string> = {
  still_requested: "still requested",
  not_approved: "never approved",
  not_in_library: "not in the library",
  already_off: "already off",
  book: "left to the Books page",
  error: "couldn’t be changed",
};

// stopLookingSummary says what a bulk delete's "Also stop looking" did, over every batch it
// was sent in: "stopped looking for 287 titles; 13 left as they were (9 still requested,
// 4 never approved)". A show whose season requests were split over two batches
// is counted once: stopped if any batch stopped it.
export function stopLookingSummary(titles: StoppedTitle[]): string {
  const byTitle = new Map<string, StoppedTitle>();
  for (const t of titles) {
    const key = t.media_type === "book" ? `book:${t.ol_key ?? ""}` : `${t.media_type}:${t.tmdb_id ?? 0}`;
    const was = byTitle.get(key);
    if (!was || !was.stopped) byTitle.set(key, t);
  }
  let stopped = 0;
  const kept = new Map<string, number>();
  for (const t of byTitle.values()) {
    if (t.stopped) { stopped++; continue; }
    const why = KEPT[t.reason ?? "error"] ?? KEPT.error;
    kept.set(why, (kept.get(why) ?? 0) + 1);
  }
  const left = [...kept.values()].reduce((a, b) => a + b, 0);
  let out = `stopped looking for ${plural(stopped, "title")}`;
  if (left > 0) {
    const reasons = [...kept.entries()].sort((a, b) => b[1] - a[1]).map(([why, n]) => `${n} ${why}`).join(", ");
    out += `; ${left} left as ${left === 1 ? "it was" : "they were"} (${reasons})`;
  }
  return out;
}
