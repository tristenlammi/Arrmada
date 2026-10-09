import { describe, expect, it } from "vitest";
import { NAV, visibleNav } from "./nav";

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
