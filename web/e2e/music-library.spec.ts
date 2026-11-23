import type { Page, Route } from "@playwright/test";
import { test, expect } from "./mockApi";
import type { Artist, MusicAlbum } from "../src/lib/api";

// FE-12: Music on the kit, same features as before. Add artist is a Modal (named, focus
// on the search box, Escape closes); removing artists, in bulk or from the artist page,
// asks the same question through the shared confirm and Cancel leaves them alone.

const artist: Artist = {
  id: 5, mbid: "mb-5", name: "The Quiet Harbours", monitored: true, quality_profile: "lossless", in_library: true,
  stats: {
    albums: 2, tracks: 20, have_tracks: 8, size_bytes: 400_000_000,
    albums_monitored: 2, albums_complete: 0, albums_unlisted: 1, albums_with_files: 1, albums_wanted: 2,
  },
  albums: [{ id: 51, artist_id: 5, mbid: "rg-51", title: "Low Tide", year: 2019, album_type: "Album", monitored: true, track_count: 10, have_tracks: 8, edition_tracks: 10 } as never],
};

// MUS-29: the album page lists tracks by disc from the album's target edition, names a
// track's own artist when it differs, and the artist page shows albums it appears on.
const album: MusicAlbum = {
  id: 51, artist_id: 5, mbid: "rg-51", title: "Low Tide", year: 2019, album_type: "Album", monitored: true,
  track_count: 3, have_tracks: 2, size_bytes: 90_000_000, edition_tracks: 3, held_tier: "MP3-320",
  artist_credit: "The Quiet Harbours", target_edition_id: 7,
  editions: [{ id: 7, album_id: 51, title: "Low Tide", track_count: 3, disc_count: 2, source: "musicbrainz", target: true, release_date: "2019-03-01", country: "GB", media_summary: "2×CD" }],
  discs: [{ id: 1, position: 1, track_count: 2 }, { id: 2, position: 2, title: "Bonus", track_count: 1 }],
  tracks: [
    { id: 1, album_id: 51, disc_number: 1, track_number: 1, title: "Undertow", monitored: true, has_file: true, format: "FLAC-24", lossless: true, codec: "flac", bit_depth: 24, sample_rate_hz: 96000, bitrate_kbps: 2100 },
    { id: 2, album_id: 51, disc_number: 1, track_number: 2, title: "Breakwater", monitored: true, has_file: true, format: "MP3-320", lossless: false, artist_credit: "The Quiet Harbours feat. Ana Lys" },
    { id: 3, album_id: 51, disc_number: 2, track_number: 1, title: "Lantern", monitored: true, has_file: false },
  ],
  extras: [{ id: 9, path: "/music/The Quiet Harbours/Low Tide/hidden track.flac", size_bytes: 20_000_000, quality: "FLAC", lossless: true }],
  notes: [{ id: 4, legacy_track_id: 3, kind: "shared_path", note: "Track 3 recorded the same file as track 1, which keeps it; this track is listed without a file." }],
};

const guest: Artist = {
  id: 6, mbid: "mb-6", name: "Ana Lys", monitored: true, quality_profile: "lossless", in_library: true,
  stats: { albums: 0, tracks: 0, have_tracks: 0, size_bytes: 0, albums_monitored: 0, albums_complete: 0, albums_unlisted: 0, albums_with_files: 0, albums_wanted: 0 },
  albums: [],
  appears_on: [{ id: 51, artist_id: 5, mbid: "rg-51", title: "Low Tide", year: 2019, album_type: "Album", monitored: true, track_count: 3, have_tracks: 2, size_bytes: 0, edition_tracks: 3, artist_credit: "The Quiet Harbours & Ana Lys" }],
};

const json = (route: Route, body: unknown) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });

async function mockMusic(page: Page) {
  const state = { deletes: 0, resolved: [] as string[] };
  await page.route((u) => /^\/api\/v1\/music\/albums\/\d+\/notes\/\d+\/resolve$/.test(u.pathname), (route) => {
    state.resolved.push(new URL(route.request().url()).pathname);
    return json(route, { status: "resolved" });
  });
  await page.route((u) => u.pathname === "/api/v1/music/artists", (route) => json(route, { artists: [artist] }));
  await page.route((u) => u.pathname === "/api/v1/music/artists/5", (route) => {
    if (route.request().method() === "DELETE") { state.deletes++; return route.fulfill({ status: 204 }); }
    return json(route, artist);
  });
  await page.route((u) => u.pathname === "/api/v1/music/artists/5/history", (route) => json(route, { events: [] }));
  await page.route((u) => u.pathname === "/api/v1/music/artists/6", (route) => json(route, guest));
  await page.route((u) => u.pathname === "/api/v1/music/artists/6/history", (route) => json(route, { events: [] }));
  await page.route((u) => u.pathname === "/api/v1/music/albums/51", (route) => json(route, album));
  return state;
}

test.describe("Music on the kit", () => {
  test.use({ persona: "admin" });

  test("Add artist is a dialog that Escape closes", async ({ page }) => {
    await mockMusic(page);
    await page.goto("/music");
    await page.getByRole("button", { name: "+ Add artist" }).click();
    const dialog = page.getByRole("dialog", { name: "Add an artist" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("textbox", { name: "Artist name" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  });

  test("bulk Remove asks first; Cancel keeps the artists", async ({ page }) => {
    const state = await mockMusic(page);
    await page.goto("/music");
    await expect(page.getByText("Partial", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await page.getByRole("button", { name: /The Quiet Harbours/ }).click();
    await page.getByRole("button", { name: "Remove", exact: true }).click();
    const ask = page.getByRole("dialog", { name: "Remove 1 artist from Arrmada?" });
    await expect(ask.getByText("Files on disk are kept.")).toBeVisible();
    await ask.getByRole("button", { name: "Cancel" }).click();
    await expect(ask).toHaveCount(0);
    expect(state.deletes).toBe(0);
  });

  test("the artist page's Delete asks the same question, then removes", async ({ page }) => {
    const state = await mockMusic(page);
    await page.goto("/music/5");
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    const ask = page.getByRole("dialog", { name: "Remove The Quiet Harbours and its albums?" });
    await expect(ask).toBeVisible();
    await ask.getByRole("button", { name: "Delete" }).click();
    await expect(page).toHaveURL(/\/music$/);
    expect(state.deletes).toBe(1);
  });

  test("the library card counts albums first", async ({ page }) => {
    await mockMusic(page);
    await page.goto("/music");
    await expect(page.getByText("0/2 albums")).toBeVisible();
    // Nothing complete, so the Complete filter has nobody.
    await page.getByRole("button", { name: /^Complete/ }).click();
    await expect(page.getByText("No artists match this filter.")).toBeVisible();
  });

  // #122: kinds the release types leave out are hidden (and counted under the list); one
  // that's wanted or has files stays, saying why.
  test("the artist page says what its release types hide and why an EP stays", async ({ page }) => {
    await mockMusic(page);
    const ep = { id: 52, artist_id: 5, mbid: "rg-52", title: "Harbour Lights", year: 2021, album_type: "EP", primary_type: "EP", monitored: true, outside_types: true, track_count: 4, have_tracks: 0, edition_tracks: 4 } as never;
    await page.route((u) => u.pathname === "/api/v1/music/artists/5", (route) =>
      json(route, { ...artist, albums: [...(artist.albums ?? []), ep], hidden_albums: 2 }));
    await page.goto("/music/5");
    await expect(page.getByText("Harbour Lights")).toBeVisible();
    await expect(page.getByText("Not a kind this artist's release types list; kept because it's wanted")).toBeVisible();
    await expect(page.getByTestId("hidden-albums")).toHaveText("2 more aren't shown: kinds this artist's release types leave out. Options changes the types.");
  });

  test("the album page lists its tracks by disc, with a featured artist named", async ({ page }) => {
    await mockMusic(page);
    await page.goto("/music/album/51");
    await expect(page.getByText("Edition: 2019-03-01 · GB · 2×CD")).toBeVisible();
    const disc1 = page.getByRole("region", { name: "Disc 1" });
    const disc2 = page.getByRole("region", { name: "Disc 2" });
    await expect(disc1.getByText("Undertow")).toBeVisible();
    await expect(disc1.getByText("The Quiet Harbours feat. Ana Lys")).toBeVisible();
    await expect(disc2.getByRole("heading", { name: "Disc 2 — Bonus" })).toBeVisible();
    await expect(disc2.getByText("Lantern")).toBeVisible();
    await expect(page.getByText("hidden track.flac")).toBeVisible();
  });

  // MUS-37: the scan results drawer says what the last scan found and links the folders
  // waiting to be matched.
  test("the last scan's results open from Library tools", async ({ page }) => {
    await mockMusic(page);
    await page.route((u) => u.pathname === "/api/v1/music/scan", (route) => json(route, {
      last: {
        folders: 1200, skipped: 1150, artists: 2, albums: 40, tracks: 480, moved: 12, unmatched: [], reviews: 3, failed: 0,
        waiting: 0, remaining: 0, opened: 520, measured: 0, workers: 4, walk_ms: 850, read_ms: 4100, total_ms: 6200,
        verify: { checked: 9000, not_found: 1, missing: 2, back: 0, changed: 1, skipped: 0 },
      },
      started_at: "2026-10-20T08:00:00Z", finished_at: "2026-10-20T08:00:06Z",
    }));
    await page.goto("/music");
    await page.getByRole("button", { name: "Library tools" }).click();
    await page.getByRole("menuitem", { name: "Last scan results" }).click();
    const drawer = page.getByRole("dialog", { name: "Library scan" });
    await expect(drawer.getByText("Unchanged since the last scan")).toBeVisible();
    await expect(drawer.getByText("Track files gone from disk")).toBeVisible();
    await expect(drawer.getByRole("link", { name: /Match the 3 folders in Reviews/ })).toHaveAttribute("href", "/activity/needs-you");
  });

  // MUS-15: the file check's markers on track rows.
  test("the album page marks tracks gone from disk or changed elsewhere", async ({ page }) => {
    await mockMusic(page);
    let searched = 0;
    await page.route((u) => u.pathname === "/api/v1/music/albums/51/search-missing", (route) => { searched++; return json(route, { status: "searching" }); });
    await page.route((u) => u.pathname === "/api/v1/music/albums/51", (route) => json(route, {
      ...album,
      missing_tracks: 1,
      tracks: [
        { ...album.tracks![0], changed_at: "2026-10-20T08:00:00Z" },
        album.tracks![1],
        { ...album.tracks![2], missing: true, file_path: "/music/The Quiet Harbours/Low Tide/03 Lantern.flac" },
      ],
    }));
    await page.goto("/music/album/51");
    await expect(page.getByText("gone from disk", { exact: true })).toBeVisible();
    await expect(page.getByText("changed", { exact: true })).toBeVisible();
    await expect(page.getByText("file missing", { exact: true })).toBeVisible();
    // Never searched by itself: the album page offers the search.
    const note = page.getByRole("region", { name: "Track files gone from disk" });
    await expect(note.getByText(/doesn't download them again by itself/)).toBeVisible();
    await expect(note.getByText(/Run a library scan first/)).toBeVisible();
    await note.getByRole("button", { name: "Search for missing tracks" }).click();
    const ask = page.getByRole("dialog", { name: "Search for the missing tracks?" });
    await expect(ask.getByText(/Moved or retagged these files yourself\?/)).toBeVisible();
    await ask.getByRole("button", { name: "Search" }).click();
    await expect.poll(() => searched).toBe(1);
  });

  test("the album page explains an upgrade note and resolves it", async ({ page }) => {
    const state = await mockMusic(page);
    await page.goto("/music/album/51");
    const notes = page.getByRole("region", { name: "Notes from the upgrade" });
    await expect(notes.getByText(/Searching skips this album/)).toBeVisible();
    await expect(notes.getByText(/Track 3 recorded the same file as track 1/)).toBeVisible();
    await notes.getByRole("button", { name: "Resolve" }).click();
    await expect.poll(() => state.resolved).toEqual(["/api/v1/music/albums/51/notes/4/resolve"]);
  });

  test("an artist page shows the albums it appears on", async ({ page }) => {
    await mockMusic(page);
    await page.goto("/music/6");
    await expect(page.getByRole("heading", { name: "Appears on" })).toBeVisible();
    await expect(page.getByText(/The Quiet Harbours & Ana Lys/)).toBeVisible();
    await page.getByRole("link", { name: /Low Tide/ }).click();
    await expect(page).toHaveURL(/\/music\/album\/51$/);
  });

  // MUS-14: the artist's options round trip. Saving sends what was chosen; wanting the
  // albums the types allow asks first and sends the counts it showed.
  test("an artist's options save, and Want matching albums asks first", async ({ page }) => {
    await mockMusic(page);
    const plan = { want: [{ id: 52, title: "Live at the Pier", year: 2021 }], unwant: [], singles: [] };
    const options = {
      monitor_new: "", release_types: null as null | { primary: string[]; secondary: string[] },
      effective_monitor_new: "all", effective_release_types: { primary: ["Album", "EP"], secondary: [] as string[] },
      default_monitor_new: "all", default_release_types: { primary: ["Album", "EP"], secondary: [] as string[] },
      quality_profile: "lossless", apply: { want: [], unwant: [], singles: [] } as typeof plan,
    };
    const choices = { primary: ["Album", "EP", "Single", "Broadcast", "Other"], secondary: ["Compilation", "Soundtrack", "Live", "Remix"] };
    const sent: unknown[] = [];
    const applied: unknown[] = [];
    await page.route((u) => u.pathname === "/api/v1/music/artists/5/options", async (route) => {
      if (route.request().method() === "PUT") {
        const body = route.request().postDataJSON() as { monitor_new: string; release_types: typeof options.release_types };
        sent.push(body);
        Object.assign(options, { monitor_new: body.monitor_new, release_types: body.release_types, effective_release_types: body.release_types ?? options.default_release_types, apply: plan });
        return json(route, { options, choices, refreshed: true });
      }
      return json(route, { options, choices });
    });
    await page.route((u) => u.pathname === "/api/v1/music/artists/5/options/apply", (route) => {
      applied.push(route.request().postDataJSON());
      return json(route, { applied: plan });
    });

    await page.goto("/music/5");
    await page.getByRole("button", { name: "Options", exact: true }).click();
    const panel = page.getByRole("region", { name: "Artist options" });
    await expect(panel.getByText("Every listed album already matches these types.")).toBeVisible();
    await panel.getByRole("switch", { name: /use the library default/ }).click();
    await panel.getByLabel("Live").check();
    await panel.getByRole("button", { name: "Save options" }).click();
    await expect.poll(() => sent).toEqual([{ monitor_new: "", release_types: { primary: ["Album", "EP"], secondary: ["Live"] } }]);

    await panel.getByRole("button", { name: "Want matching albums…" }).click();
    const ask = page.getByRole("dialog", { name: "Apply the release types to the albums already listed?" });
    await expect(ask.getByText("Wanted: Live at the Pier (2021)")).toBeVisible();
    expect(applied).toEqual([]);
    await ask.getByRole("button", { name: "Apply" }).click();
    await expect.poll(() => applied).toEqual([{ want: [52], unwant: [], singles: [] }]);
  });

  test("New releases lists what the refresh found and wants one by hand", async ({ page }) => {
    await mockMusic(page);
    const wanted: unknown[] = [];
    await page.route((u) => u.pathname === "/api/v1/music/new-releases", (route) => json(route, {
      days: 30,
      albums: [
        { id: 61, artist_id: 5, artist_name: "The Quiet Harbours", title: "Spring Tide", album_type: "Album", release_date: "2026-10-03", monitored: true, found_as: "new" },
        { id: 62, artist_id: 5, artist_name: "The Quiet Harbours", title: "Early Demos", album_type: "Album", release_date: "2009-01-01", monitored: false, found_as: "back_catalogue" },
      ],
    }));
    await page.route((u) => u.pathname === "/api/v1/music/albums/62/monitor", (route) => {
      wanted.push(route.request().postDataJSON());
      return json(route, { monitored: true });
    });
    await page.goto("/music");
    await page.getByRole("button", { name: "New releases" }).click();
    const dialog = page.getByRole("dialog", { name: "New releases" });
    await expect(dialog.getByText("New release, wanted")).toBeVisible();
    await expect(dialog.getByText(/Older music MusicBrainz only now lists/)).toBeVisible();
    await dialog.getByRole("button", { name: "Want" }).click();
    await expect.poll(() => wanted).toEqual([{ monitored: true }]);
  });
});
