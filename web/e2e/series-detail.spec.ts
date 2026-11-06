import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";

// The series page (SER-14): a hero with the next episode, monitored progress and size;
// three primary actions (Monitor, Search missing, Choose release…) plus one overflow menu
// that holds every other action, reachable by keyboard and by touch at 375px.
// SER-15: a season rail (the choice kept in the address) and episode cards with stills,
// overviews and one menu each; a long anime season pages in 50 at a time.

async function fitsScreen(page: Page) {
  const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
  expect(w.doc, "document width").toBeLessThanOrEqual(w.viewport);
}

const OVERFLOW = ["Refresh & rescan", "Forget missing files…", "Rename…", "Manual import…", "Upload .torrent or .nzb…", "Switch to anime numbering", "Delete…"];

test.describe("desktop", () => {
  test.skip(({ isMobile }) => isMobile, "desktop project only");

  test("the hero shows the next episode, progress and size, with three actions and an overflow", async ({ page, api }) => {
    await page.goto("/series/1");
    const facts = page.getByTestId("series-facts");
    await expect(facts).toContainText("Next: S03E03");
    await expect(facts).toContainText("17/21");
    await expect(facts).toContainText("monitored episodes");
    await expect(facts).toContainText("GB");

    await expect(page.getByRole("button", { name: "Search missing" })).toBeVisible();
    await expect(page.getByRole("group", { name: "Show actions" }).getByRole("button", { name: "Choose release…" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Searching: On", exact: true })).toBeVisible();
    // The old nine-button toolbar is gone.
    for (const old of ["Auto-grab missing", "Upload torrent", "Manual import", "Refresh & rescan", "Search indexers"]) {
      await expect(page.getByRole("button", { name: old, exact: true })).toHaveCount(0);
    }

    // The keyboard opens the overflow, walks it, and Escape closes it back onto the trigger.
    const more = page.getByRole("button", { name: "More actions" });
    await more.focus();
    await page.keyboard.press("Enter");
    const menu = page.getByRole("menu", { name: "More actions" });
    await expect(menu).toBeVisible();
    for (const label of OVERFLOW) await expect(menu.getByRole("menuitem", { name: label })).toBeVisible();
    await expect(menu.getByRole("menuitem", { name: "Refresh & rescan" })).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(menu.getByRole("menuitem", { name: "Forget missing files…" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(menu).toHaveCount(0);
    await expect(more).toBeFocused();

    // Each item does its old job: Refresh & rescan refreshes.
    await more.click();
    await page.getByRole("menuitem", { name: "Refresh & rescan" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/series/1/refresh").length).toBe(1);

    // Rename… opens the preview in the kit's dialog; Escape closes it.
    await more.click();
    await page.getByRole("menuitem", { name: "Rename…" }).click();
    const rename = page.getByRole("dialog", { name: "Rename files" });
    await expect(rename).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(rename).toHaveCount(0);

    // Search missing is the whole-show search.
    await page.getByRole("button", { name: "Search missing" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/series/1/search").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/series/1/search")[0].body).toBeFalsy();
  });

  // A rescan while the TV share isn't mounted changes nothing and says why, rather than
  // marking every episode missing behind a "Refreshed" toast.
  test("a rescan the server refuses shows its reason", async ({ page }) => {
    const reason = "The TV folder isn't reachable (/tv is missing or empty) — nothing was changed. Check the share or array is mounted, then try again.";
    await page.route("**/api/v1/series/1/refresh", (route) =>
      route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ status: "error", message: reason }) }));
    await page.goto("/series/1");
    await page.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Refresh & rescan" }).click();
    await expect(page.getByText(reason)).toBeVisible();
    await expect(page.getByText("Refreshed metadata and rescanned disk.")).toHaveCount(0);
  });

  // Files deleted on purpose (#97): the dialog says what happens, then what it did.
  test("Forget missing files asks first, then says how many it cleared", async ({ page }) => {
    let posts = 0;
    await page.route("**/api/v1/series/1/forget-missing", (route) => {
      posts++;
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ cleared: 3 }) });
    });
    await page.goto("/series/1");
    await page.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Forget missing files…" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Nothing on disk is touched.");
    expect(posts).toBe(0);
    await dialog.getByRole("button", { name: "Forget missing files" }).click();
    await expect(page.getByText("Forgot 3 missing files.")).toBeVisible();
    await expect(dialog).toHaveCount(0);
    expect(posts).toBe(1);
  });

  test("Forget missing files shows why it was refused and stays open", async ({ page }) => {
    const reason = "The TV folder isn't reachable (/tv is missing or empty) — nothing was changed. Check the share or array is mounted, then try again.";
    await page.route("**/api/v1/series/1/forget-missing", (route) =>
      route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ status: "error", message: reason }) }));
    await page.goto("/series/1");
    await page.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Forget missing files…" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Forget missing files" }).click();
    await expect(dialog).toContainText(reason);
  });

  test("the Searching menu turns searching off, applies a preset and toggles new seasons", async ({ page, api }) => {
    await page.goto("/series/1");
    const monitor = page.getByRole("button", { name: "Searching: On", exact: true });
    await monitor.click();
    await page.getByRole("menuitem", { name: /Monitor: Future episodes/ }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/series/1/monitor").map((c) => c.body)).toContainEqual({ preset: "future" });

    await monitor.click();
    await page.getByRole("menuitem", { name: /^Turn searching off/ }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/series/1/monitor").map((c) => c.body)).toContainEqual({ monitored: false });

    await monitor.click();
    await page.getByRole("menuitem", { name: /Monitor new seasons/ }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/series/1/monitor").map((c) => c.body)).toContainEqual({ monitor_new_seasons: false });
  });
});

test.describe("desktop seasons", () => {
  test.skip(({ isMobile }) => isMobile, "desktop project only");

  test("opens on the first season with a gap, with stills and overviews showing", async ({ page }) => {
    await page.goto("/series/1");
    await expect(page.getByRole("button", { name: /^Season 2/, pressed: true })).toBeVisible();
    const cards = page.getByTestId("episode-card");
    await expect(cards).toHaveCount(8);
    const first = cards.first();
    await expect(first.locator("img")).toHaveAttribute("src", /\/t\/p\/w300\//);
    await expect(first).toContainText("What happens in S2E1");
    // E04 has no still: the warm fallback names the episode instead.
    await expect(cards.nth(3).locator("img")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Grab again" })).toBeVisible();
  });

  test("episodes show their files' audio languages, and the season sums them up", async ({ page }) => {
    await page.goto("/series/1?season=1");
    const cards = page.getByTestId("episode-card");
    await expect(cards).toHaveCount(10);
    // Each code carries its language in words; the fit is a mark and a sentence, never colour alone.
    const e1 = cards.first().getByRole("group", { name: /^Audio: English, German\. Languages fit/ });
    await expect(e1).toBeVisible();
    await expect(e1).toHaveAttribute("data-fit", "good");
    await expect(e1).toContainText("EN");
    await expect(e1.locator("[title='German']")).toHaveText("DE");
    const e9 = cards.nth(8).getByTestId("audio-langs");
    await expect(e9).toHaveAttribute("data-fit", "partial");
    await expect(e9).toHaveAttribute("aria-label", /Missing a preferred language: No English audio/);
    // A miss that rests on a detection has no colour: no fit, no mark, still marked detected.
    const e10 = cards.nth(9).getByTestId("audio-langs");
    await expect(e10.locator("[title^='French (detected) — ']")).toHaveText("FR");
    await expect(e10).not.toHaveAttribute("data-fit");
    await expect(e10).toHaveAttribute("aria-label", "Audio: English, French (detected)");
    // The season line: languages over its analysed files, and the one that misses (the
    // detected-only miss isn't one).
    const summary = page.getByTestId("season-audio");
    await expect(summary).toContainText("EN 9/10 · DE 9/10 · FR 1/10");
    await expect(summary).toHaveAttribute("aria-label", /1 miss a preferred language/);
    if (process.env.LANG_SHOTS) await page.getByRole("region", { name: "Season 1" }).screenshot({ path: `${process.env.LANG_SHOTS}/series-episodes.png` });

    // A season whose files haven't been analysed shows nothing.
    await page.getByRole("button", { name: /^Season 2/ }).click();
    await expect(page.getByTestId("episode-card").first()).toBeVisible();
    await expect(page.getByTestId("audio-langs")).toHaveCount(0);
    await expect(page.getByTestId("season-audio")).toHaveCount(0);
  });

  test("the season is kept in the address, and Specials have no Grab missing", async ({ page }) => {
    await page.goto("/series/1");
    await page.getByRole("button", { name: /^Season 3/ }).click();
    await expect(page).toHaveURL(/season=3/);
    await expect(page.getByTestId("episode-card")).toHaveCount(4);
    await page.reload();
    await expect(page.getByRole("button", { name: /^Season 3/, pressed: true })).toBeVisible();

    await page.getByRole("button", { name: /^Specials/ }).click();
    await expect(page.getByTestId("episode-card")).toHaveCount(2);
    await expect(page.getByRole("button", { name: /^Grab (missing|again)$/ })).toHaveCount(0);
  });

  test("a long anime season pages in 50 at a time", async ({ page }) => {
    await page.goto("/series/2");
    const cards = page.getByTestId("episode-card");
    await expect(cards).toHaveCount(50);
    await page.getByRole("button", { name: "Show more" }).click();
    await expect(cards).toHaveCount(100);
    await expect(page.getByText("100 of 130")).toBeVisible();
  });

  test("a pick belongs to its season: switching season clears it", async ({ page }) => {
    await page.goto("/series/1");
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await page.getByRole("checkbox", { name: /^Select S02E06/ }).check();
    await expect(page.getByRole("region", { name: "Selected episodes" })).toContainText("1 selected");
    await page.getByRole("button", { name: /^Season 3/ }).click();
    await expect(page.getByRole("region", { name: "Selected episodes" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Actions for S03E01" })).toBeVisible();
  });

  test("each episode's menu holds its actions", async ({ page, api }) => {
    await page.goto("/series/1");
    await page.getByRole("button", { name: "Actions for S02E06" }).click();
    const menu = page.getByRole("menu", { name: "Actions for S02E06" });
    await expect(menu.getByRole("menuitem")).toHaveText(["Grab again", "Choose release…", "Stop monitoring"]);
    await menu.getByRole("menuitem", { name: "Grab again" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/series/1/autograb").map((c) => c.body)).toContainEqual({ season: 2, episode: 6 });

    await page.getByRole("button", { name: "Actions for S02E01" }).click();
    await expect(page.getByRole("menu", { name: "Actions for S02E01" }).getByRole("menuitem")).toHaveText(["Choose release…", "Replace…", "Delete file…", "File details", "Stop monitoring"]);
    await page.getByRole("menuitem", { name: "Delete file…" }).click();
    await expect(page.getByRole("dialog")).toContainText("Delete the file for S02E01?");
  });
});

test.describe("desktop tabs", () => {
  test.skip(({ isMobile }) => isMobile, "desktop project only");

  test("each tab deep-links and survives a reload", async ({ page }) => {
    await page.goto("/series/1?tab=activity");
    await expect(page.getByRole("tab", { name: "Activity", selected: true })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("tab", { name: "Activity", selected: true })).toBeVisible();
    await page.getByRole("tab", { name: "Files" }).click();
    await expect(page).toHaveURL(/tab=files/);
    await expect(page.getByRole("region", { name: "Files by season" })).toContainText("Season 2");
    await expect(page.getByRole("region", { name: "Rename" })).toContainText("Every file already follows the naming scheme.");
    await page.getByRole("tab", { name: "Episodes" }).click();
    await expect(page).not.toHaveURL(/tab=/);
    await expect(page.getByTestId("episode-card").first()).toBeVisible();
  });

  test("Activity shows grabs, imports and a search that found nothing, with its reasons", async ({ page }) => {
    await page.goto("/series/1?tab=activity");
    const list = page.getByTestId("activity-list");
    await expect(list).toContainText("S03E01 — Harbour.Lights.S03E01");
    await expect(list).toContainText("S03E02 — Harbour.Lights.S03E02");
    await expect(list).toContainText("Found 34 releases — 20 for other titles, 14 over your bitrate ceiling");
    await expect(list).toContainText("No releases found");
    // The click's own 'searched' history line is the same search: it shows once.
    await expect(list).not.toContainText("Season 2: Grabbed nothing");
    await list.getByRole("button", { name: /Found 34 releases/ }).click();
    await expect(list).toContainText("34 found · 0 fit · 0 grabbed");

    await page.getByRole("button", { name: "Searched", exact: true }).click();
    await expect(list.locator(":scope > li")).toHaveCount(2);
    await page.getByRole("button", { name: "Imported", exact: true }).click();
    await expect(list.locator(":scope > li")).toHaveCount(1);
  });

  test("Numbering is hidden on a clean standard show and shown with a pending change or held files", async ({ page }) => {
    await page.goto("/series/4");
    await expect(page.getByRole("tab", { name: "Activity" })).toBeVisible();
    await expect(page.getByRole("tab", { name: /Numbering/ })).toHaveCount(0);
    // A deep link to a tab the show doesn't have opens Episodes.
    await page.goto("/series/4?tab=numbering");
    await expect(page.getByRole("tab", { name: "Episodes", selected: true })).toBeVisible();

    await page.goto("/series/3?tab=numbering");
    await expect(page.getByRole("tab", { name: /Numbering/, selected: true })).toBeVisible();
    const change = page.getByRole("region", { name: "Numbering change" });
    await expect(change).toContainText("S01E09 → S02E01");
    await change.getByRole("button", { name: "Review" }).click();
    await expect(page.getByRole("dialog", { name: "Numbering change" })).toBeVisible();
    await page.keyboard.press("Escape");

    // Harbour Lights has a download held because its files couldn't be placed.
    await page.goto("/series/1?tab=numbering");
    await expect(page.getByRole("region", { name: "Files that couldn't be placed" })).toContainText("Fixture.Show.S01.1080p.WEB-DL-GRP");
    await expect(page.getByRole("link", { name: "Activity → Needs you" })).toHaveAttribute("href", "/activity/needs-you");
  });
});

test.describe("desktop misplaced files", () => {
  test.skip(({ isMobile }) => isMobile, "desktop project only");

  // #56: files an import put in a folder named like a season. The banner opens a preview;
  // putting back sends exactly the previewed moves and recycles, never the ones left alone.
  test("the banner opens the preview, and Put back sends what it showed", async ({ page, api }) => {
    await page.goto("/series/1");
    await expect(page.getByText(/outside its folder/)).toHaveCount(0);

    await page.goto("/series/4");
    await expect(page.getByText("2 files of this show are outside its folder, in a folder named like a season.")).toBeVisible();
    await page.getByRole("button", { name: "Review" }).click();
    const dialog = page.getByRole("dialog", { name: "Put misplaced files back" });
    await expect(dialog).toContainText("1 file to move back");
    await expect(dialog).toContainText("/tv/Anchor Point");
    await expect(dialog).toContainText("A better copy is already in the show's folder");
    await dialog.getByRole("button", { name: "Put back 2 files" }).click();
    await expect(dialog).toContainText("Moved 1 file back · 1 file sent to the recycle bin");
    const sent = api.callsTo("POST", "/api/v1/series/4/misplaced")[0].body as { items: { action: string }[] };
    expect(sent.items.map((it) => it.action)).toEqual(["move", "recycle"]);
    await dialog.getByRole("button", { name: "Close" }).last().click();
    await expect(dialog).toHaveCount(0);
  });

  test("the health check's link opens the preview straight away", async ({ page }) => {
    await page.goto("/series/4?fix=misplaced");
    await expect(page.getByRole("dialog", { name: "Put misplaced files back" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page).not.toHaveURL(/fix=misplaced/);
  });
});

test.describe("phone", () => {
  test.skip(({ isMobile }) => !isMobile, "phone project only");

  test("the hero and its menus fit a 375px screen and work by touch", async ({ page }) => {
    await page.goto("/series/1");
    await expect(page.getByTestId("series-facts")).toContainText("Next: S03E03");
    await fitsScreen(page);

    await page.getByRole("button", { name: "More actions" }).tap();
    const menu = page.getByRole("menu", { name: "More actions" });
    for (const label of OVERFLOW) await expect(menu.getByRole("menuitem", { name: label })).toBeInViewport();
    await fitsScreen(page);
    await menu.getByRole("menuitem", { name: "Delete…" }).tap();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.keyboard.press("Escape");

    await page.getByRole("button", { name: "Searching: On", exact: true }).tap();
    const mon = page.getByRole("menu", { name: "Searching: On" });
    await expect(mon.getByRole("menuitem", { name: /Monitor: All episodes/ })).toBeInViewport();
    await fitsScreen(page);
  });

  test("select mode: pick episodes, then monitor or search exactly those (SER-16)", async ({ page, api }) => {
    await page.goto("/series/1");
    await page.getByRole("button", { name: "Select", exact: true }).tap();
    for (const ep of ["S02E06", "S02E07", "S02E08"]) await page.getByRole("checkbox", { name: new RegExp(`^Select ${ep}`) }).tap();
    const bar = page.getByRole("region", { name: "Selected episodes" });
    await expect(bar).toContainText("3 selected");
    await expect(bar).toBeInViewport();
    await fitsScreen(page);
    await bar.getByRole("button", { name: "Monitor", exact: true }).tap();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/series/1/monitor/episodes").map((c) => c.body)).toContainEqual({ episode_ids: [2006, 2007, 2008], monitored: true });
    await expect(bar).toHaveCount(0);

    await page.getByRole("button", { name: "Select", exact: true }).tap();
    await page.getByRole("checkbox", { name: /^Select S02E06/ }).tap();
    await page.getByRole("checkbox", { name: /^Select S02E07/ }).tap();
    await page.getByRole("region", { name: "Selected episodes" }).getByRole("button", { name: "Search", exact: true }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/series/1/search").map((c) => c.body)).toContainEqual({ episodes: [{ season: 2, episode: 6 }, { season: 2, episode: 7 }] });
  });

  test("the Files, Activity and Numbering tabs fit a 375px screen", async ({ page }) => {
    for (const tab of ["files", "activity", "numbering"]) {
      await page.goto(`/series/1?tab=${tab}`);
      await expect(page.getByRole("tabpanel")).toBeVisible();
      await fitsScreen(page);
    }
  });

  test("episode cards: the overview opens on a tap and the menu works by touch", async ({ page }) => {
    await page.goto("/series/1");
    const card = page.getByTestId("episode-card").first();
    await expect(card.locator("img")).toBeVisible();
    const overview = card.getByRole("button", { name: /What happens in S2E1/ });
    await expect(overview).toHaveAttribute("aria-expanded", "false");
    await overview.tap();
    await expect(overview).toHaveAttribute("aria-expanded", "true");
    await fitsScreen(page);

    await card.getByRole("button", { name: "Actions for S02E01" }).tap();
    const menu = page.getByRole("menu", { name: "Actions for S02E01" });
    for (const item of ["Choose release…", "Replace…", "Delete file…", "File details", "Stop monitoring"]) {
      await expect(menu.getByRole("menuitem", { name: item })).toBeInViewport();
    }
    await fitsScreen(page);
  });
});
