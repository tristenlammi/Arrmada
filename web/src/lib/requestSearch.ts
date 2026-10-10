import type { RequestTracking } from "./api";
import { formatAgo, notFoundYet } from "./format";

// searchingDetail is the line under a request that's still being searched for, as its
// requester reads it: whether anything has turned up and when it was last checked. Times
// only — which trackers were asked, what they offered and why it was turned down is staff
// business, and a requester can't infer it from this.
//
//   never checked:            "Looking for a release"
//   checked, nothing yet:     "Looking for a release (checked 5m ago)"
//   searches came up empty:   "Still looking — nothing suitable yet (checked 2h ago)"
//   a book on its ladder:     "Not found yet — last checked 3d ago, next check Tue 14 Oct"
//   a book slowed to monthly: "Couldn't find it yet — searching again later"
//   the server's own note:    "Not out yet"
export function searchingDetail(tr: RequestTracking, eps = "", now = Date.now()): string {
  if (tr.search_stopped) return "Couldn't find it yet — searching again later";
  if (tr.next_check_at) return notFoundYet(tr.next_check_at, tr.last_search_at, now);
  const lastMs = tr.last_search_at ? Date.parse(tr.last_search_at) : NaN;
  const checked = Number.isFinite(lastMs) ? ` (checked ${formatAgo(lastMs, now)})` : "";
  if (tr.note) return tr.note + checked;
  if ((tr.misses ?? 0) > 0) return `Still looking — nothing suitable yet${checked}`;
  return (eps ? `${eps} · looking for more` : "Looking for a release") + checked;
}
