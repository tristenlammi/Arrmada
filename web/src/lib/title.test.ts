import { describe, expect, it } from "vitest";
import { formatTitle } from "./title";

describe("formatTitle", () => {
  it("prefers what the page knows over the route's name", () => {
    expect(formatTitle("Dune", "Movie")).toBe("Dune · Arrmada");
  });
  it("falls back to the route's title, then the app name", () => {
    expect(formatTitle("", "Movies")).toBe("Movies · Arrmada");
    expect(formatTitle("", "")).toBe("Arrmada");
  });
});
