import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import { longShow, newShow, partlyOwnedShow, seasonsRequest } from "./fixtures/discover";

// REQ-13: asking for a show by season. On a phone: "＋ Request" on a show opens the sheet
// on "which seasons?" and files nothing by itself; picking seasons sends just those; a
// show partly here offers "Request more seasons"; a 35-season show scrolls inside the
// sheet; staff untick seasons before approving. On a desktop: a show's hover
// quick-request opens the sheet, a film's still requests straight away.

// serve answers one Discover feed with the season-picker shows. Registered after the
// fixture table, so it wins for that endpoint.
async function serve(page: Page, path: string) {
  await page.route((u) => u.pathname === path, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [newShow, partlyOwnedShow, longShow] }) }),
  );
}

async function fitsScreen(page: Page) {
  const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
  expect(w.doc, "document width with the sheet open").toBeLessThanOrEqual(w.viewport);
}

const box = (page: Page, n: number) => page.getByRole("checkbox", { name: new RegExp(`^Season ${n}\\b`) });

test.describe("phone", () => {
  test.skip(({ isMobile }) => !isMobile, "phone project only");
  test.use({ persona: "requester" });

  test("a show's ＋ Request opens the season picker; the seasons picked are what's asked for", async ({ page, api }) => {
    await serve(page, "/api/v1/discover/trending");
    await page.goto("/discover");
    await api.quiet();

    // The hero's button (the one visible "＋ Request" on a touch screen) is on the show.
    await page.getByRole("button", { name: "＋ Request" }).first().tap();
    const sheet = page.getByRole("dialog", { name: newShow.title });
    await expect(sheet).toBeVisible();
    const picker = sheet.getByRole("region", { name: "Choose seasons" });
    await expect(picker).toBeVisible();
    await api.quiet();
    expect(api.callsTo("POST", "/api/v1/requests"), "opening the picker files nothing").toEqual([]);
    await fitsScreen(page);

    // The whole show is the default; what can't be asked for says why.
    await expect(picker.getByRole("button", { name: "All seasons", exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(box(page, 4)).toBeDisabled();
    await expect(picker.getByText("Not out yet")).toBeVisible();
    // A season someone else asked for can be ticked: asking for it follows their request.
    await expect(box(page, 3)).toBeEnabled();

    await box(page, 1).tap(); // out of "all seasons": S2 and S3 stay ticked
    await expect(picker.getByText(/you’ll follow their request/)).toBeVisible();
    await box(page, 3).tap();
    await picker.getByRole("button", { name: "Request 1 season" }).tap();

    await expect(page.getByText(`Requested “${newShow.title}” S2 — waiting for approval`)).toBeVisible();
    const posted = api.callsTo("POST", "/api/v1/requests");
    expect(posted).toHaveLength(1);
    expect(posted[0].body).toMatchObject({ media_type: "series", tmdb_id: newShow.tmdb_id, seasons: [2] });
    // Season 1 is still nobody's: it can be asked for next.
    await expect(sheet.getByRole("button", { name: "Request more seasons" })).toBeVisible();
  });

  test("a show partly here offers its missing seasons", async ({ page, api }) => {
    await serve(page, "/api/v1/discover/search");
    await page.goto(`/discover?q=${encodeURIComponent("Lighthouse")}`);
    await api.quiet();
    await page.getByRole("button", { name: `View details for ${partlyOwnedShow.title}` }).tap();

    const sheet = page.getByRole("dialog", { name: partlyOwnedShow.title });
    await expect(sheet.getByText("✓ Partly in your library")).toBeVisible();
    await sheet.getByRole("button", { name: "Request more seasons" }).tap();
    for (const n of [1, 2, 3]) await expect(box(page, n)).toBeDisabled();
    await sheet.getByRole("button", { name: "All missing" }).tap();
    await sheet.getByRole("button", { name: "Request 1 season" }).tap();

    await api.quiet();
    const posted = api.callsTo("POST", "/api/v1/requests");
    expect(posted).toHaveLength(1);
    expect(posted[0].body).toMatchObject({ tmdb_id: partlyOwnedShow.tmdb_id, seasons: [4] });
  });

  test("a 35-season show scrolls inside the sheet", async ({ page, api }) => {
    await serve(page, "/api/v1/discover/search");
    await page.goto(`/discover?q=${encodeURIComponent("Evergreen")}`);
    await api.quiet();
    await page.getByRole("button", { name: `View details for ${longShow.title}` }).tap();
    const sheet = page.getByRole("dialog", { name: longShow.title });
    await sheet.getByRole("button", { name: "＋ Request" }).tap();

    const list = sheet.getByRole("group", { name: "Seasons" });
    await expect(list).toBeVisible();
    const scroll = await list.evaluate((el) => ({ inner: el.scrollHeight, box: el.clientHeight }));
    expect(scroll.inner, "the season list scrolls on its own").toBeGreaterThan(scroll.box);
    // The submit button sits right under the list, not 35 rows down.
    await expect(sheet.getByRole("button", { name: "Request all seasons" })).toBeInViewport();
    await fitsScreen(page);
    await box(page, 35).scrollIntoViewIfNeeded();
    await expect(box(page, 35)).toBeInViewport();
  });
});

test.describe("phone, staff", () => {
  test.skip(({ isMobile }) => !isMobile, "phone project only");
  test.use({ persona: "admin" });

  test("staff untick a season before approving", async ({ page, api }) => {
    await page.goto(`/requests?id=${seasonsRequest.id}`);
    const sheet = page.getByRole("dialog", { name: seasonsRequest.title });
    await expect(sheet).toBeVisible();
    for (const n of [1, 2, 3]) await expect(box(page, n)).toBeChecked();
    await box(page, 2).tap();
    await fitsScreen(page);
    await sheet.getByRole("button", { name: "Approve S1, S3" }).tap();

    await api.quiet();
    const posted = api.callsTo("POST", `/api/v1/requests/${seasonsRequest.id}/approve`);
    expect(posted).toHaveLength(1);
    expect(posted[0].body).toMatchObject({ seasons: [1, 3] });
  });
});

test.describe("desktop", () => {
  test.skip(({ isMobile }) => !!isMobile, "desktop project only");
  test.use({ persona: "requester" });

  test("a show's hover ＋ Request opens the sheet; a film's still requests it", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();

    // Each card's poster and its hover-revealed quick button share one wrapper. The button
    // only takes clicks while the card is hovered, and the card lifts as it is, so hover and
    // click are retried together until the click lands.
    const card = (title: string) => page.getByRole("button", { name: `View details for ${title}` }).first().locator("..");
    const quick = async (title: string) => {
      const c = card(title);
      await c.scrollIntoViewIfNeeded();
      await expect(async () => {
        await c.hover();
        await c.getByRole("button", { name: "＋ Request" }).click({ timeout: 1_000 });
      }).toPass({ timeout: 10_000 });
    };
    await quick("Undertow");
    await expect(page.getByRole("dialog", { name: "Undertow" }).getByRole("region", { name: "Choose seasons" })).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/requests")).toEqual([]);
    await page.keyboard.press("Escape");

    await quick("Long Haul");
    await expect(page.getByText("Requested “Long Haul” — waiting for approval")).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/requests")[0].body).toMatchObject({ media_type: "movie", tmdb_id: 1010 });
  });
});
