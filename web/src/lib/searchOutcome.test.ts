import { describe, expect, it } from "vitest";
import type { Job, SearchAttempt, SearchOutcome } from "./api";
import { attemptLine, emptyTriesText, latestPerScope, nextTryText, outcomeLine, reasonsText, searchFinishedFor, searchJobLine } from "./searchOutcome";

const now = Date.parse("2026-10-09T12:00:00Z");
const attempt = (over: Partial<SearchAttempt>): SearchAttempt => ({
  id: 1, media_type: "movie", media_id: 4, scope: "", trigger: "sweep", started_at: now - 2 * 3600_000, duration_ms: 900,
  returned: 0, wrong_title: 0, blocklisted: 0, pending: 0, out_of_scope: 0, rejected: 0, eligible: 0, grabbed: 0,
  reasons: {}, top_reason: "", example: "", grabbed_titles: [], indexer_errors: {}, outcome: "nothing_found", reason: "no-releases",
  ...over,
});

describe("attemptLine", () => {
  it("says where the releases went, commonest reason first", () => {
    const a = attempt({ outcome: "none_suitable", returned: 15, reasons: { bitrate_ceiling: 12, wrong_title: 3 }, indexer_errors: { TorrentLeech: "login failed", "1337x": "timeout" } });
    expect(attemptLine(a, now)).toBe(
      "Last search 2 h ago — Found 15 releases — 12 over your bitrate ceiling, 3 for other titles · 2 indexers failed: 1337x, TorrentLeech",
    );
  });

  it("names a grab, an outage and a download in flight", () => {
    expect(attemptLine(attempt({ outcome: "grabbed", grabbed: 1, grabbed_titles: ["Arrival.2016.1080p"] }), now)).toBe("Last search 2 h ago — Grabbed Arrival.2016.1080p");
    expect(attemptLine(attempt({ outcome: "indexers_failed", indexer_errors: { B: "x", A: "y" } }), now)).toBe("Last search 2 h ago — Every indexer failed: A, B — not counted as a miss");
    expect(attemptLine(attempt({ outcome: "skipped_in_flight", example: "Show.S03.1080p" }), now)).toBe("Last search 2 h ago — Not searched — already downloading Show.S03.1080p");
    expect(attemptLine(attempt({}), now)).toBe("Last search 2 h ago — No releases found");
  });
});

describe("outcomeLine", () => {
  const base: SearchOutcome = { searched: true, returned: 34, matching: 14, usable: 14, grabbed: 0, reason: "all-blocklisted-or-below-profile" };
  it("words a finished Search now", () => {
    expect(outcomeLine({ ...base, reasons: { wrong_title: 20, bitrate_ceiling: 14 } })).toBe("Found 34 releases — 20 for other titles, 14 over your bitrate ceiling");
    expect(outcomeLine({ ...base, grabbed: 2, grabbed_titles: ["A", "B"], reason: "grabbed" })).toBe("Grabbed A and 1 more");
    expect(outcomeLine({ ...base, returned: 0, reason: "already-downloading", example: "X.2020" })).toBe("Not searched — already downloading X.2020");
  });
  it("leaves searches that never ran to the job's message", () => {
    expect(outcomeLine({ ...base, reason: "nothing-wanted" })).toBeNull();
    expect(outcomeLine({ ...base, reason: "already-searching" })).toBeNull();
  });
});

describe("searchJobLine", () => {
  const job = (over: Partial<Job>): Job => ({
    id: 1, kind: "movie.search", target: "movie:1", status: "succeeded", message: "12 releases found, none for this movie", error: "",
    created_at: null, started_at: null, finished_at: null, ...over,
  } as Job);
  it("prefers the outcome's own breakdown", () => {
    const result = { searched: true, returned: 12, matching: 0, usable: 0, grabbed: 0, reason: "none-for-this-title", reasons: { wrong_title: 12 } };
    expect(searchJobLine(job({ result }))).toBe("Found 12 releases — 12 for other titles");
  });
  it("falls back to the job's message or error", () => {
    expect(searchJobLine(job({ result: { reason: "nothing-wanted", returned: 0, grabbed: 0 }, message: "Nothing to search for" }))).toBe("Nothing to search for");
    expect(searchJobLine(job({ status: "failed", error: "all 2 indexers failed: A: down; B: down" }))).toBe("all 2 indexers failed: A: down; B: down");
  });
});

describe("summaries", () => {
  it("counts a run of empty searches", () => {
    expect(emptyTriesText({ empty_tries: 4, main_reason: "bitrate_ceiling" })).toBe("4 empty searches in a row, mostly over your bitrate ceiling");
    expect(emptyTriesText({ empty_tries: 1 })).toBe("");
  });
  it("says when the sweep looks next", () => {
    expect(nextTryText(new Date(now + 50 * 60_000).toISOString(), now)).toBe("next automatic search in 50 min");
    expect(nextTryText(new Date(now - 1000).toISOString(), now)).toBe("next automatic search on the next sweep");
    expect(nextTryText(undefined, now)).toBe("");
  });
  it("keeps the newest attempt per scope and leaves upgrades out", () => {
    const rows = latestPerScope([
      attempt({ id: 1, scope: "S01", started_at: 1 }),
      attempt({ id: 2, scope: "S01", started_at: 5 }),
      attempt({ id: 3, scope: "", started_at: 3 }),
      attempt({ id: 4, scope: "upgrade", started_at: 9 }),
    ]);
    expect(rows.map((r) => r.id)).toEqual([2, 3]);
  });
  it("caps the reasons listed", () => {
    expect(reasonsText({ a: 1, b: 5, c: 3, d: 2 }, 2)).toBe("5 b, 3 c");
  });
});

describe("searchFinishedFor", () => {
  it("matches only this title's finished searches", () => {
    const ev = { topic: "search.finished", data: { media_type: "series", media_id: 7, scope: "S03", outcome: "nothing_found" } };
    expect(searchFinishedFor(ev, "series", 7)).toBe(true);
    expect(searchFinishedFor(ev, "series", 8)).toBe(false);
    expect(searchFinishedFor(ev, "movie", 7)).toBe(false);
    expect(searchFinishedFor({ topic: "series.searched", data: { id: 7 } }, "series", 7)).toBe(false);
    expect(searchFinishedFor(null, "series", 7)).toBe(false);
  });
});
