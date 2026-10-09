import { describe, expect, it } from "vitest";
import { pickTab, withTab } from "./useTabParam";

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
    // A page offers only the tabs this viewer may see, so a link to another lands on the fallback.
    expect(pickTab("system", tabs, "media")).toBe("media");
  });
});

describe("withTab", () => {
  it("writes a tab other than the default", () => {
    expect(withTab(new URLSearchParams(""), "tab", "graphs", "activity").toString()).toBe("tab=graphs");
  });

  it("leaves the default out of the address", () => {
    expect(withTab(new URLSearchParams("tab=graphs"), "tab", "activity", "activity").toString()).toBe("");
  });

  it("keeps the page's other params", () => {
    expect(withTab(new URLSearchParams("q=dune&tab=movies"), "tab", "series", "discover").toString()).toBe("q=dune&tab=series");
  });

  it("uses the page's own key", () => {
    expect(withTab(new URLSearchParams("tab=x"), "media", "music", "movie").toString()).toBe("tab=x&media=music");
  });

  it("doesn't change the params it was given", () => {
    const p = new URLSearchParams("tab=graphs");
    withTab(p, "tab", "users", "activity");
    expect(p.toString()).toBe("tab=graphs");
  });
});
