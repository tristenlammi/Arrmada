import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";

// APP-05: the requester's phone shell. A bottom tab bar with only the places this session
// can reach, room left for the iPhone's status bar and home indicator, one bell on every
// page (staff too), and the shelf called "My shelf".

const tabBar = (page: Page) => page.getByRole("navigation", { name: "Primary" });

async function open(page: Page, path: string, api: { quiet: () => Promise<void> }) {
  await page.goto(path);
  await expect(page.getByRole("main")).toBeVisible();
  await api.quiet();
}

test.describe("requester at 375px", () => {
  test.use({ persona: "requester" });

  test("has a bottom tab bar, Calendar under Me, the active tab marked", async ({ page, api }) => {
    await open(page, "/discover", api);
    const bar = tabBar(page);
    await expect(bar).toBeVisible();
    await expect(bar.getByRole("link")).toHaveText(["Discover", "Requests", "Shelf", "Listen", "Me"]);
    await expect(bar.getByRole("link", { name: "Discover" })).toHaveAttribute("aria-current", "page");
    // The header names the section; the wide-screen links are hidden.
    await expect(page.getByRole("navigation", { name: "Sections" })).toBeHidden();

    await bar.getByRole("link", { name: "Shelf" }).click();
    await expect(page).toHaveURL(/\/shelf$/);
    await expect(page.getByRole("heading", { level: 1, name: "My shelf" })).toBeVisible();
    await expect(bar.getByRole("link", { name: "Shelf" })).toHaveAttribute("aria-current", "page");

    await bar.getByRole("link", { name: "Me" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "deckhand" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Calendar" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Audiobook apps & password" })).toBeVisible();
  });

  test("an old /books bookmark lands on the shelf", async ({ page, api }) => {
    await open(page, "/books", api);
    await expect(page).toHaveURL(/\/shelf$/);
    await expect(page.getByRole("heading", { level: 1, name: "My shelf" })).toBeVisible();
  });

  test("leaves room for the iPhone's status bar and home indicator", async ({ page, api }) => {
    await open(page, "/discover", api);
    const safe = await page.evaluate(() => {
      const rules: string[] = [];
      for (const sheet of Array.from(document.styleSheets)) {
        for (const r of Array.from(sheet.cssRules)) rules.push(r.cssText);
      }
      const css = rules.join("\n");
      const header = document.querySelector("header")!;
      const bar = document.querySelector<HTMLElement>('nav[aria-label="Primary"]')!;
      return {
        headerSafe: header.classList.contains("pt-safe") && header.classList.contains("px-safe"),
        ptRule: /\.pt-safe\s*\{[^}]*safe-area-inset-top/.test(css),
        pxRule: /\.px-safe\s*\{[^}]*safe-area-inset-left/.test(css),
        chromeRule: /--bottom-chrome:[^;]*safe-area-inset-bottom/.test(css),
        barPadding: bar.style.paddingBottom,
        tabbar: getComputedStyle(document.documentElement).getPropertyValue("--tabbar-h").trim(),
      };
    });
    expect(safe.headerSafe).toBe(true);
    expect(safe.ptRule).toBe(true);
    expect(safe.pxRule).toBe(true);
    expect(safe.chromeRule).toBe(true);
    expect(safe.barPadding).toContain("safe-area-inset-bottom");
    expect(safe.tabbar).toBe("56px");
  });

  test("nothing hides behind the bar: the page's end scrolls clear of it", async ({ page, api }) => {
    await open(page, "/me", api);
    const gap = await page.evaluate(() => {
      const main = document.querySelector("main")!;
      main.scrollTop = main.scrollHeight;
      const last = main.querySelector("section:last-of-type")!.getBoundingClientRect();
      const bar = document.querySelector('nav[aria-label="Primary"]')!.getBoundingClientRect();
      return bar.top - last.bottom;
    });
    expect(gap).toBeGreaterThanOrEqual(0);
  });

  test("the bar steps aside while typing in search", async ({ page, api }) => {
    await open(page, "/discover", api);
    await page.getByRole("textbox", { name: "Search movies and TV" }).focus();
    await expect(tabBar(page)).toBeHidden();
    await page.getByRole("heading", { level: 2 }).first().click();
    await expect(tabBar(page)).toBeVisible();
  });

  test("one bell on every page", async ({ page, api }) => {
    for (const path of ["/discover", "/requests", "/calendar", "/shelf", "/audiobooks", "/me"]) {
      await open(page, path, api);
      await expect(page.getByRole("button", { name: "Notifications" }), path).toHaveCount(1);
      await expect(page.getByRole("button", { name: "Notifications" }), path).toBeVisible();
    }
  });
});

test.describe("outside visitor at 375px", () => {
  test.use({ persona: "external" });

  test("gets only the tabs it can reach, and no Calendar anywhere", async ({ page, api }) => {
    await open(page, "/me", api);
    await expect(tabBar(page).getByRole("link")).toHaveText(["Discover", "Requests", "Shelf", "Listen", "Me"]);
    await expect(page.getByRole("link", { name: "Calendar" })).toHaveCount(0);
  });
});

test.describe("requester at 768px", () => {
  test.use({ persona: "requester", viewport: { width: 768, height: 1024 } });

  test("keeps the top bar links, adds the bell, and has no bottom bar", async ({ page, api }) => {
    await open(page, "/discover", api);
    await expect(tabBar(page)).toBeHidden();
    const links = page.getByRole("navigation", { name: "Sections" }).getByRole("link");
    await expect(links).toHaveText(["Discover", "Requests", "Calendar", "My shelf", "Audiobooks"]);
    await expect(page.getByRole("button", { name: "Notifications" })).toHaveCount(1);
  });
});

for (const width of [375, 1280]) {
  test.describe(`staff at ${width}px`, () => {
    test.use({ persona: "admin", viewport: { width, height: 900 } });

    test("one bell, on every page", async ({ page, api }) => {
      for (const path of ["/", "/movies", "/discover"]) {
        await open(page, path, api);
        await expect(page.getByRole("button", { name: "Notifications" }), path).toHaveCount(1);
        await expect(page.getByRole("button", { name: "Notifications" }), path).toBeVisible();
      }
      await expect(tabBar(page)).toHaveCount(0);
    });
  });
}
