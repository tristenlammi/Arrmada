import { describe, expect, it } from "vitest";
import { isWanted, libraryStatus } from "./status";

const label = (x: Parameters<typeof libraryStatus>[0]) => libraryStatus(x).label;

describe("libraryStatus", () => {
  it("names a single item by its file and monitoring", () => {
    expect(label({ hasFile: true, monitored: false })).toBe("Downloaded");
    expect(label({ hasFile: false, monitored: true })).toBe("Wanted");
    expect(label({ hasFile: false, monitored: false })).toBe("Unmonitored");
  });

  it("never calls a vanished file Downloaded", () => {
    const st = libraryStatus({ hasFile: true, monitored: true, fileMissing: true });
    expect(st.label).toBe("File missing");
    expect(st.tone).toBe("reject");
  });

  it("names a series or album by its counts", () => {
    expect(label({ multi: true, hasFile: true, monitored: true, have: 10, total: 10 })).toBe("Complete");
    expect(label({ multi: true, hasFile: true, monitored: true, have: 4, total: 10 })).toBe("Partial");
    expect(label({ multi: true, hasFile: false, monitored: true, have: 0, total: 10 })).toBe("Wanted");
    // A paused show isn't searched, so nothing more is wanted.
    expect(label({ multi: true, hasFile: true, monitored: false, have: 4, total: 10 })).toBe("Unmonitored");
    expect(label({ multi: true, hasFile: true, monitored: false, have: 10, total: 10 })).toBe("Complete");
  });

  it("filters Wanted to exactly the Wanted and Partial badges", () => {
    const items = [
      { hasFile: false, monitored: true },
      { hasFile: true, monitored: true },
      { hasFile: false, monitored: false },
      { hasFile: true, monitored: true, fileMissing: true },
      { multi: true, hasFile: true, monitored: true, have: 3, total: 9 },
      { multi: true, hasFile: true, monitored: false, have: 3, total: 9 },
      { multi: true, hasFile: true, monitored: true, have: 9, total: 9 },
    ];
    for (const x of items) {
      const l = label(x);
      expect(isWanted(x)).toBe(l === "Wanted" || l === "Partial");
    }
  });
});
