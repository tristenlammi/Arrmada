import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import type { MockRoute } from "./fixtures/routes";
import { libraryFoldersMulti } from "./fixtures/system";

// FOLDERS: more than one folder for Movies and TV. With one folder (the default fixture)
// nothing about folders shows on a title, an add or an approve; with several, the title
// shows its folder and can move, and the add and approve dialogs offer a folder picked by
// the rules.

// override answers the given endpoints before the default fixtures (the last route
// registered is tried first), recording the calls like the defaults do.
async function override(page: Page, api: MockedApi, extra: MockRoute[]) {
  await page.route((u) => u.pathname.startsWith("/api/"), async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    for (const r of extra) {
      if (r.method !== req.method() || url.pathname !== r.path) continue;
      let body: unknown;
      try { body = req.postDataJSON(); } catch { body = req.postData(); }
      api.calls.push({ method: req.method(), path: url.pathname + url.search, body });
      return route.fulfill({ status: r.status ?? 200, contentType: "application/json", body: JSON.stringify(r.body ?? {}) });
    }
    return route.fallback();
  });
}

const multi: MockRoute = { method: "GET", path: "/api/v1/library/folders", body: libraryFoldersMulti };
const suggestKids: MockRoute = { method: "GET", path: "/api/v1/library/folders/suggest", body: { folder_id: 3 } };

test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("with one folder a film shows nothing about folders", async ({ page }) => {
    await page.goto("/movies/1");
    await page.getByRole("button", { name: "More actions" }).click();
    await expect(page.getByRole("menuitem", { name: "Refresh & rescan" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Move to folder…" })).toHaveCount(0);
    await expect(page.getByText("Folder:")).toHaveCount(0);
  });

  test("with several folders a film shows its folder and moves, following the job", async ({ page, api }) => {
    await override(page, api, [
      multi,
      { method: "POST", path: "/api/v1/movies/1/move/preview", body: { folder: "Kids", moves: [{
        from: "/media/movies/The Cartographer (2021)", to: "/media/kids/The Cartographer (2021)", bytes: 4_000_000_000,
        files: ["/media/movies/The Cartographer (2021)/The Cartographer (2021).mkv", "/media/movies/The Cartographer (2021)/poster.jpg"],
      }] } },
      { method: "POST", path: "/api/v1/movies/1/move", status: 202, body: { status: "moving", job_id: 901, folder: "Kids" } },
      { method: "GET", path: "/api/v1/jobs/901", body: {
        id: 901, kind: "library.move", target: "movie:1", trigger: "user:1", status: "succeeded", progress: 1,
        message: "The Cartographer moved to Kids", error: "", created_at: null, started_at: null, finished_at: null,
      } },
    ]);
    await page.goto("/movies/1");
    await expect(page.getByText("Folder:")).toBeVisible();
    await page.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Move to folder…" }).click();
    const dialog = page.getByRole("dialog", { name: "Move to another folder" });
    await expect(dialog.getByRole("combobox")).toHaveValue("3");
    await expect(dialog.getByText("/media/kids", { exact: true })).toBeVisible();
    // The preview lists every file that moves; the move is sent with that list.
    const what = dialog.getByRole("region", { name: "What moves" });
    await expect(what.getByText("2 files")).toBeVisible();
    await expect(what.getByText("/media/movies/The Cartographer (2021)/poster.jpg")).toBeVisible();
    await dialog.getByRole("button", { name: "Move", exact: true }).click();
    await expect(dialog.getByText("The Cartographer moved to Kids")).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/movies/1/move").filter((c) => c.path === "/api/v1/movies/1/move");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ folder_id: 3, files: [
      "/media/movies/The Cartographer (2021)/The Cartographer (2021).mkv", "/media/movies/The Cartographer (2021)/poster.jpg",
    ] });
  });

  test("a film whose folder isn't provably its own says why, and Move stays off", async ({ page, api }) => {
    await override(page, api, [
      multi,
      { method: "POST", path: "/api/v1/movies/1/move/preview", body: { folder: "Kids", moves: [],
        refused: "The Cartographer stays where it is — its folder Maps isn't provably its own (another video is inside it (Atlas.mkv))" } },
    ]);
    await page.goto("/movies/1");
    await page.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Move to folder…" }).click();
    const dialog = page.getByRole("dialog", { name: "Move to another folder" });
    await expect(dialog.getByText(/Nothing will move: .*another video is inside it \(Atlas\.mkv\)/)).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Move", exact: true })).toBeDisabled();
    expect(api.callsTo("POST", "/api/v1/movies/1/move").filter((c) => c.path === "/api/v1/movies/1/move")).toHaveLength(0);
  });

  test("the add dialog preselects the folder the rules pick and adds there", async ({ page, api }) => {
    await override(page, api, [
      multi, suggestKids,
      { method: "GET", path: "/api/v1/movies/lookup", body: { results: [
        { tmdb_id: 862, title: "Toy Story", year: 1995, overview: "Toys.", poster_url: "", vote_average: 8, in_library: false },
      ] } },
      { method: "POST", path: "/api/v1/movies", status: 201, body: { id: 98, tmdb_id: 862, title: "Toy Story", year: 1995, monitored: true, has_file: false, quality_profile: "hd", min_availability: "released", library_folder_id: 3 } },
    ]);
    await page.goto("/movies");
    await page.getByRole("button", { name: "+ Add movie" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Search TMDB for a movie").fill("toy story");
    await dialog.getByRole("button", { name: "Search" }).click();
    await dialog.getByRole("button", { name: /Toy Story/ }).click();
    await expect(dialog.getByLabel("Folder")).toHaveValue("3");
    await dialog.getByRole("button", { name: "Add movie" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/movies").length).toBe(1);
    expect((api.callsTo("POST", "/api/v1/movies")[0].body as { folder_id?: number }).folder_id).toBe(3);
  });

  test("approving a request can pick the folder", async ({ page, api }) => {
    await override(page, api, [multi, suggestKids]);
    await page.goto("/requests?tab=needs&id=1");
    const sheet = page.getByRole("dialog", { name: "Saltwind" });
    await expect(sheet.getByLabel("Folder")).toHaveValue("3");
    await sheet.getByLabel("Folder").selectOption("0");
    await sheet.getByRole("button", { name: "Approve", exact: true }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/requests/1/approve").length).toBe(1);
    expect((api.callsTo("POST", "/api/v1/requests/1/approve")[0].body as { folder_id?: number }).folder_id).toBe(0);
  });

  // Issue #24: the Audiobooks folder in use doesn't exist yet. Create it sends that same
  // folder with create, and the row looks again and shows it's there.
  test("Create it makes the missing folder in use and shows it's there", async ({ page, api }) => {
    let made = false;
    const missing = (path: string) => ({
      path, exists: false, is_dir: false, writable: false, hardlink_with_downloads: null, under_data_dir: false,
      free_bytes: 0, total_bytes: 0, entries: 0, entries_capped: false,
      error: `Audiobooks: ${path} doesn't exist. Check the spelling, or create it.`,
    });
    await page.route((u) => u.pathname === "/api/v1/system/library/check", async (route) => {
      const url = new URL(route.request().url());
      const path = url.searchParams.get("path") ?? "";
      if (path !== "/media/audiobooks" || made) return route.fallback();
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(missing(path)) });
    });
    await override(page, api, [{ method: "PUT", path: "/api/v1/system/library", body: { movies: "/media/movies", tv: "/media/tv", ebooks: "/media/ebooks", audiobooks: "/media/audiobooks", music: "/media/music", downloads: "/media/downloads" } }]);
    await page.route((u) => u.pathname === "/api/v1/system/library", async (route) => {
      if (route.request().method() === "PUT") made = true;
      return route.fallback();
    });
    await page.goto("/settings/library");
    await expect(page.getByText("/media/audiobooks doesn't exist")).toBeVisible();
    await page.getByRole("button", { name: "Create it" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/system/library").length).toBe(1);
    expect(api.callsTo("PUT", "/api/v1/system/library")[0].body).toEqual({ audiobooks: "/media/audiobooks", create: true });
    await expect(page.getByText("/media/audiobooks doesn't exist")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Create it" })).toHaveCount(0);
  });

  test("Create it says so when the folder couldn't be made", async ({ page, api }) => {
    await page.route((u) => u.pathname === "/api/v1/system/library/check", async (route) => {
      const path = new URL(route.request().url()).searchParams.get("path") ?? "";
      if (path !== "/media/audiobooks") return route.fallback();
      return route.fulfill({ contentType: "application/json", body: JSON.stringify({
        path, exists: false, is_dir: false, writable: false, hardlink_with_downloads: null, under_data_dir: false,
        free_bytes: 0, total_bytes: 0, entries: 0, entries_capped: false,
        error: `Audiobooks: ${path} doesn't exist. Check the spelling, or create it.`,
      }) });
    });
    await override(page, api, [{ method: "PUT", path: "/api/v1/system/library", status: 400, body: {
      status: "error", folder: "audiobooks", message: "Audiobooks: couldn't create /media/audiobooks (permission denied). Check that the folder above it is mounted and that Arrmada's user (PUID/PGID) can write there.",
    } }]);
    await page.goto("/settings/library");
    await page.getByRole("button", { name: "Create it" }).click();
    await expect(page.getByText("couldn't create /media/audiobooks (permission denied)")).toBeVisible();
  });

  test("Settings → Library lists the extra folders with their rules", async ({ page, api }) => {
    await override(page, api, [multi]);
    await page.goto("/settings/library#more-folders");
    const card = page.locator("#more-folders");
    await expect(card.getByLabel("Name of the Kids folder")).toHaveValue("Kids");
    await expect(card.getByLabel("Name of the Anime folder")).toHaveValue("Anime");
    await expect(card.getByText("— the main folder")).toHaveCount(2);
    await expect(card.getByText("/media/kids")).toBeVisible();
  });

  test("with one folder per library the downloads option waits behind one line until asked for", async ({ page }) => {
    await page.goto("/settings/library#more-folders");
    const card = page.locator("#more-folders");
    await expect(card.getByText("— the main folder")).toHaveCount(2);
    await expect(card.getByRole("button", { name: "Download to a folder on this disk…" })).toHaveCount(0);
    await card.getByRole("button", { name: /Libraries on different disks\?/ }).click();
    await expect(card.getByRole("button", { name: "Download to a folder on this disk…" })).toHaveCount(2);
  });

  test("a folder on another disk can download onto that disk (GitHub #11)", async ({ page, api }) => {
    await override(page, api, [
      multi,
      { method: "GET", path: "/api/v1/system/browse", body: { path: "/mnt/disk2/downloads", parent: "/mnt/disk2", dirs: [], path_disabled: false } },
      { method: "PUT", path: "/api/v1/system/library/folders/downloads", body: {
        folder: { ...libraryFoldersMulti.movie.folders[1], downloads: "/mnt/disk2/downloads" },
        warning: "/mnt/disk2/downloads isn't on the same filesystem as its library folder /media/kids, so imports from it are copied, not hardlinked — pick a downloads folder on the library's disk",
      } },
    ]);
    await page.goto("/settings/library#more-folders");
    const card = page.locator("#more-folders");
    // Every folder offers it, the main ones included; nothing is set yet.
    await expect(card.getByRole("button", { name: "Download to a folder on this disk…" })).toHaveCount(4);
    await expect(card.getByText("Downloads to")).toHaveCount(0);
    // The Kids row is the second Movies row (after the main folder).
    await card.getByRole("button", { name: "Download to a folder on this disk…" }).nth(1).click();
    await page.getByRole("button", { name: "Select this folder" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/system/library/folders/downloads").length).toBe(1);
    expect(api.callsTo("PUT", "/api/v1/system/library/folders/downloads")[0].body).toEqual({ media: "movie", id: 3, path: "/mnt/disk2/downloads" });
    // Saved, but on another filesystem: the warning says so.
    await expect(page.getByText(/copied, not hardlinked/)).toBeVisible();
  });

  test("a folder's own downloads folder shows, warns, and can go back to Downloads", async ({ page, api }) => {
    const withOwn = {
      movie: { ...libraryFoldersMulti.movie, folders: [
        { ...libraryFoldersMulti.movie.folders[0], downloads: "/mnt/disk1/downloads" },
        { ...libraryFoldersMulti.movie.folders[1], downloads: "/mnt/disk1/downloads", downloads_warning: "/mnt/disk1/downloads isn't on the same filesystem as its library folder /media/kids, so imports from it are copied, not hardlinked" },
      ] },
      series: libraryFoldersMulti.series,
    };
    await override(page, api, [
      { method: "GET", path: "/api/v1/library/folders", body: withOwn },
      { method: "PUT", path: "/api/v1/system/library/folders/downloads", body: { folder: libraryFoldersMulti.movie.folders[1] } },
    ]);
    await page.goto("/settings/library#more-folders");
    const card = page.locator("#more-folders");
    await expect(card.getByText("/mnt/disk1/downloads", { exact: true })).toHaveCount(2);
    await expect(card.getByText(/copied, not hardlinked/)).toHaveCount(1);
    await card.getByRole("button", { name: "Use the Downloads folder" }).nth(1).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/system/library/folders/downloads").length).toBe(1);
    expect(api.callsTo("PUT", "/api/v1/system/library/folders/downloads")[0].body).toEqual({ media: "movie", id: 3, path: "" });
  });
});

// GitHub #3: adding a folder doesn't import what's already in it. The card offers a scan of
// just that folder, follows the job and says what it found; every extra folder can be
// scanned on its own, says when nothing in it is in the library yet, and a missing one says
// so instead of offering a scan.
test.describe("scanning an extra folder (GitHub #3)", () => {
  test.use({ persona: "admin" });

  const scanJob = (id: number, target: string) => ({
    id, kind: target === "folder:7" ? "movie.scan" : "series.scan", target, trigger: "user:1", status: "succeeded", progress: 1,
    message: "Added 3 movies; 1 folder not recognised", error: "", result: { imported: 3, skipped: 0, unmatched: 1, attached: 1, duplicates: 0 },
    created_at: null, started_at: null, finished_at: null,
  });

  test("adding a folder that holds titles offers a scan, follows it and says what it found", async ({ page, api }) => {
    const kids = { id: 7, media: "movie" as const, path: "/media/kids", label: "kids", rule: "" as const, position: 1, default: false, titles: 0 };
    let state: "none" | "added" | "scanned" = "none";
    await page.route((u) => u.pathname === "/api/v1/library/folders", async (route) => {
      const movie = state === "none"
        ? libraryFoldersMulti.movie.folders.slice(0, 1)
        : [libraryFoldersMulti.movie.folders[0], { ...kids, titles: state === "scanned" ? 4 : 0, disk: "ok", on_disk: 5 }];
      return route.fulfill({ contentType: "application/json", body: JSON.stringify({ movie: { multi: state !== "none", folders: movie }, series: libraryFoldersMulti.series }) });
    });
    await override(page, api, [
      { method: "GET", path: "/api/v1/system/browse", body: { path: "/media/kids", parent: "/media", dirs: [], path_disabled: false } },
      { method: "POST", path: "/api/v1/system/library/folders", status: 201, body: kids },
      { method: "POST", path: "/api/v1/movies/scan", status: 202, body: { status: "scanning", job_id: 931, existing: false } },
    ]);
    await page.route((u) => u.pathname === "/api/v1/jobs/931", async (route) => {
      state = "scanned";
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(scanJob(931, "folder:7")) });
    });
    await page.route((u) => u.pathname === "/api/v1/system/library/folders", async (route) => {
      if (route.request().method() === "POST") state = "added";
      return route.fallback();
    });

    await page.goto("/settings/library#more-folders");
    const card = page.locator("#more-folders");
    await card.getByRole("button", { name: "Add a folder…" }).first().click();
    await page.getByRole("button", { name: "Select this folder" }).click();
    await expect(card.getByText("This folder has 5 items. Scan it now to add what's already there?")).toBeVisible();
    const reviewReads = api.callsTo("GET", "/api/v1/movies/unmatched").length;
    await card.getByRole("button", { name: "Scan now" }).click();
    await expect(card.getByText("Found 4 titles; 1 needs review")).toBeVisible();
    // The needs-review list (in Media folders) reloads for what the scan couldn't identify.
    await expect.poll(() => api.callsTo("GET", "/api/v1/movies/unmatched").length).toBeGreaterThan(reviewReads);
    await expect(card.getByRole("link", { name: "Review them ↑" })).toHaveAttribute("href", "#needs-review");
    expect(api.callsTo("POST", "/api/v1/movies/scan").map((c) => c.body)).toEqual([{ root: "/media/kids" }]);
    // The scan's end reloads the folders: the row counts what it added.
    const row = card.locator("div.rounded-xl", { has: page.getByLabel("Name of the kids folder") });
    await expect(row.getByText("4 titles", { exact: true })).toBeVisible();
  });

  test("each extra folder scans on its own, says what isn't in the library yet, and a missing one says so", async ({ page, api }) => {
    const folders = {
      movie: libraryFoldersMulti.movie,
      series: { multi: true, folders: [
        libraryFoldersMulti.series.folders[0],
        { ...libraryFoldersMulti.series.folders[1], titles: 0, on_disk: 12 },
        { id: 5, media: "series", path: "/mnt/cartoons", label: "Cartoons", rule: "", position: 2, default: false, titles: 3, disk: "missing" },
        { id: 6, media: "series", path: "/mnt/nas/shows", label: "NAS", rule: "", position: 3, default: false, titles: 8, disk: "unknown" },
      ] },
    };
    await override(page, api, [
      { method: "GET", path: "/api/v1/library/folders", body: folders },
      { method: "POST", path: "/api/v1/movies/scan", status: 202, body: { status: "scanning", job_id: 932, existing: false } },
      { method: "POST", path: "/api/v1/series/scan", status: 202, body: { status: "scanning", job_id: 933, existing: false } },
      { method: "GET", path: "/api/v1/jobs/932", body: scanJob(932, "folder:3") },
      { method: "GET", path: "/api/v1/jobs/933", body: { ...scanJob(933, "folder:4"), result: { imported: 12, skipped: 0, unmatched: 0 } } },
    ]);
    await page.goto("/settings/library#more-folders");
    const card = page.locator("#more-folders");
    await expect(card.getByText("12 folders here aren't in the library yet.")).toBeVisible();
    await expect(card.getByText("Can't find this folder — is its drive or share mounted?")).toBeVisible();
    await expect(card.getByRole("button", { name: "Scan the Cartoons folder" })).toHaveCount(0);
    // A dead share (it didn't answer in time) says so and isn't offered a scan either.
    await expect(card.getByText("This folder is not responding — check its drive or share.")).toBeVisible();
    await expect(card.getByRole("button", { name: "Scan the NAS folder" })).toHaveCount(0);
    // The main folders scan from Media folders, not here.
    await expect(card.getByRole("button", { name: /^Scan the (Movies|TV) folder$/ })).toHaveCount(0);

    await card.getByRole("button", { name: "Scan the Anime folder" }).click();
    await expect(card.getByText("Found 12 titles.")).toBeVisible();
    await card.getByRole("button", { name: "Scan the Kids folder" }).click();
    await expect(card.getByText("Found 4 titles; 1 needs review")).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/series/scan").map((c) => c.body)).toEqual([{ root: "/media/anime" }]);
    expect(api.callsTo("POST", "/api/v1/movies/scan").map((c) => c.body)).toEqual([{ root: "/media/kids" }]);
    // Media folders' own Movies row doesn't take a folder's scan for the library's.
    await expect(page.locator("#media-folders").getByText(/Found|Done:/)).toHaveCount(0);
  });

  test("the Media folders card points to the extra folders", async ({ page }) => {
    await page.goto("/settings/library");
    await page.locator("#media-folders").getByRole("link", { name: /Add them under More Movies and TV folders below/ }).click();
    await expect(page).toHaveURL(/\/settings\/library#more-folders$/);
    await expect(page.locator("#more-folders")).toBeInViewport();
  });

  test("the settings search finds the extra folders the way people ask for them", async ({ page }) => {
    await page.goto("/settings/library");
    const search = page.getByRole("searchbox", { name: "Search settings" });
    for (const q of ["animation", "kids", "anime", "extra folder", "second folder", "multiple folders"]) {
      await search.fill(q);
      await expect(page.getByRole("button", { name: /More Movies and TV folders/ }), q).toBeVisible();
    }
    await page.getByRole("button", { name: /More Movies and TV folders/ }).click();
    await expect(page).toHaveURL(/\/settings\/library#more-folders$/);
  });
});

test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("sees a folder's downloads folder but can't change it, and nothing when there's none", async ({ page, api }) => {
    const withOwn = {
      movie: { ...libraryFoldersMulti.movie, folders: [libraryFoldersMulti.movie.folders[0], { ...libraryFoldersMulti.movie.folders[1], downloads: "/mnt/disk2/downloads" }] },
      series: libraryFoldersMulti.series,
    };
    await override(page, api, [{ method: "GET", path: "/api/v1/library/folders", body: withOwn }]);
    await page.goto("/settings/library#more-folders");
    const card = page.locator("#more-folders");
    await expect(card.getByText("/mnt/disk2/downloads")).toBeVisible();
    await expect(card.getByRole("button", { name: "Download to a folder on this disk…" })).toHaveCount(0);
    await expect(card.getByRole("button", { name: "Use the Downloads folder" })).toHaveCount(0);
  });
});

test.describe("requester", () => {
  test.use({ persona: "requester" });

  test("never sees or loads anything about folders", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();
    expect(api.callsTo("GET", "/api/v1/library/folders")).toHaveLength(0);
  });
});

// GitHub #136: a server that sent "candidates": null for a folder it found nothing for
// crashed all of Settings, and a reload brought the same list back. The page shows the
// folder with no candidates to pick, and the rest of Settings → Library works.
test.describe("needs review with a folder that has no candidates (GitHub #136)", () => {
  test.use({ persona: "admin" });

  test("Settings → Library still opens and lists the folder", async ({ page, api }) => {
    await override(page, api, [
      { method: "GET", path: "/api/v1/series/unmatched", body: { unmatched: [
        { folder: "Unknown Thing", root: "/media/tv", title: "Unknown Thing", year: 0, candidates: null },
      ] } },
      { method: "GET", path: "/api/v1/movies/unmatched", body: { unmatched: null } },
    ]);
    await page.goto("/settings/library");
    await expect(page.getByText("TV Shows — needs review")).toBeVisible();
    await expect(page.getByText("Unknown Thing", { exact: true })).toBeVisible();
    await expect(page.getByText("No candidates — search a different title below.")).toBeVisible();
    await expect(page.getByLabel("Movies folder")).toBeVisible();
    await expect(page.getByText("Something broke on this page")).toHaveCount(0);
  });
});
