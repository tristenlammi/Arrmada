// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Movies } from "./Movies";
import { api, type Movie } from "../lib/api";
import { clearQueryCache } from "../lib/query";

// The honest states FE-21 is about: before the first answer and after a failed one,
// Movies must never claim the library is empty.

beforeEach(() => {
  clearQueryCache();
  vi.spyOn(api, "qualityProfiles").mockResolvedValue({ profiles: [], formats: [] });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const page = () => render(<MemoryRouter><Movies /></MemoryRouter>);
const movie = (id: number, title: string) => ({ id, title, monitored: true, has_file: false } as unknown as Movie);

describe("Movies list states", () => {
  it("shows a skeleton, not 'No movies yet', while the first load is out", async () => {
    vi.spyOn(api, "movies").mockReturnValue(new Promise(() => {}));
    page();
    expect(screen.getByLabelText("Loading")).toBeTruthy();
    expect(screen.queryByText(/No movies yet/)).toBeNull();
  });

  it("shows an error with Retry, not the onboarding text, when the load fails", async () => {
    const movies = vi.spyOn(api, "movies").mockRejectedValueOnce(new Error("backend unreachable"));
    page();
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByText(/backend unreachable/)).toBeTruthy();
    expect(screen.queryByText(/No movies yet/)).toBeNull();

    movies.mockResolvedValueOnce({ movies: [movie(1, "Dune")], metadata_available: true });
    await act(async () => { fireEvent.click(screen.getByText("Retry")); });
    expect((await screen.findAllByText("Dune")).length).toBeGreaterThan(0);
  });

  it("shows the onboarding text only for a real empty answer", async () => {
    vi.spyOn(api, "movies").mockResolvedValue({ movies: [], metadata_available: true });
    page();
    expect(await screen.findByText(/No movies yet/)).toBeTruthy();
  });

  it("renders the cached grid at once when coming back", async () => {
    const movies = vi.spyOn(api, "movies").mockResolvedValue({ movies: [movie(1, "Dune")], metadata_available: true });
    const first = page();
    expect((await screen.findAllByText("Dune")).length).toBeGreaterThan(0);
    first.unmount();

    movies.mockReturnValue(new Promise(() => {})); // the revalidation hangs
    page();
    expect(screen.getAllByText("Dune").length).toBeGreaterThan(0); // first render, from cache
    expect(screen.queryByLabelText("Loading")).toBeNull();
  });
});
