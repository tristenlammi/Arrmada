import { test, expect } from "./mockApi";

// REQ-09: auto-approve is per media type. The users list flags a Plex sign-in that still
// approves whole shows; the editor and the Plex sign-in default save per-type choices.
test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("tightens a Plex user to movies only and sets the sign-in default", async ({ page, api }) => {
    await page.goto("/settings/users");
    await expect(page.getByText("Approves whole shows")).toBeVisible();

    await page.getByRole("button", { name: "Edit user" }).nth(1).click();
    const editor = page.getByRole("group", { name: "Auto-approve requests for" });
    await expect(editor.getByRole("checkbox", { name: "Series" })).toBeChecked();
    await editor.getByRole("checkbox", { name: "Series" }).uncheck();
    await editor.getByRole("checkbox", { name: "Books" }).uncheck();
    await page.getByRole("button", { name: "Save changes" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/users/5").length).toBe(1);
    expect(api.callsTo("PUT", "/api/v1/users/5")[0].body).toMatchObject({
      auto_approve_movie: true, auto_approve_series: false, auto_approve_book: false,
    });

    // The Plex sign-in default (in Settings → Plex now) starts at movies only.
    await page.getByRole("link", { name: "Settings → Plex" }).click();
    await expect(page).toHaveURL(/\/settings\/plex#plex-sign-in$/);
    const plex = page.getByRole("group", { name: "New Plex sign-ins auto-approve" });
    await expect(plex.getByRole("checkbox", { name: "Movies" })).toBeChecked();
    await expect(plex.getByRole("checkbox", { name: "Series" })).not.toBeChecked();
  });
});
