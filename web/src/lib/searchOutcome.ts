// Words for search outcomes (search_attempts, ACQ-15): the "why isn't it downloading?"
// lines on the detail pages and the result of a Search now. The server sends codes and
// counts; the sentences are ours, in one place.
import type { AttemptSummary, Job, SearchAttempt, SearchOutcome } from "./api";
import { ago, until } from "./taskTime";
import { jobFailed, jobToast } from "./useJob";

// A reject or drop code as the end of "12 …": "12 over your bitrate ceiling".
export const REASON_LABELS: Record<string, string> = {
  wrong_title: "for other titles",
  blocklisted: "blocklisted",
  pending: "already downloading",
  out_of_scope: "for other episodes or editions",
  not_torrent: "usenet only",
  not_wanted: "a format you don't want",
  rejected: "rejected by your profile",
  resolution: "not a resolution you want",
  source_floor: "below your minimum source",
  prerelease: "cam or screener copies",
  source_ceiling: "above your maximum source",
  bitrate_ceiling: "over your bitrate ceiling",
  seeders: "with too few seeders",
  rejected_term: "containing a rejected term",
  missing_required: "missing a required format",
  min_format_score: "below your minimum format score",
};

export function reasonLabel(code: string): string {
  return REASON_LABELS[code] ?? code.replace(/_/g, " ");
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;

// The reasons, commonest first: "20 for other titles, 14 over your bitrate ceiling".
export function reasonsText(reasons: Record<string, number> | undefined, max = 3): string {
  const parts = Object.entries(reasons ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, max)
    .map(([code, n]) => `${n} ${reasonLabel(code)}`);
  return parts.join(", ");
}

function failedIndexers(errs: Record<string, string> | undefined): string[] {
  return Object.keys(errs ?? {}).sort();
}

type Outcomeish = Pick<SearchAttempt, "returned" | "grabbed" | "reasons" | "example" | "indexer_errors"> & {
  grabbed_titles?: string[];
  error?: string;
};

// What one search came to, without the time: "Grabbed …", "Found 34 releases — 20 for
// other titles, 14 over your bitrate ceiling", "Every indexer failed: A, B", "Already
// downloading …".
function body(kind: SearchAttempt["outcome"], o: Outcomeish): string {
  const failed = failedIndexers(o.indexer_errors);
  let line: string;
  switch (kind) {
    case "grabbed": {
      const titles = o.grabbed_titles ?? [];
      line = titles.length > 0 ? `Grabbed ${titles[0]}${titles.length > 1 ? ` and ${titles.length - 1} more` : ""}` : `Grabbed ${plural(o.grabbed, "release")}`;
      break;
    }
    case "indexers_failed":
      return failed.length > 0 ? `Every indexer failed: ${failed.join(", ")}` : "Every indexer failed";
    case "skipped_in_flight":
      return o.example ? `Not searched — already downloading ${o.example}` : "Not searched — already downloading";
    case "error":
      return o.error ? `Search failed: ${o.error}` : "Search failed";
    case "nothing_found":
      line = "No releases found";
      break;
    default: {
      const why = reasonsText(o.reasons);
      line = `Found ${plural(o.returned, "release")}${why ? ` — ${why}` : ", none fit your profile"}`;
    }
  }
  if (failed.length > 0) line += ` · ${plural(failed.length, "indexer")} failed: ${failed.join(", ")}`;
  return line;
}

// A stored attempt as the detail page's line: "Last search 2 h ago — Found 15 releases —
// 12 over your bitrate ceiling · 2 indexers failed: X, Y". A search no indexer answered
// says it didn't count against the title: the sweep's backoff only counts real misses.
export function attemptLine(a: SearchAttempt, now = Date.now()): string {
  const tail = a.outcome === "indexers_failed" ? " — not counted as a miss" : "";
  return `Last search ${ago(a.started_at, now)} — ${body(a.outcome, a)}${tail}`;
}

// searchFinishedFor reports whether a live event is a search of this title finishing
// (search.finished, sent to staff for every stored attempt: the sweep's, RSS's, or a
// Search pressed on another device).
export function searchFinishedFor(ev: { topic: string; data: unknown } | null | undefined, kind: SearchAttempt["media_type"], id: number): boolean {
  if (!ev || ev.topic !== "search.finished") return false;
  const d = ev.data as { media_type?: string; media_id?: number } | null;
  return d?.media_type === kind && d.media_id === id;
}

// The kind a fresh outcome (a finished Search now job) comes to, the way the server
// stores it.
export function outcomeKind(o: SearchOutcome): SearchAttempt["outcome"] {
  if (o.grabbed > 0) return "grabbed";
  switch (o.reason) {
    case "already-downloading":
      return "skipped_in_flight";
    case "indexers-paused":
    case "indexers-failed":
    case "no-indexers":
      return "indexers_failed";
  }
  return o.returned === 0 ? "nothing_found" : "none_suitable";
}

// A finished Search now as one line. null when the search didn't run at all (nothing
// wanted, or already being searched): the job's own message says that better.
export function outcomeLine(o: SearchOutcome): string | null {
  if (o.reason === "nothing-wanted" || o.reason === "already-searching") return null;
  return body(outcomeKind(o), { ...o, reasons: o.reasons ?? {}, example: o.example ?? "", indexer_errors: o.indexer_errors ?? {} });
}

// A finished search job as the line its page shows: the outcome in the job's result when
// there is one ("Found 34 releases — 20 for other titles, 14 over your bitrate ceiling"),
// else the job's own message or error ("all 2 indexers failed: …").
export function searchJobLine(job: Job, fallback = "Search finished."): string {
  if (!jobFailed(job) && job.status !== "cancelled" && job.result && typeof job.result === "object" && "reason" in job.result) {
    const line = outcomeLine(job.result as SearchOutcome);
    if (line) return line;
  }
  return jobToast(job, fallback);
}

// The run of empty searches: "3 empty searches in a row, mostly over your bitrate
// ceiling". "" for fewer than two — one empty search is just the line above.
export function emptyTriesText(s: Pick<AttemptSummary, "empty_tries" | "main_reason"> | null | undefined): string {
  if (!s || s.empty_tries < 2) return "";
  const why = s.main_reason ? `, mostly ${reasonLabel(s.main_reason)}` : "";
  return `${s.empty_tries} empty searches in a row${why}`;
}

// When the sweep looks next: "next automatic search in 50 min", or "on the next sweep"
// when it's due. "" when no automatic search is coming.
export function nextTryText(next: string | null | undefined, now = Date.now()): string {
  if (!next) return "";
  const when = until(next, now);
  return when === "now" || when === "—" ? "next automatic search on the next sweep" : `next automatic search ${when}`;
}

// A series' or book's scope as people say it: "S03", "S03E04", "Ebook", "Audiobook".
export function scopeLabel(scope: string): string {
  if (scope === "") return "";
  if (scope === "ebook") return "Ebook";
  if (scope === "audiobook") return "Audiobook";
  if (scope === "album") return "Album";
  if (/^v\d+$/.test(scope)) return "Audio version";
  return scope;
}

// The newest attempt under each scope, newest first, upgrades left out: what a series or
// book page shows as its last searches.
export function latestPerScope(attempts: SearchAttempt[], max = 4): SearchAttempt[] {
  const seen = new Set<string>();
  const out: SearchAttempt[] = [];
  for (const a of [...attempts].sort((x, y) => y.started_at - x.started_at || y.id - x.id)) {
    if (a.scope === "upgrade" || seen.has(a.scope)) continue;
    seen.add(a.scope);
    out.push(a);
    if (out.length >= max) break;
  }
  return out;
}

// The tone a line is drawn in.
export function outcomeTone(kind: SearchAttempt["outcome"]): string {
  switch (kind) {
    case "grabbed":
      return "var(--good)";
    case "indexers_failed":
    case "error":
      return "var(--reject)";
    case "none_suitable":
    case "nothing_found":
      return "var(--avoid)";
    default:
      return "var(--ink-dim)";
  }
}
