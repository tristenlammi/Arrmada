import type { Page, Route } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import { calendarItems } from "./fixtures/media";

// APP-16: the Calendar on a phone. It opens as an agenda with readable, tappable rows;
// the month grid's '+N more' opens the whole day in a sheet that Back closes; and paging
// months quickly never shows an older month's answer.

async function open(page: Page, path: string, api: MockedApi) {
  await page.goto(path);
  await expect(page.getByRole("main")).toBeVisible();
  await api.quiet();
}

const row = (page: Page, title: string) => page.getByRole("link").filter({ hasText: title });

test.describe("requester at 375px", () => {
  test.use({ persona: "requester" });

  test("opens as an agenda with readable rows and no sideways scroll", async ({ page, api }) => {
    await open(page, "/calendar", api);
    await expect(page.getByRole("group", { name: "View" }).getByRole("button", { name: "Agenda" })).toHaveAttribute("aria-pressed", "true");
    const today = page.getByRole("region", { name: "Today" });
    await expect(today).toBeInViewport();
    // The episode's number and name are on the row itself, not in a tooltip.
    await expect(today.getByText("S01E03 · Rip Current")).toBeVisible();
    await expect(today.getByText("Movie · 2026")).toBeVisible();
    await expect(page.getByRole("region", { name: "Tomorrow" })).toHaveCount(0); // nothing tomorrow
    // Two days before today is outside the agenda's window.
    await expect(page.getByText("The Cartographer")).toHaveCount(0);
    const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
    expect(w.doc).toBeLessThanOrEqual(w.viewport);
    // Rows are full-size tap targets.
    const box = await row(page, "Rip Current").boundingBox();
    expect(box!.height).toBeGreaterThanOrEqual(44);
  });

  test("tapping an episode opens the show's Discover page", async ({ page, api }) => {
    await open(page, "/calendar", api);
    await row(page, "Rip Current").click();
    await expect(page).toHaveURL(/\/discover\/series\/1009$/);
    await expect(page.getByRole("dialog", { name: "Undertow" })).toBeVisible();
  });

  test("'+2 more' in the month grid opens the whole day, and Back closes it", async ({ page, api }) => {
    await open(page, "/calendar", api);
    await page.getByRole("button", { name: "Month", exact: true }).click();
    await expect(page).toHaveURL(/\?view=month$/);
    await page.getByRole("button", { name: /^\+2 more on / }).click();
    const sheet = page.getByRole("dialog");
    await expect(sheet).toBeVisible();
    const busy = calendarItems.filter((it) => it.date === calendarItems[4].date);
    await expect(sheet.getByRole("link")).toHaveCount(busy.length);
    await expect(sheet.getByText("S04E09 · Fog Signal")).toBeVisible();
    await expect(sheet.getByText("Unmonitored")).toBeVisible();

    await page.goBack();
    await expect(sheet).toBeHidden();
    await expect(page).toHaveURL(/\/calendar\?view=month$/);

    // Escape closes it too, and leaves no extra history behind.
    await page.getByRole("button", { name: /^\+2 more on / }).click();
    await expect(sheet).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();
    await expect(page).toHaveURL(/\/calendar\?view=month$/);
  });

  test("remembers the view picked", async ({ page, api }) => {
    await open(page, "/calendar", api);
    await page.getByRole("button", { name: "Month", exact: true }).click();
    await open(page, "/calendar", api);
    await expect(page.getByRole("button", { name: "Month", exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByRole("heading", { name: "October 2026" })).toBeVisible();
  });

  test("paging months quickly never shows an older month's answer", async ({ page, api }) => {
    // November answers slowly, December quickly, so November's answer arrives last. Both
    // grids hold 10 Dec, so if November's late answer replaced December's it would show.
    const answers: Record<string, { title: string; delay: number }> = {
      "2026-11-01": { title: "Stale November answer", delay: 1500 },
      "2026-11-29": { title: "Fresh December answer", delay: 200 },
    };
    await page.route((u) => u.pathname === "/api/v1/calendar", async (route: Route) => {
      const url = new URL(route.request().url());
      const start = url.searchParams.get("start") ?? "";
      api.calls.push({ method: "GET", path: url.pathname + url.search, body: undefined });
      const a = answers[start];
      if (!a) return route.fallback(); // October: the normal fixture
      await new Promise((r) => setTimeout(r, a.delay));
      const items = [{ date: "2026-12-10", type: "movie", title: a.title, subtitle: "", ref_id: 1, has_file: false, monitored: true, tmdb_id: 1004, media_type: "movie" }];
      try {
        await route.fulfill({ contentType: "application/json", body: JSON.stringify({ start, end: url.searchParams.get("end"), items }) });
      } catch { /* the page gave up on it, which is the point */ }
    });
    await open(page, "/calendar?view=month", api);
    await expect(page.getByRole("heading", { name: "October 2026" })).toBeVisible();
    const next = page.getByRole("button", { name: "Next month" });
    await next.click();
    await next.click();
    await expect(page.getByRole("heading", { name: "December 2026" })).toBeVisible();
    await expect(page.getByText("Fresh December answer")).toBeVisible();
    await page.waitForTimeout(1800); // November's answer has had time to land
    await expect(page.getByText("Stale November answer")).toHaveCount(0);
    await expect(page.getByText("Fresh December answer")).toBeVisible();
    // And while a month loads, the previous month's items are gone, not lingering.
    await page.getByRole("button", { name: "Previous month" }).click();
    await expect(page.getByText("Fresh December answer")).toHaveCount(0);
  });
});

test.describe("staff at 375px", () => {
  test.use({ persona: "admin" });

  test("rows go to the library page", async ({ page, api }) => {
    await open(page, "/calendar", api);
    await expect(row(page, "Rip Current")).toHaveAttribute("href", "/series/9");
    await expect(row(page, "Lanterns Over")).toHaveAttribute("href", "/movies/2");
  });
});

test.describe("staff at 1280px", () => {
  test.use({ persona: "admin", viewport: { width: 1280, height: 900 } });

  test("opens on the month grid", async ({ page, api }) => {
    await open(page, "/calendar", api);
    await expect(page.getByRole("heading", { name: "October 2026" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Month", exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByRole("link", { name: /The Cartographer/ })).toHaveAttribute("href", "/movies/7");
  });
});
