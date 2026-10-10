// Pure arithmetic behind the audiobook player: where a moment of the book is among its
// files, which chapter it's in, how long until a chapter ends, and the words for times.
// No DOM and no state, so it's all tested in playerMath.test.ts.

/** One audio file of a book: where it starts in the whole book and how long it is. */
export interface TrackSpan { start_offset: number; duration: number }
/** A chapter (or any section) of the book in book time. */
export interface Span { start: number; end: number }

/** The whole book's length: where the last file ends. */
export function bookLength(tracks: TrackSpan[]): number {
  const last = tracks[tracks.length - 1];
  return last ? last.start_offset + last.duration : 0;
}

/**
 * locate finds a moment of the book (seconds) among its files: the file it falls in and
 * how far into that file. A moment on a boundary belongs to the file that starts there;
 * past the end is the end of the last file.
 */
export function locate(tracks: TrackSpan[], t: number): { idx: number; offset: number } {
  if (tracks.length === 0) return { idx: 0, offset: 0 };
  const at = Math.max(0, Number.isFinite(t) ? t : 0);
  for (let i = tracks.length - 1; i >= 0; i--) {
    if (at >= tracks[i].start_offset) {
      return { idx: i, offset: Math.min(at - tracks[i].start_offset, tracks[i].duration) };
    }
  }
  return { idx: 0, offset: 0 };
}

/** globalOf is locate's inverse: a file and a spot in it, as a moment of the book. */
export function globalOf(tracks: TrackSpan[], idx: number, offset: number): number {
  const tr = tracks[idx];
  return tr ? tr.start_offset + Math.max(0, offset) : 0;
}

/**
 * chapterAt is the index of the chapter playing at t (-1 with no chapters). A moment on a
 * boundary is the chapter that starts there; before the first chapter reads as the first,
 * past the last as the last.
 */
export function chapterAt(chapters: Span[], t: number): number {
  if (chapters.length === 0) return -1;
  for (let i = chapters.length - 1; i >= 0; i--) {
    if (t >= chapters[i].start) return i;
  }
  return 0;
}

/**
 * sections is what "previous / next chapter" steps through: the chapters, or for a book
 * without any, its files (a book ripped one file per chapter has no chapter marks).
 */
export function sections(chapters: Span[], tracks: TrackSpan[]): Span[] {
  if (chapters.length > 0) return chapters;
  return tracks.map((tr) => ({ start: tr.start_offset, end: tr.start_offset + tr.duration }));
}

/**
 * prevSectionStart is where "previous chapter" goes: back to the start of this one, or
 * to the one before when you're within the first few seconds of this one — what a CD
 * player's back button does.
 */
export function prevSectionStart(list: Span[], t: number, grace = 3): number {
  const i = chapterAt(list, t);
  if (i < 0) return 0;
  if (t - list[i].start > grace || i === 0) return list[i].start;
  return list[i - 1].start;
}

/** nextSectionStart is where "next chapter" goes, or null in the last one. */
export function nextSectionStart(list: Span[], t: number): number | null {
  const i = chapterAt(list, t);
  if (i < 0 || i + 1 >= list.length) return null;
  return list[i + 1].start;
}

/**
 * chapterEndAt is where the chapter playing at t ends, in book time: the "end of chapter"
 * sleep timer's deadline. With no chapters it's the end of the book.
 */
export function chapterEndAt(chapters: Span[], t: number, length: number): number {
  const i = chapterAt(chapters, t);
  if (i < 0) return length;
  return chapters[i].end > chapters[i].start ? chapters[i].end : (chapters[i + 1]?.start ?? length);
}

/** Playback speeds the player offers: 0.8× to 3.0× in tenths. */
export const MIN_RATE = 0.8;
export const MAX_RATE = 3;

/** clampRate keeps a speed on the 0.1 grid and inside the offered range (1× when unreadable). */
export function clampRate(r: number): number {
  if (!Number.isFinite(r) || r <= 0) return 1;
  return Math.min(MAX_RATE, Math.max(MIN_RATE, Math.round(r * 10) / 10));
}

/** fmtTime is a clock time: 4:05, or 1:02:03 past an hour. */
export function fmtTime(sec: number): string {
  const s = Math.max(0, Math.floor(Number.isFinite(sec) ? sec : 0));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

/** fmtLeft is time remaining in words people say: "2 h 5 min left", "12 min left", "40 s left". */
export function fmtLeft(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return `${s} s left`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min left`;
  return `${Math.floor(m / 60)} h ${m % 60} min left`;
}

/**
 * deviceName names this browser's device the way the devices list and the admin's
 * Listening tab show it ("Web player · iPhone"). Only the kind of device: nothing that
 * identifies the person or what they're listening to.
 */
export function deviceName(ua: string, touchMac = false): string {
  if (/iPhone|iPod/.test(ua)) return "iPhone";
  // iPadOS asks for desktop sites, so an iPad says "Macintosh"; touch gives it away.
  if (/iPad/.test(ua) || (touchMac && /Macintosh/.test(ua))) return "iPad";
  if (/Android/.test(ua)) return "Android";
  if (/Macintosh|Mac OS X/.test(ua)) return "Mac";
  if (/Windows/.test(ua)) return "Windows";
  if (/CrOS/.test(ua)) return "Chromebook";
  if (/Linux/.test(ua)) return "Linux";
  return "Browser";
}

/** The kinds of held place a sync reply can describe (see holdFrom). */
export type HoldKind = "back" | "forward" | "again";

/**
 * holdFrom reads a sync reply: is the spot the player is at held by the place guards, and
 * which way? "again" is a finished book being listened to from the start ("Listen
 * again"), held until it's listened on a moment; "back" and "forward" are big jumps.
 */
export function holdFrom(r: { position: number; held_position: number | null; finished: boolean }): { position: number; kind: HoldKind } | null {
  if (r.held_position === null || r.held_position === undefined) return null;
  const kind: HoldKind = r.finished ? "again" : r.held_position > r.position ? "forward" : "back";
  return { position: r.held_position, kind };
}
