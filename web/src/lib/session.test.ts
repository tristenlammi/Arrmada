import { describe, expect, it } from "vitest";
import { sanitizeNext } from "./session";

describe("sanitizeNext", () => {
  it("keeps a same-site path", () => {
    expect(sanitizeNext("/movies/12")).toBe("/movies/12");
    expect(sanitizeNext("/discover?tab=books#top")).toBe("/discover?tab=books#top");
  });

  it("rejects anything that could leave the site", () => {
    for (const bad of ["//evil", "//evil.example/x", "/\\evil", "https://x", "http://evil.example", "javascript:alert(1)", "movies/12", "/\t/evil.example", "/x?u=https://evil"]) {
      expect(sanitizeNext(bad), bad).toBeNull();
    }
  });

  it("returns null for empty input", () => {
    expect(sanitizeNext("")).toBeNull();
    expect(sanitizeNext(null)).toBeNull();
    expect(sanitizeNext(undefined)).toBeNull();
  });
});
