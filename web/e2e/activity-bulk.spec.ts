import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import { STALLED_QUEUE_HASH } from "./fixtures/media";

// Issue #112: the Queue's select mode. Pick several downloads (or everything a filter shows)
// and remove them in one go, keeping or deleting their files — one request carrying exactly
// the picked downloads and the files choice. Runs at 1440 px and on a 375 px phone.

const DRIFTWOOD = { hash: "e2e0000000000000000000000000000000000001", name: "Driftwood.2025.2160p.WEB-DL.DV.HDR.H265-FIXTURE" };
const OLD_FILM = { hash: STALLED_QUEUE_HASH, name: "Old.Film.2019.1080p.WEB-DL-FIXTURE" };

const overflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("pick two downloads and remove them with their files", async ({ page, api }) => {
    await page.goto("/activity/queue");
    await expect(page.getByTestId("transfer-card")).toHaveCount(3);
    await page.getByRole("button", { name: "Select", exact: true }).click();
    const bar = page.getByRole("region", { name: "Selected downloads" });
    await expect(bar).toContainText("0 selected");
    await expect(bar.getByRole("button", { name: "Remove" })).toBeDisabled();

    await page.getByRole("checkbox", { name: "Select Driftwood" }).click();
    await page.getByRole("checkbox", { name: "Select Old Film" }).click();
    await expect(bar).toContainText("2 selected");
    // Select mode covers the cards' own buttons, so a tap can't pause one by mistake.
    expect(await overflow(page)).toBeLessThanOrEqual(0);

    await bar.getByRole("button", { name: "Remove" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: "Remove 2 downloads?" })).toBeVisible();
    // Keeping the files is the default; deleting says how many lose their data.
    await expect(dialog.getByRole("radio", { name: /keep the files/ })).toBeChecked();
    await expect(dialog.getByTestId("bulk-delete-warning")).toHaveCount(0);
    await dialog.getByRole("radio", { name: /delete the files of all 2/ }).check();
    await expect(dialog.getByTestId("bulk-delete-warning")).toHaveText("The data of 2 downloads will be deleted. This can't be undone.");
    expect(await overflow(page)).toBeLessThanOrEqual(0);
    await dialog.getByRole("button", { name: "Remove 2 and delete files" }).click();

    await expect(page.getByText("Removed 2 downloads — their files deleted.")).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/queue/remove");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ items: [DRIFTWOOD, OLD_FILM], mode: "delete_files" });
    // No single removes were fired alongside it.
    expect(api.callsTo("DELETE", "/api/v1/queue/")).toHaveLength(0);
    // Done: select mode ends once everything picked has gone.
    await expect(bar).toHaveCount(0);
  });

  test("All picks exactly what the filters show, and files are kept by default", async ({ page, api }) => {
    await page.goto("/activity/queue?type=movie");
    await expect(page.getByTestId("transfer-card")).toHaveCount(2);
    await page.getByRole("button", { name: "Select", exact: true }).click();
    const bar = page.getByRole("region", { name: "Selected downloads" });
    await bar.getByRole("button", { name: "All" }).click();
    await expect(bar).toContainText("2 selected");
    await expect(bar.getByRole("button", { name: "All" })).toHaveCount(0);
    expect(await overflow(page)).toBeLessThanOrEqual(0);

    await bar.getByRole("button", { name: "Remove" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Remove 2, keep files" }).click();
    await expect(page.getByText("Removed 2 downloads — the files are kept.")).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/queue/remove");
    expect(calls).toHaveLength(1);
    // The series download the Movies filter hides is not among them.
    expect(calls[0].body).toEqual({ items: [DRIFTWOOD, OLD_FILM], mode: "keep_files" });
  });

  test("Clear and Done leave nothing picked, and Cancel removes nothing", async ({ page, api }) => {
    await page.goto("/activity/queue");
    await page.getByRole("button", { name: "Select", exact: true }).click();
    const bar = page.getByRole("region", { name: "Selected downloads" });
    await page.getByRole("checkbox", { name: "Select Driftwood" }).click();
    await bar.getByRole("button", { name: "Remove" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
    await expect(bar).toContainText("1 selected");
    await bar.getByRole("button", { name: "Clear" }).click();
    await expect(bar).toContainText("0 selected");
    await page.getByRole("button", { name: "Done", exact: true }).click();
    await expect(bar).toHaveCount(0);
    await expect(page.getByRole("checkbox", { name: /^Select / })).toHaveCount(0);
    expect(api.callsTo("POST", "/api/v1/queue/remove")).toHaveLength(0);
  });
});
