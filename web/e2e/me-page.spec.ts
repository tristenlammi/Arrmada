import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import type { MockRoute } from "./fixtures/routes";

// The Me page as the home of everything about you (APP-13/14/15, CFG-12, SEC-15): one
// "Get notified" switch with a plain answer when push can't be on, per-event choices,
// changing your password and the devices you're signed in on.

const KEY = "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U";

// override answers the given endpoints before the default fixtures (the last route
// registered is tried first), recording the calls like the defaults do.
async function override(page: Page, api: MockedApi, extra: MockRoute[]) {
  await page.route((u) => u.pathname.startsWith("/api/"), async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    for (const r of extra) {
      if (r.method !== req.method()) continue;
      const m = typeof r.path === "string" ? (url.pathname === r.path ? [] : null) : url.pathname.match(r.path);
      if (!m) continue;
      let body: unknown;
      try { body = req.postDataJSON(); } catch { body = req.postData(); }
      api.calls.push({ method: req.method(), path: url.pathname + url.search, body });
      if (r.status === 204) return route.fulfill({ status: 204 });
      const out = r.respond ? r.respond({ url, params: m.slice(1), body }) : r.body;
      return route.fulfill({ status: r.status ?? 200, contentType: "application/json", body: JSON.stringify(out ?? {}) });
    }
    return route.fallback();
  });
}

const pushRoutes: MockRoute[] = [
  { method: "GET", path: "/api/v1/me/push/key", body: { key: KEY } },
  { method: "POST", path: "/api/v1/me/push/subscribe", body: { subscribed: true } },
  { method: "POST", path: "/api/v1/me/push/unsubscribe", body: { subscribed: false } },
];

// fakePush stands in for the browser's push machinery (the e2e run blocks service workers):
// a registration whose push manager subscribes, and a Notification permission that a tap
// can grant. secure:false pretends the page came over plain http.
async function fakePush(page: Page, opts: { permission?: NotificationPermission; secure?: boolean } = {}) {
  await page.addInitScript(({ permission, secure }) => {
    let state: NotificationPermission = permission;
    let sub: { endpoint: string; toJSON: () => unknown; unsubscribe: () => Promise<boolean> } | null = null;
    Object.defineProperty(Notification, "permission", { configurable: true, get: () => state });
    Notification.requestPermission = async () => { if (state === "default") state = "granted"; return state; };
    const reg = {
      pushManager: {
        getSubscription: async () => sub,
        subscribe: async () => (sub = {
          endpoint: "https://push.example/device-1",
          toJSON: () => ({ keys: { p256dh: "p256", auth: "auth" } }),
          unsubscribe: async () => { sub = null; return true; },
        }),
      },
    };
    const sw = { ready: Promise.resolve(reg), getRegistration: async () => reg, register: async () => reg, addEventListener() {}, removeEventListener() {} };
    Object.defineProperty(navigator, "serviceWorker", { configurable: true, get: () => sw });
    if (!secure) Object.defineProperty(window, "isSecureContext", { configurable: true, get: () => false });
  }, { permission: opts.permission ?? "default", secure: opts.secure ?? true });
}

const notified = (page: Page) => page.getByRole("region", { name: "Get notified" });

test.describe("Get notified at 375px", () => {
  test.use({ persona: "requester" });

  test("the switch turns push on and off for this device", async ({ page, api }) => {
    await fakePush(page);
    await override(page, api, pushRoutes);
    await page.goto("/me");
    const sw = notified(page).getByRole("switch", { name: "Notifications on this device" });
    await expect(sw).toHaveAttribute("aria-checked", "false");
    await expect(notified(page).getByText("Off on this phone")).toBeVisible();

    await sw.tap();
    await expect(sw).toHaveAttribute("aria-checked", "true");
    await expect(notified(page).getByText("On for this phone")).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/me/push/subscribe")).toHaveLength(1);
    expect(api.callsTo("POST", "/api/v1/me/push/subscribe")[0].body).toMatchObject({ endpoint: "https://push.example/device-1", keys: { p256dh: "p256", auth: "auth" } });

    await sw.tap();
    await expect(sw).toHaveAttribute("aria-checked", "false");
    expect(api.callsTo("POST", "/api/v1/me/push/unsubscribe")).toHaveLength(1);
  });

  test("no push key on the server: says so instead of hiding push", async ({ page }) => {
    await page.goto("/me");
    await expect(notified(page).getByText("Push isn’t set up on this server yet — ask the admin.", { exact: false })).toBeVisible();
    await expect(notified(page).getByRole("switch")).toHaveCount(0);
  });

  test("plain http: points at the secure address", async ({ page, api }) => {
    await fakePush(page, { secure: false });
    await override(page, api, pushRoutes);
    await page.goto("/me");
    await expect(notified(page).getByText("Notifications need the secure address", { exact: false })).toBeVisible();
    await expect(notified(page).getByRole("switch")).toHaveCount(0);
  });

  test("blocked: explains how to allow notifications again", async ({ page, api }) => {
    await fakePush(page, { permission: "denied" });
    await override(page, api, pushRoutes);
    await page.goto("/me");
    await expect(notified(page).getByText("Notifications are blocked for this site.", { exact: false })).toBeVisible();
    await expect(notified(page).getByRole("switch")).toHaveCount(0);
  });

  test("Apprise is still there, under Advanced", async ({ page }) => {
    await page.goto("/me");
    const advanced = notified(page).getByText("Advanced: Discord, ntfy, email (Apprise)");
    await expect(notified(page).getByRole("textbox", { name: "Your Apprise URL" })).toBeHidden();
    await advanced.tap();
    await expect(notified(page).getByRole("textbox", { name: "Your Apprise URL" })).toBeVisible();
    // No two options share a name.
    await expect(page.getByText("Push notifications", { exact: false })).toHaveCount(0);
  });

  test("the bell has a Settings button to here, and no gear", async ({ page }) => {
    await page.goto("/discover");
    await page.getByRole("button", { name: "Notifications" }).tap();
    await expect(page.getByRole("button", { name: "Notification settings" })).toHaveCount(0);
    await page.getByRole("link", { name: "Settings", exact: true }).tap();
    await expect(page).toHaveURL(/\/me#notifications$/);
    await expect(notified(page)).toBeVisible();
  });
});

test.describe("Get notified on an iPhone outside the Home Screen app", () => {
  test.use({
    persona: "requester",
    userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  });

  test("gives the Add to Home Screen steps", async ({ page, api }) => {
    await fakePush(page);
    await override(page, api, pushRoutes);
    await page.goto("/me");
    await expect(notified(page).getByText("Add to Home Screen")).toBeVisible();
    await expect(notified(page).getByRole("switch")).toHaveCount(0);
  });
});

test.describe("Get notified on a desktop", () => {
  test.use({ persona: "admin", viewport: { width: 1280, height: 900 } });

  test("the sidebar name opens Me, and the switch works there too", async ({ page, api }) => {
    await fakePush(page);
    await override(page, api, pushRoutes);
    await page.goto("/");
    await page.getByRole("link", { name: "admiral — Me" }).click();
    await expect(page).toHaveURL(/\/me$/);
    const sw = notified(page).getByRole("switch", { name: "Notifications on this device" });
    await sw.click();
    await expect(sw).toHaveAttribute("aria-checked", "true");
    expect(api.callsTo("POST", "/api/v1/me/push/subscribe")).toHaveLength(1);
  });
});
