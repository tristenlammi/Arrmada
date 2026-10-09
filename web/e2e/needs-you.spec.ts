import { test, expect } from "./mockApi";
import { attention } from "./fixtures/system";

// OBS-08: the staff shell answers "what needs me?" on every page — count pills in the
// sidebar, a dot on the phone's menu button, and the Dashboard's first card — from one
// shared poll of /api/v1/attention. Requesters never ask for it.
test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("the Dashboard card and the sidebar pills show what's waiting", async ({ page, api }) => {
    await page.goto("/");
    const card = page.getByRole("region", { name: /Needs you/ });
    await expect(card.getByText("2 requests are waiting for approval")).toBeVisible();
    await expect(card.getByText("1 import needs review")).toBeVisible();
    await expect(card.getByText("The last backup is 3 days old")).toBeVisible();
    await expect(card.getByRole("link", { name: "Fix →" })).toHaveAttribute("href", "/settings/system#backups");
    await expect(card.getByRole("link", { name: "Open Review →" })).toHaveAttribute("href", "/review");

    const sidebar = page.getByRole("complementary");
    await expect(sidebar.locator('a[href="/discover"]')).toContainText("2");
    await expect(sidebar.locator('a[href="/review"]')).toContainText("1");
    await expect(sidebar.locator('a[href="/downloads"]')).toContainText("1");
    await expect(sidebar.locator('a[href="/settings"]')).toContainText("1");

    // One poller for the whole shell, however many parts of the page show the answer.
    await api.quiet();
    expect(api.callsTo("GET", "/api/v1/attention")).toHaveLength(1);
  });

  test("nothing waiting reads as all clear, with no pills", async ({ page }) => {
    await page.route("**/api/v1/attention", (route) => route.fulfill({
      json: { ...attention, counts: { requests: 0, reviews: 0, downloads: 0, imports: 0, searches: 0, health: 0, health_errors: 0, total: 0 }, groups: [], items: [] },
    }));
    await page.goto("/");
    await expect(page.getByText("All clear — nothing needs you.")).toBeVisible();
    await expect(page.getByRole("complementary").locator('a[href="/discover"]')).toHaveText("Discover");
  });

  test("on a phone the menu button carries a dot", async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto("/");
    await expect(page.getByTestId("needs-you-dot")).toBeVisible();
    await expect(page.getByRole("button", { name: "Open menu — something needs you" })).toBeVisible();
  });

  test("Downloads narrows to problems from the card's link", async ({ page }) => {
    await page.goto("/downloads?show=problems");
    await expect(page.getByRole("button", { name: "Problems only ✕" })).toBeVisible();
    await page.getByRole("button", { name: "Problems only ✕" }).click();
    await expect(page).toHaveURL(/\/downloads$/);
  });
});

test.describe("requester", () => {
  test.use({ persona: "requester" });

  test("a requester's shell never asks for the staff counts", async ({ page, api }) => {
    await page.goto("/discover");
    await expect(page.getByRole("tablist", { name: "Discover sections" })).toBeVisible();
    await api.quiet();
    expect(api.callsTo("GET", "/api/v1/attention")).toHaveLength(0);
  });
});
