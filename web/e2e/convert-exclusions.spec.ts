import { test, expect, type MockedApi } from "./mockApi";
import type { MockRoute } from "./fixtures/routes";
import type { ConvertCandidate, ConvertPlan, ConvertTitleExclusion } from "../src/lib/api";
import { convertSettings, convertStats } from "./fixtures/modules";

// Convert's "leave alone" rules: a title's own "Don't convert this" (its page, its plan
// card), whole library folders and resolutions (Settings → Convert), and the Overview line
// that says how many files each rule keeps from converting.

const GB = 1024 ** 3;

// exclusionRoutes answers the list and the switch from one in-memory list, as the server would.
function exclusionRoutes(list: ConvertTitleExclusion[]): MockRoute[] {
  return [
    { method: "GET", path: "/api/v1/convert/exclusions", respond: () => ({ items: list }) },
    { method: "PUT", path: "/api/v1/convert/exclusions", respond: ({ body }) => {
      const b = body as { kind: "movie" | "series"; movie_id?: number; series_id?: number; season?: number; excluded: boolean };
      const season = b.season ?? -1;
      const same = (x: ConvertTitleExclusion) => x.kind === b.kind && (x.movie_id ?? x.series_id) === (b.movie_id ?? b.series_id) && x.season === season;
      const at = list.findIndex(same);
      if (at >= 0) list.splice(at, 1);
      if (b.excluded) list.push({ kind: b.kind, movie_id: b.movie_id, series_id: b.series_id, season, title: "A title", created_at: "" });
      return { excluded: b.excluded, dropped: b.excluded ? 1 : 0 };
    } },
  ];
}

test.describe("leave alone", () => {
  test.use({ persona: "admin" });

  test("a film's page turns “Don't convert this” on and back off", async ({ page, api }) => {
    await installExtra(page, api, exclusionRoutes([]));
    await page.goto("/movies/1");
    await expect(page.getByTestId("convert-left-out")).toHaveCount(0);

    await page.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Don't convert this film" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/convert/exclusions").map((c) => c.body)).toEqual([{ kind: "movie", movie_id: 1, excluded: true }]);
    await expect(page.getByRole("status").filter({ hasText: "Convert will leave this film alone. 1 waiting file was taken off the queue." })).toBeVisible();
    const chip = page.getByTestId("convert-left-out");
    await expect(chip).toContainText("Not converted");

    await chip.getByRole("button", { name: "Let Convert work on it" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/convert/exclusions").map((c) => (c.body as { excluded: boolean }).excluded)).toEqual([true, false]);
    await expect(page.getByTestId("convert-left-out")).toHaveCount(0);
    await page.getByRole("button", { name: "More actions" }).click();
    await expect(page.getByRole("menuitem", { name: "Don't convert this film" })).toBeVisible();
  });

  test("a show's menu leaves the whole show alone", async ({ page, api }) => {
    await installExtra(page, api, exclusionRoutes([]));
    await page.goto("/series/1");
    await page.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Don't convert this show" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/convert/exclusions").map((c) => c.body)).toEqual([{ kind: "series", series_id: 1, excluded: true }]);
    await expect(page.getByTestId("convert-left-out").first()).toContainText("leave this show alone");
  });

  test("an episode's plan card leaves its season alone, and says so", async ({ page, api }) => {
    const episode: ConvertCandidate = {
      kind: "episode", key: "episode:10:2:1", series_id: 10, season: 2, episode: 1, title: "Show - S02E01", path: "/tv/Show/S02E01.mkv",
      candidate: true, worth: true, save_bytes: 2 * GB, est_bytes: 2 * GB, needs: { video: true, subs: false, audio: false, save: 2 * GB, worth: true },
    };
    const plan = (excluded: boolean): ConvertPlan => ({
      key: episode.key, kind: "episode", title: episode.title, size_bytes: 4 * GB,
      needs: excluded ? { video: false, subs: false, audio: false, save: 0, worth: false, excluded: "title", why: "you chose not to convert this season" } : episode.needs,
      video: { from_codec: "H264", from_res: "1080p", from_hdr: "SDR", from_bits: 8, est_bytes: 2 * GB, ...(excluded ? {} : { to_codec: "HEVC", to_bits: 10, crf: 18 }) },
      audio: [], subs: [], container: { from: "MKV", to: "MKV" }, warnings: [], eta_sec: null,
    });
    const list: ConvertTitleExclusion[] = [];
    await installExtra(page, api, [
      ...exclusionRoutes(list),
      { method: "GET", path: "/api/v1/convert/library", respond: ({ url }) => url.searchParams.get("series") ? { items: [episode] }
        : { series: [{ series_id: 10, title: "Show", files: 1, convertible: 1, reencode: 1, tidy_only: 0, total_bytes: 4 * GB, est_bytes: 2 * GB, save_bytes: 2 * GB }] } },
      { method: "GET", path: "/api/v1/convert/plan", respond: () => plan(list.some((x) => x.season === 2)) },
    ]);
    await page.goto("/convert?tab=library&media=tv");
    await page.getByRole("button", { name: "Episodes" }).click();
    await page.getByRole("button", { name: "Show the conversion plan for Show - S02E01" }).click();
    await expect(page.getByRole("button", { name: "Don't convert this show" })).toBeVisible();
    await page.getByRole("button", { name: "Don't convert season 2" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/convert/exclusions").map((c) => c.body)).toEqual([{ kind: "series", series_id: 10, season: 2, excluded: true }]);
    await expect(page.getByText("Not converted: you chose not to convert this season")).toBeVisible();
    await expect(page.getByRole("button", { name: "Let Convert work on season 2" })).toBeVisible();
  });

  test("Settings → Convert leaves resolutions and library folders alone, and lists left-out titles", async ({ page, api }) => {
    await installExtra(page, api, [
      ...exclusionRoutes([{ kind: "movie", movie_id: 1, season: -1, title: "The Cartographer", created_at: "" }]),
      { method: "PUT", path: "/api/v1/convert/settings", respond: ({ body }) => ({ ...convertSettings, ...(body as object) }) },
    ]);
    await page.goto("/settings/convert");
    const res = page.getByRole("group", { name: "Resolutions to leave alone" });
    await expect(res.getByRole("checkbox", { name: "4K" })).not.toBeChecked();
    await res.getByRole("checkbox", { name: "4K" }).check();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/convert/settings").map((c) => c.body)).toEqual([{ skip_resolutions: ["2160p"] }]);
    await expect(res.getByRole("checkbox", { name: "4K" })).toBeChecked();

    const folders = page.getByRole("group", { name: "Library folders to leave alone" });
    await expect(folders.getByRole("checkbox")).toHaveCount(3);
    await folders.getByRole("checkbox", { name: "Leave Movies folder Kids alone" }).check();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/convert/settings").map((c) => c.body)).toEqual([{ skip_resolutions: ["2160p"] }, { skip_folders: ["movie:3"] }]);

    await expect(page.getByRole("link", { name: "The Cartographer" })).toBeVisible();
    await page.getByRole("button", { name: "Let Convert work on it" }).click();
    await expect.poll(() => api.callsTo("PUT", "/api/v1/convert/exclusions").map((c) => c.body)).toEqual([{ kind: "movie", movie_id: 1, excluded: false }]);
    await expect(page.getByText(/^None\. To leave a film or show alone/)).toBeVisible();
  });

  test("the Overview says how many files the rules leave alone", async ({ page, api }) => {
    await installExtra(page, api, [
      { method: "GET", path: "/api/v1/convert/stats", body: { ...convertStats, total: { ...convertStats.total, left_out: { title: 3, folder: 0, resolution: 9, save_bytes: 40 * GB } } } },
    ]);
    await page.goto("/convert");
    await expect(page.getByTestId("convert-left-out-summary")).toHaveText("12 files worth converting left alone by your choices: 3 by title, 9 by resolution · ~40.0 GB they'd free");
  });
});

// installExtra puts fixtures in front of the defaults for one test.
async function installExtra(page: import("@playwright/test").Page, api: MockedApi, extra: MockRoute[]) {
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
