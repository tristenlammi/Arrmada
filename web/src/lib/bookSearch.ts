import type { Book } from "./api";
import { formatCheckDay, notFoundYet } from "./format";

type SearchState = Pick<Book, "monitored" | "search_misses" | "last_search_at" | "next_search_at">;

// wantedCopy says honestly where a wanted edition stands: just added and being searched,
// searched and not found (with when it looks next), or not searched at all because the
// book isn't monitored. It used to say "Arrmada is searching" whatever the truth was.
export function wantedCopy(b: SearchState, label: string, now = Date.now()): string {
  if (!b.monitored) return `Wanted — not monitored, so the ${label.toLowerCase()} isn't being searched for.`;
  if ((b.search_misses ?? 0) > 0) return notFoundYet(b.next_search_at, b.last_search_at, now);
  return "Wanted — searching.";
}

// wantedTitle is the library's hover text for a "Wanted" book: when it looks next.
export function wantedTitle(b: SearchState, now = Date.now()): string | undefined {
  if (!b.monitored) return undefined;
  if ((b.search_misses ?? 0) > 0) return `Not found yet — next check ${formatCheckDay(b.next_search_at, now)}`;
  return "Searching";
}
