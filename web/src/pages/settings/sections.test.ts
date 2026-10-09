import { describe, expect, it } from "vitest";
import { LINKS } from "../../lib/links";
import { SEARCH_INDEX, SECTIONS, searchSettings, sectionFromPath, settingsRedirect, visibleSections } from "./sections";

const admin = visibleSections(true).map((s) => s.id);
const manager = visibleSections(false).map((s) => s.id);

describe("visibleSections", () => {
  it("gives a manager only what the old Media and Library tabs showed", () => {
    expect(manager).toEqual(["library", "media"]);
  });

  it("gives an admin every section, Status included", () => {
    expect(admin).toEqual(SECTIONS.map((s) => s.id));
    expect(admin).toContain("status");
  });
});

describe("settingsRedirect", () => {
  it("leaves a section the viewer can open alone", () => {
    expect(settingsRedirect("/settings/system", "", "#api-keys", admin)).toBeNull();
    expect(settingsRedirect("/settings/library", "", "", manager)).toBeNull();
  });

  it("opens Library for bare /settings", () => {
    expect(settingsRedirect("/settings", "", "", admin)).toBe("/settings/library");
    expect(settingsRedirect("/settings/", "", "", manager)).toBe("/settings/library");
  });

  it("sends every old tab to its section", () => {
    expect(settingsRedirect("/settings", "?tab=media", "", admin)).toBe("/settings/media");
    expect(settingsRedirect("/settings", "?tab=library", "", admin)).toBe("/settings/library");
    expect(settingsRedirect("/settings", "?tab=system", "", admin)).toBe("/settings/system");
    expect(settingsRedirect("/settings", "?tab=users", "", admin)).toBe("/settings/users");
  });

  it("follows an old card anchor to the section that now holds it", () => {
    expect(settingsRedirect("/settings", "?tab=system", "#api-keys", admin)).toBe("/settings/system#api-keys");
    expect(settingsRedirect("/settings", "?tab=system", "#disk-guard", admin)).toBe("/settings/downloads#disk-guard");
    expect(settingsRedirect("/settings", "?tab=system", "#recycle-bin", admin)).toBe("/settings/downloads#recycle-bin");
    expect(settingsRedirect("/settings", "?tab=library", "#media-folders", admin)).toBe("/settings/library#media-folders");
    expect(settingsRedirect("/settings", "", "#users", admin)).toBe("/settings/users#users");
  });

  it("keeps other query params", () => {
    expect(settingsRedirect("/settings", "?tab=users&x=1", "", admin)).toBe("/settings/users?x=1");
  });

  it("falls back for a section the viewer can't open or that doesn't exist", () => {
    expect(settingsRedirect("/settings/system", "", "", manager)).toBe("/settings/library");
    expect(settingsRedirect("/settings", "?tab=system", "#api-keys", manager)).toBe("/settings/library#api-keys");
    expect(settingsRedirect("/settings/nope", "", "", admin)).toBe("/settings/library");
  });
});

describe("LINKS into Settings", () => {
  // Every deep link copy uses must name a section and a card that exist.
  it.each([LINKS.apiKeys, LINKS.diskGuard, LINKS.recycleBin, LINKS.libraryFolders, LINKS.users])("%s resolves", (link) => {
    const [path, anchor] = link.split("#");
    const section = sectionFromPath(path);
    expect(admin).toContain(section);
    expect(settingsRedirect(path, "", anchor ? `#${anchor}` : "", admin)).toBeNull();
    if (anchor) expect(SEARCH_INDEX.find((e) => e.anchor === anchor)?.section).toBe(section);
  });
});

describe("searchSettings", () => {
  const labels = (q: string, visible: readonly string[] = admin) => searchSettings(q, visible).map((e) => e.label);

  it("finds the recycle bin, Plex sign-in and the naming cards", () => {
    expect(labels("recycle")).toEqual(["Recycle bin"]);
    expect(labels("plex")).toContain("Plex sign-in");
    // The section name counts too, so "naming" also lists the rest of Naming & metadata.
    expect(labels("naming")).toEqual(["Movie naming", "Series naming", "Metadata"]);
  });

  it("needs every word typed, in any case", () => {
    expect(labels("SERIES episode")).toEqual(["Series naming"]);
    expect(labels("recycle tautulli")).toEqual([]);
    expect(labels("   ")).toEqual([]);
  });

  it("hides admin-only cards from a manager", () => {
    expect(labels("recycle", manager)).toEqual([]);
    expect(labels("plex", manager)).toEqual(["Metadata"]);
  });

  it("has a unique anchor per card", () => {
    const anchors = SEARCH_INDEX.map((e) => e.anchor);
    expect(new Set(anchors).size).toBe(anchors.length);
  });
});
