import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import type { MockRoute } from "./fixtures/routes";
import { ebookOnlyBook, requestableBook } from "./fixtures/books";

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
async function fakePush(page: Page, opts: { permission?: NotificationPermission; secure?: boolean; subscribed?: boolean } = {}) {
  await page.addInitScript(({ permission, secure, subscribed }) => {
    let state: NotificationPermission = permission;
    type Sub = { endpoint: string; toJSON: () => unknown; unsubscribe: () => Promise<boolean> };
    const make = (): Sub => ({
      endpoint: "https://push.example/device-1",
      toJSON: () => ({ keys: { p256dh: "p256", auth: "auth" } }),
      unsubscribe: async () => { sub = null; return true; },
    });
    let sub: Sub | null = subscribed ? make() : null;
    Object.defineProperty(Notification, "permission", { configurable: true, get: () => state });
    Notification.requestPermission = async () => { if (state === "default") state = "granted"; return state; };
    const reg = { pushManager: { getSubscription: async () => sub, subscribe: async () => (sub = make()) } };
    const sw = { ready: Promise.resolve(reg), getRegistration: async () => reg, register: async () => reg, addEventListener() {}, removeEventListener() {} };
    Object.defineProperty(navigator, "serviceWorker", { configurable: true, get: () => sw });
    if (!secure) Object.defineProperty(window, "isSecureContext", { configurable: true, get: () => false });
  }, { permission: opts.permission ?? "default", secure: opts.secure ?? true, subscribed: opts.subscribed ?? false });
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

// APP-15: per-event choices. They save, come back on reload, and staff get one more.
test.describe("notify me when", () => {
  test.use({ persona: "requester" });

  test("the checkboxes save and reload", async ({ page, api }) => {
    let saved = { approved: true, declined: true, ready: true, new_request: true };
    await override(page, api, [
      { method: "GET", path: "/api/v1/me/notify-prefs", respond: () => saved },
      { method: "PUT", path: "/api/v1/me/notify-prefs", respond: ({ body }) => (saved = { ...saved, ...(body as object) }) },
    ]);
    await page.goto("/me");
    const group = page.getByRole("group", { name: "Notify me when" });
    await expect(group.getByRole("checkbox")).toHaveCount(3); // no staff-only choice
    const approved = group.getByRole("checkbox", { name: "A request is approved" });
    await expect(approved).toBeChecked();
    await approved.tap();
    await expect(approved).not.toBeChecked();
    expect(api.callsTo("PUT", "/api/v1/me/notify-prefs").map((c) => c.body)).toEqual([{ approved: false }]);

    await page.reload();
    await expect(page.getByRole("group", { name: "Notify me when" }).getByRole("checkbox", { name: "A request is approved" })).not.toBeChecked();
    await expect(page.getByRole("group", { name: "Notify me when" }).getByRole("checkbox", { name: "It’s ready to watch or read" })).toBeChecked();
  });
});

test.describe("notify me when, as staff", () => {
  test.use({ persona: "manager" });

  test("staff can turn off 'Someone requests something'", async ({ page, api }) => {
    await page.goto("/me");
    const box = page.getByRole("group", { name: "Notify me when" }).getByRole("checkbox", { name: "Someone requests something" });
    await expect(box).toBeChecked();
    await box.tap();
    await expect(box).not.toBeChecked();
    expect(api.callsTo("PUT", "/api/v1/me/notify-prefs")[0].body).toEqual({ new_request: false });
  });
});

// APP-14: after a request, "Get notified when it's ready?" — once per device, and only
// when the answer can be yes here.
async function requestBook(page: Page, api: MockedApi, title: string, button: string) {
  const poster = page.getByRole("button", { name: `View details for ${title}` }).first();
  await poster.scrollIntoViewIfNeeded();
  await poster.tap();
  await page.getByRole("button", { name: button }).last().tap();
  await api.quiet();
}
const prompt = (page: Page) => page.getByRole("dialog", { name: "Get notified when it’s ready?" });

async function openBooks(page: Page, api: MockedApi) {
  await page.goto("/discover?tab=books");
  await expect(page.getByRole("main")).toBeVisible();
  await api.quiet();
}

test.describe("the push prompt after a request", () => {
  test.use({ persona: "requester" });

  test("shows once after the first request, and Turn on registers this device", async ({ page, api }) => {
    await fakePush(page);
    await override(page, api, pushRoutes);
    await openBooks(page, api);
    await expect(prompt(page)).toHaveCount(0); // never on page load
    await requestBook(page, api, requestableBook.title, "＋ Request");
    expect(api.callsTo("POST", "/api/v1/requests")).toHaveLength(1);
    await expect(prompt(page)).toBeVisible();
    await prompt(page).getByRole("button", { name: "Turn on" }).tap();
    await expect(prompt(page)).toHaveCount(0);
    expect(api.callsTo("POST", "/api/v1/me/push/subscribe")).toHaveLength(1);
  });

  test("Not now stops it coming back on this device", async ({ page, api }) => {
    await fakePush(page);
    await override(page, api, pushRoutes);
    await openBooks(page, api);
    await requestBook(page, api, requestableBook.title, "＋ Request");
    await prompt(page).getByRole("button", { name: "Not now" }).tap();
    await expect(prompt(page)).toHaveCount(0);

    // A later request, even after a reload, asks nothing.
    await openBooks(page, api);
    await requestBook(page, api, ebookOnlyBook.title, "＋ Request audiobook");
    expect(api.callsTo("POST", "/api/v1/requests")).toHaveLength(2);
    await expect(prompt(page)).toHaveCount(0);
    expect(api.callsTo("POST", "/api/v1/me/push/subscribe")).toHaveLength(0);
  });

  test("no prompt when push is unavailable on the server", async ({ page, api }) => {
    await openBooks(page, api);
    await requestBook(page, api, requestableBook.title, "＋ Request");
    expect(api.callsTo("POST", "/api/v1/requests")).toHaveLength(1);
    await expect(prompt(page)).toHaveCount(0);
  });

  test("no prompt when push is already on", async ({ page, api }) => {
    await fakePush(page, { permission: "granted", subscribed: true });
    await override(page, api, pushRoutes);
    await openBooks(page, api);
    await requestBook(page, api, requestableBook.title, "＋ Request");
    expect(api.callsTo("POST", "/api/v1/requests")).toHaveLength(1);
    await expect(prompt(page)).toHaveCount(0);
  });

  test("no prompt when notifications are blocked", async ({ page, api }) => {
    await fakePush(page, { permission: "denied" });
    await override(page, api, pushRoutes);
    await openBooks(page, api);
    await requestBook(page, api, requestableBook.title, "＋ Request");
    await expect(prompt(page)).toHaveCount(0);
  });
});

test.describe("the push prompt on an iPhone outside the Home Screen app", () => {
  test.use({
    persona: "requester",
    userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  });

  test("explains Add to Home Screen instead, and Got it counts as an answer", async ({ page, api }) => {
    await fakePush(page);
    await override(page, api, pushRoutes);
    await openBooks(page, api);
    await requestBook(page, api, requestableBook.title, "＋ Request");
    await expect(prompt(page).getByText("Add to Home Screen")).toBeVisible();
    await prompt(page).getByRole("button", { name: "Got it" }).tap();
    await expect(prompt(page)).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem("arrmada.pushPrompt"))).toBe("dismissed");
  });
});

// CFG-12: change your own password, sign out your other devices.
const account = (page: Page) => page.getByRole("region", { name: "Account" });
const fitsScreen = async (page: Page) => {
  const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
  expect(w.doc, "page width").toBeLessThanOrEqual(w.viewport);
};

test.describe("account at 375px", () => {
  test.use({ persona: "requester" });

  test("changes the password with the current one, and says a wrong one is wrong", async ({ page, api }) => {
    await override(page, api, [{ method: "POST", path: "/api/v1/me/password", body: { password_set: true, signed_out: 2 } }]);
    // A wrong current password answers 400, as the server does (registered last, so first).
    await page.route("**/api/v1/me/password", async (route) => {
      const body = route.request().postDataJSON() as { current: string };
      if (body.current !== "oldpassword") return route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ message: "Current password is wrong" }) });
      return route.fallback();
    });
    await page.goto("/me#account");
    await account(page).getByRole("button", { name: "Change" }).tap();
    const form = account(page).getByRole("form", { name: "Change password" });
    await form.getByLabel("Current password").fill("guess");
    await form.getByLabel("New password", { exact: true }).fill("brandnewpass");
    await form.getByLabel("New password again").fill("brandnewpass");
    await fitsScreen(page);
    await form.getByRole("button", { name: "Change password" }).tap();
    await expect(form.getByRole("alert")).toHaveText("Current password is wrong");

    await form.getByLabel("Current password").fill("oldpassword");
    await form.getByRole("button", { name: "Change password" }).tap();
    await expect(page.getByText("Password changed — 2 other devices were signed out")).toBeVisible();
    await expect(form).toHaveCount(0);
    expect(api.callsTo("POST", "/api/v1/me/password").slice(-1)[0]?.body).toEqual({ current: "oldpassword", new: "brandnewpass" });
  });

  test("a Plex-only account sets its first password without a current one", async ({ page, api }) => {
    await override(page, api, [
      { method: "GET", path: "/api/v1/me/account", body: { password_set: false } },
      { method: "POST", path: "/api/v1/me/password", body: { password_set: true, signed_out: 0 } },
    ]);
    await page.goto("/me");
    await expect(account(page).getByText("You sign in with Plex.", { exact: false })).toBeVisible();
    await account(page).getByRole("button", { name: "Set a password" }).tap();
    const form = account(page).getByRole("form", { name: "Set a password" });
    await expect(form.getByLabel("Current password")).toHaveCount(0);
    await form.getByLabel("New password", { exact: true }).fill("firstpassword");
    await form.getByLabel("New password again").fill("firstpassword");
    await form.getByRole("button", { name: "Set password" }).tap();
    await expect(page.getByText("Password set")).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/me/password")[0].body).toEqual({ current: "", new: "firstpassword" });
    await expect(account(page).getByRole("button", { name: "Change" })).toBeVisible();
  });

  test("sign out other devices asks first and keeps this one", async ({ page, api }) => {
    await override(page, api, [{ method: "POST", path: "/api/v1/me/sessions/revoke-others", body: { signed_out: 3 } }]);
    await page.goto("/me");
    await account(page).getByRole("button", { name: "Sign out others" }).tap();
    const dialog = page.getByRole("dialog", { name: "Sign out your other devices?" });
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Sign out others" }).tap();
    await expect(page.getByText("Signed out of 3 other devices")).toBeVisible();
    expect(api.callsTo("POST", "/api/v1/me/sessions/revoke-others")).toHaveLength(1);
    await expect(page.getByRole("heading", { level: 1, name: "deckhand" })).toBeVisible();
  });
});
