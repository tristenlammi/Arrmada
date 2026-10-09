import type { Movie, MovieVersion } from "./api";
import type { Tone } from "../ui";

// One place decides what state a movie (or one of its tracks) is in, so the header chip,
// the grid, the table and the versions list can't contradict each other — a recorded file
// that's gone from disk is never "Downloaded" next to a "File missing" panel.
//
// The labels are kept in MOVIE_STATUS_LABELS so the shared status vocabulary (COPY-12) can
// replace them in one edit.

export type MovieStatusKey = "downloading" | "missing" | "downloaded" | "wanted" | "unmonitored";

export const MOVIE_STATUS_LABELS: Record<MovieStatusKey, string> = {
  downloading: "Downloading",
  missing: "File missing",
  downloaded: "Downloaded",
  wanted: "Wanted",
  unmonitored: "Unmonitored",
};

const TONES: Record<MovieStatusKey, Tone> = {
  downloading: "accent",
  missing: "avoid",
  downloaded: "good",
  wanted: "avoid",
  unmonitored: "faint",
};

// Text colour and soft fill per tone, matching StatusChip's panel surface.
const COLOR: Record<Tone, string> = {
  accent: "var(--accent-text)",
  good: "var(--good-text)",
  avoid: "var(--avoid-text)",
  reject: "var(--reject-text)",
  faint: "var(--ink-faint)",
};
const SOFT: Record<Tone, string> = {
  accent: "var(--accent-soft)",
  good: "var(--good-soft)",
  avoid: "var(--avoid-soft)",
  reject: "var(--reject-soft)",
  faint: "var(--panel-2)",
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

function make(key: MovieStatusKey): MovieStatus {
  const tone = TONES[key];
  return { key, label: MOVIE_STATUS_LABELS[key], tone, color: COLOR[tone], soft: SOFT[tone] };
}

// movieStatus is the movie's state, or one track's when track is given. A download in
// flight wins (it's what's happening now); a recorded file that's gone from disk comes
// before "Downloaded".
export function movieStatus(m: Pick<Movie, "has_file" | "monitored" | "file" | "download">, track?: Pick<MovieVersion, "has_file" | "monitored" | "file">): MovieStatus {
  if (!track && m.download) return make("downloading");
  const t = track ?? m;
  if (t.has_file && t.file?.missing) return make("missing");
  if (t.has_file) return make("downloaded");
  if (t.monitored) return make("wanted");
  return make("unmonitored");
}

// trackStatus is one version track's state (no download view: downloads are per movie).
export function trackStatus(t: Pick<MovieVersion, "has_file" | "monitored" | "file">): MovieStatus {
  return movieStatus({ has_file: t.has_file, monitored: t.monitored, file: t.file }, t);
}
