// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { UnmatchedReview } from "./Library";
import { api, type UnmatchedFolder } from "../lib/api";
import { ToastProvider } from "../ui";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

// #136: a server that sent "candidates": null for a folder it found nothing for crashed
// Settings → Library. The row shows "No candidates" instead.
describe("UnmatchedReview", () => {
  it("shows a folder whose candidates came back null", async () => {
    const folder = { folder: "Unknown Thing", title: "Unknown Thing", year: 0, candidates: null } satisfies UnmatchedFolder;
    vi.spyOn(api, "seriesUnmatched").mockResolvedValue([folder]);
    render(<ToastProvider><UnmatchedReview media="series" reloadKey={0} /></ToastProvider>);
    expect(await screen.findByText("Unknown Thing")).toBeTruthy();
    expect(screen.getByText(/No candidates/)).toBeTruthy();
  });

  it("shows nothing when the list itself came back null", async () => {
    const list = vi.spyOn(api, "moviesUnmatched").mockResolvedValue(null as unknown as UnmatchedFolder[]);
    const { container } = render(<ToastProvider><UnmatchedReview media="movie" reloadKey={0} /></ToastProvider>);
    await vi.waitFor(() => expect(list).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(container.textContent).toBe("");
  });
});
