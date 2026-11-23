import type { Page } from "@playwright/test";
import { test, expect } from "./mockApi";
import type { ConvertCandidate, ConvertPlan, Movie, MovieFile, RankedRelease, SmallerList } from "../src/lib/api";

// "Find a smaller release": for a film Convert would re-encode, the plan card (and the
// movie page, by its file) opens the release search narrowed to sensible smaller
// replacements. Each says why it qualifies and what it gives up; the rest are listed with
// why they were left out; Grab is an ordinary grab marked replace_smaller.

const GB = 1024 ** 3;
const ID = 7;

const candidate: ConvertCandidate = {
  kind: "movie", key: `movie:${ID}`, movie_id: ID, title: "Heat", year: 1995,
  path: "/movies/Heat (1995)/Heat (1995) - 1080p BluRay.mkv", candidate: true, worth: true, save_bytes: 11 * GB, est_bytes: 9 * GB,
  needs: { video: true, subs: false, audio: false, save: 11 * GB, worth: true },
  info: { container: "MKV", video_codec: "h264", width: 1920, height: 800, resolution: "1080p", hdr: "", bitrate_kbps: 16800, frame_rate: 23.976,
    duration_sec: 10200, size_bytes: 20 * GB, audio_tracks: 1, sub_tracks: 1, ten_bit: false },
};

const plan: ConvertPlan = {
  key: `movie:${ID}`, kind: "movie", title: "Heat", size_bytes: 20 * GB, needs: candidate.needs,
  video: { from_codec: "H.264", from_res: "1080p", from_hdr: "", from_bits: 8, to_codec: "HEVC", to_bits: 10, crf: 20, est_bytes: 9 * GB },
  audio: [{ type: "audio", index: 0, codec: "dts", lang: "eng", channels: 6, keep: true }],
  subs: [{ type: "subtitle", index: 0, codec: "subrip", lang: "eng", keep: true }],
  container: { from: "MKV", to: "MKV" }, warnings: [], eta_sec: null,
};

const good: RankedRelease = {
  title: "Heat.1995.1080p.BluRay.x265-GOOD", indexer: "Fake", token: "tok-good", size_gb: 6, bitrate_mbps: 5.1, seeders: 40, peers: 50,
  summary: "1080p · BluRay", eligible: true, recommended: false,
  replace: { saves_gb: 14, saves_pct: 70, bitrate_mbps: 5.1, why: ["Saves 14.0 GB (70%)", "HEVC at 5.1 Mb/s"], loses: ["Lower bitrate: 5.1 Mb/s, your file 16.8 Mb/s"] },
};

const smaller: SmallerList = {
  profile: "custom:1", releases: [good], searched: 1,
  current: { release: "Heat.1995.1080p.BluRay.x264-GRP", size_gb: 20, bitrate_mbps: 16.8, resolution: "1080p", codec: "x264", hdr: "SDR",
    runtime_min: 170, floor_mbps: 3, floor_from_profile: false },
  notes: ["When the new file is imported, this one goes to the recycle bin, as an upgrade's old file does."],
  not_suitable: [{ title: "Heat.1995.1080p.BluRay.x265-TINY", summary: "1080p · BluRay", size_gb: 2, indexer: "Fake", why: "1.7 Mb/s is under the 3.0 Mb/s floor — likely a starved encode" }],
};

interface Seen { method: string; path: string; body: unknown }

// serve answers the Convert library, the plan, the smaller-release search and the grab.
async function serve(page: Page, list: SmallerList = smaller): Promise<Seen[]> {
  const seen: Seen[] = [];
  const json = (body: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  await page.route((u) => ["/api/v1/convert/library", "/api/v1/convert/plan", "/api/v1/grab", `/api/v1/movies/${ID}/smaller-releases`].includes(u.pathname), (route) => {
    const req = route.request();
    const url = new URL(req.url());
    let body: unknown;
    try { body = req.postDataJSON(); } catch { body = undefined; }
    seen.push({ method: req.method(), path: url.pathname + url.search, body });
    switch (url.pathname) {
      case "/api/v1/convert/library": return route.fulfill(json({ items: [candidate] }));
      case "/api/v1/convert/plan": return route.fulfill(json(plan));
      case "/api/v1/grab": return route.fulfill(json({ status: "grabbed", title: good.title }));
    }
    return route.fulfill(json(list));
  });
  return seen;
}

test.describe("plan card", () => {
  test.use({ persona: "admin" });

  test("finds a smaller release, says why it qualifies, and grabs it marked as a replacement", async ({ page }) => {
    const seen = await serve(page);
    await page.goto("/convert?tab=library");
    await page.getByRole("button", { name: "Show the conversion plan for Heat" }).click();
    await page.getByRole("button", { name: "Find a smaller release" }).click();

    const dialog = page.getByRole("dialog", { name: "Find a smaller release — Heat" });
    await expect(dialog).toBeVisible();
    expect(seen.map((s) => s.path)).toContain(`/api/v1/movies/${ID}/smaller-releases?version=0`);
    const current = dialog.getByTestId("smaller-current");
    await expect(current).toContainText("Your file: 20.0 GB · 16.8 Mb/s · 1080p · X264");
    await expect(current).toContainText("stay above 3.0 Mb/s (the built-in floor");
    await expect(current).toContainText("Converting it is expected to save about");
    await expect(current).toContainText("goes to the recycle bin");

    const fit = dialog.getByTestId("replace-fit");
    await expect(fit).toContainText("✓ Saves 14.0 GB (70%)");
    await expect(fit).toContainText("− Lower bitrate: 5.1 Mb/s, your file 16.8 Mb/s");

    await dialog.getByRole("button", { name: "Show the 1 left out" }).click();
    await expect(dialog.getByTestId("not-suitable")).toContainText("under the 3.0 Mb/s floor");

    await dialog.getByRole("button", { name: "Grab" }).click();
    await expect(dialog.getByRole("button", { name: "Grabbed ✓" })).toBeVisible();
    expect(seen.filter((s) => s.method === "POST").map((s) => s.body)).toEqual([{ token: "tok-good", movie_id: ID, replace_smaller: true }]);
  });

  test("says so when nothing found would be a sensible replacement", async ({ page }) => {
    await serve(page, { ...smaller, releases: [] });
    await page.goto("/convert?tab=library");
    await page.getByRole("button", { name: "Show the conversion plan for Heat" }).click();
    await page.getByRole("button", { name: "Find a smaller release" }).click();
    await expect(page.getByText("None of the 1 release found would be a sensible replacement — Convert is the way to shrink this one.")).toBeVisible();
  });

  test("isn't offered for a file Convert would only tidy", async ({ page }) => {
    await page.route((u) => u.pathname === "/api/v1/convert/library" || u.pathname === "/api/v1/convert/plan", (route) => {
      const tidy = { ...candidate.needs, video: false, subs: true };
      const body = new URL(route.request().url()).pathname.endsWith("plan") ? { ...plan, needs: tidy } : { items: [{ ...candidate, needs: tidy }] };
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    });
    await page.goto("/convert?tab=library");
    await page.getByRole("button", { name: "Show the conversion plan for Heat" }).click();
    await expect(page.getByText("Conversion plan", { exact: true })).toBeVisible();
    await expect(page.getByText("Subtitles · 1 kept")).toBeVisible();
    await expect(page.getByRole("button", { name: "Find a smaller release" })).toHaveCount(0);
  });
});

// The movie page offers the same search by the film's file.
const file: MovieFile = {
  path: candidate.path, filename: "Heat (1995) - 1080p BluRay.mkv", size_bytes: 20 * GB, quality: "1080p BluRay", codec: "x264", probed: true, missing: false,
};
const movie: Movie = {
  id: ID, tmdb_id: 949, title: "Heat", year: 1995, monitored: true, quality_profile: "hd-1080p", min_availability: "released",
  has_file: true, movie_file_path: file.path, file,
  versions: [{ id: 0, is_default: true, label: "Default", quality_profile: "hd-1080p", monitored: true, has_file: true, file_path: file.path, file }],
};

test.describe("movie page", () => {
  test.use({ persona: "manager" });

  test("the file's Find a smaller release opens the same search", async ({ page, api }) => {
    const seen: Seen[] = [];
    const json = (body: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
    await page.route((u) => u.pathname.startsWith(`/api/v1/movies/${ID}`) || u.pathname === "/api/v1/recycle/mode" || u.pathname === "/api/v1/grab", (route) => {
      const req = route.request();
      const url = new URL(req.url());
      let body: unknown;
      try { body = req.postDataJSON(); } catch { body = undefined; }
      seen.push({ method: req.method(), path: url.pathname + url.search, body });
      const p = url.pathname.replace(`/api/v1/movies/${ID}`, "");
      if (url.pathname === "/api/v1/recycle/mode") return route.fulfill(json({ enabled: true, dirs: [], retention_days: 30, max_gb: 0 }));
      if (url.pathname === "/api/v1/grab") return route.fulfill(json({ status: "grabbed", title: good.title }));
      if (p === "" && req.method() === "GET") return route.fulfill(json(movie));
      if (p === "/smaller-releases") return route.fulfill(json(smaller));
      if (p === "/history") return route.fulfill(json({ events: [] }));
      if (p === "/blocklist") return route.fulfill(json({ blocklist: [] }));
      if (p === "/watch-stats") return route.fulfill(json({ plays: 0, watchers: [] }));
      if (p === "/collection") return route.fulfill(json({ name: "", members: [] }));
      return route.fulfill(json({ message: `e2e convert-find-smaller: no answer for ${req.method()} ${url.pathname}` }, 501));
    });
    await page.goto(`/movies/${ID}`);
    await api.quiet();
    await page.getByRole("button", { name: "Find a smaller release" }).click();
    const dialog = page.getByRole("dialog", { name: "Find a smaller release — Heat" });
    await expect(dialog.getByTestId("replace-fit")).toContainText("HEVC at 5.1 Mb/s");
    await dialog.getByRole("button", { name: "Grab" }).click();
    await expect.poll(() => seen.filter((s) => s.method === "POST").map((s) => s.body)).toEqual([{ token: "tok-good", movie_id: ID, replace_smaller: true }]);
    expect(seen.map((s) => s.path)).toContain(`/api/v1/movies/${ID}/smaller-releases?version=0`);

    // The page is no wider than the screen with the search open.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });
});
