import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import type { MockRoute } from "./fixtures/routes";
import { myAudio } from "./fixtures/system";
import { personas } from "./fixtures/users";
import { NOW } from "./fixtures/clock";
import type { AudioHistoryEntry, MyAudio } from "../src/lib/api";

// The You page's place timeline at phone width (AUD-05): a later spot an app sent that
// wasn't used is offered back, every timeline row says why in words and can be gone back
// to, and a place an app removed shows under Recently removed with Restore.

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
    await page.goto("/audiobooks");
    await expect(page.getByText("Your Pixel 8 uploaded a later spot")).toBeVisible();
    await expect(page.getByText("4:02:11")).toBeVisible();

    // Nothing pushes the page wider than the phone.
    const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, vp: document.documentElement.clientWidth }));
    expect(w.doc).toBeLessThanOrEqual(w.vp);

    await page.getByRole("button", { name: "Use it" }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/restore").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/restore")[0].body).toEqual({ item: "b12", history_id: 77 });

    await page.getByRole("button", { name: "Dismiss" }).tap();
    await expect.poll(() => api.callsTo("POST", "/api/v1/me/audio/dismiss").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/me/audio/dismiss")[0].body).toEqual({ item: "b12", history_id: 77 });

    await page.getByRole("button", { name: "Earlier places" }).tap();
    for (const words of [
      "played on iPad", "not used: older than your place", "not used: jump not proven",
      "place before a change", "held: jumped back", "discarded in Lissen",
    ]) {
      await expect(page.getByText(words)).toBeVisible();
    }
    await expect(page.getByRole("button", { name: "Go back here" })).toHaveCount(history.length);

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
