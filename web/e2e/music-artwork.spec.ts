import type { Page, Route } from "@playwright/test";
import { test, expect } from "./mockApi";
import type { Artist } from "../src/lib/api";

// #113: the Music library shows each artist's picture (one of its album covers, standing
// in for a photo no source supplies) and the artist page each album's cover. No picture,
// or one the Cover Art Archive can't find, shows a placeholder: the artist's initials, a
// record for an album. Runs on the desktop and the phone, which must not scroll sideways.

const caa = (rg: string) => `https://coverartarchive.org/release-group/${rg}/front-500`;
const stats = { albums: 3, tracks: 15, have_tracks: 8, size_bytes: 400_000_000, albums_monitored: 2, albums_complete: 0, albums_unlisted: 0, albums_with_files: 1, albums_wanted: 2 };
const none = { albums: 0, tracks: 0, have_tracks: 0, size_bytes: 0, albums_monitored: 0, albums_complete: 0, albums_unlisted: 0, albums_with_files: 0, albums_wanted: 0 };

const harbours: Artist = {
  id: 5, mbid: "mb-5", name: "The Quiet Harbours", monitored: true, quality_profile: "lossless", in_library: true, stats,
  cover_url: caa("rg-51"),
  albums: [
    { id: 51, artist_id: 5, mbid: "rg-51", title: "Low Tide", year: 2019, album_type: "Album", monitored: true, track_count: 10, have_tracks: 8, size_bytes: 300_000_000, edition_tracks: 10, cover_url: caa("rg-51") },
    { id: 52, artist_id: 5, mbid: "rg-52", title: "Bare Sleeve", year: 2021, album_type: "EP", monitored: true, track_count: 4, have_tracks: 0, size_bytes: 0, edition_tracks: 4 },
    { id: 53, artist_id: 5, mbid: "rg-404", title: "Lost Art", year: 2022, album_type: "Single", monitored: false, track_count: 1, have_tracks: 0, size_bytes: 0, edition_tracks: 1, cover_url: caa("rg-404") },
  ],
};
const bare: Artist = { id: 7, mbid: "mb-7", name: "Marrow Lane", monitored: true, quality_profile: "lossless", in_library: true, stats: none };
const broken: Artist = { ...bare, id: 8, mbid: "mb-8", name: "Glass Owl", cover_url: caa("rg-404") };

const json = (route: Route, body: unknown) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });

async function mockMusic(page: Page) {
  await page.route((u) => u.pathname === "/api/v1/music/artists", (route) => json(route, { artists: [harbours, bare, broken] }));
  await page.route((u) => u.pathname === "/api/v1/music/artists/5", (route) => json(route, harbours));
  await page.route((u) => u.pathname === "/api/v1/music/artists/5/history", (route) => json(route, { events: [] }));
  // The Cover Art Archive has nothing for rg-404 (the mock answers other images blank).
  await page.route(/coverartarchive\.org\/release-group\/rg-404\//, (route) => route.fulfill({ status: 404, body: "" }));
}

// Neither the document nor <main> (where the page scrolls) is wider than the screen.
const noSidewaysScroll = () => [document.documentElement, document.querySelector("main")].every((el) => !el || el.scrollWidth <= el.clientWidth);

test.describe("Music artwork", () => {
  test.use({ persona: "admin" });

  test("artist cards show a picture, or the artist's initials", async ({ page }) => {
    await mockMusic(page);
    await page.goto("/music");
    const card = (name: string) => page.getByRole("link", { name: new RegExp(name) });
    await expect(card("The Quiet Harbours").locator("img")).toHaveAttribute("src", caa("rg-51"));
    await expect(card("The Quiet Harbours").locator("img")).toBeVisible();
    await expect(card("Marrow Lane").locator('[data-placeholder="artist"]')).toHaveText("ML");
    await expect(card("Marrow Lane").locator("img")).toHaveCount(0);
    // A cover that won't load falls back the same way.
    await expect(card("Glass Owl").locator('[data-placeholder="artist"]')).toHaveText("GO");
    await expect(card("Glass Owl").locator("img")).toHaveCount(0);
    expect(await page.evaluate(noSidewaysScroll)).toBe(true);
  });

  test("the artist page shows its picture and each album's cover", async ({ page }) => {
    await mockMusic(page);
    await page.goto("/music/5");
    const row = (title: string) => page.getByRole("link", { name: new RegExp(title) });
    // Album rows ask the Cover Art Archive for its smaller thumbnail.
    const thumb = "https://coverartarchive.org/release-group/rg-51/front-250";
    await expect(row("Low Tide").locator("img")).toHaveAttribute("src", thumb);
    await expect(row("Low Tide").locator("img")).toBeVisible();
    await expect(row("Bare Sleeve").locator('[data-placeholder="album"]')).toBeVisible();
    await expect(row("Lost Art").locator('[data-placeholder="album"]')).toBeVisible();
    await expect(row("Lost Art").locator("img")).toHaveCount(0);
    // The header's picture is the artist's stand-in cover.
    await expect(page.locator(`main img[src="${thumb}"]`)).toHaveCount(2);
    expect(await page.evaluate(noSidewaysScroll)).toBe(true);
  });
});
