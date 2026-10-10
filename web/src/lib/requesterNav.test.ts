import { describe, expect, it } from "vitest";
import { requesterNav, sectionFor } from "./requesterNav";

const header = (external: boolean, booksEnabled: boolean) => requesterNav({ external, booksEnabled }).filter((n) => n.header).map((n) => n.label);
const tabs = (external: boolean, booksEnabled: boolean) => requesterNav({ external, booksEnabled }).filter((n) => n.tab).map((n) => n.tabLabel);

describe("requesterNav", () => {
  it("is the top bar at home and from outside", () => {
    expect(header(false, true)).toEqual(["Discover", "Requests", "Calendar", "My shelf", "Audiobooks"]);
    expect(header(true, true)).toEqual(["Discover", "Requests", "My shelf", "Audiobooks"]);
  });

  it("drops the shelf while the Books module is off", () => {
    expect(header(false, false)).toEqual(["Discover", "Requests", "Calendar", "Audiobooks"]);
    expect(tabs(true, false)).toEqual(["Discover", "Requests", "Listen", "Me"]);
  });

  // Requests has its own tab, so Calendar moves under Me and the bar stays at five.
  it("gives a phone at most five tabs, Me last, Calendar under Me", () => {
    for (const external of [false, true]) {
      const t = tabs(external, true);
      expect(t).toEqual(["Discover", "Requests", "Shelf", "Listen", "Me"]);
      expect(t.length).toBeLessThanOrEqual(5);
    }
  });

  it("names the section an address belongs to", () => {
    const nav = requesterNav({ external: false, booksEnabled: true });
    expect(sectionFor(nav, "/discover/movie/12")?.label).toBe("Discover");
    expect(sectionFor(nav, "/shelf")?.label).toBe("My shelf");
    expect(sectionFor(nav, "/me")?.label).toBe("Me");
    expect(sectionFor(nav, "/discovery")).toBeUndefined();
  });
});
