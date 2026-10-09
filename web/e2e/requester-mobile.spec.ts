import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import { requestableCard } from "./fixtures/discover";

// The requester shell on a phone (the "phone" project: touch, no hover). Two things
// shipped broken here before and nothing caught them: the page growing wider than the
// screen, and a poster tap filing a request through an invisible hover button.

const PAGES = ["/discover", "/calendar", "/books", "/audiobooks"];
const VIEWPORTS = [
  { width: 375, height: 812 },
  { width: 320, height: 640 },
];

// widths reports whether anything pushes the document, or the scrolling <main>,
// wider than the screen. Rows meant to scroll sideways (poster rails, the nav strip)
// scroll inside their own box and don't count.
//
// The screen width is the layout viewport (documentElement.clientWidth), not
// innerWidth: a phone browser zooms out to fit an overflowing page, and innerWidth
// grows with it, so innerWidth would hide exactly the overflow this looks for.
async function widths(page: Page) {
  return page.evaluate(() => {
    const main = document.querySelector("main");
    return {
      doc: document.documentElement.scrollWidth,
      viewport: document.documentElement.clientWidth,
      main: main?.scrollWidth ?? 0,
      mainBox: main?.clientWidth ?? 0,
    };
  });
}

// invisibleTapTargets lists buttons and links a finger could hit but nobody can see:
// laid out, taking pointer events, but faded to (near) zero opacity. That is the shape
// of a hover-only control, which on a phone is a trap.
async function invisibleTapTargets(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const out: string[] = [];
    for (const el of document.querySelectorAll<HTMLElement>("button, a[href], [role=button]")) {
      const box = el.getBoundingClientRect();
      if (box.width === 0 || box.height === 0) continue;
      if (el.closest("[inert], [aria-hidden=true]") || (el as HTMLButtonElement).disabled) continue;
      const style = getComputedStyle(el);
      if (style.pointerEvents === "none" || style.visibility === "hidden") continue;
      let opacity = 1;
      for (let n: HTMLElement | null = el; n; n = n.parentElement) opacity *= Number(getComputedStyle(n).opacity);
      if (opacity < 0.05) out.push(el.getAttribute("aria-label") || el.textContent?.trim() || el.outerHTML.slice(0, 80));
    }
    return out;
  });
}

async function open(page: Page, path: string, api: { quiet: () => Promise<void> }) {
  await page.goto(path);
  await expect(page.getByRole("main")).toBeVisible();
  await api.quiet();
}

test.describe("requester", () => {
  test.use({ persona: "requester" });

  for (const vp of VIEWPORTS) {
    test.describe(`${vp.width}x${vp.height}`, () => {
      test.use({ viewport: vp });

      for (const path of PAGES) {
        test(`${path} fits the screen`, async ({ page, api }) => {
          await open(page, path, api);
          const w = await widths(page);
          expect(w.doc, `document width on ${path}`).toBeLessThanOrEqual(w.viewport);
          expect(w.main, `<main> content width on ${path}`).toBeLessThanOrEqual(w.mainBox);
        });
      }
    });
  }

  test("tapping a poster opens its details and requests nothing", async ({ page, api }) => {
    // The project emulates a phone; if this ever reports a hovering pointer the test
    // would prove nothing about touch.
    expect(await page.evaluate(() => matchMedia("(hover: hover) and (pointer: fine)").matches)).toBe(false);

    await open(page, "/discover", api);
    const poster = page.getByRole("button", { name: `View details for ${requestableCard.title}` }).first();
    await poster.scrollIntoViewIfNeeded();

    // On touch there must be no hover-revealed control: nothing tappable that is
    // invisible until a hover that never comes.
    expect(await invisibleTapTargets(page), "tappable but invisible until hover").toEqual([]);

    // Tap the bottom-left corner: where the desktop quick-request button sits, the spot a
    // stray tap used to file a request from.
    const box = (await poster.boundingBox())!;
    await poster.tap({ position: { x: 16, y: box.height - 14 } });

    const dialog = page.getByRole("dialog", { name: requestableCard.title });
    await expect(dialog).toBeVisible();
    await api.quiet();
    expect(api.callsTo("POST", "/api/v1/requests"), "a tap must never file a request").toEqual([]);
  });
});

test.describe("outside visitor", () => {
  test.use({ persona: "external" });

  test("gets the outside shell without Calendar, and it fits the screen", async ({ page, api }) => {
    await open(page, "/discover", api);
    await expect(page.getByRole("link", { name: "Discover" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Calendar" })).toHaveCount(0);
    const w = await widths(page);
    expect(w.doc).toBeLessThanOrEqual(w.viewport);
  });
});
