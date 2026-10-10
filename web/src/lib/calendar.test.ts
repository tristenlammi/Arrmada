import { describe, expect, it } from "vitest";
import type { CalendarItem } from "./api";
import { agendaRange, dayLabel, episodeCode, groupByDate, itemHref, itemLine, itemStatus, monthCells, ymd } from "./calendar";

const item = (over: Partial<CalendarItem>): CalendarItem => ({
  date: "2026-10-09", type: "episode", title: "Harbour Lights", subtitle: "S2 · E5 · The Breakwater", ref_id: 11,
  has_file: false, monitored: true, tmdb_id: 501, media_type: "series", season: 2, episode: 5, episode_title: "The Breakwater",
  ...over,
});

describe("groupByDate", () => {
  it("puts the days in date order and keeps each day's own order", () => {
    const groups = groupByDate([
      item({ date: "2026-11-02", title: "C" }),
      item({ date: "2026-10-31", title: "A" }),
      item({ date: "2026-11-02", title: "D" }),
      item({ date: "2026-11-01", title: "B" }),
    ]);
    expect(groups.map((g) => g.date)).toEqual(["2026-10-31", "2026-11-01", "2026-11-02"]);
    expect(groups[2].items.map((i) => i.title)).toEqual(["C", "D"]);
  });

  it("is empty for no items", () => {
    expect(groupByDate([])).toEqual([]);
  });
});

describe("dayLabel", () => {
  it("names the days around today, across a month boundary", () => {
    const today = new Date(2026, 9, 31, 22, 30); // late on 31 Oct
    expect(dayLabel("2026-10-30", today)).toBe("Yesterday");
    expect(dayLabel("2026-10-31", today)).toBe("Today");
    expect(dayLabel("2026-11-01", today)).toBe("Tomorrow");
    expect(dayLabel("2026-11-02", today)).toBe("Mon 2 Nov");
  });

  it("crosses a year boundary", () => {
    const today = new Date(2026, 11, 31, 8);
    expect(dayLabel("2027-01-01", today)).toBe("Tomorrow");
    expect(dayLabel("2027-01-05", today)).toBe("Tue 5 Jan");
  });
});

describe("item lines and links", () => {
  it("writes the episode code and name, or the movie's year", () => {
    expect(episodeCode(2, 5)).toBe("S02E05");
    expect(episodeCode(12, 105)).toBe("S12E105");
    expect(itemLine(item({}))).toBe("S02E05 · The Breakwater");
    expect(itemLine(item({ episode_title: "" }))).toBe("S02E05");
    expect(itemLine(item({ type: "movie", media_type: "movie", year: 2026 }))).toBe("Movie · 2026");
    // An older server without the fields: fall back to its subtitle.
    expect(itemLine(item({ season: undefined, episode: undefined }))).toBe("S2 · E5 · The Breakwater");
  });

  it("sends staff to the library page and requesters to Discover", () => {
    expect(itemHref(item({}), true)).toBe("/series/11");
    expect(itemHref(item({ type: "movie", media_type: "movie", ref_id: 22 }), true)).toBe("/movies/22");
    expect(itemHref(item({}), false)).toBe("/discover/series/501");
    expect(itemHref(item({ type: "movie", media_type: "movie", tmdb_id: 601 }), false)).toBe("/discover/movie/601");
    expect(itemHref(item({ tmdb_id: 0 }), false)).toBeNull();
  });

  it("says where an item stands", () => {
    expect(itemStatus(item({ has_file: true }), "2026-10-09")).toBe("Downloaded");
    expect(itemStatus(item({ monitored: false }), "2026-10-09")).toBe("Unmonitored");
    expect(itemStatus(item({ date: "2026-10-09" }), "2026-10-09")).toBe("Upcoming");
    expect(itemStatus(item({ date: "2026-10-08" }), "2026-10-09")).toBe("Wanted");
  });
});

describe("ranges", () => {
  it("covers six whole weeks from the Sunday before the 1st", () => {
    const cells = monthCells(new Date(2026, 9, 1)); // October 2026 starts on a Thursday
    expect(cells).toHaveLength(42);
    expect(ymd(cells[0])).toBe("2026-09-27");
    expect(ymd(cells[41])).toBe("2026-11-07");
  });

  it("starts the agenda yesterday and grows it six weeks at a time", () => {
    const today = new Date(2026, 9, 9);
    expect(agendaRange(today, 0)).toEqual({ start: "2026-10-08", end: "2026-11-20" });
    expect(agendaRange(today, 1)).toEqual({ start: "2026-10-08", end: "2027-01-01" });
  });
});
