import { describe, expect, it } from "vitest";
import { refToPath, titlePath } from "./refLink";

// The same vectors as TestRefPath in internal/requests/refpath_test.go: the
// bell (here) and Web Push (there) must open the same place for the same notice.
const VECTORS: [string, string | null][] = [
  ["movie:603", "/discover/movie/603"],
  ["movie:603:approved:1700000000", "/discover/movie/603"],
  ["movie:603:declined:1700000000", "/discover/movie/603"],
  ["series:1399", "/discover/series/1399"],
  ["series:1399:r12", "/discover/series/1399"],
  ["series:1399:r12:s3", "/discover/series/1399"],
  ["series:1399:r12:approved:1700000000", "/discover/series/1399"],
  ["book:OL27448W", "/discover?tab=books&work=OL27448W"],
  ["book:OL27448W:audiobook", "/discover?tab=books&work=OL27448W"],
  ["book:hc:123", "/discover?tab=books&work=hc%3A123"],
  ["book:hc:123:approved:1700000000", "/discover?tab=books&work=hc%3A123"],
  ["book:hc:123:audiobook", "/discover?tab=books&work=hc%3A123"],
  ["book:a b/c", "/discover?tab=books&work=a%20b%2Fc"],
  ["request:40:new:1700000000", "/requests?id=40"],
  ["request:40:new:1700000000:2", "/requests?id=40"],
  ["1007", null],
  ["", null],
  ["movie:", null],
  ["movie:abc", null],
  ["movie:0", null],
  ["book:", null],
  ["episode:5", null],
];

describe("refToPath", () => {
  it.each(VECTORS)("%s", (ref, want) => {
    expect(refToPath(ref)).toBe(want);
  });

  it("is the title's own address", () => {
    expect(titlePath("series", 1399)).toBe("/discover/series/1399");
  });
});
