import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import { connections, jellyfinConnected } from "./fixtures/media";
import { NOW } from "./fixtures/clock";
import type { ConnectionItem } from "../src/lib/connectionsApi";

// Jellyfin (J5a): Settings → Connections opens with a "Media servers" section, a card per
// server side by side. Jellyfin is a quiet Connect card until it's connected; its editor
// saves the URL, key and public address (the key is never shown back), tests the server,
// and warns when the Watch link address only works at home.

const recent = new Date(NOW - 3 * 60_000).toISOString();
const plexOn: ConnectionItem = {
  kind: "plex", ref: "default", card: "plex", name: "Plex", subtitle: "http://192.168.1.10:32400", configured: true, enabled: true,
  state: "ok", testable: true, last_ok_at: recent, detail: "Basement · watch monitoring on", fix_path: "/settings/connections#plex",
};
const jellyfinOn: ConnectionItem = {
  kind: "jellyfin", ref: "default", card: "jellyfin", name: "Jellyfin", subtitle: "http://jellyfin:8096", configured: true, enabled: true,
  state: "ok", testable: true, last_ok_at: recent, used_for: ["watch", "library", "insights"],
};

// The connection list with the media servers swapped for what servers() returns at the
// time of each read, and Jellyfin's own settings once it's connected.
async function withServers(page: Page, servers: () => ConnectionItem[]) {
  await page.route("**/api/v1/connections", (r) => {
    if (r.request().method() !== "GET") return r.fallback();
    const sections = connections.sections.map((s) => s.id !== "other" ? s : {
      ...s, items: [...servers(), ...s.items.filter((x) => x.kind !== "plex" && x.kind !== "jellyfin")],
    });
    return r.fulfill({ json: { sections } });
  });
  await page.route("**/api/v1/jellyfin", (r) =>
    r.request().method() === "GET" && servers().some((x) => x.kind === "jellyfin" && x.configured) ? r.fulfill({ json: jellyfinConnected }) : r.fallback());
}

test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("a connected Plex and a quiet Jellyfin sit side by side, then both connected", async ({ page, api }) => {
    let servers = [plexOn];
    await withServers(page, () => servers);
    await page.goto("/settings/connections");
    const section = page.locator("#media-servers");
    await expect(section.getByRole("heading", { name: "Media servers" })).toBeVisible();
    const plex = section.locator("#plex");
    const jf = section.locator("#jellyfin");
    await expect(plex.getByText("Connected", { exact: true })).toBeVisible();
    await expect(plex.getByText("Basement · watch monitoring on")).toBeVisible();
    await expect(plex.getByRole("list", { name: "What Arrmada uses Plex for" })).toContainText("Watch links");
    await expect(jf.getByText("Not connected")).toBeVisible();
    await expect(jf.getByRole("button", { name: "Connect Jellyfin" })).toBeVisible();
    // Side by side: same row, Jellyfin to the right.
    const a = await plex.boundingBox();
    const b = await jf.boundingBox();
    expect(a && b && Math.abs(a.y - b.y) < 2 && b.x > a.x + a.width - 1).toBe(true);
    // The old section keeps alerts and apps only.
    const other = page.locator("#other-connections");
    await expect(other.getByRole("heading", { name: "Alerts and apps" })).toBeVisible();
    await expect(other.locator("#plex")).toHaveCount(0);
    // Nothing asks Jellyfin's settings while it isn't connected.
    await api.quiet();
    expect(api.callsTo("GET", "/api/v1/jellyfin")).toHaveLength(0);

    servers = [plexOn, jellyfinOn];
    await page.reload();
    await expect(jf.getByText("Attic · Jellyfin 10.10.3")).toBeVisible();
    await expect(jf.getByText("Connected", { exact: true })).toBeVisible();
    await expect(jf.getByRole("list", { name: "What Arrmada uses Jellyfin for" })).toHaveText("Watch linksLibrary updatesInsights");
    await jf.getByRole("button", { name: "Test" }).click();
    await expect(jf.getByRole("status")).toHaveText("✓ Connected");
    expect(api.callsTo("POST", "/api/v1/connections/jellyfin/default/test")).toHaveLength(1);
  });

  test("the Jellyfin editor tests and saves; the key goes up once and never comes back", async ({ page, api }) => {
    // A link to the card while Jellyfin isn't connected opens its editor.
    await page.goto("/settings/connections#jellyfin");
    const jf = page.locator("#jellyfin");
    const form = jf.getByRole("form", { name: "Jellyfin connection" });
    await expect(form).toBeVisible();
    await expect(form.getByText("No key yet")).toBeVisible();
    await expect(form.getByRole("button", { name: "Save" })).toBeDisabled();

    await form.getByLabel("Server URL").fill("http://unreachable:8096");
    await form.getByRole("button", { name: "Test" }).click();
    await expect(form.getByRole("status")).toHaveText("✕ dial tcp: connection refused");

    await form.getByLabel("Server URL").fill("http://jellyfin:8096");
    await form.getByLabel("API key").fill("jf-key-123");
    // #129: the card says notifications' Watch links use this address, not the connection's.
    await expect(form.getByText(/Watch links in notifications and "ready" messages use this address/)).toBeVisible();
    await form.getByLabel("Public address for Watch links").fill("https://jellyfin.example.com");
    await form.getByRole("button", { name: "Test" }).click();
    await expect(form.getByRole("status")).toHaveText("✓ Connected to Attic · Jellyfin 10.10.3");
    expect(api.callsTo("POST", "/api/v1/jellyfin/test")[1]?.body).toEqual({ url: "http://jellyfin:8096", api_key: "jf-key-123" });

    const listReads = api.callsTo("GET", "/api/v1/connections").length;
    await form.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText("Jellyfin connection saved")).toBeVisible();
    expect(api.callsTo("PUT", "/api/v1/jellyfin")).toHaveLength(1);
    expect(api.callsTo("PUT", "/api/v1/jellyfin")[0].body).toEqual({ url: "http://jellyfin:8096", public_url: "https://jellyfin.example.com", api_key: "jf-key-123" });
    // The key field empties and the form only says a key is set; the card is read again.
    await expect(form.getByLabel("API key")).toHaveValue("");
    await expect(form.getByLabel("API key")).toHaveAttribute("placeholder", "Key set — leave blank to keep it");
    await expect(form.getByText("Key set", { exact: true })).toBeVisible();
    await expect.poll(() => api.callsTo("GET", "/api/v1/connections").length).toBeGreaterThan(listReads);
    expect(await page.content()).not.toContain("jf-key-123");

    // A new address asks for the key again: the saved key is only sent to the address it
    // was saved for, so Save waits for it. Back at the saved address, nothing is asked.
    const key = form.getByLabel("API key");
    const save = form.getByRole("button", { name: "Save" });
    await form.getByLabel("Server URL").fill("http://jellyfin:8097");
    await expect(key).toHaveAttribute("placeholder", "Enter the key for the new address");
    await expect(form.getByText("New address: enter its API key. The saved key is only sent to the address it was saved for.")).toBeVisible();
    await expect(save).toBeDisabled();
    await key.fill("jf-key-456");
    await expect(save).toBeEnabled();
    await key.fill("");
    await expect(save).toBeDisabled();
    await form.getByLabel("Server URL").fill("http://JELLYFIN:8096/");
    await expect(key).toHaveAttribute("placeholder", "Key set — leave blank to keep it");
    await expect(save).toBeEnabled();

    // A second save at the same address without typing a key leaves it out, which keeps
    // the saved one.
    await form.getByLabel("Public address for Watch links").fill("https://jf.example.com");
    await save.click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/jellyfin").length).toBe(2);
    expect(api.callsTo("PUT", "/api/v1/jellyfin")[1].body).not.toHaveProperty("api_key");
  });

  test("the public address follows the URL and warns when it only works at home", async ({ page }) => {
    await page.goto("/settings/connections");
    const jf = page.locator("#jellyfin");
    await jf.getByRole("button", { name: "Connect Jellyfin" }).click();
    const form = jf.getByRole("form", { name: "Jellyfin connection" });
    const warning = form.getByText(/only works on your home network/);
    const publicAddress = form.getByLabel("Public address for Watch links");

    await form.getByLabel("Server URL").fill("http://192.168.1.20:8096");
    await expect(publicAddress).toHaveValue("http://192.168.1.20:8096");
    await expect(warning).toBeVisible();
    await publicAddress.fill("https://jf.example.com");
    await expect(warning).toHaveCount(0);
    // Typed in itself, it no longer follows the URL.
    await form.getByLabel("Server URL").fill("http://jellyfin:8096");
    await expect(publicAddress).toHaveValue("https://jf.example.com");
    for (const lan of ["http://jellyfin.local:8096", "http://localhost:8096", "http://10.0.0.5:8096", "http://jellyfin:8096"]) {
      await publicAddress.fill(lan);
      await expect(warning, lan).toBeVisible();
    }
    // Close puts the card back as it was.
    await jf.getByRole("button", { name: "Close" }).click();
    await expect(jf.getByRole("button", { name: "Connect Jellyfin" })).toBeVisible();
  });
});
