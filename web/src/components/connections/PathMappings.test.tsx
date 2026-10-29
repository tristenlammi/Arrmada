// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { type DownloadClient, type PathMap } from "../../lib/api";
import { downloadClientApi } from "../../lib/downloadClientApi";
import { clientBody } from "./ClientEditor";
import { PathMappings } from "./PathMappings";

// INT-25: a download client's path mappings save as they're added or removed, and "Check
// paths" shows which of its paths Arrmada can see, with a mapping that would fix the rest.

const seedbox = (over: Partial<DownloadClient> = {}): DownloadClient => ({ id: 7, name: "Seedbox", kind: "qbittorrent", url: "http://sb:8080", enabled: true, priority: 25, ...over });
const show = (c: DownloadClient) => render(<PathMappings client={c} body={(maps: PathMap[]) => clientBody(c, { path_maps: maps })} />);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("path mappings", () => {
  it("adds a mapping at once, keeping the password blank (kept), and shows a refusal", async () => {
    const upd = vi.spyOn(downloadClientApi, "updateDownloadClient")
      .mockResolvedValueOnce(seedbox({ path_maps: [{ remote: "/downloads", local: "/media/downloads" }] }))
      .mockRejectedValueOnce(new Error("/data/x is Arrmada's data folder, where its database lives"));
    show(seedbox());
    const form = screen.getByRole("form", { name: "Add a path mapping" });
    fireEvent.change(within(form).getByLabelText("qBittorrent's path"), { target: { value: "/downloads/" } });
    fireEvent.change(within(form).getByLabelText("Arrmada's path"), { target: { value: "/media/downloads" } });
    await act(async () => { fireEvent.click(within(form).getByRole("button", { name: "Add mapping" })); });
    expect(upd).toHaveBeenCalledWith(7, expect.objectContaining({ password: "", path_maps: [{ remote: "/downloads/", local: "/media/downloads" }] }));
    expect(screen.getByText(/\/media\/downloads$/)).toBeTruthy();
    expect((within(form).getByLabelText("qBittorrent's path") as HTMLInputElement).value).toBe("");

    fireEvent.change(within(form).getByLabelText("qBittorrent's path"), { target: { value: "/other" } });
    fireEvent.change(within(form).getByLabelText("Arrmada's path"), { target: { value: "/data/x" } });
    await act(async () => { fireEvent.click(within(form).getByRole("button", { name: "Add mapping" })); });
    expect(screen.getByRole("alert").textContent).toMatch(/data folder/);
    // The refused one isn't shown as saved, and what was typed stays to fix.
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect((within(form).getByLabelText("Arrmada's path") as HTMLInputElement).value).toBe("/data/x");
  });

  it("removes a mapping at once", async () => {
    const upd = vi.spyOn(downloadClientApi, "updateDownloadClient").mockResolvedValue(seedbox({ path_maps: [] }));
    show(seedbox({ path_maps: [{ remote: "/downloads", local: "/media/downloads" }] }));
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Remove the mapping for /downloads" })); });
    expect(upd).toHaveBeenCalledWith(7, expect.objectContaining({ path_maps: [] }));
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
  });

  it("Check paths lists what Arrmada can't see and offers the mapping that fixes it", async () => {
    vi.spyOn(downloadClientApi, "testDownloadClient").mockResolvedValue({
      ok: true,
      paths: {
        ok: false,
        checks: [
          { what: "save_folder", remote: "/downloads", local: "/downloads", visible: false },
          { what: "newest_download", remote: "/downloads/Film", local: "/downloads/Film", visible: false },
        ],
        suggestion: { remote: "/downloads", local: "/media/downloads" },
        summary: "Seedbox saves to /downloads, which Arrmada can't see. Add the path mapping /downloads → /media/downloads.",
      },
    });
    const upd = vi.spyOn(downloadClientApi, "updateDownloadClient").mockResolvedValue(seedbox({ path_maps: [{ remote: "/downloads", local: "/media/downloads" }] }));
    show(seedbox());
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Check paths" })); });
    expect(screen.getByText(/Save folder:/).parentElement?.textContent).toMatch(/✕.*\/downloads.*can't see it/);
    expect(screen.getByText(/Newest finished download:/)).toBeTruthy();
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Use this mapping" })); });
    expect(upd).toHaveBeenCalledWith(7, expect.objectContaining({ path_maps: [{ remote: "/downloads", local: "/media/downloads" }] }));
  });

  it("shows qBittorrent's incomplete folder as information, never as a failure", async () => {
    // Imports wait for a torrent to leave the incomplete folder, so Arrmada needn't see it:
    // the line is advisory (ⓘ, no "can't see it"), the report is OK, and the summary names
    // the qBittorrent setting that turns it off.
    vi.spyOn(downloadClientApi, "testDownloadClient").mockResolvedValue({
      ok: true,
      paths: {
        ok: true,
        checks: [
          { what: "save_folder", remote: "/completed", local: "/completed", visible: true },
          { what: "incomplete_folder", remote: "/incomplete", local: "/incomplete", visible: false, advisory: true },
        ],
        summary: "Seedbox saves to /completed, which Arrmada can see ✓. Seedbox downloads into /incomplete first and moves each torrent to its save folder when it finishes. Arrmada waits for that move before importing, so it needn't see /incomplete — but turning off \"Default Download Path (incomplete torrents)\" in Seedbox's Downloads settings means there's no move to wait for, and finished downloads aren't copied a second time.",
      },
    });
    show(seedbox());
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Check paths" })); });
    const line = screen.getByText(/Incomplete folder:/).parentElement?.textContent ?? "";
    expect(line).toMatch(/ⓘ.*\/incomplete.*doesn't need to see it/);
    expect(line).not.toMatch(/✕|can't see it/);
    expect(screen.getByText(/Default Download Path \(incomplete torrents\)/)).toBeTruthy();
  });

  it("the bundled client gets the check but no mapping editor", async () => {
    vi.spyOn(downloadClientApi, "testDownloadClient").mockResolvedValue({ ok: false, error: "connection refused" });
    show(seedbox({ bundled: true }));
    expect(screen.queryByRole("form", { name: "Add a path mapping" })).toBeNull();
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Check paths" })); });
    expect(screen.getByRole("alert").textContent).toMatch(/connection refused/);
  });
});
