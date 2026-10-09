import type { Movie, MovieVersion } from "./api";
import type { Tone } from "../ui";
import { STATUS_LABEL, libraryStatus, toneLook, type StatusLabel } from "./status";

// One place decides what state a movie (or one of its tracks) is in, so the header chip,
// the grid, the table, the filters and the versions list can't contradict each other — a
// recorded file that's gone from disk is never "Downloaded" next to a "File missing" panel.
//
// The words and tones are the shared library vocabulary (lib/status, COPY-12); only
// "Downloading" is the movie's own, because a movie's download is shown on its page.

export type MovieStatusKey = "downloading" | "missing" | "downloaded" | "wanted" | "unmonitored";

export const MOVIE_STATUS_LABELS: Record<MovieStatusKey, string> = {
  downloading: "Downloading",
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

// movieStatus is the movie's state, or one track's when track is given. A download in
// flight wins (it's what's happening now); everything else is libraryStatus's answer, so
// a recorded file that's gone from disk comes before "Downloaded".
export function movieStatus(m: Pick<Movie, "has_file" | "monitored" | "file" | "download">, track?: Pick<MovieVersion, "has_file" | "monitored" | "file">): MovieStatus {
  if (!track && m.download) {
    return { key: "downloading", label: MOVIE_STATUS_LABELS.downloading, tone: DOWNLOADING_TONE, ...toneLook(DOWNLOADING_TONE) };
  }
  const t = track ?? m;
  const s = libraryStatus({ hasFile: t.has_file, monitored: t.monitored, fileMissing: t.has_file && !!t.file?.missing });
  return { key: KEY_OF[s.label] ?? "wanted", label: s.label, tone: s.tone, color: s.color, soft: s.soft };
}

// trackStatus is one version track's state (no download view: downloads are per movie).
export function trackStatus(t: Pick<MovieVersion, "has_file" | "monitored" | "file">): MovieStatus {
  return movieStatus({ has_file: t.has_file, monitored: t.monitored, file: t.file }, t);
}

// The Movies filters read the badge itself, so "Wanted" lists exactly the films badged
// Wanted and "Downloaded" exactly those badged Downloaded (COPY-12's invariant). A film
// with a download in flight is badged Downloading and sits in neither until it lands.
export function isMovieWanted(m: Pick<Movie, "has_file" | "monitored" | "file" | "download">): boolean {
  return movieStatus(m).key === "wanted";
}

export function isMovieDownloaded(m: Pick<Movie, "has_file" | "monitored" | "file" | "download">): boolean {
  return movieStatus(m).key === "downloaded";
}
