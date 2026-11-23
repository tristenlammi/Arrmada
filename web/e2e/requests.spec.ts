import { test, expect } from "./mockApi";
import * as discover from "./fixtures/discover";

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

  // Issue #130: staff can clear many requests at once from any tab, and are told first
  // that the library is left as it is.
  test("deletes the ticked requests in one go after saying what stays", async ({ page, api }) => {
    await page.goto("/requests?tab=active");
    await page.getByLabel("Select all on this page").check();
    await page.getByRole("button", { name: "Delete 4" }).click();
    const dialog = page.getByRole("alertdialog").or(page.getByRole("dialog"));
    await expect(dialog.getByText(/nothing in the library changes/)).toBeVisible();
    await expect(dialog.getByText(/files already downloaded are kept/)).toBeVisible();
    // Stopping the search for the titles is opt-in: the box starts unticked.
    await expect(dialog.getByRole("checkbox", { name: /Also stop looking for these titles/ })).not.toBeChecked();
    await dialog.getByRole("button", { name: "Delete 4" }).click();
    await expect(page.getByText("Deleted 4 of 4")).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/requests/bulk");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ action: "delete", ids: [1, 2, 3, 4] });
  });

  // #130 follow-up: ticking "Also stop looking" sends it with the delete, and the toast says
  // how many titles were stopped and why the rest were left.
  test("a bulk delete can also stop looking for the titles", async ({ page, api }) => {
    await page.goto("/requests?tab=active");
    await page.getByLabel("Select all on this page").check();
    await page.getByRole("button", { name: "Delete 4" }).click();
    const dialog = page.getByRole("alertdialog").or(page.getByRole("dialog"));
    await dialog.getByRole("checkbox", { name: /Also stop looking for these titles/ }).check();
    await dialog.getByRole("button", { name: "Delete 4" }).click();
    await expect(page.getByText("Deleted 4 of 4; stopped looking for 3 titles; 1 left as it was (1 still requested)")).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/requests/bulk");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ action: "delete", ids: [1, 2, 3, 4], stop_looking: true });
  });

  test("Select all takes every matching request, not only the loaded page, and sends them in batches", async ({ page, api }) => {
    // 150 approved requests in progress: the page loads 50, Select all fetches the rest.
    const row = (id: number) => ({ ...discover.requests.requests[1], id, tmdb_id: 5000 + id, title: `Film ${id}` });
    const all = Array.from({ length: 150 }, (_, i) => row(i + 1));
    await page.route((u) => u.pathname === "/api/v1/requests", (route) => {
      const url = new URL(route.request().url());
      const limit = Number(url.searchParams.get("limit") ?? 50);
      const offset = Number(url.searchParams.get("offset") ?? 0);
      return route.fulfill({ contentType: "application/json", body: JSON.stringify({
        ...discover.requests, counts: { needs_approval: 0, in_progress: 150, ready: 0, declined: 0 }, total: 150,
        requests: all.slice(offset, offset + limit),
      }) });
    });
    await page.goto("/requests?tab=active");
    await page.getByRole("button", { name: "Select all 150" }).click();
    await expect(page.getByText("150 selected")).toBeVisible();
    await page.getByRole("button", { name: "Delete 150" }).click();
    await page.getByRole("alertdialog").or(page.getByRole("dialog")).getByRole("button", { name: "Delete 150" }).click();
    await expect(page.getByText("Deleted 150 of 150")).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/requests/bulk");
    expect(calls.map((c) => (c.body as { ids: number[] }).ids.length)).toEqual([100, 50]);
    expect(calls.every((c) => (c.body as { action: string }).action === "delete")).toBe(true);
  });

  // REQ-08: a row's Decline opens the request on its reason box; the reason goes with it.
  test("declining from a row asks for a reason and sends it", async ({ page, api }) => {
    await page.goto("/requests?tab=needs");
    await page.getByRole("button", { name: "Decline", exact: true }).click();
    const sheet = page.getByRole("dialog");
    await expect(sheet.getByText(/They’ll be told\./)).toBeVisible();
    await sheet.getByRole("button", { name: "Already available elsewhere" }).click();
    await sheet.getByRole("button", { name: "Decline and tell them" }).click();
    await expect(page.getByText(/^Declined “/)).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/requests/1/decline");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ reason: "Already available elsewhere" });
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

  // REQ-06: the strip puts what's waiting first, says how many, and links to them.
  test("the Discover strip leads with what's waiting and links to it", async ({ page, api }) => {
    await page.goto("/discover");
    await expect(page.getByText("1 waiting")).toBeVisible();
    await expect(page.getByRole("button", { name: /^Open the request for / }).first()).toHaveAccessibleName("Open the request for Saltwind");
    expect(api.callsTo("GET", "/api/v1/requests?section=strip").length).toBeGreaterThan(0);
    await page.getByRole("link", { name: "See all →" }).click();
    await expect(page).toHaveURL(/\/requests\?tab=needs$/);
  });

  test("the Needs-you deep link opens what's waiting", async ({ page }) => {
    await page.goto("/requests?section=pending");
    await expect(page.getByRole("tab", { name: /Needs approval/ })).toHaveAttribute("aria-selected", "true");
  });

  // What approving would grab, looked up in advance: on the row, and in the sheet with
  // the reason it ranks first, and Approve & grab takes exactly that release.
  test("shows the previewed release and approves with it", async ({ page, api }) => {
    await page.goto("/requests?tab=needs");
    await expect(page.getByTestId("preview-line")).toHaveText("Would grab 1080p · BluRay · x264 · 11.8 GB");
    await page.getByRole("button", { name: "Open the request for Saltwind" }).click();
    const sheet = page.getByRole("dialog", { name: "Saltwind" });
    const preview = sheet.getByTestId("release-preview");
    await expect(preview.getByText("Would grab")).toBeVisible();
    await expect(preview.getByText("Saltwind.2023.1080p.BluRay.x264-FIXTURE")).toBeVisible();
    await expect(preview.getByText("54 seeders")).toBeVisible();
    await expect(preview.getByText("TorrentLeech")).toBeVisible();
    await expect(preview.getByText(/Higher resolution than the others on offer/)).toBeVisible();
    // Fresh: opening it didn't search again.
    expect(api.callsTo("POST", "/api/v1/requests/1/preview")).toHaveLength(0);
    await sheet.getByRole("button", { name: "Approve & grab this" }).click();
    await expect(page.getByText(/Approved “Saltwind” — grabbing Saltwind\.2023/)).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/requests/1/approve");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toMatchObject({ grab_preview: true, preview_checked_at: Math.floor(Date.parse("2026-10-01T09:00:00Z") / 1000) });
  });

  test("Check again re-runs the preview as a job", async ({ page, api }) => {
    await page.goto("/requests?tab=needs&id=1");
    const preview = page.getByRole("dialog", { name: "Saltwind" }).getByTestId("release-preview");
    await preview.getByRole("button", { name: "Check again" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/requests/1/preview").length).toBe(1);
    await expect.poll(() => api.callsTo("GET", "/api/v1/jobs/7701").length).toBeGreaterThan(0);
    await expect(preview.getByText("Would grab")).toBeVisible();
  });
});

// A requester's own waiting request never says what Arrmada would grab for it.
test.describe("requester", () => {
  test.use({ persona: "requester" });

  test("sees no release preview on their pending request", async ({ page, api }) => {
    await page.goto("/requests?id=1");
    const sheet = page.getByRole("dialog", { name: "Saltwind" });
    await expect(sheet.getByRole("button", { name: "Withdraw" })).toBeVisible();
    await expect(sheet.getByTestId("release-preview")).toHaveCount(0);
    await expect(page.getByText("Would grab")).toHaveCount(0);
    await expect(page.getByTestId("preview-line")).toHaveCount(0);
    expect(api.callsTo("POST", "/api/v1/requests/1/preview")).toHaveLength(0);
  });
});
