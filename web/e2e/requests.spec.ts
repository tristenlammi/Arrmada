import { test, expect } from "./mockApi";

// REQ-05: the routed Requests page. Staff decide pending requests in bulk from plain
// buttons; the tab, filters and an open request live in the address.
test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("selects the waiting requests and approves them in one go", async ({ page, api }) => {
    const errors: string[] = [];
    page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
    await page.goto("/requests?tab=needs");
    await expect(page.getByRole("heading", { level: 1, name: "Requests" })).toBeVisible();
    await expect(page.getByRole("tab", { name: /Needs approval/ })).toHaveAttribute("aria-selected", "true");
    // The row shows who asked and their note, with visible Approve and Decline.
    await expect(page.getByText("“The extended cut, if there is one”")).toBeVisible();
    await expect(page.getByRole("button", { name: "Approve", exact: true })).toBeVisible();

    await page.getByLabel("Select all on this page").check();
    await page.getByRole("button", { name: "Approve 1" }).click();
    await expect(page.getByText("Approved 1 of 1")).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/requests/bulk");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ action: "approve", ids: [1] });
    expect(errors).toEqual([]);
  });

  test("a type filter and an open request survive a reload", async ({ page, api }) => {
    await page.goto("/requests?tab=active&type=movie&id=2");
    await expect(page.getByRole("dialog", { name: "Driftwood" })).toBeVisible();
    await page.reload();
    await expect(page.getByRole("dialog", { name: "Driftwood" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Movies", pressed: true })).toBeVisible();
    await api.quiet();
    expect(api.callsTo("GET", "/api/v1/requests?").some((c) => c.path.includes("media_type=movie") && c.path.includes("section=in_progress"))).toBe(true);
    // Closing the sheet drops ?id but keeps the rest.
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/requests\?tab=active&type=movie$/);
  });

  test("the Needs-you deep link opens what's waiting", async ({ page }) => {
    await page.goto("/requests?section=pending");
    await expect(page.getByRole("tab", { name: /Needs approval/ })).toHaveAttribute("aria-selected", "true");
  });
});
