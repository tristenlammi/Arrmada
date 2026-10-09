import { describe, expect, it } from "vitest";
import { importListNotice } from "./api";

describe("importListNotice", () => {
  it("says nothing for a complete list", () => {
    expect(importListNotice({ truncated: false })).toBeNull();
    expect(importListNotice({})).toBeNull();
  });

  it("says the list is cut short", () => {
    expect(importListNotice({ truncated: true })).toBe("Showing the first 500 files — pick a narrower folder.");
  });

  it("prefers the server's note (a listing that ran out of time)", () => {
    expect(importListNotice({ truncated: true, note: "took too long" })).toBe("took too long");
  });
});
