import { test, expect } from "./mockApi";
import { readyRequests } from "./fixtures/discover";

// Phase 8's deeper Discover on a desktop: the payoff rows, 'See all' grids, paged search,
// person pages and collection pages, with Back retracing each step.

test.describe("requester on a desktop", () => {
  test.use({ persona: "requester" });

  // REQ-18: both payoff rows, the ready one with Watch on Plex.
  test("Ready for you and Recently added", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();
    await expect(page.getByRole("heading", { name: "Ready for you" })).toBeVisible();
    await expect(page.getByRole("link", { name: `Watch ${readyRequests.requests[0].title} on Plex` })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Recently added" })).toBeVisible();
  });
});
