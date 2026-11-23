import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import { reviewImportJob, reviews } from "./fixtures/media";
import { unmanagedImportJob } from "./fixtures/system";
import type { Job } from "../src/lib/api";

// #15: Import anyway (and Choose different…, Map files…) runs as a job. The card says it's
// importing, with the copy's progress, until the job ends: then it leaves with "Imported
// into …", or stays with the reason it failed. A page opened mid-import finds the job.

const movieCard = (page: Page) => page.getByTestId("review-card").filter({ hasText: "Other.Movie.2019" });

// answerJob makes GET /jobs/912 answer whatever `now()` says, so a spec can move the job on.
async function answerJob(page: Page, now: () => Job) {
  await page.route(`**/api/v1/jobs/${reviewImportJob.id}`, (route) => route.fulfill({ json: now() }));
}

const running = (progress: number, message: string): Job => ({ ...reviewImportJob, status: "running", progress, message, finished_at: null });

test.describe("manager", () => {
  test.use({ persona: "manager" });

  test("Import anyway shows the import's progress, then removes the card", async ({ page, api }) => {
    let job = running(0.4, "Copying… 1.5 of 3.7 GB");
    await answerJob(page, () => job);
    await page.goto("/activity/needs-you");
    const card = movieCard(page);
    await card.getByRole("button", { name: "Import anyway" }).click();

    const importing = card.getByTestId("review-importing");
    await expect(importing).toContainText("Copying… 1.5 of 3.7 GB");
    await expect(importing.getByRole("progressbar", { name: "Import progress" })).toHaveAttribute("aria-valuenow", "40");
    // Every action waits for the import.
    for (const name of ["Import anyway", /^Choose different/, "Reject & find another", "Dismiss"]) {
      await expect(card.getByRole("button", { name })).toBeDisabled();
    }
    expect(api.callsTo("POST", "/api/v1/reviews/1/import")).toHaveLength(1);

    job = reviewImportJob; // it lands
    await expect(page.getByText("Imported into Fixture Movie.")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("Other.Movie.2019.1080p.BluRay-GRP")).toHaveCount(0);
  });

  test("a failed import stays on its card with the reason, and the buttons come back", async ({ page }) => {
    await answerJob(page, () => ({ ...reviewImportJob, status: "failed", message: "Importing…", error: "write /media/movies/Fixture Movie (2020).mkv: no space left on device" }));
    await page.goto("/activity/needs-you");
    const card = movieCard(page);
    await card.getByRole("button", { name: "Import anyway" }).click();

    await expect(card.getByRole("alert")).toContainText("no space left on device", { timeout: 15_000 });
    await expect(card.getByRole("button", { name: "Import anyway" })).toBeEnabled();
    await expect(card.getByRole("button", { name: "Dismiss" })).toBeEnabled();
  });

  test("a refusal the server answers at once shows on the card", async ({ page }) => {
    await page.route("**/api/v1/reviews/1/import", (route) => route.fulfill({
      status: 410, json: { status: "error", message: "the download is no longer on disk — it was removed or cleaned up since this was held for review" },
    }));
    await page.goto("/activity/needs-you");
    const card = movieCard(page);
    await card.getByRole("button", { name: "Import anyway" }).click();
    await expect(card.getByRole("alert")).toContainText("no longer on disk");
  });

  test("Map files… closes once the mapping is accepted and the card follows the import", async ({ page, api }) => {
    await page.route("**/api/v1/reviews/2/files", (route) => route.fulfill({
      json: { files: [{ rel_path: "Fixture.Show.E01.mkv", size: 1e9, video: true, guess: { season: 1, episodes: [1] } }], truncated: false },
    }));
    await answerJob(page, () => ({ ...running(0, "Importing…"), target: "review:2" }));
    await page.goto("/activity/needs-you");
    const card = page.getByTestId("review-card").filter({ hasText: "Fixture.Show.S01" });
    await card.getByRole("button", { name: "Map files…" }).click();
    const dialog = page.getByRole("dialog", { name: "Map files to episodes" });
    await dialog.getByRole("button", { name: "Import 1 file" }).click();
    await expect(dialog).toHaveCount(0);
    await expect(card.getByTestId("review-importing")).toContainText("Importing…");
    await expect(card.getByRole("button", { name: "Map files…" })).toBeDisabled();
    expect(api.callsTo("POST", "/api/v1/reviews/2/map")[0].body).toEqual({ series_id: 1, files: [{ rel_path: "Fixture.Show.E01.mkv", season: 1, episodes: [1] }] });
  });

  test("Import into… for a download Arrmada didn't grab follows its job, and a failure stays on the card", async ({ page, api }) => {
    let job: Job = { ...unmanagedImportJob, status: "running", progress: 0.25, message: "Copying… 2.1 of 8.4 GB", finished_at: null };
    await page.route(`**/api/v1/jobs/${unmanagedImportJob.id}`, (route) => route.fulfill({ json: job }));
    await page.goto("/activity/needs-you");
    await page.getByRole("button", { name: /Not managed by Arrmada/ }).click();
    const card = page.getByTestId("unmanaged-item").filter({ hasText: "Night.Train.1999" });
    await card.getByRole("button", { name: "Import into…" }).click();
    await page.getByRole("dialog").getByRole("button", { name: /^Night Train \(1999\)/ }).click();

    const importing = card.getByTestId("review-importing");
    await expect(importing).toContainText("Copying… 2.1 of 8.4 GB");
    await expect(importing.getByRole("progressbar", { name: "Import progress" })).toHaveAttribute("aria-valuenow", "25");
    await expect(card.getByRole("button", { name: "Import into…" })).toBeDisabled();
    expect(api.callsTo("POST", "/api/v1/queue/e2e0000000000000000000000000000000000006/import")[0].body).toEqual({ kind: "movie", target_id: 7 });

    job = { ...unmanagedImportJob, status: "failed", message: "Importing…", error: "no video files found in the download" };
    await expect(card.getByRole("alert")).toContainText("no video files found", { timeout: 15_000 });
    await expect(card.getByRole("button", { name: "Import into…" })).toBeEnabled();
  });

  test("a different import while one runs is refused, and the card follows the running one", async ({ page }) => {
    await page.route("**/api/v1/reviews/1/import", (route) => route.fulfill({
      status: 409,
      json: { status: "error", message: "This download is already being imported into Fixture Movie — wait for that to finish.", existing: true, job_id: reviewImportJob.id, title: "Fixture Movie" },
    }));
    await answerJob(page, () => running(0.5, "Copying… 1.9 of 3.7 GB"));
    await page.goto("/activity/needs-you");
    const card = movieCard(page);
    await card.getByRole("button", { name: "Import anyway" }).click();
    await expect(page.getByText("already being imported into Fixture Movie")).toBeVisible();
    await expect(card.getByTestId("review-importing")).toContainText("Copying… 1.9 of 3.7 GB");
    await expect(card.getByRole("button", { name: "Import anyway" })).toBeDisabled();
  });

  test("a page opened mid-import finds the running job", async ({ page, api }) => {
    await page.route("**/api/v1/reviews", (route) => route.fulfill({
      json: { reviews: reviews.reviews.map((r) => (r.id === 1 ? { ...r, import_job: running(0.1, "Copying… 0.4 of 3.7 GB") } : r)) },
    }));
    await answerJob(page, () => running(0.6, "Copying… 2.2 of 3.7 GB"));
    await page.goto("/activity/needs-you");
    const card = movieCard(page);
    await expect(card.getByTestId("review-importing")).toContainText(/Copying… (0\.4|2\.2) of 3\.7 GB/);
    await expect(card.getByRole("button", { name: "Import anyway" })).toBeDisabled();
    expect(api.callsTo("POST", "/api/v1/reviews/1/import")).toHaveLength(0);
  });

  test("a pack whose episode titles and numbers disagree says so and offers Map files…", async ({ page }) => {
    const reason = "The episode titles in this download's filenames disagree with the show's episode list, so its files can't be placed safely: Fixture.Show.S01E04.Brians.Wedding.mkv (numbered S01E04, titled \"Brians Wedding\") would go to S01E05 by its title";
    await page.route("**/api/v1/reviews", (route) => route.fulfill({
      json: { reviews: reviews.reviews.map((r) => (r.id === 2 ? { ...r, reason_code: "title_conflict", reason } : r)) },
    }));
    await page.goto("/activity/needs-you");
    const card = page.getByTestId("review-card").filter({ hasText: "Fixture.Show.S01" });
    await expect(card.getByText("Titles disagree")).toBeVisible();
    await expect(card).toContainText("numbered S01E04, titled \"Brians Wedding\"");
    await expect(card.getByRole("button", { name: "Map files…" })).toBeEnabled();
    // Another release may number its episodes as the list does.
    await expect(card.getByRole("button", { name: /^Reject/ })).toBeEnabled();
    await expect(card.getByRole("button", { name: "Import anyway" })).toHaveCount(0);
  });
});
