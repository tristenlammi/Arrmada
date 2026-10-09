import { describe, expect, it } from "vitest";
import { NAV, crumbFor, visibleNav } from "./nav";

const tos = (viewer: Parameters<typeof visibleNav>[0]) => visibleNav(viewer).flatMap((g) => g.items.map((i) => i.to));
const all = { admin: true, booksEnabled: true, musicEnabled: true };

describe("visibleNav", () => {
  it("drops Books while the Books module is off", () => {
    expect(tos(all)).toContain("/books");
    expect(tos({ ...all, booksEnabled: false })).not.toContain("/books");
  });

  it("drops Music while the Music module is off", () => {
    expect(tos(all)).toContain("/music");
    expect(tos({ ...all, musicEnabled: false })).not.toContain("/music");
  });

  it("keeps admin-only pages from managers", () => {
    expect(tos(all)).toContain("/logs");
    expect(tos({ ...all, admin: false })).not.toContain("/logs");
  });

  it("drops a group left empty", () => {
    const nav = [{ group: "Library", items: [{ to: "/music", label: "Music", icon: "music" as const, module: "music" as const }] }];
    expect(visibleNav({ ...all, musicEnabled: false }, nav)).toEqual([]);
  });
});

describe("NAV", () => {
  it("groups the sidebar as Home, Activity, Library, Tools, Plex, System", () => {
    expect(NAV.map((g) => g.group)).toEqual(["", "Activity", "Library", "Tools", "Plex", "System"]);
  });

  it("gives every entry an icon and a unique address", () => {
    const items = NAV.flatMap((g) => g.items);
    for (const i of items) expect(i.icon, i.to).toBeTruthy();
    expect(new Set(items.map((i) => i.to)).size).toBe(items.length);
  });
});

describe("crumbFor", () => {
  it("calls the dashboard Home", () => {
    expect(crumbFor("/")).toBe("Home");
  });

  it("gives detail pages their sidebar parent", () => {
    expect(crumbFor("/movies/12")).toBe("Library / Movies");
    expect(crumbFor("/books/author/x")).toBe("Library / Books");
    expect(crumbFor("/music/album/3")).toBe("Library / Music");
  });

  it("names the group the sidebar shows the page in", () => {
    expect(crumbFor("/quality")).toBe("System / Quality profiles");
    expect(crumbFor("/convert")).toBe("Tools / Convert");
    expect(crumbFor("/downloads")).toBe("Activity / Downloads");
    expect(crumbFor("/insights")).toBe("Plex / Insights");
    expect(crumbFor("/discover")).toBe("Home / Discover");
    expect(crumbFor("/settings/system")).toBe("System / Settings");
  });

  it("matches whole path segments only", () => {
    // "/series" must not claim "/seriesfoo", and "/" must not claim everything.
    expect(crumbFor("/seriesfoo")).toBeUndefined();
    expect(crumbFor("/nope")).toBeUndefined();
  });

  it("follows the sidebar for every entry", () => {
    for (const g of NAV) {
      for (const i of g.items) {
        if (i.to === "/") continue;
        expect(crumbFor(i.to)).toBe(`${g.crumb ?? g.group} / ${i.label}`);
      }
    }
  });
});
