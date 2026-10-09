import { describe, expect, it } from "vitest";
import { formatAgo, formatBytes, formatCheckDay, formatDuration, formatEta, formatReleaseAge, notFoundYet } from "./format";

describe("formatReleaseAge", () => {
  const now = Date.parse("2026-10-09T12:00:00Z");
  it("reads in the largest sensible unit", () => {
    expect(formatReleaseAge("2026-10-09T11:50:00Z", now)).toBe("1 h");
    expect(formatReleaseAge("2026-10-09T07:00:00Z", now)).toBe("5 h");
    expect(formatReleaseAge("2026-10-06T12:00:00Z", now)).toBe("3 d");
    expect(formatReleaseAge("2026-06-01T12:00:00Z", now)).toBe("4 mo");
    expect(formatReleaseAge("2024-09-01T12:00:00Z", now)).toBe("2 y");
  });
  it("is blank when the indexer gave no date", () => {
    expect(formatReleaseAge(undefined, now)).toBe("");
    expect(formatReleaseAge("", now)).toBe("");
    expect(formatReleaseAge("garbage", now)).toBe("");
  });
});

describe("formatCheckDay", () => {
  const now = Date.parse("2026-10-09T12:00:00Z");
  it("names the day in the viewer's locale", () => {
    expect(formatCheckDay("2026-10-13T09:00:00Z", now, "en-GB")).toBe("Tue 13 Oct");
  });
  it("reads soon when due, overdue or unknown", () => {
    expect(formatCheckDay("2026-10-09T11:00:00Z", now)).toBe("soon");
    expect(formatCheckDay("", now)).toBe("soon");
    expect(formatCheckDay(undefined, now)).toBe("soon");
    expect(formatCheckDay("not a date", now)).toBe("soon");
  });
});

describe("notFoundYet", () => {
  const now = Date.parse("2026-10-09T12:00:00Z");
  it("says when it looks next, and when it last looked if known", () => {
    expect(notFoundYet("2026-10-08T09:00:00Z", undefined, now)).toBe("Not found yet · next check soon");
    expect(notFoundYet(undefined, "2026-10-06T12:00:00Z", now)).toBe("Not found yet — last checked 3d ago, next check soon");
  });
});

describe("formatBytes", () => {
  it("is 1024-based and dashes anything with no size", () => {
    expect(formatBytes(0)).toBe("—");
    expect(formatBytes(-5)).toBe("—");
    expect(formatBytes(NaN)).toBe("—");
    expect(formatBytes(undefined)).toBe("—");
    expect(formatBytes(1)).toBe("1 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1.5 * 1024 ** 3)).toBe("1.5 GB");
    expect(formatBytes(1.5 * 1024 ** 3, 2)).toBe("1.50 GB");
    expect(formatBytes(3 * 1024 ** 6)).toBe("3072.0 PB"); // capped at the largest unit
  });
});

describe("formatDuration", () => {
  it("uses the two largest units", () => {
    expect(formatDuration(0)).toBe("—");
    expect(formatDuration(-1)).toBe("—");
    expect(formatDuration(45)).toBe("45s");
    expect(formatDuration(60)).toBe("1m");
    expect(formatDuration(3600)).toBe("1h");
    expect(formatDuration(3600 + 40 * 60)).toBe("1h 40m");
    expect(formatDuration(86400)).toBe("1d");
    expect(formatDuration(2 * 86400 + 4 * 3600 + 59)).toBe("2d 4h");
  });
});

describe("formatAgo", () => {
  const now = 1_800_000_000_000;
  it("buckets into just now / m / h / d", () => {
    expect(formatAgo(0, now)).toBe("never");
    expect(formatAgo(now, now)).toBe("just now");
    expect(formatAgo(now - 59_999, now)).toBe("just now");
    expect(formatAgo(now - 60_000, now)).toBe("1m ago");
    expect(formatAgo(now - 3_599_000, now)).toBe("59m ago");
    expect(formatAgo(now - 3_600_000, now)).toBe("1h ago");
    expect(formatAgo(now - 86_399_000, now)).toBe("23h ago");
    expect(formatAgo(now - 86_400_000 * 3, now)).toBe("3d ago");
  });
  it("treats the future (clock skew) as just now", () => {
    expect(formatAgo(now + 500_000, now)).toBe("just now");
  });
});

describe("formatEta", () => {
  it("matches the request strip's wording", () => {
    expect(formatEta(0)).toBe("under a minute");
    expect(formatEta(59)).toBe("under a minute");
    expect(formatEta(60)).toBe("1m");
    expect(formatEta(89)).toBe("1m");
    expect(formatEta(90)).toBe("2m");
    expect(formatEta(59 * 60)).toBe("59m");
    expect(formatEta(3600)).toBe("1h 0m");
    expect(formatEta(3600 * 23 + 59 * 60)).toBe("23h 59m");
    expect(formatEta(86400 * 2 + 3600 * 4)).toBe("2d 4h");
  });
});
