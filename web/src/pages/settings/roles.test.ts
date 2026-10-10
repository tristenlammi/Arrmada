import { describe, expect, it } from "vitest";
import { andList, managerExceptions, requesterPages } from "./roles";

describe("role legend", () => {
  it("lists a requester's pages, marking the at-home-only one", () => {
    expect(requesterPages(true)).toBe("Discover, Requests, Calendar (at home only), My shelf, Audiobooks");
    expect(requesterPages(false)).toBe("Discover, Requests, Calendar (at home only), Audiobooks");
  });

  it("names what a manager can't open", () => {
    const line = managerExceptions();
    expect(line).toContain("Logs");
    expect(line).toContain("the audiobook server settings");
    expect(line).toContain("Settings → Downloads, Users, Import, System, Status");
    expect(line).not.toContain("Library,");
  });

  it("joins lists the way a sentence does", () => {
    expect(andList([])).toBe("");
    expect(andList(["a"])).toBe("a");
    expect(andList(["a", "b", "c"])).toBe("a, b and c");
  });
});
