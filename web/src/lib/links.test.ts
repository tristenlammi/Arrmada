import { describe, expect, it } from "vitest";
import { LINKS, fixLink } from "./links";

describe("fixLink", () => {
  it("prefers the LINKS entry the server named", () => {
    expect(fixLink({ link_key: "indexers", link: "/old-indexers" })).toBe(LINKS.indexers);
  });

  it("falls back to the server's path for a key this build doesn't know", () => {
    expect(fixLink({ link_key: "somethingNew", link: "/somewhere?tab=x" })).toBe("/somewhere?tab=x");
  });

  it("follows only in-app paths", () => {
    expect(fixLink({ link: "https://evil.example" })).toBeUndefined();
    expect(fixLink({ link: "//evil.example" })).toBeUndefined();
    expect(fixLink({ link_key: "toString" })).toBeUndefined();
    expect(fixLink({})).toBeUndefined();
  });
});
