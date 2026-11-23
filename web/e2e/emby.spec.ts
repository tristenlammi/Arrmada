import type { Page } from "@playwright/test";
import { test, expect, type MockedApi } from "./mockApi";
import { connections, jellyfinConnected } from "./fixtures/media";
import { personas } from "./fixtures/users";
import { status } from "./fixtures/system";
import type { MockRoute } from "./fixtures/routes";
import type { ConnectionItem } from "../src/lib/connectionsApi";

// Emby (#60) connects through the Jellyfin slot: the connection card's Server choice says
// which, and from then on the app names and draws it as Emby — the card, the login button
// and its sheet (which has no Quick Connect: Emby doesn't offer it). Every call is a
// fixture; no Emby is contacted.

async function installExtra(page: Page, api: MockedApi, extra: MockRoute[]) {
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
      const out = r.respond ? r.respond({ url, params: m.slice(1), body }) : r.body;
      return route.fulfill({ status: r.status ?? 200, contentType: "application/json", body: JSON.stringify(out ?? {}) });
    }
    return route.fallback();
  });
}

test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("the connection card connects an Emby server: its words, test and save follow the choice", async ({ page, api }) => {
    await page.goto("/settings/connections#jellyfin");
    const jf = page.locator("#jellyfin");
    const form = jf.getByRole("form", { name: "Jellyfin connection" });
    await expect(form).toBeVisible();
    await expect(form.getByLabel("Server", { exact: true })).toHaveValue("jellyfin");
    await expect(form.getByText(/Emby has no Quick Connect/)).toHaveCount(0);

    await form.getByLabel("Server", { exact: true }).selectOption("emby");
    const emby = jf.getByRole("form", { name: "Emby connection" });
    await expect(emby).toBeVisible();
    await expect(emby.getByText(/Emby has no Quick Connect/)).toBeVisible();
    await expect(emby.getByText("Make one in Emby: Dashboard → Advanced → API Keys → New API Key. It's never shown again once saved.")).toBeVisible();

    await emby.getByLabel("Server URL").fill("http://emby:8096");
    await emby.getByLabel("API key").fill("emby-key-1");
    await emby.getByLabel("Public address for Watch links").fill("https://emby.example.com");
    await emby.getByRole("button", { name: "Test" }).click();
    await expect(emby.getByRole("status")).toHaveText("✓ Connected to Attic · Emby 4.8.10.0");
    const tests = api.callsTo("POST", "/api/v1/jellyfin/test");
    expect(tests[tests.length - 1]?.body).toEqual({ server_kind: "emby", url: "http://emby:8096", api_key: "emby-key-1" });

    await emby.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("Emby connection saved")).toBeVisible();
    expect(api.callsTo("PUT", "/api/v1/jellyfin")[0].body).toEqual({ server_kind: "emby", url: "http://emby:8096", public_url: "https://emby.example.com", api_key: "emby-key-1" });
    await expect(emby.getByLabel("API key")).toHaveValue("");
    expect(await page.content()).not.toContain("emby-key-1");
  });

  test("a connected Emby is named and drawn as Emby on Connections", async ({ page, api }) => {
    const embyOn: ConnectionItem = {
      kind: "jellyfin", ref: "default", card: "jellyfin", name: "Emby", subtitle: "http://emby:8096", configured: true, enabled: true,
      state: "ok", testable: true, used_for: ["watch", "library"],
    };
    await installExtra(page, api, [
      { method: "GET", path: "/api/v1/status", body: { ...status(personas.admin), jellyfin_server_kind: "emby" } },
      { method: "GET", path: "/api/v1/jellyfin", body: { ...jellyfinConnected, server_kind: "emby", url: "http://emby:8096", version: "4.8.10.0" } },
      {
        method: "GET", path: "/api/v1/connections",
        body: { sections: connections.sections.map((s) => s.id !== "other" ? s : { ...s, items: [embyOn, ...s.items.filter((x) => x.kind !== "jellyfin")] }) },
      },
    ]);
    await page.goto("/settings/connections");
    const card = page.locator("#jellyfin");
    await expect(card.getByText("Attic · Emby 4.8.10.0")).toBeVisible();
    await expect(card.getByRole("list", { name: "What Arrmada uses Emby for" })).toHaveText("Watch linksLibrary updates");
    await expect(card.getByText("Jellyfin", { exact: true })).toHaveCount(0);
    // Editing it, the Server choice opens on Emby.
    await card.getByRole("button", { name: "Edit" }).click();
    await expect(card.getByRole("form", { name: "Emby connection" }).getByLabel("Server", { exact: true })).toHaveValue("emby");
  });
});

test.describe("login page", () => {
  test.use({ persona: "requester" });

  test("Sign in with Emby: username and password only, no Quick Connect", async ({ page, api }) => {
    await installExtra(page, api, [
      { method: "GET", path: "/api/v1/auth/me", status: 401, body: { status: "error", message: "authentication required" } },
      { method: "GET", path: "/api/v1/status", body: { ...status(personas.requester), authenticated: false, plex_login: false, jellyfin_login: true, jellyfin_quickconnect: false, jellyfin_server_kind: "emby" } },
      { method: "POST", path: "/api/v1/auth/jellyfin", body: { user: personas.requester.user } },
    ]);
    await page.addInitScript(() => { if (!sessionStorage.getItem("e2e-once")) { sessionStorage.setItem("e2e-once", "1"); sessionStorage.setItem("arrmada.next", "/requests"); } });
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Sign in with Jellyfin" })).toHaveCount(0);
    await page.getByRole("button", { name: "Sign in with Emby" }).click();
    const sheet = page.getByRole("dialog", { name: "Sign in with Emby" });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByRole("button", { name: "Use Quick Connect" })).toHaveCount(0);
    await sheet.getByLabel("Emby username").fill("kid");
    await sheet.getByLabel("Password").fill("kid-password");
    await sheet.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(/\/requests$/, { timeout: 10_000 });
    const posts = api.callsTo("POST", "/api/v1/auth/jellyfin").filter((c) => c.path === "/api/v1/auth/jellyfin");
    expect(posts.map((c) => c.body)).toEqual([{ username: "kid", password: "kid-password" }]);
  });
});
