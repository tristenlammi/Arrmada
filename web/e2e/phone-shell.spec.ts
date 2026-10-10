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

// APP-06: Back (Android's button, the iOS swipe) closes an open sheet instead of leaving
// the page, and an open sheet covers the tab bar.
test.describe("sheets on a phone", () => {
  test.use({ persona: "requester" });

  test("Back closes the request sheet and stays on Discover", async ({ page, api }) => {
    await open(page, "/discover", api);
    const poster = page.getByRole("button", { name: "Open the request for Saltwind" });
    await poster.tap();
    const sheet = page.getByRole("dialog", { name: "Saltwind" });
    await expect(sheet).toBeVisible();

    // The sheet covers the tab bar: a tap where the bar is lands on the sheet's overlay.
    const onBar = await page.evaluate(() => {
      const bar = document.querySelector('nav[aria-label="Primary"]')!.getBoundingClientRect();
      const hit = document.elementFromPoint(bar.left + bar.width / 2, bar.top + bar.height / 2);
      return !!hit?.closest('nav[aria-label="Primary"]');
    });
    expect(onBar).toBe(false);

    await page.goBack();
    await expect(sheet).toHaveCount(0);
    await expect(page).toHaveURL(/\/discover$/);
    await expect(tabBar(page)).toBeVisible();

    // Closed by its own button it leaves no entry behind, so the next Back really
    // leaves Discover.
    await poster.tap();
    await expect(sheet).toBeVisible();
    await sheet.getByRole("button", { name: "Close", exact: true }).tap();
    await expect(sheet).toHaveCount(0);
    await expect(page).toHaveURL(/\/discover$/);
    await page.goBack();
    await expect(page).not.toHaveURL(/\/discover/);
  });
});

// APP-07: a title's own address. Tapping a poster goes there, Back closes it where the
// list was, and the address opens cold (a reload, a shared link, a notification).
test.describe("title addresses on a phone", () => {
  test.use({ persona: "requester" });

  test("a poster opens its address and Back returns to the same spot", async ({ page, api }) => {
    await open(page, "/discover", api);
    const poster = page.getByRole("button", { name: "View details for Iron Tide" }).first();
    await poster.scrollIntoViewIfNeeded();
    const before = await page.evaluate(() => document.querySelector("main")!.scrollTop);
    expect(before).toBeGreaterThan(0);
    await poster.tap();
    await expect(page).toHaveURL(/\/discover\/movie\/1004$/);
    await expect(page.getByRole("dialog", { name: "Iron Tide" })).toBeVisible();
    await expect(page).toHaveTitle("Iron Tide · Arrmada");

    await page.goBack();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page).toHaveURL(/\/discover$/);
    expect(await page.evaluate(() => document.querySelector("main")!.scrollTop)).toBe(before);
  });

  test("opens cold with the right badge, and the old tv address redirects", async ({ page, api }) => {
    await open(page, "/discover/tv/1003", api);
    await expect(page).toHaveURL(/\/discover\/series\/1003$/);
    const sheet = page.getByRole("dialog", { name: "Saltwind" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByText("Requested — waiting for approval")).toBeVisible();

    // Nothing underneath to step back to: Close lands on Discover itself.
    await sheet.getByRole("button", { name: "Close", exact: true }).tap();
    await expect(page).toHaveURL(/\/discover$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("a hidden title says it isn't available", async ({ page, api }) => {
    await open(page, "/discover/movie/9999", api);
    const sheet = page.getByRole("dialog", { name: "Not available" });
    await expect(sheet).toBeVisible();
    await sheet.getByRole("button", { name: "Back to Discover" }).tap();
    await expect(page).toHaveURL(/\/discover$/);
  });
});

// APP-08: a notification opens exactly what it's about, by its reference, from any page.
test.describe("notifications on a phone", () => {
  test.use({ persona: "requester" });

  test("a 'ready' notice opens that exact title", async ({ page, api }) => {
    await open(page, "/shelf", api);
    await page.getByRole("button", { name: "Notifications" }).tap();
    await page.getByRole("button", { name: /“The Cartographer” is ready to watch/ }).tap();
    await expect(page).toHaveURL(/\/discover\/movie\/1007$/);
    await expect(page.getByRole("dialog", { name: "The Cartographer" })).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/me/notifications/1/read")).toHaveLength(1);
  });

  test("a book notice opens the book on the Books tab, Hardcover key and all", async ({ page, api }) => {
    await open(page, "/discover", api);
    await page.getByRole("button", { name: "Notifications" }).tap();
    await page.getByRole("button", { name: /“Moby-Dick” was approved/ }).tap();
    await expect(page).toHaveURL(/\/discover\?tab=books&work=hc%3A4242$/);
    // The Books hero can feature the same fixture book behind the sheet, depending on which
    // loads first; the sheet's heading is the later one.
    await expect(page.getByRole("heading", { level: 2, name: "The Salt Road" }).last()).toBeVisible();
    await api.quiet();
    expect(api.calls.some((c) => c.path === "/api/v1/books/discover/detail?key=hc%3A4242")).toBe(true);
  });

  test("an old notice without a reference still lands on a search", async ({ page, api }) => {
    await open(page, "/me", api);
    await page.getByRole("button", { name: "Notifications" }).tap();
    await page.getByRole("button", { name: /“Saltwind” is ready to watch/ }).tap();
    await expect(page).toHaveURL(/\/discover\?q=Saltwind$/);
  });
});

test.describe("title addresses from outside", () => {
  test.use({ persona: "external" });

  test("an outside session opens a title cold", async ({ page, api }) => {
    await open(page, "/discover/movie/1007", api);
    const sheet = page.getByRole("dialog", { name: "The Cartographer" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByText("✓ In your library")).toBeVisible();
  });
});
