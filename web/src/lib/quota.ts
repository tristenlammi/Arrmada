import { useEffect, useState } from "react";
import { api, type MyQuota } from "./api";

// Request limits as a requester sees them: "3 movie requests left this week". Only lazily
// loaded parts of the app (the quota hint, the season picker, the Requests page) import
// this, so it stays out of the first load.

export type QuotaKind = "movie" | "season" | "book";

const NOUN: Record<QuotaKind, [string, string]> = {
  movie: ["movie request", "movie requests"],
  season: ["season", "seasons"],
  book: ["book request", "book requests"],
};

/** How many more of kind fit now; null when there's no limit (or it isn't known). */
export function quotaLeft(q: MyQuota | null | undefined, kind: QuotaKind): number | null {
  const u = q?.[kind];
  if (!u || u.limit <= 0) return null;
  return Math.max(u.limit - u.used, 0);
}

const windowWords = (days: number) => (days === 7 ? "this week" : `in ${days} days`);

/** When the oldest use frees up, as "Tue 14 Oct". */
export function quotaResets(q: MyQuota | null | undefined, kind: QuotaKind): string {
  const at = q?.[kind]?.resets_at;
  if (!at) return "";
  const d = new Date(at);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
}

/** "3 movie requests left this week", "No seasons left this week — the next frees up
 *  Tue 14 Oct", or "" when there's no limit. */
export function quotaLine(q: MyQuota | null | undefined, kind: QuotaKind): string {
  const left = quotaLeft(q, kind);
  if (left === null || !q) return "";
  const [one, many] = NOUN[kind];
  if (left > 0) return `${left} ${left === 1 ? one : many} left ${windowWords(q.days)}`;
  const when = quotaResets(q, kind);
  return `No ${many} left ${windowWords(q.days)}${when ? ` — the next frees up ${when}` : ""}`;
}

/** useQuota loads the caller's limits once; null until (or unless) it answers. */
export function useQuota(): MyQuota | null {
  const [q, setQ] = useState<MyQuota | null>(null);
  useEffect(() => {
    let alive = true;
    api.myQuota().then((r) => { if (alive) setQ(r); }, () => {});
    return () => { alive = false; };
  }, []);
  return q;
}
