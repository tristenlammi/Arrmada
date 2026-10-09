import type { Tone } from "../ui";

// The library status words — one meaning each, on every library and detail page:
//
//   Downloaded    a file is on disk (a movie, a book, a movie version)              good
//   Complete      every monitored episode or track is downloaded (series, albums)  good
//   Partial       some downloaded, more wanted                                     avoid
//   Wanted        monitored and no file yet: Arrmada is searching for it           avoid
//   Unmonitored   Arrmada won't search for it (a paused series or artist too)      faint
//   File missing  Arrmada recorded a file but it is gone from disk                 reject
//
// "Searching" and "Upcoming" belong to Downloads' live tabs and request cards only.
// Library filters use the same words: "Wanted" (not "Missing") means monitored with no
// file, or for a series/artist monitored and not complete; "Downloaded" (not "Available").
// Monitor toggles read "Monitored" or "Monitor" everywhere.

// STATUS_LABEL is the glossary: pages that need one of these words (lib/movieStatus) take
// it from here rather than spelling it again.
export const STATUS_LABEL = {
  downloaded: "Downloaded",
  complete: "Complete",
  partial: "Partial",
  wanted: "Wanted",
  unmonitored: "Unmonitored",
  fileMissing: "File missing",
} as const;

export type StatusLabel = (typeof STATUS_LABEL)[keyof typeof STATUS_LABEL];

// The tone each word always wears.
const TONE: Record<StatusLabel, Tone> = {
  Downloaded: "good",
  Complete: "good",
  Partial: "avoid",
  Wanted: "avoid",
  Unmonitored: "faint",
  "File missing": "reject",
};

export interface LibraryStatus {
  label: StatusLabel;
  /** For StatusChip. */
  tone: Tone;
  /** The text-tuned hue, for a bare status word on a panel. */
  color: string;
  /** The soft fill behind a pill. */
  soft: string;
}

const LOOK: Record<Tone, { color: string; soft: string }> = {
  accent: { color: "var(--accent-text)", soft: "var(--accent-soft)" },
  good: { color: "var(--good-text)", soft: "var(--good-soft)" },
  avoid: { color: "var(--avoid-text)", soft: "var(--avoid-soft)" },
  reject: { color: "var(--reject-text)", soft: "var(--reject-soft)" },
  faint: { color: "var(--ink-faint)", soft: "var(--panel-2)" },
};

// toneLook is a tone's text hue and soft fill, for a status word outside the glossary
// (a movie's "Downloading").
export function toneLook(tone: Tone): { color: string; soft: string } {
  return LOOK[tone];
}

function status(label: StatusLabel): LibraryStatus {
  const tone = TONE[label];
  return { label, tone, ...LOOK[tone] };
}

export interface StatusInput {
  hasFile: boolean;
  monitored: boolean;
  /** A file is recorded but gone from disk. */
  fileMissing?: boolean;
  /** For a multi-file item (series, album, artist): files on disk, and how many are wanted in all. */
  have?: number;
  total?: number;
  multi?: boolean;
}

// libraryStatus is the one status word for a library item. For a series or artist,
// Unmonitored wins over Partial: a paused show isn't searched, so nothing more is
// "wanted", and the Wanted filter (monitored and not complete) then matches exactly the
// items badged Wanted or Partial.
export function libraryStatus(x: StatusInput): LibraryStatus {
  const have = x.have ?? 0;
  const total = x.total ?? 0;
  if (x.fileMissing) return status(STATUS_LABEL.fileMissing);
  if (x.multi && total > 0 && have >= total) return status(STATUS_LABEL.complete);
  if (!x.multi && x.hasFile) return status(STATUS_LABEL.downloaded);
  if (!x.monitored) return status(STATUS_LABEL.unmonitored);
  if (have > 0) return status(STATUS_LABEL.partial);
  return status(STATUS_LABEL.wanted);
}

// isWanted is the library "Wanted" filter: the items whose badge says Wanted or Partial.
export function isWanted(x: StatusInput): boolean {
  const l = libraryStatus(x).label;
  return l === "Wanted" || l === "Partial";
}
