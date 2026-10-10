import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import { mockAudio } from "./fixtures/audio";
import { NOW } from "./fixtures/clock";
import { myAudio } from "./fixtures/system";
import { personas } from "./fixtures/users";

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
    expect(sync.current_time).toBeGreaterThanOrEqual(3); // into file 2
    expect(sync.time_listened).toBeGreaterThan(0);

    // Pause closes the session where it got to.
    await mini.getByRole("button", { name: "Pause", exact: true }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close").length).toBe(1);
    const closed = api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close")[0].body as { current_time: number };
    expect(closed.current_time).toBeGreaterThanOrEqual(3);
    await expect(mini.getByRole("button", { name: "Play", exact: true })).toBeVisible();
  });

  test("page hide closes the session with a beacon", async ({ page, api }) => {
    await mockAudio(page, api);
    await startFromShelf(page, api);
    await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent("pagehide")));
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close")[0].body).toMatchObject({ current_time: expect.any(Number), time_listened: expect.any(Number) });
  });

  test("Listen tab: shelves, the book sheet, play from a chapter (APP-10)", async ({ page, api }) => {
    await mockAudio(page, api);
    await page.clock.install({ time: NOW });
    await page.goto("/audiobooks");
    for (const shelf of ["Continue listening", "Continue series", "Recently added", "Finished"]) {
      await expect(page.getByRole("region", { name: shelf })).toBeVisible();
    }
    await expect(page.getByRole("region", { name: "All audiobooks" }).getByRole("button", { name: /Carl's Doomsday Scenario/ })).toBeVisible();
    const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, vp: document.documentElement.clientWidth }));
    expect(w.doc).toBeLessThanOrEqual(w.vp);

    await page.getByRole("region", { name: "Continue listening" }).getByRole("button", { name: /Dungeon Crawler Carl/ }).tap();
    await expect(page).toHaveURL(/[?&]book=b12/);
    const sheet = page.getByRole("dialog", { name: "Dungeon Crawler Carl" });
    await expect(sheet.getByRole("button", { name: /Resume at 0:01/ })).toBeVisible();
    await expect(sheet.getByRole("region", { name: "Your place" })).toBeVisible();
    await expect(sheet.getByRole("button", { name: "Dungeon Crawler Carl (Full cast)" })).toBeVisible();

    // A chapter plays from its start: the session opens at the saved place, and the jump
    // is reported straight away so a hold would show at once.
    await sheet.getByRole("button", { name: /Chapter 2: Mordecai/ }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/items/b12/play").length).toBe(1);
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/sessions/s-b12/sync").length).toBeGreaterThanOrEqual(1);
    expect((api.callsTo("POST", "/api/v1/me/audio/sessions/s-b12/sync")[0].body as { current_time: number }).current_time).toBeCloseTo(100, 0);
    await expect(sheet.getByRole("button", { name: /Chapter 2: Mordecai/ })).toHaveAttribute("aria-current", "true");
    await expect(sheet.getByRole("button", { name: /Pause/ })).toBeVisible();

    // Back closes the sheet; the book plays on in the mini-player.
    await page.goBack();
    await expect(sheet).toHaveCount(0);
    await expect(page.getByTestId("mini-player").getByRole("button", { name: "Pause", exact: true })).toBeVisible();
  });

  test("Listen tab: ?book= opens the sheet, ?tab=apps opens Apps & devices", async ({ page, api }) => {
    await mockAudio(page, api);
    await page.goto("/audiobooks?book=b13");
    await expect(page.getByRole("dialog", { name: "Carl's Doomsday Scenario" }).getByRole("button", { name: "▶ Play" })).toBeVisible();
    await page.goto("/audiobooks?tab=apps");
    await expect(page.getByRole("tab", { name: "Apps & devices" })).toHaveAttribute("aria-selected", "true");
  });

  test("Listen tab: switched off", async ({ page, api }) => {
    await mockAudio(page, api, { status: 403 });
    await page.goto("/audiobooks");
    await expect(page.getByText("Audiobooks are switched off at the moment")).toBeVisible();
  });

  test("full player: chapters, speed, sleep timer, bookmarks, Back closes (APP-11)", async ({ page, api }) => {
    await mockAudio(page, api);
    await page.clock.install({ time: NOW });
    await page.goto("/shelf?sleepdebug=1");
    await page.getByRole("button", { name: /^Listen to The Lighthouse Keeper/ }).tap();
    const mini = page.getByTestId("mini-player");
    await expect(mini.getByRole("button", { name: "Pause", exact: true })).toBeVisible();
    await mini.getByRole("button", { name: /^Open the player/ }).tap();
    const full = page.getByRole("dialog", { name: "Audiobook player" });
    await expect(full).toBeVisible();

    // Chapters: the playing one is marked; a tap goes there.
    await expect(full.getByRole("button", { name: /Chapter 1: The Stairwell/ })).toHaveAttribute("aria-current", "true");
    await full.getByRole("button", { name: /Chapter 3: The Safe Room/ }).tap();
    await expect(full.getByRole("button", { name: /Chapter 3: The Safe Room/ })).toHaveAttribute("aria-current", "true");
    await full.getByRole("button", { name: "Previous chapter" }).tap();
    await expect(full.getByRole("button", { name: /Chapter 2: Mordecai/ })).toHaveAttribute("aria-current", "true");

    // Speed: remembered on this device.
    await full.getByRole("button", { name: "Faster" }).tap();
    await full.getByRole("button", { name: "Faster" }).tap();
    await expect(full.getByText("1.2×")).toBeVisible();
    expect(await page.evaluate(() => localStorage.getItem("arrmada.player.rate"))).toBe("1.2");

    // Bookmarks: add with a note, delete after confirming.
    await full.getByRole("button", { name: /^Add at / }).tap();
    await full.getByRole("textbox", { name: "Bookmark note" }).fill("Mordecai shows up");
    await full.getByRole("button", { name: "Save" }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/items/b2v7/bookmarks").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/items/b2v7/bookmarks")[0].body).toMatchObject({ title: "Mordecai shows up" });
    await expect(full.getByText("Mordecai shows up")).toBeVisible();
    await full.getByRole("button", { name: /^Delete the bookmark at 3:20/ }).tap();
    await page.getByRole("button", { name: "Delete", exact: true }).tap();
    await expect.poll(() => api.callsTo("DELETE", "/api/v1/me/audio/items/b2v7/bookmarks/200").length).toBe(1);

    // Sleep: a minute (the debug option), then it pauses and saves the place.
    await full.getByRole("combobox", { name: "Sleep timer" }).selectOption("1");
    await expect(full.getByTestId("sleep-left")).toHaveText(/1 min left|60 s left/);
    await expect(mini.getByTestId("mini-sleep")).toBeVisible();
    await page.clock.runFor(61_000);
    await expect(full.getByRole("button", { name: "Play", exact: true })).toBeVisible();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/sessions/s-b2v7/close").length).toBe(1);
    await expect(full.getByTestId("sleep-left")).toHaveText("Off");

    // Back closes it, leaving the bar.
    await page.goBack();
    await expect(full).toHaveCount(0);
    await expect(mini).toBeVisible();
  });

  test("full player: a held jump back offers Keep it now", async ({ page, api }) => {
    const m = await mockAudio(page, api, { startAt: 400, reply: { position: 400, held_position: 4 } });
    const mini = await startFromShelf(page, api);
    await page.clock.runFor(15_500);
    await mini.getByRole("button", { name: /^Open the player/ }).tap();
    const full = page.getByRole("dialog", { name: "Audiobook player" });
    await expect(full.getByText(/Jumped back to/)).toBeVisible();
    m.reply = {};
    await full.getByRole("button", { name: "Keep it now" }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/accept").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/accept")[0].body).toEqual({ item: "b2v7" });
    await expect(full.getByText(/Jumped back to/)).toHaveCount(0);
  });

  test("Apps & devices: Android setup ticks green when the phone signs in (APP-12)", async ({ page, api }) => {
    let mine = myAudio(personas.requester);
    await page.route((u) => u.pathname === "/api/v1/me/audio" || u.pathname === "/api/v1/me/audio/password", async (route) => {
      const req = route.request();
      const path = new URL(req.url()).pathname;
      let body: unknown;
      try { body = req.postDataJSON(); } catch { body = req.postData(); }
      api.calls.push({ method: req.method(), path, body });
      if (path.endsWith("/password")) mine = { ...mine, has_password: true };
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(mine) });
    });
    await page.clock.install({ time: NOW });
    // The Me page's link (the account menu's too) opens the tab directly.
    await page.goto("/me");
    await page.getByRole("link", { name: "Audiobook apps & password" }).tap();
    await expect(page).toHaveURL(/\/audiobooks\?tab=apps/);

    const how = page.getByRole("radiogroup", { name: "How you'll listen" });
    const options = how.getByRole("radio");
    await expect(options.first()).toContainText("Lissen");
    await expect(options.first()).toContainText("Works");
    await how.getByRole("radio", { name: /Lissen/ }).tap();
    // No password yet: step 2 is the form.
    await page.getByPlaceholder("Password", { exact: true }).fill("listening-time");
    await page.getByPlaceholder("Type it again").fill("listening-time");
    // Enter submits (a tap here races the tab bar coming back as the keyboard closes).
    await page.getByPlaceholder("Type it again").press("Enter");
    await expect.poll(() => api.callsTo("PUT", "/api/v1/me/audio/password").length).toBe(1);
    await expect(page.getByText("Enter these in Lissen")).toBeVisible();
    await expect(page.getByText("Server type")).toBeVisible();
    await expect(page.getByText("Waiting for your phone…")).toBeVisible();

    // The phone signs in; the next check (4 s) ticks the step and names it.
    mine = { ...mine, devices: [{ id: "d1", user_id: 3, username: "deckhand", client: "Lissen", device: "Pixel 8", created_at: NOW, last_used_at: NOW }] };
    await page.clock.runFor(4_500);
    await expect(page.getByText("Pixel 8 · Lissen signed in ✓")).toBeVisible();
    const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, vp: document.documentElement.clientWidth }));
    expect(w.doc).toBeLessThanOrEqual(w.vp);
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

// An iPhone: no iPhone app has been listened with against Arrmada yet, so the setup
// recommends listening right here (from the Home Screen) and marks every app untested.
test.describe("Apps & devices on an iPhone", () => {
  test.use({ persona: "requester", userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1" });

  test("recommends the web player and never calls an untested app working", async ({ page }) => {
    await page.goto("/audiobooks?tab=apps");
    const how = page.getByRole("radiogroup", { name: "How you'll listen" });
    const options = how.getByRole("radio");
    await expect(options.first()).toContainText("Listen here");
    await expect(options.first()).toContainText("recommended");
    await expect(how).not.toContainText("Lissen");
    const rest = (await options.allTextContents()).slice(1);
    expect(rest.length).toBeGreaterThan(0);
    for (const t of rest) expect(t).toContain("Not tested yet");
    await options.first().tap();
    await expect(page.getByText(/Add to Home Screen/)).toBeVisible();
    await how.getByRole("radio", { name: /ShelfPlayer/ }).tap();
    await expect(page.getByText(/hasn't been tried with Arrmada on a phone yet/)).toBeVisible();
  });
});
