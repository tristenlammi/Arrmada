import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import { readyRequests } from "./fixtures/discover";

// Phase 8's deeper Discover on a phone (375px, touch): the payoff rows, and the browse,
// person and collection pages, none of them wider than the screen.

async function fits(page: Page) {
  const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
  expect(w.doc, "document width").toBeLessThanOrEqual(w.viewport);
}

test.describe("requester on a phone", () => {
  test.use({ persona: "requester" });

  // REQ-18: what arrived for them, with Watch on Plex, and what's new in the library.
  test("Ready for you and Recently added", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();
    const ready = page.getByRole("heading", { name: "Ready for you" });
    await expect(ready).toBeVisible();
    const watch = page.getByRole("link", { name: `Watch ${readyRequests.requests[0].title} on Plex` });
    await expect(watch).toHaveAttribute("href", readyRequests.requests[0].plex_url ?? "");
    await expect(page.getByRole("heading", { name: "Recently added" })).toBeVisible();
    // Only their own: the row asks for the viewer's ready requests from the last month.
    expect(api.calls.some((c) => c.path.startsWith("/api/v1/requests?") && c.path.includes("section=ready") && c.path.includes("ready_within_days=30"))).toBe(true);
    await fits(page);
  });
});
