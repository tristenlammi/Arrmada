import { describe, expect, it } from "vitest";
import type { SearchAttempt, WantedRow } from "./api";
import { wantedChip, wantedHref, wantedLine } from "./wanted";

const now = Date.parse("2026-10-10T12:00:00Z");
const iso = (msFromNow: number) => new Date(now + msFromNow).toISOString();
const H = 3600_000;

const row = (over: Partial<WantedRow>): WantedRow => ({
  media_type: "movie", id: 4, title: "Arrival", year: 2016, quality_profile: "HD", state: "searching", search_misses: 0, ...over,
});
const attempt = (over: Partial<SearchAttempt>): SearchAttempt => ({
  id: 1, media_type: "movie", media_id: 4, scope: "", trigger: "sweep", started_at: now - 3 * H, duration_ms: 900,
  returned: 0, wrong_title: 0, blocklisted: 0, pending: 0, out_of_scope: 0, rejected: 0, eligible: 0, grabbed: 0,
  reasons: {}, top_reason: "", example: "", grabbed_titles: [], indexer_errors: {}, outcome: "nothing_found", reason: "no-releases",
  ...over,
});

describe("wantedLine", () => {
  it("waits for the first search", () => {
    expect(wantedLine(row({ due: true, next_search_at: iso(0) }), now)).toBe("Waiting for the first search");
  });

  it("says how the last search went, the empty run and the next try", () => {
    const r = row({
      last_search_at: iso(-3 * H), search_misses: 6, next_search_at: iso(9 * H),
      last_search: {
        latest: attempt({ outcome: "none_suitable", returned: 34, reasons: { wrong_title: 20, bitrate_ceiling: 14 } }),
        empty_tries: 6, main_reason: "bitrate_ceiling",
      },
    });
    expect(wantedLine(r, now)).toBe(
      "Last search 3 h ago — Found 34 releases — 20 for other titles, 14 over your bitrate ceiling · 6 empty searches in a row, mostly over your bitrate ceiling · next automatic search in 9 h",
    );
  });

  it("falls back to the sweep's own stamp when no attempt is stored", () => {
    expect(wantedLine(row({ last_search_at: iso(-2 * H), search_misses: 2, next_search_at: iso(-60_000), due: true }), now))
      .toBe("Last searched 2 h ago · next automatic search on the next sweep");
  });

  it("never calls an indexer outage a miss", () => {
    const r = row({ state: "indexers_failed", last_search: { latest: attempt({ outcome: "indexers_failed", indexer_errors: { TorrentLeech: "down" } }), empty_tries: 0 } });
    expect(wantedLine(r, now)).toBe("Last search 3 h ago — Every indexer failed: TorrentLeech — not counted as a miss");
  });

  it("names what a row is waiting on instead of searching", () => {
    expect(wantedLine(row({ media_type: "series", state: "waiting_download", waiting_on: "Show.S03.1080p", stalled: true }), now))
      .toBe("Waiting on Show.S03.1080p (stalled — nobody is sending data) — not searched until it finishes or fails");
    expect(wantedLine(row({ state: "held_for_review" }), now)).toMatch(/^Its download finished but is held in Review/);
    expect(wantedLine(row({ state: "unknown" }), now)).toMatch(/download client isn't answering/);
  });

  it("notes the seasons downloading while the rest of a show is searched", () => {
    expect(wantedLine(row({ media_type: "series", waiting_note: "S01 downloading", next_search_at: iso(H) }), now))
      .toBe("Waiting for the first search · S01 downloading · next automatic search in 60 min");
  });

  it("reads 'just now' after Search now", () => {
    const r = row({ last_search: { latest: attempt({ started_at: now - 1000 }), empty_tries: 1 } });
    expect(wantedLine(r, now)).toMatch(/^Last search just now — No releases found/);
  });
});

describe("wantedChip", () => {
  it("pulses only for a search due now", () => {
    expect(wantedChip(row({ due: true }))).toEqual({ text: "Searching", tone: "var(--avoid)", pulse: true });
    expect(wantedChip(row({ due: false })).pulse).toBe(false);
    expect(wantedChip(row({ media_type: "series", episode_count: 1 })).text).toBe("1 episode");
  });

  it("never says Searching for a title that isn't being searched", () => {
    expect(wantedChip(row({ state: "waiting_download", stalled: true })).text).toBe("Waiting · stalled");
    expect(wantedChip(row({ state: "held_for_review", due: true })).text).toBe("Held for review");
    expect(wantedChip(row({ state: "unknown", due: true }))).toMatchObject({ text: "Status unknown", pulse: false });
    expect(wantedChip(row({ state: "indexers_failed" })).text).toBe("Indexers failed last try");
    expect(wantedChip(row({ media_type: "book", state: "slowed" })).text).toBe("Checked monthly");
    expect(wantedChip(row({ media_type: "music", state: "slowed" })).text).toBe("Checked weekly");
  });
});

describe("wantedHref", () => {
  it("links each kind to its page", () => {
    expect(wantedHref(row({}))).toBe("/movies/4");
    expect(wantedHref(row({ media_type: "series", id: 7 }))).toBe("/series/7");
    expect(wantedHref(row({ media_type: "book", id: 8 }))).toBe("/books/8");
    expect(wantedHref(row({ media_type: "music", id: 9 }))).toBe("/music/album/9");
  });
});
