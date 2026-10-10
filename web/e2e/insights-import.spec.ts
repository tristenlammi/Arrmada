import { test, expect } from "./mockApi";

// PLEX-02/16: Settings → Import shows each Tautulli import as a run with its summary, a
// timed-out run offers Retry, the saved key stays masked, and "Remove this import" asks
// with the exact count before undoing exactly that run.
test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("import runs: summary, retry, masked key and undo with the count", async ({ page, api }) => {
    await page.goto("/settings/import");
    const card = page.locator("#tautulli-import");
    await expect(card.getByText("Imported 12,340 · 210 already there · 980 recorded live · 3 invalid · 0 failed")).toBeVisible();
    await expect(card.getByText("Timed out")).toBeVisible();
    await expect(card.getByRole("button", { name: "Retry" })).toHaveCount(1); // only the timed-out run
    await expect(card.getByPlaceholder("saved — leave blank to keep it")).toHaveValue("");
    await expect(card.getByLabel("Tautulli URL")).toHaveValue("http://192.168.50.247:8181");
    await expect(card.getByText(/Only import plays from before/)).toBeVisible();

    await card.getByRole("button", { name: "Remove this import" }).last().click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Remove this import's 12,340 plays?")).toBeVisible();
    await dialog.getByRole("button", { name: "Remove" }).click();
    await expect.poll(() => api.callsTo("DELETE", "/api/v1/insights/import/runs/1/rows").map((c) => c.path))
      .toEqual(["/api/v1/insights/import/runs/1/rows?expected=12340"]);
    await expect(page.getByText("Removed 12340 imported plays")).toBeVisible();
  });
});
