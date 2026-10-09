import { test, expect } from "./mockApi";

// MOV-08: Movies → Wanted says what Arrmada is still looking for and why, and Search all
// queues the lot through the throttled movie search queue after a confirmation.
test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("Missing and Cutoff unmet explain each film, and Search all queues them", async ({ page, api }) => {
    const errors: string[] = [];
    page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
    await page.goto("/movies");
    await page.getByRole("link", { name: "Wanted", exact: true }).click();
    await expect(page).toHaveURL(/\/movies\/wanted$/);

    const lines = page.getByTestId("movie-wanted-line");
    await expect(lines.first()).toContainText("3 empty searches in a row");
    await expect(page.getByText("Extra versions still missing (1)")).toBeVisible();

    await page.getByRole("button", { name: "Search all (2)" }).click();
    await page.getByRole("button", { name: "Queue 2 searches" }).click();
    await expect(page.getByText(/Queued 2 searches/)).toBeVisible();
    const calls = api.callsTo("POST", "/api/v1/movies/search");
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ ids: [2, 1], kind: "missing" });

    await page.getByRole("tab", { name: /Cutoff unmet/ }).click();
    await expect(lines.first()).toContainText("x264 isn't a codec this target wants");
    await expect(page.getByText("Will upgrade")).toBeVisible();
    expect(errors).toEqual([]);
  });
});
