import { describe, expect, it } from "vitest";
import type { AttentionCounts } from "./api";
import { badgeFor } from "./useAttention";

const zero: AttentionCounts = { requests: 0, reviews: 0, downloads: 0, imports: 0, searches: 0, health: 0, health_errors: 0, total: 0 };

describe("badgeFor", () => {
  it("hides every pill at zero and before the first answer", () => {
    for (const b of ["requests", "review", "activity", "issues"] as const) {
      expect(badgeFor(b, zero)).toBeNull();
      expect(badgeFor(b, undefined)).toBeNull();
    }
  });

  it("counts requests, reviews, and downloads plus failing imports", () => {
    const c = { ...zero, requests: 2, reviews: 1, downloads: 2, imports: 1, total: 6 };
    expect(badgeFor("requests", c)).toMatchObject({ count: 2, tone: "accent" });
    expect(badgeFor("review", c)).toMatchObject({ count: 1, tone: "accent", label: "1 import needs review" });
    expect(badgeFor("activity", c)).toMatchObject({ count: 3, tone: "accent" });
  });

  it("shows health errors in red ahead of warnings in amber", () => {
    expect(badgeFor("issues", { ...zero, health: 3, health_errors: 1 })).toMatchObject({ count: 1, tone: "reject" });
    expect(badgeFor("issues", { ...zero, health: 2 })).toMatchObject({ count: 2, tone: "avoid", label: "2 health warnings" });
  });

  it("leaves stuck searches out of the pills (they're on the Dashboard card)", () => {
    const c = { ...zero, searches: 5, total: 5 };
    expect(["requests", "review", "activity", "issues"].map((b) => badgeFor(b as "requests", c))).toEqual([null, null, null, null]);
  });
});
