import type { MediaRequest } from "./api";
import { formatAgo, formatEta, notFoundYet } from "./format";
import type { Tone } from "../ui";

// One vocabulary for where a request has got to, shared by the Discover strip, the
// Requests page and the RequestSheet, so the three never word the same request
// differently.

// Order for a requester's strip: what's moving first, then what's waiting, then what's
// done. Staff use the server's order instead (waiting for approval first).
export const STAGE_ORDER: Record<string, number> = {
  downloading: 0, importing: 1, queued: 2, paused: 3, failed: 4, searching: 5, pending: 6,
  partial: 7, available: 8, declined: 9,
};

// Stages read off the download queue: while downloads can't be checked, these can't be told.
const QUEUE_STAGES = new Set(["searching", "queued", "downloading", "paused", "failed"]);

// Stages that move on their own, so a view showing one refreshes more often.
export const MOVING_STAGES = new Set(["downloading", "importing", "queued"]);

export interface RequestStage {
  badge: string;
  tone: Tone;
  detail: string;
  detailTone?: string;
  unknown?: boolean;
}

// requestStage turns a request's tracking into its badge and a one-line detail. queueKnown
// false (downloads can't be checked right now) makes a queue-read stage "Status unknown".
export function requestStage(rq: MediaRequest, queueKnown = true): RequestStage {
  const tr = rq.tracking;
  if (!queueKnown && QUEUE_STAGES.has(tr?.stage ?? "")) {
    return { badge: "Status unknown", tone: "faint", detail: "Can't check downloads right now", unknown: true };
  }
  const ready = rq.media_type === "book" ? "Ready" : "Ready to watch";
  const eps = tr?.total ? `${tr.have ?? 0} of ${tr.total} episodes` : "";
  const pct = Math.round((tr?.progress ?? 0) * 100);
  switch (tr?.stage) {
    case "available":
      return { badge: "Ready", tone: "good", detail: ready, detailTone: "var(--good-text)" };
    case "partial":
      return { badge: "Partly ready", tone: "good", detail: `${eps} ready`, detailTone: "var(--good-text)" };
    case "downloading": {
      const parts = [`${pct}%`];
      if (tr.eta_seconds) parts.push(`${formatEta(tr.eta_seconds)} left`);
      else if (tr.note) parts.push(tr.note.toLowerCase());
      if (eps) parts.push(eps);
      return { badge: "Downloading", tone: "accent", detail: parts.join(" · "), detailTone: "var(--accent-text)" };
    }
    case "importing":
      return { badge: "Importing", tone: "accent", detail: "Adding to the library…", detailTone: "var(--accent-text)" };
    case "queued":
      return { badge: "Starting", tone: "accent", detail: tr.note || "Starting the download" };
    case "paused":
      return { badge: "Paused", tone: "faint", detail: `Paused at ${pct}%` };
    case "failed":
      return { badge: "Retrying", tone: "avoid", detail: tr.note || "The download failed" };
    case "searching":
      // A book the searches keep missing: "Not found yet · next check Tue 14 Oct".
      if (tr.next_check_at) return { badge: "Searching", tone: "accent", detail: notFoundYet(tr.next_check_at) };
      return { badge: "Searching", tone: "accent", detail: tr.note || (eps ? `${eps} · looking for more` : "Looking for a release") };
    case "declined":
      return { badge: "Declined", tone: "reject", detail: "Declined" };
    case "pending":
      return { badge: "Pending", tone: "avoid", detail: "Waiting for approval" };
  }
  // No tracking (an older server): fall back to the plain status.
  return rq.available ? { badge: "Ready", tone: "good", detail: ready }
    : rq.status === "declined" ? { badge: "Declined", tone: "reject", detail: "Declined" }
    : rq.status === "approved" ? { badge: "Requested", tone: "accent", detail: "Looking for a release" }
    : { badge: "Pending", tone: "avoid", detail: "Waiting for approval" };
}

// sortForRequester puts a requester's strip in moving-first order, newest change first
// within a stage.
export function sortForRequester(items: MediaRequest[]): MediaRequest[] {
  return [...items].sort(
    (a, b) =>
      (STAGE_ORDER[a.tracking?.stage ?? ""] ?? 6) - (STAGE_ORDER[b.tracking?.stage ?? ""] ?? 6) ||
      b.updated_at.localeCompare(a.updated_at),
  );
}

// mediaLabel is the short type chip: Movie, TV or Book.
export function mediaLabel(t: MediaRequest["media_type"]): string {
  return t === "series" ? "TV" : t === "book" ? "Book" : "Movie";
}

// requestAge is how long ago a request was made, from its SQLite timestamp ("3d ago").
export function requestAge(at: string, now = Date.now()): string {
  const ms = Date.parse(at.includes("T") ? at : at.replace(" ", "T") + "Z");
  return Number.isFinite(ms) ? formatAgo(ms, now) : "";
}

// libraryPath is the staff library page a request became, when it has one.
export function libraryPath(rq: MediaRequest): string | null {
  if (!rq.library_id) return null;
  return rq.media_type === "movie" ? `/movies/${rq.library_id}` : rq.media_type === "series" ? `/series/${rq.library_id}` : `/books/${rq.library_id}`;
}
