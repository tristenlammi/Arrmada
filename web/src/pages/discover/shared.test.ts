import { describe, expect, it } from "vitest";
import { baseOf, discoverPlace } from "./shared";

// Discover reads its address to know what's under the sheet (the rows or a browse grid)
// and what's open over it.
describe("discoverPlace", () => {
  it("reads the view and the overlay", () => {
    expect(discoverPlace("/discover")).toEqual({ browse: false, overlay: null });
    expect(discoverPlace("/discover/movie/603")).toEqual({ browse: false, overlay: { kind: "movie", id: 603 } });
    expect(discoverPlace("/discover/browse")).toEqual({ browse: true, overlay: null });
    expect(discoverPlace("/discover/browse/series/1399")).toEqual({ browse: true, overlay: { kind: "series", id: 1399 } });
    expect(discoverPlace("/discover/browse/person/287")).toEqual({ browse: true, overlay: { kind: "person", id: 287 } });
    expect(discoverPlace("/discover/collection/8091/")).toEqual({ browse: false, overlay: { kind: "collection", id: 8091 } });
  });

  it("ignores what isn't one of its addresses", () => {
    expect(discoverPlace("/discover/tv/1399").overlay).toBeNull();
    expect(discoverPlace("/discover/movie/abc").overlay).toBeNull();
    expect(discoverPlace("/requests").browse).toBe(false);
  });

  it("closes back to the view underneath", () => {
    expect(baseOf("/discover/browse/movie/1")).toBe("/discover/browse");
    expect(baseOf("/discover/person/2")).toBe("/discover");
  });
});
