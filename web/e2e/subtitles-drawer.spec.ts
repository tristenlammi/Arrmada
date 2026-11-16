import { test, expect, type MockedApi } from "./mockApi";
import type { Page } from "@playwright/test";
import type { MockRoute } from "./fixtures/routes";
import type { Movie, SubFileEntry } from "../src/lib/api";
import * as media from "./fixtures/media";

// SUB-27 / SUB-29: the per-file subtitle drawer — opened from the Movie page's subtitle
// line, a Library row and a Series episode chip — with previews, timing nudges and
// Delete to the recycle bin, usable at 375 px.

const SHOTS = process.env.E2E_SHOTS; // a folder: save screenshots for a look by eye

async function shot(page: Page, name: string) {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: false });
}

async function fitsScreen(page: Page) {
  const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
  expect(w.doc, "document width").toBeLessThanOrEqual(w.viewport);
}

// The Cartographer (movie 1) with its file on disk.
const movieWithFile: MockRoute = {
  method: "GET", path: "/api/v1/movies/1",
  respond: () => ({
    ...media.movieDetail(1),
    file: {
      path: "/media/Fixture Film (2020)/Fixture Film (2020).mkv", filename: "Fixture Film (2020).mkv", size_bytes: 8e9, quality: "Bluray-1080p",
      subtitles: ["Fixture Film (2020).en.srt"], orphan_subtitles: ["Old.Name.eng.srt"], missing: false,
    },
  } as Movie),
};
const shift: MockRoute = { method: "POST", path: "/api/v1/subtitles/file/shift", respond: ({ body }) => ({ name: (body as { name: string }).name, lang: "en", ext: ".srt", size: 1, modified_at: 0, cue_count: 812, first_cues: [], shiftable: true, owned: true, source: "opensubtitles" }) };
const syncFix: MockRoute = { method: "POST", path: "/api/v1/subtitles/sync-fix", body: { status: "fixing", file: "Fixture Film (2020).en.srt" } };
const remove: MockRoute = { method: "DELETE", path: "/api/v1/subtitles/file", respond: ({ url }) => ({ removed: url.searchParams.get("name") }) };

for (const scheme of ["dark", "light"] as const) {
  test.describe(`movie page (${scheme})`, () => {
    test.use({ persona: "admin", colorScheme: scheme });

    test("chips, then the drawer: preview, nudge + apply, delete to the bin", async ({ page, api, isMobile }) => {
      await installExtra(page, api, [movieWithFile, shift, remove, syncFix]);
      await page.goto("/movies/1");
      await expect(page.getByRole("button", { name: "✓ en · OpenSubtitles · Health 92" })).toBeVisible();
      await expect(page.getByRole("button", { name: /es missing · next try in 6 days/ })).toBeVisible();
      await page.getByRole("button", { name: "Find subtitles" }).click();

      const drawer = page.getByRole("dialog", { name: /Subtitles — Fixture Film/ });
      await expect(drawer.getByText("Where were you last night?")).toBeVisible();
      await expect(drawer.getByText("Health 92", { exact: true })).toBeVisible();
      await expect(drawer.getByText("1:01.200")).toBeVisible();
      await fitsScreen(page);
      await shot(page, `drawer-movie-${isMobile ? "phone" : "desktop"}-${scheme}`);

      await drawer.getByRole("button", { name: "Move +1 s" }).click();
      await expect(drawer.getByText("1:02.200")).toBeVisible();
      await drawer.getByRole("button", { name: "Apply +1 s" }).click();
      await expect.poll(() => api.callsTo("POST", "/api/v1/subtitles/file/shift").length).toBe(1);
      expect(api.callsTo("POST", "/api/v1/subtitles/file/shift")[0].body).toEqual({ kind: "movie", movie_id: 1, name: "Fixture Film (2020).en.srt", offset_ms: 1000 });

      // A tenth of a second at a time, for an offset the half-second steps can't reach.
      await drawer.getByRole("button", { name: "Move −0.1 s" }).click();
      await drawer.getByRole("button", { name: "Move −0.1 s" }).click();
      await expect(drawer.getByRole("button", { name: "Apply −0.2 s" })).toBeVisible();
      await drawer.getByRole("button", { name: "Reset" }).click();
      // Fix sync measures it and moves it; the request names the file by its title and language.
      await drawer.getByRole("button", { name: "Fix sync" }).first().click();
      await expect.poll(() => api.callsTo("POST", "/api/v1/subtitles/sync-fix").map((c) => c.body)).toEqual([{ kind: "movie", movie_id: 1, lang: "en", variant: "" }]);

      await drawer.getByRole("button", { name: "Delete…" }).first().click();
      const confirm = page.getByRole("dialog", { name: /Delete Fixture Film \(2020\)\.en\.srt\?/ });
      await expect(confirm).toContainText("recycle bin");
      await shot(page, `drawer-delete-${isMobile ? "phone" : "desktop"}-${scheme}`);
      await confirm.getByRole("button", { name: "Move to recycle bin" }).click();
      await expect.poll(() => api.callsTo("DELETE", "/api/v1/subtitles/file").length).toBe(1);
      expect(api.callsTo("DELETE", "/api/v1/subtitles/file")[0].path).toContain("name=Fixture+Film+%282020%29.en.srt");
    });
  });
}

// SUB-28: the drawer's OpenSubtitles search and Use this; Generate with AI.
const search: MockRoute = {
  method: "POST", path: "/api/v1/subtitles/search",
  respond: () => ({
    lang: "es", min_score: 60, can_download: true, quota_remaining: 18, quota_reset_at: 0, provider_state: "ok", searched_at: 1,
    candidates: [
      { file_id: "501", language: "es", release: "Fixture.Film.2020.1080p.BluRay.x264-GRP", file_name: "Fixture.Film.2020.1080p.BluRay.x264-GRP.spa.srt", downloads: 4210, hearing_impaired: false, hash_match: true, score: 100, reasons: ["+100 hash match"] },
      { file_id: "502", language: "es", release: "Fixture.Film.2020.720p.WEBRip-OTHER", downloads: 15_002, hearing_impaired: true, hash_match: false, fps: 25, score: 12, reasons: ["−15 WEBRip vs BluRay", "−40 25 vs 23.976 fps"] },
      { file_id: "503", language: "es", release: "Fixture.Film.2020.BluRay-GRP", downloads: 80, hearing_impaired: false, hash_match: false, score: 55, reasons: ["+25 same group (GRP)", "+20 same source (BluRay)"], blocklisted: true },
    ],
  }),
};
const redo: MockRoute = { method: "POST", path: "/api/v1/subtitles/redo", status: 202, respond: ({ body }) => ({ id: 77, kind: "movie", movie_id: 1, title: "Fixture Film", state: "queued", at: 0, priority: 1, mode: (body as { mode: string }).mode }) };

test.describe("search and pick", () => {
  test.use({ persona: "admin" });

  test("search one language, read the scores, use one; generate with AI", async ({ page, api, isMobile }) => {
    await installExtra(page, api, [movieWithFile, search, redo]);
    await page.goto("/movies/1");
    await page.getByRole("button", { name: "Find subtitles" }).click();
    const drawer = page.getByRole("dialog", { name: /Subtitles — Fixture Film/ });
    await expect(drawer.getByText("Where were you last night?")).toBeVisible();

    await drawer.getByRole("button", { name: "Search OpenSubtitles" }).nth(1).click(); // es
    const results = drawer.getByRole("region", { name: /OpenSubtitles results for/ });
    await expect(results.getByText("Fixture.Film.2020.1080p.BluRay.x264-GRP", { exact: true })).toBeVisible();
    await expect(results.getByText("18 downloads left today. Searching is free.")).toBeVisible();
    await expect(results.getByText("blocked for this file")).toBeVisible();
    await expect(results.getByRole("button", { name: "Use this" }).nth(2)).toBeDisabled();
    await fitsScreen(page);
    await shot(page, `drawer-search-${isMobile ? "phone" : "desktop"}`);
    expect(api.callsTo("POST", "/api/v1/subtitles/search")[0].body).toEqual({ kind: "movie", movie_id: 1, lang: "es", refresh: false });

    await results.getByRole("button", { name: "Use this" }).first().click();
    await page.getByRole("dialog", { name: /Use Fixture\.Film/ }).getByRole("button", { name: "Use this" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/subtitles/redo").length).toBe(1);
    expect(api.callsTo("POST", "/api/v1/subtitles/redo")[0].body).toEqual({ kind: "movie", movie_id: 1, lang: "es", mode: "pick", file_id: "501" });

    await drawer.getByRole("button", { name: "Generate with AI" }).first().click();
    await page.getByRole("dialog", { name: /with AI\?/ }).getByRole("button", { name: "Queue it" }).click();
    await expect.poll(() => api.callsTo("POST", "/api/v1/subtitles/redo").length).toBe(2);
    expect(api.callsTo("POST", "/api/v1/subtitles/redo")[1].body).toEqual({ kind: "movie", movie_id: 1, lang: "en", mode: "ai" });
  });
});

test.describe("Subtitles Library", () => {
  test.use({ persona: "admin" });

  test("a row's title opens its drawer", async ({ page, api }) => {
    const items: SubFileEntry[] = [{
      kind: "movie", movie_id: 1, title: "Fixture Film", year: 2020, path: "/media/Fixture Film (2020)/Fixture Film (2020).mkv",
      embedded: [], external: ["en"], languages: [{ lang: "en", have: true }], missing: 0,
    }];
    await installExtra(page, api, [{ method: "GET", path: "/api/v1/subtitles/library", body: { items, scanning: false, stale: false } }]);
    await page.goto("/subtitles?tab=library");
    await page.getByRole("button", { name: /Fixture Film/ }).click();
    await expect(page.getByRole("dialog", { name: /Subtitles — Fixture Film/ }).getByText("Where were you last night?")).toBeVisible();
    expect(api.callsTo("GET", "/api/v1/subtitles/file")[0].path).toBe("/api/v1/subtitles/file?kind=movie&id=1");
  });
});

// Season 1 of Harbour Lights: E01 has both kept languages, E02 is missing Spanish.
function episode(n: number, missing: boolean): SubFileEntry {
  return {
    kind: "episode", series_id: 1, season: 1, episode: n, title: `Harbour Lights - S01E0${n}`, path: `/tv/x/S01E0${n}.mkv`,
    embedded: [], external: missing ? ["en"] : ["en", "es"], missing: missing ? 1 : 0,
    languages: [{ lang: "en", have: true }, missing ? { lang: "es", have: false, reason: "no_match", next_retry_at: 2_000_000_000 } : { lang: "es", have: true }],
  };
}
const episodes: MockRoute = { method: "GET", path: "/api/v1/subtitles/library", respond: ({ url }) => ({ items: url.searchParams.get("series") === "1" ? [episode(1, false), episode(2, true)] : [] }) };

test.describe("series page", () => {
  test.use({ persona: "admin" });

  test("episode chips load with the cards and open the episode's drawer", async ({ page, api, isMobile }) => {
    await installExtra(page, api, [episodes]);
    // Another tab first: the show's files aren't probed until the episode cards are seen.
    await page.goto("/series/1?tab=files");
    await api.quiet();
    expect(api.callsTo("GET", "/api/v1/subtitles/library")).toHaveLength(0);

    await page.goto("/series/1?season=1");
    const e2 = page.locator('[data-episode="S01E02"]');
    // The chips load once the cards are seen: on a phone they start below the fold.
    await e2.scrollIntoViewIfNeeded();
    await expect(e2.getByRole("button", { name: "subs · 1 missing" })).toBeVisible();
    await expect(page.locator('[data-episode="S01E01"]').getByRole("button", { name: "subs ✓" })).toBeVisible();
    await e2.getByRole("button", { name: "subs · 1 missing" }).click();
    const drawer = page.getByRole("dialog", { name: /Subtitles — Show - S01E01/ });
    await expect(drawer.getByText("Where were you last night?")).toBeVisible();
    await fitsScreen(page);
    await shot(page, `drawer-episode-${isMobile ? "phone" : "desktop"}`);
    expect(api.callsTo("GET", "/api/v1/subtitles/file").slice(-1)[0]?.path).toBe("/api/v1/subtitles/file?kind=episode&series=1&season=1&episode=2");
  });
});

// installExtra puts routes ahead of the default table (a later page.route runs first),
// recording their calls like the rest.
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
