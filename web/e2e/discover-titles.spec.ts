import { test, expect } from "./mockApi";
import { requestableCard } from "./fixtures/discover";

// APP-07: every Discover title has its own address. The tab and the search live in the
// address too, so a reload keeps them, and "More like this" hops are steps Back retraces.

test.describe("staff on a desktop", () => {
  test.use({ persona: "admin" });

  test("a reload keeps the tab and the search", async ({ page, api }) => {
    await page.goto("/discover?tab=series&q=dune");
    await api.quiet();
    await expect(page.getByRole("heading", { name: /Results for “dune”/ })).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Search movies and TV" })).toHaveValue("dune");
    await page.reload();
    await api.quiet();
    await expect(page.getByRole("heading", { name: /Results for “dune”/ })).toBeVisible();
    await expect(page).toHaveURL(/\?tab=series&q=dune$/);
  });

  test("More like this hops, Back retraces them, Close returns to the list", async ({ page, api }) => {
    await page.goto("/discover?tab=movies");
    await api.quiet();
    await page.getByRole("button", { name: `View details for ${requestableCard.title}` }).first().click();
    await expect(page).toHaveURL(/\/discover\/movie\/1001\?tab=movies$/);
    await expect(page.getByRole("dialog", { name: requestableCard.title })).toBeVisible();

    await page.getByRole("button", { name: "View Saltwind" }).click();
    await expect(page).toHaveURL(/\/discover\/series\/1003\?tab=movies$/);
    await expect(page.getByRole("dialog", { name: "Saltwind" })).toBeVisible();
    await page.getByRole("button", { name: "View Iron Tide" }).click();
    await expect(page).toHaveURL(/\/discover\/movie\/1004/);
    await expect(page.getByRole("dialog", { name: "Iron Tide" })).toBeVisible();

    await page.goBack();
    await expect(page).toHaveURL(/\/discover\/series\/1003/);
    await expect(page.getByRole("dialog", { name: "Saltwind" })).toBeVisible();

    await page.getByRole("dialog", { name: "Saltwind" }).getByRole("button", { name: "Close", exact: true }).click();
    await expect(page).toHaveURL(/\/discover\?tab=movies$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    // The page's tab survived the round trip.
    await expect(page.getByRole("tab", { name: "Movies" })).toHaveAttribute("aria-selected", "true");
  });
});
