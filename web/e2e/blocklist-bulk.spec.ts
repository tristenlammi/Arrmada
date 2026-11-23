import type { Route } from "@playwright/test";
import { test, expect } from "./mockApi";
import type { BlocklistRow } from "../src/lib/api";

// #133: the Blocklist unblocks many entries at once — ticked rows, the whole page, or
// everything the current filter matches across every page — after one confirm.
test.describe("Blocklist bulk unblock", () => {
  test.use({ persona: "manager" });

  test("ticks rows and unblocks just those, after a confirm", async ({ page, api }) => {
    await page.goto("/activity/blocklist");
    await api.quiet();
    await expect(page.getByTestId("blocklist-row")).toHaveCount(2);

    await page.getByRole("checkbox", { name: "Select Totally.Legit.2024.1080p.exe" }).check();
    await expect(page.getByText("1 selected")).toBeVisible();

    // Cancelling the confirm sends nothing.
    await page.getByRole("button", { name: "Unblock selected (1)" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText(/blocked for every title/)).toBeVisible();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    expect(api.callsTo("POST", "/api/v1/blocklist/bulk-unblock")).toHaveLength(0);

    const reads = api.callsTo("GET", "/api/v1/blocklist").length;
    await page.getByRole("button", { name: "Unblock selected (1)" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Unblock 1" }).click();
    await expect(page.getByText("Unblocked 1 release.")).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/blocklist/bulk-unblock").map((c) => c.body)).toEqual([{ ids: [2] }]);
    // The list is read again, and nothing stays ticked.
    await expect.poll(() => api.callsTo("GET", "/api/v1/blocklist").length).toBeGreaterThan(reads);
    await expect(page.getByText("Select all on this page")).toBeVisible();
  });

  test("selects everything matching the filter across pages and unblocks it by filter", async ({ page, api }) => {
    // 250 stalled global entries: three pages of 100.
    const TOTAL = 250, NEWEST = 900;
    await page.route((u) => u.pathname === "/api/v1/blocklist", (route: Route) => {
      const u = new URL(route.request().url());
      api.calls.push({ method: "GET", path: u.pathname + u.search, body: undefined });
      const offset = Number(u.searchParams.get("offset") ?? 0);
      const items: BlocklistRow[] = Array.from({ length: Math.min(100, TOTAL - offset) }, (_, i) => ({
        id: NEWEST - offset - i, type: "global", item_id: 0, item_title: "", title: `Junk.${offset + i}.2024.1080p`, reason: "stalled", created_at: "2026-10-01 12:00:00",
      }));
      return route.fulfill({ json: { items, total: TOTAL, newest_id: NEWEST } });
    });

    await page.goto("/activity/blocklist");
    await page.getByRole("button", { name: "Global" }).click();
    await page.getByRole("textbox", { name: "Search the blocklist" }).fill("junk");
    await expect.poll(() => api.calls.some((c) => c.path.includes("q=junk"))).toBe(true);
    await expect(page.getByTestId("blocklist-row")).toHaveCount(100);

    await page.getByRole("checkbox", { name: "Select every entry on this page" }).check();
    await expect(page.getByText("100 selected")).toBeVisible();
    await page.getByRole("button", { name: "Select all 250 matching this filter" }).click();
    await expect(page.getByText("250 selected")).toBeVisible();
    await expect(page.getByText("Every entry matching this filter is selected.")).toBeVisible();

    // Still everything on the next page.
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("checkbox", { name: "Select Junk.100.2024.1080p" })).toBeChecked();

    await page.getByRole("button", { name: "Unblock all 250" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText(/on every page/)).toBeVisible();
    await dialog.getByRole("button", { name: "Unblock all 250" }).click();
    await expect(page.getByText("Unblocked 2 releases.")).toBeVisible();
    // One request by filter, bounded by the newest entry the list showed — not 250 ids.
    expect(api.callsTo("POST", "/api/v1/blocklist/bulk-unblock").map((c) => c.body)).toEqual([
      { all: true, type: "global", q: "junk", through_id: NEWEST },
    ]);
  });

  test("unticking one row drops back to this page's rows", async ({ page, api }) => {
    await page.route((u) => u.pathname === "/api/v1/blocklist", (route: Route) => {
      const items: BlocklistRow[] = Array.from({ length: 100 }, (_, i) => ({
        id: 500 - i, type: "movie", item_id: 1, item_title: "Fixture Movie", title: `Fixture.${i}.1080p`, reason: "stalled", created_at: "2026-10-01 12:00:00",
      }));
      return route.fulfill({ json: { items, total: 300, newest_id: 500 } });
    });
    await page.goto("/activity/blocklist");
    await api.quiet();
    await page.getByRole("checkbox", { name: "Select every entry on this page" }).check();
    await page.getByRole("button", { name: "Select all 300 entries" }).click();
    await expect(page.getByText("300 selected")).toBeVisible();
    await page.getByRole("checkbox", { name: "Select Fixture.0.1080p" }).uncheck();
    await expect(page.getByText("99 selected")).toBeVisible();
    await page.getByRole("button", { name: "Clear" }).click();
    await expect(page.getByText("Select all on this page")).toBeVisible();
  });
});
