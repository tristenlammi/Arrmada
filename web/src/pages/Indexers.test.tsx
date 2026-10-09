// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { Indexers } from "./Indexers";
import { api, type Indexer } from "../lib/api";
import { clearQueryCache } from "../lib/query";
import { ConfirmProvider } from "../ui";
import { MemoryRouter } from "react-router-dom";

// INT-04: the Indexers page's foot-guns — a delete that fired on one click, a new
// indexer always named '1337x', and 'Used for' pills that read backwards.

beforeEach(() => {
  clearQueryCache();
  vi.spyOn(api, "prowlarrInfo").mockResolvedValue({ url: "", has_key: false });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const idx = (over: Partial<Indexer> = {}): Indexer => ({ id: 1, name: "TorrentLeech", kind: "torrentleech", priority: 25, enabled: true, ...over });
const page = () => render(<MemoryRouter><ConfirmProvider><Indexers /></ConfirmProvider></MemoryRouter>);
const pill = (label: string) => screen.getByRole("button", { name: label });
const lit = (label: string) => pill(label).getAttribute("aria-pressed") === "true";

describe("Indexers add form", () => {
  it("names a new indexer after its kind until the user types a name", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([]);
    page();
    await screen.findByText(/No indexers yet/);
    fireEvent.click(screen.getByText("+ Add indexer"));
    const name = screen.getByDisplayValue("1337x") as HTMLInputElement;
    const kind = screen.getByRole("combobox") as HTMLSelectElement;
    fireEvent.change(kind, { target: { value: "torrentleech" } });
    expect(name.value).toBe("TorrentLeech");
    fireEvent.change(kind, { target: { value: "myanonamouse" } });
    expect(name.value).toBe("MyAnonaMouse");
    fireEvent.change(name, { target: { value: "My tracker" } });
    fireEvent.change(kind, { target: { value: "torznab" } });
    expect(name.value).toBe("My tracker");
  });

  it("is one column on a phone and two from the sm breakpoint", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([]);
    page();
    await screen.findByText(/No indexers yet/);
    fireEvent.click(screen.getByText("+ Add indexer"));
    const grid = screen.getByRole("combobox").closest(".grid") as HTMLElement;
    expect(grid.className).toContain("grid-cols-1");
    expect(grid.className).toContain("sm:grid-cols-2");
    // Priority is explained where it's edited.
    expect(screen.getByText(/Only breaks ties/)).toBeTruthy();
  });
});

describe("Indexers delete", () => {
  it("asks first and does nothing on Cancel", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx()]);
    const del = vi.spyOn(api, "deleteIndexer").mockResolvedValue(undefined);
    page();
    fireEvent.click(await screen.findByText("Delete"));
    const dialog = screen.getByRole("dialog", { name: "Delete TorrentLeech?" });
    expect(within(dialog).getByText(/14-day default/)).toBeTruthy();
    fireEvent.click(within(dialog).getByText("Cancel"));
    await act(async () => {});
    expect(del).not.toHaveBeenCalled();
  });

  it("shows the server's error when the delete fails", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx()]);
    vi.spyOn(api, "deleteIndexer").mockRejectedValue(new Error("database is locked"));
    page();
    fireEvent.click(await screen.findByText("Delete"));
    await act(async () => { fireEvent.click(within(screen.getByRole("dialog")).getByText("Delete")); });
    expect(await screen.findByText(/database is locked/)).toBeTruthy();
  });
});

describe("Indexers 'Used for' pills", () => {
  it("lights every pill for an unscoped indexer, and only its areas for a scoped one", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx()]);
    page();
    await screen.findByText("TorrentLeech");
    expect(["Movies", "TV", "Books", "Music"].every(lit)).toBe(true);
    expect(screen.getByText("· all areas")).toBeTruthy();
    cleanup();

    clearQueryCache();
    vi.spyOn(api, "indexers").mockResolvedValue([idx({ media_types: ["book"] })]);
    page();
    await screen.findByText("TorrentLeech");
    expect(lit("Books")).toBe(true);
    expect(lit("Movies") || lit("TV") || lit("Music")).toBe(false);
    expect(screen.queryByText("· all areas")).toBeNull();
  });

  it("clicking a lit pill on an unscoped indexer excludes just that area", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx()]);
    const upd = vi.spyOn(api, "updateIndexer").mockResolvedValue(undefined);
    page();
    await screen.findByText("TorrentLeech");
    await act(async () => { fireEvent.click(pill("Music")); });
    expect(upd).toHaveBeenCalledWith(1, expect.objectContaining({ media_types: ["movie", "series", "book"] }));
  });

  it("re-lighting every pill stores [] (all areas)", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx({ media_types: ["movie", "series", "book"] })]);
    const upd = vi.spyOn(api, "updateIndexer").mockResolvedValue(undefined);
    page();
    await screen.findByText("TorrentLeech");
    await act(async () => { fireEvent.click(pill("Music")); });
    expect(upd).toHaveBeenCalledWith(1, expect.objectContaining({ media_types: [] }));
  });

  it("won't turn off the last lit pill", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx({ media_types: ["book"] })]);
    const upd = vi.spyOn(api, "updateIndexer");
    page();
    await screen.findByText("TorrentLeech");
    await act(async () => { fireEvent.click(pill("Books")); });
    expect(upd).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toMatch(/Disable the indexer instead/);
    expect(lit("Books")).toBe(true);
  });

  it("shows a failed save and reverts the pills", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx()]);
    vi.spyOn(api, "updateIndexer").mockRejectedValue(new Error("indexer not found"));
    page();
    await screen.findByText("TorrentLeech");
    await act(async () => { fireEvent.click(pill("Music")); });
    expect(screen.getByRole("alert").textContent).toMatch(/indexer not found/);
    expect(lit("Music")).toBe(true);
  });

  it("explains the tie-break number on the row", async () => {
    vi.spyOn(api, "indexers").mockResolvedValue([idx()]);
    page();
    const tb = await screen.findByText("tie-break 25");
    expect(tb.getAttribute("title")).toMatch(/Only breaks ties/);
  });
});
