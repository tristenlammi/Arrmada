import { describe, expect, it } from "vitest";
import { stopLookingSummary } from "./stopLooking";

describe("stopLookingSummary", () => {
  it("counts what was stopped and why the rest was left", () => {
    expect(stopLookingSummary([
      { media_type: "movie", tmdb_id: 1, title: "A", stopped: true },
      { media_type: "movie", tmdb_id: 2, title: "B", stopped: false, reason: "not_approved" },
      { media_type: "series", tmdb_id: 3, title: "C", stopped: false, reason: "still_requested" },
      { media_type: "series", tmdb_id: 4, title: "D", stopped: false, reason: "still_requested" },
    ])).toBe("stopped looking for 1 title; 3 left as they were (2 still requested, 1 never approved)");
  });

  it("counts a show split over two batches once, as stopped when any batch stopped it", () => {
    expect(stopLookingSummary([
      { media_type: "series", tmdb_id: 3, title: "C", stopped: false, reason: "still_requested" },
      { media_type: "series", tmdb_id: 3, title: "C", stopped: true },
      { media_type: "movie", tmdb_id: 3, title: "Film", stopped: true },
    ])).toBe("stopped looking for 2 titles");
  });

  it("says nothing was stopped", () => {
    expect(stopLookingSummary([])).toBe("stopped looking for 0 titles");
  });
});
