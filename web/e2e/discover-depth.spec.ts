import { test, expect } from "./mockApi";
import { readyRequests, requestableCard } from "./fixtures/discover";

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

test.describe("browse grids on a desktop", () => {
  test.use({ persona: "requester" });

  // REQ-19: a row's See all opens its whole list, which keeps loading past 20 titles.
  test("See all on Popular movies pages as it scrolls", async ({ page, api }) => {
    await page.goto("/discover");
    await api.quiet();
    await page.getByRole("link", { name: "See all: Popular movies" }).click();
    await expect(page).toHaveURL(/\/discover\/browse\?list=popular&media=movie$/);
    await expect(page.getByRole("heading", { name: "Popular movies" })).toBeVisible();
    await expect(page.getByRole("button", { name: `View details for ${requestableCard.title}` })).toBeVisible();
    await page.getByRole("button", { name: /View details for/ }).last().scrollIntoViewIfNeeded();
    await expect(page.getByRole("button", { name: "View details for Page 2 title 1", exact: true })).toBeVisible();
    expect(api.calls.some((c) => c.path === "/api/v1/discover/browse?list=popular&media=movie&page=2")).toBe(true);

    // A title opened from the grid sits over it; closing it lands back on the grid.
    await page.getByRole("button", { name: `View details for ${requestableCard.title}` }).click();
    await expect(page).toHaveURL(/\/discover\/browse\/movie\/1001\?list=popular&media=movie$/);
    await page.getByRole("dialog", { name: requestableCard.title }).getByRole("button", { name: "Close", exact: true }).click();
    await expect(page).toHaveURL(/\/discover\/browse\?list=popular&media=movie$/);
    await expect(page.getByRole("button", { name: "View details for Page 2 title 1", exact: true })).toBeVisible();
  });

  test("filters live in the address and survive a reload", async ({ page, api }) => {
    await page.goto("/discover/browse?media=movie");
    await api.quiet();
    await page.getByRole("group", { name: "Genres" }).getByRole("button", { name: "Science Fiction" }).click();
    await page.getByRole("combobox", { name: "Minimum rating" }).selectOption("7");
    await page.getByRole("textbox", { name: "From year" }).fill("1990");
    await page.getByRole("textbox", { name: "To year" }).fill("1999");
    await page.getByRole("textbox", { name: "To year" }).press("Enter");
    await expect(page).toHaveURL(/genre=878/);
    await expect(page).toHaveURL(/rating=7/);
    await expect(page).toHaveURL(/year_from=1990&year_to=1999/);
    await page.reload();
    await api.quiet();
    await expect(page.getByRole("group", { name: "Genres" }).getByRole("button", { name: "Science Fiction" })).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByRole("combobox", { name: "Minimum rating" })).toHaveValue("7");
    await expect(page.getByRole("textbox", { name: "From year" })).toHaveValue("1990");
    // The reloaded grid asked for exactly the filtered first page.
    const parts = ["media=movie", "genre=878", "year_from=1990", "year_to=1999", "rating=7", "page=1"];
    expect(api.callsTo("GET", "/api/v1/discover/browse").some((c) => parts.every((part) => c.path.includes(part)))).toBe(true);
  });

  test("search results keep loading past the first page", async ({ page, api }) => {
    await page.goto("/discover?q=star");
    await api.quiet();
    await expect(page.getByRole("heading", { name: /Results for “star”/ })).toBeVisible();
    await page.getByRole("button", { name: /View details for/ }).last().scrollIntoViewIfNeeded();
    await expect(page.getByRole("button", { name: "View details for Page 2 title 1", exact: true })).toBeVisible();
    expect(api.calls.some((c) => c.path === "/api/v1/discover/search?q=star&page=2")).toBe(true);
  });
});
