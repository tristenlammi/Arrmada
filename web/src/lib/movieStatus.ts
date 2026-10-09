import type { Movie, MovieVersion } from "./api";
import type { Tone } from "../ui";
import { STATUS_LABEL, libraryStatus, toneLook, type StatusLabel } from "./status";

// One place decides what state a movie (or one of its tracks) is in, so the header chip,
// the grid, the table, the filters and the versions list can't contradict each other — a
// recorded file that's gone from disk is never "Downloaded" next to a "File missing" panel.
//
// The words and tones are the shared library vocabulary (lib/status, COPY-12); only
// "Downloading" and "Status unknown" are the movie's own: a movie's download is shown on
// its page, and while the download client can't be read (ACQ-23) a wanted film may
// already be downloading.

export type MovieStatusKey = "downloading" | "unknown" | "missing" | "downloaded" | "wanted" | "unmonitored";

export const MOVIE_STATUS_LABELS: Record<MovieStatusKey, string> = {
  downloading: "Downloading",
  unknown: "Status unknown",
  missing: STATUS_LABEL.fileMissing,
  downloaded: STATUS_LABEL.downloaded,
  wanted: STATUS_LABEL.wanted,
  unmonitored: STATUS_LABEL.unmonitored,
};

// A single movie or track never reads Complete or Partial (those are for series and
// albums), so only these glossary words map back to a key.
const KEY_OF: Partial<Record<StatusLabel, MovieStatusKey>> = {
  [STATUS_LABEL.fileMissing]: "missing",
  [STATUS_LABEL.downloaded]: "downloaded",
  [STATUS_LABEL.wanted]: "wanted",
  [STATUS_LABEL.unmonitored]: "unmonitored",
};

export interface MovieStatus {
  key: MovieStatusKey;
  label: string;
  /** The StatusChip tone. */
  tone: Tone;
  /** Text colour for a bare label. */
  color: string;
  /** Soft fill for a pill. */
  soft: string;
}

const DOWNLOADING_TONE: Tone = "accent";
const UNKNOWN_TONE: Tone = "faint";

// movieStatus is the movie's state, or one track's when track is given. A download in
// flight wins (it's what's happening now); everything else is libraryStatus's answer, so
// a recorded file that's gone from disk comes before "Downloaded". queueKnown false (the
// list's client_health: the download client can't be read) makes a movie that would read
// Wanted "Status unknown" instead.
export function movieStatus(m: Pick<Movie, "has_file" | "monitored" | "file" | "download">, track?: Pick<MovieVersion, "has_file" | "monitored" | "file">, queueKnown = true): MovieStatus {
  if (!track && m.download) {
    return { key: "downloading", label: MOVIE_STATUS_LABELS.downloading, tone: DOWNLOADING_TONE, ...toneLook(DOWNLOADING_TONE) };
  }
  const t = track ?? m;
  const s = libraryStatus({ hasFile: t.has_file, monitored: t.monitored, fileMissing: t.has_file && !!t.file?.missing });
  const key = KEY_OF[s.label] ?? "wanted";
  if (key === "wanted" && !track && !queueKnown) {
    return { key: "unknown", label: MOVIE_STATUS_LABELS.unknown, tone: UNKNOWN_TONE, ...toneLook(UNKNOWN_TONE) };
  }
  return { key, label: s.label, tone: s.tone, color: s.color, soft: s.soft };
}

// trackStatus is one version track's state (no download view: downloads are per movie).
export function trackStatus(t: Pick<MovieVersion, "has_file" | "monitored" | "file">): MovieStatus {
  return movieStatus({ has_file: t.has_file, monitored: t.monitored, file: t.file }, t);
}

// The Movies filters read the badge itself, so "Wanted" lists exactly the films badged
// Wanted and "Downloaded" exactly those badged Downloaded (COPY-12's invariant). A film
// with a download in flight is badged Downloading, and one whose state the download client
// can't confirm "Status unknown"; each sits in neither until it's known.
export function isMovieWanted(m: Pick<Movie, "has_file" | "monitored" | "file" | "download">, queueKnown = true): boolean {
  return movieStatus(m, undefined, queueKnown).key === "wanted";
}

export function isMovieDownloaded(m: Pick<Movie, "has_file" | "monitored" | "file" | "download">, queueKnown = true): boolean {
  return movieStatus(m, undefined, queueKnown).key === "downloaded";
}
