import type { Page, Route } from "@playwright/test";
import { test, expect } from "./mockApi";
import * as ins from "./fixtures/insights";
import type { PlexConfig } from "../src/lib/api";

// Insights (PLEX-08/09/18/19): the header badge says what monitoring is really doing, the
// recorded-plays tabs say when nothing new is being recorded, database tabs never hide
// behind Plex errors, and every tab fits a phone.

// serveInsights answers the Insights endpoints for a saved server. It is registered after
// the default table, so it wins for these paths. PUT /insights/plex records the body and
// flips the served config to what was saved.
async function serveInsights(page: Page, start: PlexConfig, opts: { plexDown?: boolean } = {}) {
  let cfg = start;
  const puts: unknown[] = [];
  const table = ins.routes(() => cfg, opts);
  await page.route((u) => u.pathname.startsWith("/api/v1/insights/"), async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    if (req.method() === "PUT" && url.pathname === "/api/v1/insights/plex") {
      const body = req.postDataJSON() as Partial<PlexConfig>;
      puts.push(body);
      cfg = { ...cfg, ...body, status: body.enabled ? "recording" : "off" };
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(cfg) });
    }
    const r = table.find((t) => t.method === req.method() && t.path === url.pathname);
    if (!r) return route.fallback();
    const out = r.respond ? r.respond({ url, params: [], body: undefined }) : r.body;
    return route.fulfill({ status: r.status ?? 200, contentType: "application/json", body: JSON.stringify(out) });
  });
  return { puts, set: (c: PlexConfig) => { cfg = c; } };
}

test.describe("admin", () => {
  test.use({ persona: "admin" });

  test("the badge has four states, and the unreachable one explains itself", async ({ page }) => {
    const srv = await serveInsights(page, ins.config("unconfigured"));
    for (const [status, label] of [
      ["unconfigured", "Not connected"],
      ["off", "Connected · not recording"],
      ["recording", "Recording"],
      ["unreachable", "Plex unreachable"],
    ] as const) {
      srv.set(ins.config(status, status === "unreachable" ? { last_error: "connection refused" } : {}));
      await page.goto("/insights?tab=history");
      const badge = page.getByText(label, { exact: true });
      await expect(badge).toBeVisible();
      if (status === "unreachable") await expect(badge).toHaveAttribute("title", "connection refused");
    }
  });

  test("monitoring off: the recorded tabs carry a banner whose Turn on saves the switch", async ({ page }) => {
    const { puts } = await serveInsights(page, ins.config("off"));
    await page.goto("/insights?tab=history");
    const banner = page.getByRole("status").filter({ hasText: "Monitoring is off — nothing new is recorded." });
    await expect(banner).toBeVisible();
    // The data is still there under the banner.
    await expect(page.getByRole("cell", { name: /The Cartographer/ })).toBeVisible();
    await banner.getByRole("button", { name: "Turn on" }).click();
    await expect.poll(() => puts.length).toBe(1);
    expect(puts[0]).toEqual({ url: "http://plex.lan:32400", enabled: true });
    await expect(banner).toHaveCount(0);
    await expect(page.getByText("Recording", { exact: true })).toBeVisible();
  });

  test("Activity has no monitoring banner (it asks Plex directly)", async ({ page }) => {
    await serveInsights(page, ins.config("off"));
    await page.goto("/insights");
    await expect(page.getByText("2 streams active")).toBeVisible();
    await expect(page.getByText("Monitoring is off — nothing new is recorded.")).toHaveCount(0);
  });

  test("Convert says pausing needs monitoring when it is off", async ({ page }) => {
    await page.goto("/convert?tab=settings");
    await expect(page.getByText(/Needs Plex monitoring turned on/)).toBeVisible();
    await expect(page.getByRole("link", { name: "Plex settings" })).toBeVisible();
  });
});
