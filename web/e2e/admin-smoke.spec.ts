import { test, expect } from "./mockApi";

// The staff console at desktop width: every page in the sidebar opens, renders its
// heading and logs no console errors. A page that throws (and lands on the error
// boundary), logs a React error, or calls an endpoint with no fixture fails here.
for (const persona of ["admin", "manager"] as const) {
  test.describe(persona, () => {
    test.use({ persona });

    test("every sidebar page loads with a heading and no console errors", async ({ page, api }) => {
      const errors: string[] = [];
      page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
      page.on("pageerror", (e) => errors.push(`uncaught: ${e.message}`));

      await page.goto("/");
      await expect(page.getByRole("heading", { level: 1 }).first()).toBeVisible();

      // Read the page list off the sidebar itself, so a page added to (or moved in) the
      // nav is covered without editing this spec.
      const sidebar = page.getByRole("complementary");
      const hrefs = await sidebar.getByRole("link").evaluateAll((links) =>
        links.map((a) => a.getAttribute("href") ?? "").filter((h) => h.startsWith("/")),
      );
      const pages = [...new Set(hrefs)];
      // The console has well over a dozen pages; a near-empty list means the selector
      // broke, not that the app shrank.
      expect(pages.length, `sidebar links: ${pages.join(", ")}`).toBeGreaterThan(10);
      // Logs is admin-only; a manager's sidebar must not offer it.
      expect(pages.includes("/logs")).toBe(persona === "admin");

      for (const href of pages) {
        await test.step(href, async () => {
          await sidebar.locator(`a[href="${href}"]`).first().click();
          await expect(page).toHaveURL(new RegExp(`${href === "/" ? "/" : href}$`));
          await expect.soft(page.getByRole("heading", { level: 1 }).first(), `${href} renders an h1`).toBeVisible();
          // Let the page's first round of requests answer and render before judging it.
          await api.quiet();
          // Errors since the last check, so the first step also covers the initial load.
          expect.soft(errors, `console errors on ${href}`).toEqual([]);
          errors.length = 0;
        });
      }
    });
  });
}
