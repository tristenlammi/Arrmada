import { describe, expect, it } from "vitest";
import type { SeriesSeason } from "./api";
import { latestSeason, missingSeasons, pickLabel, sameSeasons, seasonChip, seasonMeta, seasonSelectable, seasonTitle } from "./seasons";

const season = (n: number, over: Partial<SeriesSeason> = {}): SeriesSeason => ({
  number: n, name: `Season ${n}`, episode_count: 10, air_date: `${2018 + n}-03-01`, have: 0, aired: 10,
  state: "requestable", requestable: true, ...over,
});

describe("season picker rules", () => {
  it("ticks what may be asked for, and someone else's request (to follow), never your own", () => {
    expect(seasonSelectable(season(1))).toBe(true);
    expect(seasonSelectable(season(1, { state: "partial", have: 4 }))).toBe(true);
    expect(seasonSelectable(season(1, { state: "in_library", requestable: false }))).toBe(false);
    expect(seasonSelectable(season(1, { state: "on_the_way", requestable: false }))).toBe(false);
    expect(seasonSelectable(season(1, { state: "unaired", requestable: false }))).toBe(false);
    expect(seasonSelectable(season(1, { state: "requested", requestable: false, request: { request_id: 4, status: "pending", mine: true } }))).toBe(false);
    expect(seasonSelectable(season(1, { state: "requested", requestable: false, request: { request_id: 4, status: "pending", mine: false } }))).toBe(true);
  });

  it("words each state, naming who asked only when the server sent a name (staff)", () => {
    expect(seasonChip(season(1))).toBeNull();
    expect(seasonChip(season(1, { state: "in_library" }))?.label).toBe("In library ✓");
    expect(seasonChip(season(1, { state: "partial", have: 4, aired: 10 }))?.label).toBe("4 of 10");
    expect(seasonChip(season(1, { state: "on_the_way" }))?.label).toBe("On the way");
    expect(seasonChip(season(1, { state: "unaired" }))?.label).toBe("Not out yet");
    expect(seasonChip(season(1, { state: "requested", request: { request_id: 1, status: "pending", mine: true } }))?.label).toBe("You asked for this");
    expect(seasonChip(season(1, { state: "requested", request: { request_id: 1, status: "approved", mine: false } }))?.label).toBe("Requested");
    expect(seasonChip(season(1, { state: "requested", request: { request_id: 1, status: "approved", mine: false, requested_by_name: "alice" } }))?.label).toBe("Requested by alice");
  });

  it("names and describes a season", () => {
    expect(seasonTitle({ number: 2, name: "Season 2" })).toBe("Season 2");
    expect(seasonTitle({ number: 2, name: "The Return" })).toBe("Season 2 · The Return");
    expect(seasonTitle({ number: 3 })).toBe("Season 3");
    expect(seasonMeta({ episode_count: 1, air_date: "2021-05-02" })).toBe("1 episode · 2021");
    expect(seasonMeta({ episode_count: 0 })).toBe("");
  });

  it("picks all missing and the latest season, and labels the button", () => {
    const show = [season(1, { state: "in_library", requestable: false }), season(2), season(3, { state: "partial", have: 2 }), season(4, { state: "unaired", requestable: false })];
    expect(missingSeasons(show)).toEqual([2, 3]);
    expect(latestSeason(show)).toBe(3);
    expect(latestSeason([season(1, { state: "in_library", requestable: false })])).toBeNull();
    expect(pickLabel(null)).toBe("Request all seasons");
    expect(pickLabel([])).toBe("Tick a season");
    expect(pickLabel([2])).toBe("Request 1 season");
    expect(pickLabel([2, 3])).toBe("Request 2 seasons");
    expect(sameSeasons([3, 2], [2, 3])).toBe(true);
    expect(sameSeasons(null, null)).toBe(true);
    expect(sameSeasons(null, [])).toBe(false);
  });
});
