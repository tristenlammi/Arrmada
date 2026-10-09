import { describe, expect, it } from "vitest";
import { pickTab } from "./useTabParam";

describe("pickTab", () => {
  const tabs = ["media", "library"] as const;

  it("opens a tab the viewer may see", () => {
    expect(pickTab("library", tabs, "media")).toBe("library");
  });

  it("falls back when ?tab= is missing or unknown", () => {
    expect(pickTab(null, tabs, "media")).toBe("media");
    expect(pickTab("foo", tabs, "media")).toBe("media");
  });

  it("falls back for a tab this viewer isn't offered", () => {
    // A manager's Settings offers only media and library, so a link to System lands on Media.
    expect(pickTab("system", tabs, "media")).toBe("media");
  });
});
