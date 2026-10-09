import { describe, expect, it } from "vitest";
import type { ReviewFile } from "../../lib/api";
import { guessLabel, parseEpisodes, prefill } from "./episodes";

const file = (guess: ReviewFile["guess"], video = true): ReviewFile => ({ rel_path: "x.mkv", size: 1, video, guess });

describe("parseEpisodes", () => {
  it("reads single, listed and ranged episodes", () => {
    expect(parseEpisodes("3")).toEqual([3]);
    expect(parseEpisodes("3,4")).toEqual([3, 4]);
    expect(parseEpisodes(" 3 4 ")).toEqual([3, 4]);
    expect(parseEpisodes("3-5")).toEqual([3, 4, 5]);
  });

  it("treats blank as skip and nonsense as invalid", () => {
    expect(parseEpisodes("")).toBeNull();
    expect(parseEpisodes("   ")).toBeNull();
    expect(parseEpisodes("0")).toBeUndefined();
    expect(parseEpisodes("a")).toBeUndefined();
    expect(parseEpisodes("5-3")).toBeUndefined();
  });
});

describe("guessLabel and prefill", () => {
  it("label and prefill a numbered file", () => {
    expect(guessLabel(file({ season: 1, episodes: [3, 4] }))).toBe("S01E03-E04");
    expect(prefill(file({ season: 1, episodes: [3, 4] }))).toEqual({ season: "1", episodes: "3,4" });
  });

  it("leave an unnumbered file blank, and say nothing for a non-video", () => {
    expect(guessLabel(file({}))).toBe("no number");
    expect(prefill(file({}))).toEqual({ season: "", episodes: "" });
    expect(guessLabel(file({ absolute: [137] }))).toBe("#137");
    expect(guessLabel(file({ season: 1, episodes: [1] }, false))).toBe("");
  });
});
