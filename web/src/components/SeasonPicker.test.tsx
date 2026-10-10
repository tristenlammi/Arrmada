// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SeasonPicker } from "./SeasonPicker";
import type { SeriesSeason } from "../lib/api";

// REQ-13: the title sheet's "which seasons?" — the whole show by default, ticked seasons
// otherwise, and nothing tickable that can't be asked for.

const season = (n: number, over: Partial<SeriesSeason> = {}): SeriesSeason => ({
  number: n, name: `Season ${n}`, episode_count: 8, air_date: `${2018 + n}-01-01`, have: 0, aired: 8,
  state: "requestable", requestable: true, ...over,
});
const show = [
  season(1, { state: "in_library", have: 8, requestable: false }),
  season(2, { state: "requested", requestable: false, request: { request_id: 3, status: "pending", mine: true } }),
  season(3),
  season(4, { state: "partial", have: 3 }),
  season(5, { state: "unaired", aired: 0, requestable: false }),
];
const box = (n: number) => screen.getByRole("checkbox", { name: new RegExp(`^Season ${n}\\b`) }) as HTMLInputElement;

afterEach(cleanup);

describe("SeasonPicker", () => {
  it("starts on the whole show and sends null", () => {
    const submit = vi.fn();
    render(<SeasonPicker seasons={show} busy={false} onSubmit={submit} onCancel={() => {}} />);
    expect(screen.getByRole("button", { name: "All seasons" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Request all seasons" }));
    expect(submit).toHaveBeenCalledWith(null);
  });

  it("disables what's here, yours or not out, and says why", () => {
    render(<SeasonPicker seasons={show} busy={false} onSubmit={() => {}} onCancel={() => {}} />);
    expect(box(1).disabled && box(2).disabled && box(5).disabled).toBe(true);
    expect(box(3).disabled || box(4).disabled).toBe(false);
    for (const t of ["In library ✓", "You asked for this", "3 of 8", "Not out yet"]) expect(screen.getByText(t)).toBeTruthy();
  });

  it("asks for just the seasons ticked, and not for none", () => {
    const submit = vi.fn();
    render(<SeasonPicker seasons={show} busy={false} onSubmit={submit} onCancel={() => {}} />);
    fireEvent.click(box(3)); // out of "all": everything else that can be ticked
    expect(screen.getByRole("button", { name: "Request 1 season" })).toBeTruthy();
    fireEvent.click(box(4));
    expect((screen.getByRole("button", { name: "Tick a season" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Latest season" }));
    fireEvent.click(screen.getByRole("button", { name: "Request 1 season" }));
    expect(submit).toHaveBeenLastCalledWith([4]);
    fireEvent.click(screen.getByRole("button", { name: "All missing" }));
    fireEvent.click(screen.getByRole("button", { name: "Request 2 seasons" }));
    expect(submit).toHaveBeenLastCalledWith([3, 4]);
  });
});
