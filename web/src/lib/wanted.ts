// Words for the Wanted view (the Downloads page's Searching and Upcoming tabs): what is
// really happening to each wanted title, and when it was and will next be searched. The
// server sends states and times; the sentences are here, in one place.
import type { WantedRow } from "./api";
import { ago } from "./taskTime";
import { attemptLine, emptyTriesText, nextTryText } from "./searchOutcome";

// Where a row links: the title's own page (an album's is its album page).
export function wantedHref(r: WantedRow): string {
  switch (r.media_type) {
    case "series":
      return `/series/${r.id}`;
    case "book":
      return `/books/${r.id}`;
    case "music":
      return `/music/album/${r.id}`;
    default:
      return `/movies/${r.id}`;
  }
}

export interface WantedChip {
  text: string;
  tone: string;
  /** The pulsing dot: only for a search due on the sweep's next run. */
  pulse: boolean;
}

// The row's state word, right-aligned: never "Searching" for a title that is really
// waiting on a download, held for review, or unknown while the client is down.
export function wantedChip(r: WantedRow): WantedChip {
  switch (r.state) {
    case "unknown":
      return { text: "Status unknown", tone: "var(--ink-faint)", pulse: false };
    case "waiting_download":
      return r.stalled
        ? { text: "Waiting · stalled", tone: "var(--avoid)", pulse: false }
        : { text: "Waiting on download", tone: "var(--accent)", pulse: false };
    case "held_for_review":
      return { text: "Held for review", tone: "var(--avoid)", pulse: false };
    case "indexers_failed":
      return { text: "Indexers failed last try", tone: "var(--reject)", pulse: false };
    case "slowed":
      return { text: r.media_type === "music" ? "Checked weekly" : "Checked monthly", tone: "var(--ink-faint)", pulse: false };
    case "not_released":
      return { text: "Not released", tone: "var(--ink-faint)", pulse: false };
  }
  const n = r.episode_count ?? 0;
  const text = r.media_type === "series" ? `${n} episode${n === 1 ? "" : "s"}` : "Searching";
  return { text, tone: "var(--avoid)", pulse: !!r.due };
}

// The row's second line: why it isn't downloading. "Last search 3 h ago — Found 34
// releases — 20 for other titles, 14 over your bitrate ceiling · 6 empty searches in a
// row, mostly over your bitrate ceiling · next automatic search in 9 h", or "Waiting for
// the first search".
export function wantedLine(r: WantedRow, now = Date.now()): string {
  switch (r.state) {
    case "unknown":
      return "The download client isn't answering, so it may already be downloading — automatic searches are paused until it's back";
    case "waiting_download":
      return `Waiting on ${r.waiting_on ?? "a download"}${r.stalled ? " (stalled — nobody is sending data)" : ""} — not searched until it finishes or fails`;
    case "held_for_review":
      return "Its download finished but is held in Review — not searched again until it's decided";
  }
  const parts: string[] = [];
  const s = r.last_search;
  if (s) {
    let line = attemptLine(s.latest, now);
    if (s.latest.outcome === "indexers_failed") line += " — not counted as a miss";
    parts.push(line);
  } else if (r.last_search_at) {
    parts.push(`Last searched ${ago(r.last_search_at, now)}`);
  } else {
    parts.push("Waiting for the first search");
  }
  const empty = emptyTriesText(s);
  if (empty) parts.push(empty);
  if (r.waiting_note) parts.push(r.waiting_note);
  // A never-searched title due now says so once, not twice.
  const next = r.due && !s && !r.last_search_at ? "" : nextTryText(r.next_search_at, now);
  if (next) parts.push(next);
  return parts.join(" · ");
}

// A row's key in a list: kinds share id spaces.
export const wantedKey = (r: WantedRow) => `${r.media_type}:${r.id}`;

