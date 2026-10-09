import { test as base, expect, type Page, type Route } from "@playwright/test";
import { personas, type Persona } from "./fixtures/users";
import { routes as defaultRoutes, type MockRoute } from "./fixtures/routes";
import { NOW } from "./fixtures/clock";

// One request the page made to /api, as the specs see it.
export interface ApiCall {
  method: string;
  path: string; // pathname + search
  body: unknown;
}

export interface MockedApi {
  /** Every /api call the page made, in order. */
  calls: ApiCall[];
  /** Calls with no fixture. They answer 501 and fail the test at teardown. */
  unmocked: string[];
  /** Calls matching a method and a path prefix, e.g. ("POST", "/api/v1/requests"). */
  callsTo(method: string, pathPrefix: string): ApiCall[];
  /**
   * Resolves once the page has made no new /api call for `quietMs`. A client-side
   * navigation never fires the browser's own "networkidle" again, so specs use this to
   * let a freshly opened page fetch and render before judging it.
   */
  quiet(quietMs?: number): Promise<void>;
}

// installMockApi answers every /api request from the fixture table, so the build
// under test never needs the Go backend. Routes are checked in order and the first
// match wins; `extra` routes go first so a spec can override one endpoint.
//
// An unknown endpoint answers 501 and is recorded instead of falling through to the
// network: a page that starts calling something new fails loudly, naming the call,
// rather than flaking on a missing server.
export async function installMockApi(page: Page, persona: Persona, extra: MockRoute[] = []): Promise<MockedApi> {
  const table = [...extra, ...defaultRoutes(personas[persona])];
  const api: MockedApi = {
    calls: [],
    unmocked: [],
    callsTo: (method, prefix) => api.calls.filter((c) => c.method === method && c.path.startsWith(prefix)),
    quiet: async (quietMs = 300) => {
      // Pages that poll every 1.5 s still leave quiet gaps; give up waiting after 5 s.
      const deadline = Date.now() + 5_000;
      let seen = -1;
      while (seen !== api.calls.length && Date.now() < deadline) {
        seen = api.calls.length;
        await page.waitForTimeout(quietMs);
      }
    },
  };

  // Pin the date so date-driven pages match the fixtures; timers still run normally.
  await page.clock.setFixedTime(NOW);

  // The live-updates socket: accept it and never send anything. Without this the
  // upgrade goes to vite preview, fails, and the app reconnects every 2 s.
  await page.routeWebSocket("**/api/v1/ws", () => {});

  await page.route((u) => u.pathname.startsWith("/api/"), async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const method = req.method();
    let body: unknown = undefined;
    try { body = req.postDataJSON(); } catch { body = req.postData(); }
    api.calls.push({ method, path: url.pathname + url.search, body });

    for (const r of table) {
      if (r.method !== method) continue;
      const m = typeof r.path === "string" ? (url.pathname === r.path ? [] : null) : url.pathname.match(r.path);
      if (!m) continue;
      const out = r.respond ? r.respond({ url, params: m.slice(1), body }) : r.body;
      if (r.status === 204) return route.fulfill({ status: 204 });
      return route.fulfill({ status: r.status ?? 200, contentType: "application/json", body: JSON.stringify(out ?? {}) });
    }
    api.unmocked.push(`${method} ${url.pathname}`);
    return route.fulfill({ status: 501, contentType: "application/json", body: JSON.stringify({ message: `e2e: no fixture for ${method} ${url.pathname}` }) });
  });

  // Posters, backdrops and avatars point at TMDB / Open Library. Answer them with a
  // blank image so the run is offline and deterministic.
  await page.route(/^https?:\/\/(?!localhost[:/])/, (route) =>
    route.fulfill({ status: 200, contentType: "image/png", body: BLANK_PNG }),
  );
  return api;
}

const BLANK_PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=",
  "base64",
);

// test is @playwright/test's test with the mocked API installed before each test.
// Pick the signed-in persona with test.use({ persona: "requester" }).
export const test = base.extend<{ persona: Persona; api: MockedApi }>({
  persona: ["admin", { option: true }],
  // auto: every test gets the mock, even one that never asks for `api`, so nothing
  // can reach for a real backend.
  api: [async ({ page, persona }, use) => {
    const api = await installMockApi(page, persona);
    await use(api);
    expect(api.unmocked, "API calls with no fixture in e2e/fixtures/routes.ts").toEqual([]);
  }, { auto: true }],
});

export { expect };
