// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { DownloadClients } from "./DownloadClients";
import { api, type DownloadClient } from "../lib/api";
import { clearQueryCache } from "../lib/query";
import { ConfirmProvider } from "../ui";

beforeEach(() => {
  clearQueryCache();
  vi.spyOn(api, "downloadClientStatus").mockResolvedValue({ listen_port: 0 });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const client = (over: Partial<DownloadClient> = {}): DownloadClient => ({ id: 1, name: "qBittorrent", kind: "qbittorrent", url: "http://qb:8080", enabled: true, ...over });
const page = () => render(<MemoryRouter><ConfirmProvider><DownloadClients /></ConfirmProvider></MemoryRouter>);

describe("DownloadClients delete (INT-04)", () => {
  it("asks first, naming what it leaves alone, and does nothing on Cancel", async () => {
    vi.spyOn(api, "downloadClients").mockResolvedValue([client()]);
    const del = vi.spyOn(api, "deleteDownloadClient").mockResolvedValue(undefined);
    page();
    fireEvent.click(await screen.findByText("Delete"));
    const dialog = screen.getByRole("dialog", { name: "Remove qBittorrent?" });
    expect(within(dialog).getByText(/Torrents already there are untouched/)).toBeTruthy();
    fireEvent.click(within(dialog).getByText("Cancel"));
    await act(async () => {});
    expect(del).not.toHaveBeenCalled();
  });

  it("shows the server's error when the delete fails", async () => {
    vi.spyOn(api, "downloadClients").mockResolvedValue([client()]);
    vi.spyOn(api, "deleteDownloadClient").mockRejectedValue(new Error("database is locked"));
    page();
    fireEvent.click(await screen.findByText("Delete"));
    await act(async () => { fireEvent.click(within(screen.getByRole("dialog")).getByText("Remove")); });
    expect(await screen.findByText(/database is locked/)).toBeTruthy();
  });
});

describe("DownloadClients add form (INT-04)", () => {
  it("starts with no URL, a container-relative placeholder, and one column on a phone", async () => {
    vi.spyOn(api, "downloadClients").mockResolvedValue([]);
    page();
    await screen.findByText(/No download clients yet/);
    fireEvent.click(screen.getByText("+ Add client"));
    const url = screen.getByPlaceholderText(/as reached from the Arrmada container/) as HTMLInputElement;
    expect(url.value).toBe("");
    expect(screen.getByText(/localhost here means the Arrmada container itself/)).toBeTruthy();
    const grid = url.closest(".grid") as HTMLElement;
    expect(grid.className).toContain("grid-cols-1");
    expect(grid.className).toContain("sm:grid-cols-2");
  });
});
