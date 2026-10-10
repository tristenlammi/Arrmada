import type { Page, Route } from "@playwright/test";
import { test, expect } from "./mockApi";
import * as ins from "./fixtures/insights";

// Insights on a 375 px phone (PLEX-18): no tab makes the page scroll sideways, the tab
// bar swipes, and a deep link opens its tab with the tab in view.

async function serveInsights(page: Page) {
  const table = ins.routes(() => ins.config("recording"));
  await page.route((u) => u.pathname.startsWith("/api/v1/insights/"), async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const r = table.find((t) => t.method === req.method() && t.path === url.pathname);
    if (!r) return route.fallback();
    const out = r.respond ? r.respond({ url, params: [], body: undefined }) : r.body;
    return route.fulfill({ status: r.status ?? 200, contentType: "application/json", body: JSON.stringify(out) });
  });
}

// The layout viewport, not innerWidth: a phone zooms out to fit an overflowing page.
const overflow = (page: Page) => page.evaluate(() => {
  const main = document.querySelector("main");
  return {
    doc: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    main: main ? main.scrollWidth - main.clientWidth : 0,
  };
});

test.describe("admin on a phone", () => {
  test.use({ persona: "admin" });

  for (const tab of ["activity", "history", "people", "graphs", "reliability"]) {
    test(`${tab} fits 375 px`, async ({ page, api }) => {
      await serveInsights(page);
      await page.goto(`/insights?tab=${tab}`);
      await expect(page.getByRole("tab", { selected: true })).toBeVisible();
      await api.quiet();
      expect(await overflow(page)).toEqual({ doc: 0, main: 0 });
    });
  }

  test("a deep link opens its tab, scrolled into the bar, and Back returns", async ({ page, api }) => {
    await serveInsights(page);
    await page.goto("/insights?tab=graphs");
    await page.goto("/insights?tab=reliability");
    const active = page.getByRole("tab", { name: "Reliability" });
    await expect(active).toHaveAttribute("aria-selected", "true");
    await api.quiet();
    // Reliability is the last tab (Settings moved to Settings → Plex), so it sits at the bar's
    // very end, where sub-pixel tab widths can leave a hair's width outside: 0.99, not 1.
    await expect(active).toBeInViewport({ ratio: 0.99 });
    await page.getByRole("tab", { name: "Graphs" }).click();
    await expect(page).toHaveURL(/tab=graphs/);
    await page.goBack();
    await expect(page).toHaveURL(/tab=reliability/);
    await expect(page.getByRole("tab", { name: "Reliability" })).toHaveAttribute("aria-selected", "true");
  });

  test("the old Users tab address opens People", async ({ page }) => {
    await serveInsights(page);
    await page.goto("/insights?tab=users");
    await expect(page).toHaveURL(/tab=people/);
    await expect(page.getByRole("tab", { name: "People" })).toHaveAttribute("aria-selected", "true");
  });
});
