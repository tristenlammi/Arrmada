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

export type StatusLabel = "Downloaded" | "Complete" | "Partial" | "Wanted" | "Unmonitored" | "File missing";

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

function status(label: StatusLabel, tone: Tone): LibraryStatus {
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
  if (x.fileMissing) return status("File missing", "reject");
  if (x.multi && total > 0 && have >= total) return status("Complete", "good");
  if (!x.multi && x.hasFile) return status("Downloaded", "good");
  if (!x.monitored) return status("Unmonitored", "faint");
  if (have > 0) return status("Partial", "avoid");
  return status("Wanted", "avoid");
}

// isWanted is the library "Wanted" filter: the items whose badge says Wanted or Partial.
export function isWanted(x: StatusInput): boolean {
  const l = libraryStatus(x).label;
  return l === "Wanted" || l === "Partial";
}
