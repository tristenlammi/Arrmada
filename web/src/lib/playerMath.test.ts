import { describe, expect, it } from "vitest";
import {
  bookLength, chapterAt, chapterEndAt, clampRate, deviceName, fmtLeft, fmtTime, globalOf, holdFrom, locate,
  nextSectionStart, prevSectionStart, sections,
} from "./playerMath";

// Three files: 0–100, 100–250, 250–400.
const tracks = [
  { start_offset: 0, duration: 100 },
  { start_offset: 100, duration: 150 },
  { start_offset: 250, duration: 150 },
];
const chapters = [
  { start: 0, end: 60 },
  { start: 60, end: 180 },
  { start: 180, end: 400 },
];

describe("locate and globalOf", () => {
  it("round-trip across three files", () => {
    for (const t of [0, 1.5, 99.9, 100, 175, 249.99, 250, 333, 400]) {
      const { idx, offset } = locate(tracks, t);
      expect(globalOf(tracks, idx, offset)).toBeCloseTo(t, 6);
    }
  });
  it("puts a boundary in the file that starts there", () => {
    expect(locate(tracks, 100)).toEqual({ idx: 1, offset: 0 });
    expect(locate(tracks, 250)).toEqual({ idx: 2, offset: 0 });
    expect(locate(tracks, 99.5)).toEqual({ idx: 0, offset: 99.5 });
  });
  it("clamps before the start and past the end", () => {
    expect(locate(tracks, -5)).toEqual({ idx: 0, offset: 0 });
    expect(locate(tracks, 9999)).toEqual({ idx: 2, offset: 150 });
    expect(locate(tracks, Number.NaN)).toEqual({ idx: 0, offset: 0 });
    expect(locate([], 10)).toEqual({ idx: 0, offset: 0 });
  });
  it("measures the whole book", () => {
    expect(bookLength(tracks)).toBe(400);
    expect(bookLength([])).toBe(0);
  });
});

describe("chapters", () => {
  it("finds the chapter at its boundaries", () => {
    expect(chapterAt(chapters, 0)).toBe(0);
    expect(chapterAt(chapters, 59.99)).toBe(0);
    expect(chapterAt(chapters, 60)).toBe(1);
    expect(chapterAt(chapters, 180)).toBe(2);
    expect(chapterAt(chapters, 400)).toBe(2);
    expect(chapterAt([], 10)).toBe(-1);
  });
  it("steps back to this chapter's start, or the previous one near its start", () => {
    expect(prevSectionStart(chapters, 100)).toBe(60);
    expect(prevSectionStart(chapters, 61)).toBe(0);
    expect(prevSectionStart(chapters, 2)).toBe(0);
  });
  it("steps forward to the next chapter, none from the last", () => {
    expect(nextSectionStart(chapters, 10)).toBe(60);
    expect(nextSectionStart(chapters, 60)).toBe(180);
    expect(nextSectionStart(chapters, 300)).toBeNull();
  });
  it("falls back to the files for a book without chapter marks", () => {
    expect(sections([], tracks)).toEqual([{ start: 0, end: 100 }, { start: 100, end: 250 }, { start: 250, end: 400 }]);
    expect(sections(chapters, tracks)).toBe(chapters);
  });
});

describe("end-of-chapter sleep timer", () => {
  it("ends where the playing chapter ends", () => {
    expect(chapterEndAt(chapters, 10, 400)).toBe(60);
    expect(chapterEndAt(chapters, 60, 400)).toBe(180);
    expect(chapterEndAt(chapters, 179.9, 400)).toBe(180);
    expect(chapterEndAt(chapters, 399, 400)).toBe(400);
  });
  it("is the end of the book without chapters", () => {
    expect(chapterEndAt([], 10, 400)).toBe(400);
  });
  it("uses the next chapter's start when a chapter has no end", () => {
    expect(chapterEndAt([{ start: 0, end: 0 }, { start: 50, end: 0 }], 10, 400)).toBe(50);
    expect(chapterEndAt([{ start: 0, end: 0 }, { start: 50, end: 0 }], 70, 400)).toBe(400);
  });
});

describe("speed", () => {
  it("keeps to tenths between 0.8× and 3×", () => {
    expect(clampRate(1.25)).toBe(1.3);
    expect(clampRate(0.5)).toBe(0.8);
    expect(clampRate(5)).toBe(3);
    expect(clampRate(Number.NaN)).toBe(1);
    expect(clampRate(0)).toBe(1);
  });
});

describe("words", () => {
  it("formats clock times", () => {
    expect(fmtTime(0)).toBe("0:00");
    expect(fmtTime(65)).toBe("1:05");
    expect(fmtTime(3723)).toBe("1:02:03");
  });
  it("formats time left", () => {
    expect(fmtLeft(40)).toBe("40 s left");
    expect(fmtLeft(720)).toBe("12 min left");
    expect(fmtLeft(7500)).toBe("2 h 5 min left");
  });
  it("names the device, never the person", () => {
    expect(deviceName("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")).toBe("iPhone");
    expect(deviceName("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", true)).toBe("iPad");
    expect(deviceName("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")).toBe("Mac");
    expect(deviceName("Mozilla/5.0 (Linux; Android 14; Pixel 8)")).toBe("Android");
    expect(deviceName("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBe("Windows");
    expect(deviceName("Mozilla/5.0 (X11; Linux x86_64)")).toBe("Linux");
  });
});

describe("holdFrom", () => {
  it("reads a held jump back, a held jump ahead and listening again", () => {
    expect(holdFrom({ position: 3600, held_position: 120, finished: false })).toEqual({ position: 120, kind: "back" });
    expect(holdFrom({ position: 3600, held_position: 35900, finished: false })).toEqual({ position: 35900, kind: "forward" });
    expect(holdFrom({ position: 36000, held_position: 0, finished: true })).toEqual({ position: 0, kind: "again" });
    expect(holdFrom({ position: 3600, held_position: null, finished: false })).toBeNull();
  });
});
