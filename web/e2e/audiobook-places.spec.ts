import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import type { MockRoute } from "./fixtures/routes";
import { myAudio } from "./fixtures/system";
import { personas } from "./fixtures/users";
import { NOW } from "./fixtures/clock";
import { mockAudio } from "./fixtures/audio";
import type { AudioHistoryEntry, MyAudio } from "../src/lib/api";

// The place timeline at phone width (AUD-05, on the Listen tab since APP-10): a book whose
// place needs a look is listed, its sheet offers back a later spot an app sent that
// wasn't used, every timeline row says why in words and can be gone back to, Back closes
// the sheet, and a place an app removed shows under Recently removed with Restore.

const t = (min: number) => NOW - min * 60_000;

function withPlaces(): MyAudio {
  return {
    ...myAudio(personas.requester),
    has_password: true,
    places: [{
      item_key: "b12", book_id: 12, title: "Dungeon Crawler Carl", author: "Matt Dinniman",
      position: 3610, duration: 36000, finished: false, updated_at: t(30), device: "iPad",
      offer: { history_id: 77, position: 14531, at: t(60), device: "Pixel 8", reason: "older" },
    }],
    removed: [{
      item_key: "b13", book_id: 13, title: "Carl's Doomsday Scenario", author: "Matt Dinniman",
      position: 5400, duration: 40000, finished: false, discarded_at: t(60 * 26), device: "Lissen",
    }],
  };
}

const history: AudioHistoryEntry[] = [
  { id: 80, position: 3610, at: t(30), device: "iPad", reason: "forward", kind: "applied" },
  { id: 77, position: 14531, at: t(60), device: "Pixel 8", reason: "older", kind: "rejected" },
  { id: 76, position: 9000, at: t(70), device: "app", reason: "unproven", kind: "rejected", dismissed: true },
  { id: 75, position: 3600, at: t(240), device: "Pixel 8", reason: "before", kind: "before" },
  { id: 74, position: 200, at: t(300), device: "app", reason: "held", kind: "held" },
  { id: 73, position: 2000, at: t(400), device: "Lissen", reason: "discard", kind: "discarded" },
];

async function override(page: Page, api: MockedApi, extra: MockRoute[]) {
  await page.route((u) => u.pathname.startsWith("/api/"), async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    for (const r of extra) {
      if (r.method !== req.method() || url.pathname !== r.path) continue;
      let body: unknown;
      try { body = req.postDataJSON(); } catch { body = req.postData(); }
      api.calls.push({ method: req.method(), path: url.pathname + url.search, body });
      if (r.status === 204) return route.fulfill({ status: 204 });
      return route.fulfill({ status: r.status ?? 200, contentType: "application/json", body: JSON.stringify(r.body ?? {}) });
    }
    return route.fallback();
  });
}

const routes: MockRoute[] = [
  { method: "GET", path: "/api/v1/me/audio", body: withPlaces() },
  { method: "GET", path: "/api/v1/me/audio/history", body: { history } },
  { method: "POST", path: "/api/v1/me/audio/restore", body: { position: 14531 } },
  { method: "POST", path: "/api/v1/me/audio/dismiss", status: 204 },
  { method: "POST", path: "/api/v1/me/audio/undiscard", body: { position: 5400 } },
];

test.describe("audiobook places at 375px", () => {
  test.use({ persona: "requester" });

  test("offer, timeline reasons and Recently removed", async ({ page, api }) => {
    await override(page, api, routes);
    await mockAudio(page, api);
    await page.goto("/audiobooks");
    const check = page.locator("section", { has: page.getByRole("heading", { name: "Check your place" }) });
    await expect(check.getByText("Dungeon Crawler Carl")).toBeVisible();
    await expect(check.getByText("4:02:11")).toBeVisible();

    // Nothing pushes the page wider than the phone.
    const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, vp: document.documentElement.clientWidth }));
    expect(w.doc).toBeLessThanOrEqual(w.vp);

    // The book's sheet has the offer and the place's history.
    await check.getByRole("button", { name: /Dungeon Crawler Carl/ }).tap();
    await expect(page).toHaveURL(/[?&]book=b12/);
    const sheet = page.getByRole("dialog", { name: "Dungeon Crawler Carl" });
    await expect(sheet.getByText("Your Pixel 8 uploaded a later spot")).toBeVisible();

    await sheet.getByRole("button", { name: "Use it" }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/restore").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/restore")[0].body).toEqual({ item: "b12", history_id: 77 });

    await sheet.getByRole("button", { name: "Dismiss" }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/dismiss").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/dismiss")[0].body).toEqual({ item: "b12", history_id: 77 });

    await sheet.getByRole("button", { name: "Earlier places" }).tap();
    for (const words of [
      "played on iPad", "not used: older than your place", "not used: jump not proven",
      "place before a change", "held: jumped back", "discarded in Lissen",
    ]) {
      await expect(sheet.getByText(words)).toBeVisible();
    }
    await expect(sheet.getByRole("button", { name: "Go back here" })).toHaveCount(history.length);
    const open = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, vp: document.documentElement.clientWidth }));
    expect(open.doc).toBeLessThanOrEqual(open.vp);

    // Back closes the sheet.
    await page.goBack();
    await expect(sheet).toHaveCount(0);
    await expect(page).not.toHaveURL(/book=/);

    const removed = page.locator("section", { has: page.getByRole("heading", { name: "Recently removed" }) });
    await expect(removed.getByText("Carl's Doomsday Scenario")).toBeVisible();
    await expect(removed.getByText(/1:30:00 · removed .* in Lissen/)).toBeVisible();
    await removed.getByRole("button", { name: "Restore" }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/undiscard").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/undiscard")[0].body).toEqual({ item: "b13" });

    const after = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, vp: document.documentElement.clientWidth }));
    expect(after.doc).toBeLessThanOrEqual(after.vp);
  });
});
