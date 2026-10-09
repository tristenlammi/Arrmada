import { test, expect } from "./mockApi";

// OBS-10/11/13/14, SEC-08: alerts live in Settings → Alerts. Old addresses land there,
// saved links show only a hint, events come from the catalog in groups, each card says
// how its last alert went, and managers can look but not change anything.
test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("old addresses land on Settings → Alerts, and Insights has no Notifications tab", async ({ page }) => {
    await page.goto("/notifications");
    await expect(page).toHaveURL(/\/settings\/alerts$/);
    await page.goto("/insights?tab=notifications");
    await expect(page).toHaveURL(/\/settings\/alerts$/);
    await page.goto("/insights?tab=settings");
    await expect(page.getByRole("tab", { name: "Settings" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Notifications" })).toHaveCount(0);
    await expect(page.getByRole("link", { name: /Alert settings moved/ })).toHaveAttribute("href", "/settings/alerts");
  });

  test("connections show a hint, grouped events, delivery status and the log", async ({ page, api }) => {
    await page.goto("/settings/alerts");
    const discord = page.locator("div.rounded-xl", { has: page.locator('input[value="Family Discord"]') }).last();
    await expect(discord.getByLabel("Link")).toHaveAttribute("placeholder", /discord:\/\/••••OKEN — saved, leave blank to keep/);
    await expect(discord.getByLabel("Link")).toHaveValue("");
    await expect(discord.getByText(/Last alert failed: apprise: exit status 1/)).toBeVisible();
    await expect(discord.getByText("Library", { exact: true })).toBeVisible();
    await expect(discord.getByLabel("Movie imported")).toBeChecked();
    await expect(discord.getByLabel("Episodes imported")).not.toBeChecked();
    // Groups with nothing in them yet aren't shown.
    await expect(discord.getByText("Needs you", { exact: true })).toHaveCount(0);

    await discord.getByRole("button", { name: "Recent deliveries" }).click();
    await expect(discord.getByText("📥 The Cartographer")).toBeVisible();

    await discord.getByRole("button", { name: "Test" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/notifications/1/test").length).toBe(1);
    // A saved card's Test never sends the link from the browser.
    expect(api.callsTo("POST", "/api/v1/notifications/test")).toHaveLength(0);

    await expect(page.getByText("Web Push to every device the account turned push on for.")).toBeVisible();
    await expect(page.getByText("On iPhone, push works only when Arrmada is added to the Home Screen.")).toBeVisible();
  });
});

test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("sees the connections read-only", async ({ page }) => {
    await page.goto("/settings/alerts");
    await expect(page.getByText("Only an admin can add or change alert connections.")).toBeVisible();
    await expect(page.getByText("discord://••••OKEN")).toBeVisible();
    await expect(page.getByRole("button", { name: "Save" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "+ Add connection" })).toHaveCount(0);
  });
});
