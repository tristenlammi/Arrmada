import { describe, expect, it } from "vitest";
import { posterThumb } from "./img";

describe("posterThumb", () => {
  it("passes empty values through", () => {
    expect(posterThumb(undefined)).toBeUndefined();
    expect(posterThumb("")).toBe("");
  });

  it("shrinks TMDB posters to w342", () => {
    expect(posterThumb("https://image.tmdb.org/t/p/w500/abc.jpg")).toBe("https://image.tmdb.org/t/p/w342/abc.jpg");
    expect(posterThumb("https://image.tmdb.org/t/p/original/abc.jpg")).toBe("https://image.tmdb.org/t/p/w342/abc.jpg");
  });

  it("swaps Open Library large covers for medium ones", () => {
    expect(posterThumb("https://covers.openlibrary.org/b/id/123-L.jpg")).toBe("https://covers.openlibrary.org/b/id/123-M.jpg");
    expect(posterThumb("https://covers.openlibrary.org/b/id/123-L.PNG")).toBe("https://covers.openlibrary.org/b/id/123-M.PNG");
  });

  it("leaves other URLs alone", () => {
    expect(posterThumb("/api/v1/books/7/cover")).toBe("/api/v1/books/7/cover");
    expect(posterThumb("https://covers.openlibrary.org/b/id/123-S.jpg")).toBe("https://covers.openlibrary.org/b/id/123-S.jpg");
  });
});
