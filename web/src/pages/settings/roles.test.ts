import { describe, expect, it } from "vitest";
import { requesterNav } from "../../lib/nav";
import { andList, managerExceptions, requesterPages } from "./roles";

describe("requesterNav", () => {
  const labels = (external: boolean, booksEnabled: boolean) => requesterNav({ external, booksEnabled }).map((n) => n.label);

  it("is the requester top bar at home and from outside", () => {
    expect(labels(false, true)).toEqual(["Discover", "Calendar", "Books", "Audiobooks"]);
    expect(labels(true, true)).toEqual(["Discover", "Books", "Audiobooks"]);
  });

  it("drops Books while the module is off", () => {
    expect(labels(false, false)).toEqual(["Discover", "Calendar", "Audiobooks"]);
    expect(labels(true, false)).toEqual(["Discover", "Audiobooks"]);
  });
});

describe("role legend", () => {
  it("lists a requester's pages, marking the at-home-only one", () => {
    expect(requesterPages(true)).toBe("Discover, Calendar (at home only), Books, Audiobooks");
    expect(requesterPages(false)).toBe("Discover, Calendar (at home only), Audiobooks");
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
