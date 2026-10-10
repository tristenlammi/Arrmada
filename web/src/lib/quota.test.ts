import { describe, expect, it } from "vitest";
import type { MyQuota } from "./api";
import { quotaLeft, quotaLine } from "./quota";

const q = (over: Partial<MyQuota> = {}): MyQuota => ({
  days: 7, movie: { limit: 0, used: 0 }, season: { limit: 0, used: 0 }, book: { limit: 0, used: 0 }, ...over,
});

describe("request limits", () => {
  it("says nothing without a limit", () => {
    expect(quotaLeft(q(), "movie")).toBeNull();
    expect(quotaLine(q(), "movie")).toBe("");
    expect(quotaLine(null, "movie")).toBe("");
  });

  it("counts what's left in the window", () => {
    expect(quotaLine(q({ movie: { limit: 10, used: 7 } }), "movie")).toBe("3 movie requests left this week");
    expect(quotaLine(q({ season: { limit: 5, used: 4 } }), "season")).toBe("1 season left this week");
    expect(quotaLine(q({ days: 30, book: { limit: 2, used: 0 } }), "book")).toBe("2 book requests left in 30 days");
  });

  it("says when the next one frees up at zero", () => {
    const line = quotaLine(q({ movie: { limit: 2, used: 2, resets_at: "2026-10-14T09:00:00Z" } }), "movie");
    expect(line.startsWith("No movie requests left this week — the next frees up ")).toBe(true);
    expect(quotaLeft(q({ movie: { limit: 2, used: 3 } }), "movie")).toBe(0);
  });
});
