import { describe, expect, it } from "vitest";
import { queueCounts, queueLine, queuedNote } from "./movieQueue";

describe("movie search queue copy", () => {
  it("names the position only when others are ahead", () => {
    expect(queuedNote({ queued: true, position: 4 })).toBe("Queued (position 4)");
    expect(queuedNote({ queued: true, position: 1 })).toBe("");
    expect(queuedNote({ queued: true, position: 0 })).toBe("");
    expect(queuedNote(undefined)).toBe("");
  });

  it("words the header line", () => {
    expect(queueLine(2, 298)).toBe("Searching 2 · 298 queued");
    expect(queueLine(1, 0)).toBe("Searching 1");
    expect(queueLine(0, 3)).toBe("3 queued");
    expect(queueLine(0, 0)).toBe("");
  });

  it("reads counts from queue events only", () => {
    expect(queueCounts({ topic: "movie.search.started", data: { id: 1, kind: "search", running: 2, depth: 5 } })).toEqual({ running: 2, queued: 5 });
    expect(queueCounts({ topic: "release.grabbed", data: { running: 2, depth: 5 } })).toBeNull();
    expect(queueCounts({ topic: "movie.search.done", data: null })).toBeNull();
    expect(queueCounts(null)).toBeNull();
  });
});
