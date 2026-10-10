import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import { mockAudio } from "./fixtures/audio";
import { NOW } from "./fixtures/clock";

// The built-in audiobook player at 375px (APP-09): Listen on the shelf starts the book in
// the mini-player through the listening API, playback carries on across pages and across
// the book's files, the place is synced every 15 s and the session closed on pause and on
// page hide, and a jump the place guards hold says so.
//
// The "files" are silent WAVs (fixtures/audio.ts), so Chromium really plays them. The
// clock is installed so a spec can run 15 s of timers without waiting for them.

test.describe("audiobook player at 375px", () => {
  test.use({ persona: "requester" });

  async function startFromShelf(page: Page, api: MockedApi) {
    await page.clock.install({ time: NOW });
    await page.goto("/shelf");
    await page.getByRole("button", { name: /^Listen to The Lighthouse Keeper/ }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/items/b2v7/play").length).toBe(1);
    const mini = page.getByTestId("mini-player");
    await expect(mini.getByRole("button", { name: "Pause", exact: true })).toBeVisible();
    return mini;
  }

  test("plays from the shelf, keeps playing across pages, syncs and closes", async ({ page, api }) => {
    await mockAudio(page, api);
    const mini = await startFromShelf(page, api);

    // The device is named by kind, nothing else.
    expect(api.callsTo("POST", "/api/v1/me/audio/items/b2v7/play")[0].body).toMatchObject({ device_name: "Android" });
    // The bar sits above the tab bar and the page pads past both.
    const geo = await page.evaluate(() => {
      const bar = document.querySelector("[data-testid=mini-player]")!.getBoundingClientRect();
      const tabs = document.querySelector("nav[aria-label=Primary]")!.getBoundingClientRect();
      return { barBottom: bar.bottom, tabsTop: tabs.top, hasPlayer: document.documentElement.classList.contains("has-player") };
    });
    expect(geo.hasPlayer).toBe(true);
    expect(geo.barBottom).toBeLessThanOrEqual(geo.tabsTop + 1.5); // the bar may sit on the tab bar's 1px top border

    // Playback crosses from file 1 (3 s) into file 2 on its own.
    await expect.poll(() => api.callsTo("GET", "/api/v1/me/audio/items/b2v7/file/2").length, { timeout: 10_000 }).toBeGreaterThan(0);

    // Another page: the bar and the sound stay.
    await page.getByRole("link", { name: "Discover" }).first().tap();
    await expect(page).toHaveURL(/\/discover/);
    await expect(mini.getByRole("button", { name: "Pause", exact: true })).toBeVisible();

    // Every 15 s while playing.
    await page.clock.runFor(15_500);
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/sync").length).toBeGreaterThanOrEqual(2);
    const syncs = api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/sync");
    const sync = syncs[syncs.length - 1].body as { current_time: number; time_listened: number };
    expect(sync.current_time).toBeGreaterThan(3);
    expect(sync.time_listened).toBeGreaterThan(0);

    // Pause closes the session where it got to.
    await mini.getByRole("button", { name: "Pause", exact: true }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close").length).toBe(1);
    const closed = api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close")[0].body as { current_time: number };
    expect(closed.current_time).toBeGreaterThan(3);
    await expect(mini.getByRole("button", { name: "Play", exact: true })).toBeVisible();
  });

  test("page hide closes the session with a beacon", async ({ page, api }) => {
    await mockAudio(page, api);
    await startFromShelf(page, api);
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent("pagehide")));
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close")[0].body).toMatchObject({ current_time: expect.any(Number), time_listened: expect.any(Number) });
  });

  test("a held jump back says so", async ({ page, api }) => {
    const m = await mockAudio(page, api, { startAt: 400 });
    const mini = await startFromShelf(page, api);
    m.reply = { position: 400, held_position: 4 };
    await page.clock.runFor(15_500);
    await expect(mini.getByText("Jumped back — kept once you listen on")).toBeVisible();
    m.reply = {};
    await page.clock.runFor(15_500);
    await expect(mini.getByText("Jumped back — kept once you listen on")).toHaveCount(0);
  });
});
