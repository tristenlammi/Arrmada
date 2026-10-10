import type { CalendarItem } from "./api";

// Date and label helpers for the Calendar page, kept out of the component so they can be
// tested on their own. Every date here is a local calendar day: the API speaks
// YYYY-MM-DD with no time or zone, and so do these.

const DOW_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MON_SHORT = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function ymd(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** The local midnight of a YYYY-MM-DD day (new Date("2026-10-09") would be UTC midnight). */
export function parseYmd(s: string): Date {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y, (m || 1) - 1, d || 1);
}

export function addDays(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
}

/** The 6-week grid covering cursor's month, Sunday first, with the neighbours' days around it. */
export function monthCells(cursor: Date): Date[] {
  const first = new Date(cursor.getFullYear(), cursor.getMonth(), 1);
  const gridStart = addDays(first, -first.getDay());
  return Array.from({ length: 42 }, (_, i) => addDays(gridStart, i));
}

export interface DayGroup { date: string; items: CalendarItem[] }

/** Items grouped by day, the days in date order; within a day the server's order is kept. */
export function groupByDate(items: CalendarItem[]): DayGroup[] {
  const by = new Map<string, CalendarItem[]>();
  for (const it of items) {
    const list = by.get(it.date);
    if (list) list.push(it);
    else by.set(it.date, [it]);
  }
  return [...by.keys()].sort().map((date) => ({ date, items: by.get(date)! }));
}

/** 'Thu 16 Oct'. */
export function shortDay(date: string): string {
  const d = parseYmd(date);
  return `${DOW_SHORT[d.getDay()]} ${d.getDate()} ${MON_SHORT[d.getMonth()]}`;
}

/** 'Yesterday' / 'Today' / 'Tomorrow' near today, else 'Thu 16 Oct'. */
export function dayLabel(date: string, today: Date): string {
  const d = parseYmd(date);
  const t = new Date(today.getFullYear(), today.getMonth(), today.getDate());
  // Whole local days apart; rounding absorbs a daylight-saving hour.
  const diff = Math.round((d.getTime() - t.getTime()) / 86_400_000);
  if (diff === 0) return "Today";
  if (diff === 1) return "Tomorrow";
  if (diff === -1) return "Yesterday";
  return shortDay(date);
}

/** 'S02E05'. */
export function episodeCode(season: number, episode: number): string {
  return `S${String(season).padStart(2, "0")}E${String(episode).padStart(2, "0")}`;
}

/** The line under a title: 'S02E05 · Episode name' or 'Movie · 2026'. */
export function itemLine(it: CalendarItem): string {
  if (it.type === "movie") return it.year ? `Movie · ${it.year}` : "Movie";
  if (it.season == null || it.episode == null) return it.subtitle; // an older server
  const code = episodeCode(it.season, it.episode);
  return it.episode_title ? `${code} · ${it.episode_title}` : code;
}

/**
 * Where tapping an item goes: staff to the library page, everyone else to the title's
 * Discover page (the only title page a requester has). null when there's nowhere to go.
 */
export function itemHref(it: CalendarItem, staff: boolean): string | null {
  if (staff) return it.type === "movie" ? `/movies/${it.ref_id}` : `/series/${it.ref_id}`;
  if (!it.tmdb_id) return null;
  return `/discover/${it.media_type === "movie" ? "movie" : "series"}/${it.tmdb_id}`;
}

/** The library status a row shows, in the glossary's words (lib/status) plus Upcoming. */
export function itemStatus(it: CalendarItem, today: string): "Downloaded" | "Unmonitored" | "Upcoming" | "Wanted" {
  if (it.has_file) return "Downloaded";
  if (!it.monitored) return "Unmonitored";
  return it.date >= today ? "Upcoming" : "Wanted";
}

/** The agenda starts yesterday and runs six weeks, plus six more per 'Show more'. */
export const AGENDA_WEEKS = 6;
export function agendaRange(today: Date, extra: number): { start: string; end: string } {
  return { start: ymd(addDays(today, -1)), end: ymd(addDays(today, AGENDA_WEEKS * 7 * (extra + 1))) };
}
