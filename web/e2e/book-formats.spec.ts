import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import { ebookOnlyBook, requestableBook } from "./fixtures/books";

// Book requests on a phone: Read / Listen / Both on the request sheet (remembered for
// next time), and "Request audiobook" on a book the library holds only as an ebook.

async function openBooks(page: Page, api: { quiet: () => Promise<void> }) {
  await page.goto("/discover?tab=books");
  await expect(page.getByRole("main")).toBeVisible();
  await api.quiet();
}

async function openSheet(page: Page, title: string) {
  const poster = page.getByRole("button", { name: `View details for ${title}` }).first();
  await poster.scrollIntoViewIfNeeded();
  await poster.tap();
}

async function fitsScreen(page: Page) {
  const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
  expect(w.doc, "document width with the sheet open").toBeLessThanOrEqual(w.viewport);
}

test.describe("book formats", () => {
  test.use({ persona: "requester" });

  test("the sheet asks Read / Listen / Both and remembers the choice", async ({ page, api }) => {
    await openBooks(page, api);
    await openSheet(page, requestableBook.title);

    const group = page.getByRole("radiogroup", { name: "Format" });
    await expect(group).toBeVisible();
    // Nothing picked before: it starts on the owner's default (the ebook).
    await expect(group.getByRole("radio", { name: "Read (ebook)" })).toHaveAttribute("aria-checked", "true");
    await fitsScreen(page);

    await group.getByRole("radio", { name: "Listen (audiobook)" }).tap();
    // The sheet's own button (the hero behind it has one too).
    await group.locator("..").getByRole("button", { name: "＋ Request" }).tap();
    await api.quiet();
    const posted = api.callsTo("POST", "/api/v1/requests");
    expect(posted).toHaveLength(1);
    expect((posted[0].body as { formats?: string }).formats).toBe("audiobook");

    // A fresh visit starts on the last choice.
    await openBooks(page, api);
    await openSheet(page, requestableBook.title);
    await expect(page.getByRole("radiogroup", { name: "Format" }).getByRole("radio", { name: "Listen (audiobook)" })).toHaveAttribute("aria-checked", "true");
  });

  test("a book held as an ebook offers the audiobook", async ({ page, api }) => {
    await openBooks(page, api);
    await openSheet(page, ebookOnlyBook.title);

    await expect(page.getByText("Ebook ✓").first()).toBeVisible();
    const ask = page.getByRole("button", { name: "＋ Request audiobook" });
    await expect(ask).toBeVisible();
    await fitsScreen(page);
    await ask.tap();
    await api.quiet();
    const posted = api.callsTo("POST", "/api/v1/requests");
    expect(posted).toHaveLength(1);
    expect(posted[0].body).toMatchObject({ media_type: "book", ol_key: ebookOnlyBook.key, formats: "audiobook" });
  });
});
