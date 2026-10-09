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

describe("DownloadClients edit and enable (INT-05)", () => {
  it("edits in place: prefilled, blank password kept, then tested", async () => {
    vi.spyOn(api, "downloadClients").mockResolvedValue([client({ username: "admin" })]);
    const upd = vi.spyOn(api, "updateDownloadClient").mockResolvedValue(client({ name: "Home qB" }));
    const test = vi.spyOn(api, "testDownloadClient").mockResolvedValue({ ok: true });
    page();
    fireEvent.click(await screen.findByText("Edit"));
    expect((screen.getByDisplayValue("http://qb:8080") as HTMLInputElement).readOnly).toBe(false);
    expect(screen.getByDisplayValue("admin")).toBeTruthy();
    const pw = screen.getByPlaceholderText(/leave blank to keep/) as HTMLInputElement;
    expect(pw.value).toBe("");
    fireEvent.change(screen.getByDisplayValue("qBittorrent"), { target: { value: "Home qB" } });
    await act(async () => { fireEvent.click(screen.getByText("Save changes")); });
    expect(upd).toHaveBeenCalledWith(1, expect.objectContaining({ name: "Home qB", url: "http://qb:8080", username: "admin", password: "", enabled: true }));
    expect(test).toHaveBeenCalledWith(1);
    expect(await screen.findByText("✓ Connected")).toBeTruthy();
  });

  it("keeps the bundled client's URL read-only and warns its delete won't stick", async () => {
    vi.spyOn(api, "downloadClients").mockResolvedValue([client({ bundled: true, url: "http://arrmada-qbittorrent:8080" })]);
    page();
    fireEvent.click(await screen.findByText("Edit"));
    expect((screen.getByDisplayValue("http://arrmada-qbittorrent:8080") as HTMLInputElement).readOnly).toBe(true);
    fireEvent.click(screen.getByText("Delete"));
    expect(within(screen.getByRole("dialog")).getByText(/re-added on the next restart; disable it instead/)).toBeTruthy();
  });

  it("the switch saves enabled via PUT, and a disabled card says it gets no downloads", async () => {
    const list = vi.spyOn(api, "downloadClients").mockResolvedValue([client()]);
    const upd = vi.spyOn(api, "updateDownloadClient").mockResolvedValue(client({ enabled: false }));
    page();
    const sw = await screen.findByRole("switch", { name: "qBittorrent enabled" });
    expect(sw.getAttribute("aria-checked")).toBe("true");
    list.mockResolvedValue([client({ enabled: false })]);
    await act(async () => { fireEvent.click(sw); });
    expect(upd).toHaveBeenCalledWith(1, expect.objectContaining({ enabled: false, password: "" }));
    expect(await screen.findByText(/Disabled: gets no new downloads/)).toBeTruthy();
    expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("false");
  });

  it("shows a failed toggle on the card", async () => {
    vi.spyOn(api, "downloadClients").mockResolvedValue([client()]);
    vi.spyOn(api, "updateDownloadClient").mockRejectedValue(new Error("download client not found"));
    page();
    const sw = await screen.findByRole("switch");
    await act(async () => { fireEvent.click(sw); });
    expect(await screen.findByText(/Couldn't disable it: download client not found/)).toBeTruthy();
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
