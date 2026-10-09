// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { editionState, initialBookFormats, lastBookFormats, rememberBookFormats, stillComingLine } from "./bookFormats";

describe("book format memory", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    try { localStorage.clear(); } catch { /* none */ }
  });

  it("starts on the server default, then on the viewer's last choice", () => {
    expect(initialBookFormats("both")).toBe("both");
    rememberBookFormats("audiobook");
    expect(lastBookFormats()).toBe("audiobook");
    expect(initialBookFormats("ebook")).toBe("audiobook");
  });

  it("ignores a junk stored value and an unknown default", () => {
    localStorage.setItem("arrmada.bookFormats", "paperback");
    expect(initialBookFormats("paperback")).toBeNull();
    expect(initialBookFormats("ebook")).toBe("ebook");
  });

  it("still works when storage is blocked (a private window)", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("blocked"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("blocked"); });
    expect(() => rememberBookFormats("both")).not.toThrow();
    expect(lastBookFormats()).toBeNull();
    expect(initialBookFormats("ebook")).toBe("ebook");
  });
});

describe("editionState", () => {
  it("offers the audiobook for a book held as an ebook", () => {
    expect(editionState({ in_library: true, has_ebook: true })).toEqual({ label: "Ebook ✓", missing: "audiobook" });
  });
  it("offers the ebook for a book held as an audiobook", () => {
    expect(editionState({ in_library: true, has_audiobook: true })).toEqual({ label: "Audiobook ✓", missing: "ebook" });
  });
  it("reads both when both are here", () => {
    expect(editionState({ in_library: true, has_ebook: true, has_audiobook: true })).toEqual({ label: "Ebook ✓ · Audiobook ✓" });
  });
  it("says the other format is asked for instead of offering it again", () => {
    expect(editionState({ in_library: true, has_ebook: true, request_status: "pending", request_formats: "both" }))
      .toEqual({ label: "Ebook ✓", requested: "audiobook" });
    // A declined request for it can be asked again.
    expect(editionState({ in_library: true, has_ebook: true, request_status: "declined", request_formats: "audiobook" }))
      .toEqual({ label: "Ebook ✓", missing: "audiobook" });
  });
  it("is nothing for a book with no edition on disk or not in the library", () => {
    expect(editionState({ in_library: true })).toBeNull();
    expect(editionState({ in_library: false, has_ebook: true })).toBeNull();
  });
});

describe("stillComingLine", () => {
  it("names what's still on the way", () => {
    expect(stillComingLine("audiobook")).toBe("Audiobook on the way");
    expect(stillComingLine("both")).toBe("Ebook and audiobook on the way");
    expect(stillComingLine(undefined)).toBe("");
  });
});
