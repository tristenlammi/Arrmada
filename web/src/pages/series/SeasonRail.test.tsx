// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { Season } from "../../lib/api";
import { SeasonRail } from "./SeasonRail";

// GH-80: the seasons mass edit. A click opens a season normally; in select mode the same
// click picks it for the bulk delete instead, and the rail says which are picked.

const season = (n: number, episodes = 0): Season => ({
  id: n + 1,
  season_number: n,
  monitored: true,
  episodes: Array.from({ length: episodes }, (_, i) => ({
    id: n * 100 + i, season_number: n, episode_number: i + 1, title: "",
    air_date: "2020-01-01", monitored: true, has_file: true,
  })),
} as Season);

afterEach(cleanup);

describe("SeasonRail", () => {
  it("opens a season when not selecting", () => {
    const onSelect = vi.fn();
    render(<SeasonRail seasons={[season(1, 2), season(2)]} onSelect={onSelect} />);
    fireEvent.click(screen.getByRole("button", { name: /Season 2/ }));
    expect(onSelect).toHaveBeenCalledWith(2);
    expect(screen.queryByRole("button", { name: "Select seasons" })).toBeNull();
  });

  it("picks seasons instead of opening them in select mode", () => {
    const onSelect = vi.fn();
    const onTogglePick = vi.fn();
    const onToggleSelecting = vi.fn();
    render(
      <SeasonRail
        seasons={[season(1, 2), season(2)]}
        selected={1}
        onSelect={onSelect}
        selecting
        picked={new Set([2])}
        onToggleSelecting={onToggleSelecting}
        onTogglePick={onTogglePick}
      />,
    );
    expect(screen.getByRole("group", { name: "Seasons to delete" })).toBeTruthy();
    const picked = screen.getByRole("button", { name: /Season 2/ });
    expect(picked.getAttribute("aria-pressed")).toBe("true");
    // The open season isn't marked as picked just because it's on screen.
    expect(screen.getByRole("button", { name: /Season 1/ }).getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(picked);
    expect(onTogglePick).toHaveBeenCalledWith(2);
    expect(onSelect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onToggleSelecting).toHaveBeenCalled();
  });
});
