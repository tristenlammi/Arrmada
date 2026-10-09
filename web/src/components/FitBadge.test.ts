import { describe, expect, it } from "vitest";
import { FIT_COLOR, bitrateColor, fitRank, fitTitle, hasIssue } from "./FitBadge";
import type { FileFit, FitItem, FitStatus } from "../lib/api";

const item = (fit?: FileFit, profile?: string) => ({ season: 0, facts: {}, fit, profile }) as unknown as FitItem;

describe("fitRank", () => {
  it("sorts the worst first and leaves unjudged files unranked", () => {
    const order: FitStatus[] = ["over", "under", "mismatch", "fits"];
    expect(order.map((status) => fitRank(item({ status })))).toEqual([0, 1, 2, 3]);
    expect(fitRank(undefined)).toBeUndefined();
    expect(fitRank(item(undefined))).toBeUndefined();
  });
});

describe("fitTitle", () => {
  it("explains a missing analysis or a missing ideal file", () => {
    expect(fitTitle(undefined)).toMatch(/^Not analysed yet/);
    expect(fitTitle(item(undefined))).toMatch(/no ideal file set up/);
  });

  it("lists the profile, every issue and the window that applied", () => {
    const fit: FileFit = {
      status: "over",
      window: { min: 8, max: 20 },
      issues: [{ kind: "bitrate", msg: "Bitrate 31 Mb/s is over 20" }, { kind: "codec", msg: "H.264, want HEVC" }],
    };
    expect(fitTitle(item(fit, "HD-1080p")).split("\n")).toEqual([
      "Profile: HD-1080p",
      "• Bitrate 31 Mb/s is over 20",
      "• H.264, want HEVC",
      "Bitrate window: 8–20 Mb/s",
    ]);
  });

  it("words one-sided windows", () => {
    expect(fitTitle(item({ status: "fits", window: { min: 0, max: 20 } }))).toBe("Bitrate window: up to 20 Mb/s");
    expect(fitTitle(item({ status: "fits", window: { min: 8, max: 0 } }))).toBe("Bitrate window: at least 8 Mb/s");
  });

  it("falls back to a plain fits line when there is nothing to list", () => {
    expect(fitTitle(item({ status: "fits" }))).toBe("Fits its profile's ideal file");
  });
});

describe("hasIssue", () => {
  it("matches issues by kind", () => {
    const fit: FileFit = { status: "mismatch", issues: [{ kind: "hdr", msg: "SDR" }] };
    expect(hasIssue(fit, "hdr")).toBe(true);
    expect(hasIssue(fit, "codec")).toBe(false);
    expect(hasIssue({ status: "fits" }, "hdr")).toBe(false);
    expect(hasIssue(undefined, "hdr")).toBe(false);
  });
});

describe("bitrateColor", () => {
  const window = { min: 8, max: 20 };
  it("is plain when no window applies", () => {
    expect(bitrateColor(undefined)).toBeUndefined();
    expect(bitrateColor({ status: "over" })).toBeUndefined();
  });

  it("colours over, under and inside the window", () => {
    expect(bitrateColor({ status: "over", window })).toBe(FIT_COLOR.over);
    expect(bitrateColor({ status: "under", window })).toBe(FIT_COLOR.under);
    // A mismatch on something other than bitrate still has a bitrate inside its window.
    expect(bitrateColor({ status: "mismatch", window })).toBe(FIT_COLOR.fits);
    expect(bitrateColor({ status: "fits", window })).toBe(FIT_COLOR.fits);
  });
});
