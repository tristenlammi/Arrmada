// Book formats: a book request asks to read it (the ebook), listen to it (the
// audiobook), or both. The server stores the choice on the request, puts the book on
// the matching profile, and says "ready" per format.

export type BookFormats = "ebook" | "audiobook" | "both";

export const BOOK_FORMATS: readonly BookFormats[] = ["ebook", "audiobook", "both"];

// FORMAT_CHOICE is the request control's wording; FORMAT_BADGE the short badge on a
// request row.
export const FORMAT_CHOICE: Record<BookFormats, string> = {
  ebook: "Read (ebook)",
  audiobook: "Listen (audiobook)",
  both: "Both",
};
export const FORMAT_BADGE: Record<BookFormats, string> = { ebook: "Read", audiobook: "Listen", both: "Both" };

export function isBookFormats(v: unknown): v is BookFormats {
  return v === "ebook" || v === "audiobook" || v === "both";
}

// The viewer's last choice, so the next request starts there. Every storage access is
// guarded: a private window or blocked storage just means no memory.
const LAST_KEY = "arrmada.bookFormats";

export function lastBookFormats(): BookFormats | null {
  try {
    const v = localStorage.getItem(LAST_KEY);
    return isBookFormats(v) ? v : null;
  } catch {
    return null;
  }
}

export function rememberBookFormats(f: BookFormats): void {
  try { localStorage.setItem(LAST_KEY, f); } catch { /* storage blocked: nothing to remember */ }
}

// initialBookFormats is where the request control starts: the viewer's last choice,
// else the owner's default (from the server), else nothing picked yet.
export function initialBookFormats(serverDefault?: string): BookFormats | null {
  return lastBookFormats() ?? (isBookFormats(serverDefault) ? serverDefault : null);
}

// coversFormat reports whether a request's formats include one edition.
export function coversFormat(formats: string | undefined, edition: "ebook" | "audiobook"): boolean {
  return formats === "both" || formats === edition;
}

// The per-format state of a Discover card for a book already in the library.
export interface CardEditions {
  in_library: boolean;
  has_ebook?: boolean;
  has_audiobook?: boolean;
  request_status?: string;
  request_formats?: string;
}

// EditionState says what a card shows once the book has at least one edition on disk:
// the label ("Ebook ✓", "Ebook ✓ · Audiobook ✓") and, when the other format isn't here
// or already asked for, which one can still be requested.
export interface EditionState {
  label: string;
  missing?: "ebook" | "audiobook";
  // The other format is already asked for (pending or approved).
  requested?: "ebook" | "audiobook";
}

export function editionState(b: CardEditions): EditionState | null {
  if (!b.in_library) return null;
  const e = !!b.has_ebook;
  const a = !!b.has_audiobook;
  if (e && a) return { label: "Ebook ✓ · Audiobook ✓" };
  if (!e && !a) return null;
  const other: "ebook" | "audiobook" = e ? "audiobook" : "ebook";
  const label = e ? "Ebook ✓" : "Audiobook ✓";
  const open = b.request_status === "pending" || b.request_status === "approved";
  if (open && coversFormat(b.request_formats, other)) return { label, requested: other };
  return { label, missing: other };
}

// stillComingLine is the "on the way" line for a request partly here or not yet here.
export function stillComingLine(waiting: string | undefined): string {
  switch (waiting) {
    case "audiobook": return "Audiobook on the way";
    case "ebook": return "Ebook on the way";
    case "both": return "Ebook and audiobook on the way";
  }
  return "";
}
