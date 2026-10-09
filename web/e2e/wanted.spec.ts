import { test, expect } from "./mockApi";

// ACQ-18: Downloads → Searching says why each wanted title isn't downloading — the last
// search, the run of empty ones and the next automatic try — and Search now reports what
// it found.
test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("Searching explains each wanted title, and Search now reports back", async ({ page, api }) => {
    await page.goto("/downloads?tab=searching");
    const lines = page.getByTestId("wanted-line");
    await expect(lines.first()).toHaveText(
      "Last search 60 min ago — Found 12 releases — 9 for other titles, 3 over your bitrate ceiling · 3 empty searches in a row, mostly for other titles · next automatic search in 60 min",
    );
    await expect(lines.nth(1)).toHaveText("Waiting for the first search");
    await expect(page.getByText("Ada Fenwick")).toBeVisible();

    await page.getByRole("button", { name: "Search now" }).first().click();
    await expect(lines.first()).toHaveText("Found 12 releases — 12 for other titles");
    expect(api.callsTo("POST", "/api/v1/wanted/movie/2/search")).toHaveLength(1);
  });
});
