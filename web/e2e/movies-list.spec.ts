import { test, expect } from "./mockApi";

// MOV-10: the library list is slim summaries; an upgrade downloading on a film that has
// its file shows a labelled indicator (and the film stays Downloaded); while downloads run
// the grid polls only the small downloads endpoint, never the whole list.
test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("the grid labels upgrade progress and polls only the downloads endpoint", async ({ page, api }) => {
    await page.goto("/movies");
    await expect(page.getByText("Upgrade", { exact: true })).toBeVisible();
    await expect(page.getByText("Downloaded", { exact: true }).first()).toBeVisible(); // The Cartographer keeps its badge
    await page.waitForRequest((r) => new URL(r.url()).pathname === "/api/v1/movies/downloads", { timeout: 8000 });
    expect(api.calls.filter((c) => c.method === "GET" && c.path === "/api/v1/movies")).toHaveLength(1);
  });
});
