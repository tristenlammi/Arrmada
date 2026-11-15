// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render } from "@testing-library/react";
import { Artwork, caaSized, initials } from "./Artwork";

afterEach(cleanup);

describe("initials", () => {
  it("takes the first letters of the first two words, past a leading The", () => {
    expect(initials("The Quiet Harbours")).toBe("QH");
    expect(initials("Daft Punk")).toBe("DP");
    expect(initials("The The")).toBe("TH");
  });
  it("takes two letters of a one-word name, accents and all", () => {
    expect(initials("björk")).toBe("BJ");
    expect(initials("Ólafur Arnalds")).toBe("ÓA");
  });
  it("skips punctuation and falls back to a note", () => {
    expect(initials("!!! (Chk Chk Chk)")).toBe("CC");
    expect(initials("   ")).toBe("♫");
    expect(initials("†††")).toBe("♫");
  });
});

describe("caaSized", () => {
  it("asks the Cover Art Archive for the size wanted", () => {
    expect(caaSized("https://coverartarchive.org/release-group/abc/front-500", 250)).toBe("https://coverartarchive.org/release-group/abc/front-250");
    expect(caaSized("https://coverartarchive.org/release-group/abc/front-250", 500)).toBe("https://coverartarchive.org/release-group/abc/front-500");
  });
  it("leaves other URLs alone", () => {
    expect(caaSized("https://image.tmdb.org/t/p/w500/a.jpg", 250)).toBe("https://image.tmdb.org/t/p/w500/a.jpg");
  });
});

describe("Artwork", () => {
  it("shows the picture, then the artist's initials when it won't load", () => {
    const { container } = render(<Artwork src="https://coverartarchive.org/release-group/x/front-500" name="Daft Punk" kind="artist" />);
    const img = container.querySelector("img")!;
    expect(img.getAttribute("src")).toBe("https://coverartarchive.org/release-group/x/front-250");
    fireEvent.error(img);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector('[data-placeholder="artist"]')?.textContent).toBe("DP");
  });
  it("shows a record for an album with no cover", () => {
    const { container } = render(<Artwork name="Low Tide" kind="album" />);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector('[data-placeholder="album"] svg')).not.toBeNull();
  });
  it("tries a new picture after one failed", () => {
    const { container, rerender } = render(<Artwork src="a/front-500" name="X" kind="album" />);
    fireEvent.error(container.querySelector("img")!);
    rerender(<Artwork src="b/front-500" name="X" kind="album" />);
    expect(container.querySelector("img")?.getAttribute("src")).toBe("b/front-250");
  });
});
