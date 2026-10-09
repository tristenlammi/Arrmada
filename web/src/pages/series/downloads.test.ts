import { describe, expect, it } from "vitest";
import type { Series } from "../../lib/api";
import { downloadingCount, mergeDownloads } from "./downloads";

const show = (): Series =>
  ({
    id: 1,
    title: "Show",
    seasons: [
      { id: 1, season_number: 1, monitored: true, episodes: [
        { id: 11, season_number: 1, episode_number: 1, monitored: true, has_file: false, download: { state: "downloading", progress: 0.2 } },
        { id: 12, season_number: 1, episode_number: 2, monitored: true, has_file: false },
      ] },
      { id: 2, season_number: 2, monitored: true, episodes: [
        { id: 21, season_number: 2, episode_number: 1, monitored: true, has_file: true },
      ] },
    ],
  }) as unknown as Series;

describe("mergeDownloads", () => {
  it("updates progress, adds new downloads and drops finished ones", () => {
    const s = show();
    const next = mergeDownloads(s, [{ season: 1, episode: 2, state: "downloading", progress: 0.5 }]);
    expect(next.seasons?.[0].episodes?.[0].download).toBeUndefined();
    expect(next.seasons?.[0].episodes?.[1].download).toEqual({ state: "downloading", progress: 0.5 });
    expect(downloadingCount(next)).toBe(1);
    // An untouched season keeps its identity.
    expect(next.seasons?.[1]).toBe(s.seasons?.[1]);
  });

  it("returns the same object when nothing moved", () => {
    const s = show();
    expect(mergeDownloads(s, [{ season: 1, episode: 1, state: "downloading", progress: 0.2 }])).toBe(s);
  });
});
