import { defineConfig, devices } from "@playwright/test";

// Browser smoke tests for the web UI (FE-10). They run against the production build
// served by `vite preview`, with every /api/v1 call answered from typed fixtures
// (e2e/mockApi.ts), so no Go backend, database or network is needed.
//
// `npm run e2e` builds first so it never tests a stale bundle. CI's web job has just
// built, so it sets E2E_NO_BUILD=1 to skip a second build.
const PORT = 4179;
const preview = `npx vite preview --port ${PORT} --strictPort`;

// e2e/screens.spec.ts (before/after screenshots of every page) runs only on demand,
// when SCREENS_DIR names a folder outside the repo for its snapshots.
const SCREENS_DIR = process.env.SCREENS_DIR;
const screens = SCREENS_DIR
  ? [
      { name: "screens-1440", testMatch: /screens\.spec\.ts/, use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } },
      { name: "screens-375", testMatch: /screens\.spec\.ts/, use: { ...devices["Pixel 7"], viewport: { width: 375, height: 812 } } },
    ]
  : [];

// e2e/readme-shots.spec.ts (the README and website pictures in docs/images) runs only on
// demand, with README_SHOTS=1.
const readmeShots = process.env.README_SHOTS
  ? [
      { name: "readme-shots", testMatch: /readme-shots\.spec\.ts/, use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 } },
      { name: "readme-shots-phone", testMatch: /readme-shots\.spec\.ts/, use: { ...devices["Pixel 7"], viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 } },
    ]
  : [];

export default defineConfig({
  testDir: "./e2e",
  ...(SCREENS_DIR ? { snapshotPathTemplate: `${SCREENS_DIR}/{arg}{ext}` } : {}),
  // Every request is mocked, so a test that runs long is stuck, not slow.
  timeout: 20_000,
  expect: { timeout: 5_000 },
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  // On CI the "github" reporter turns each failure into an annotation on the run, which
  // can be read without signing in (the job log can't).
  reporter: process.env.CI ? [["list"], ["github"], ["html", { open: "never" }]] : [["list"]],
  use: {
    baseURL: `http://localhost:${PORT}`,
    // The app registers /sw.js; a service worker would answer fetches itself and
    // page.route would never see them.
    serviceWorkers: "block",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "desktop",
      testMatch: /(admin-smoke|media-servers|quality-editor|discover-split|activity|activity-bulk|blocklist-bulk|activity-stalled|wanted|movies-list|movie-versions|movie-other-files|movie-delete-folder|movies-scan-add|library|library-state|requests|needs-you|review-import|alerts|seasons|users-autoapprove|insights|insights-servers|discover-titles|insights-import|plex|discover-depth|live-updates|account-menu|connections|settings-autosave|disk-guard-measure|module-settings|people|system-status|series-detail|convert-plan|convert|convert-hold|issues-staff|subtitles-library|subtitles-queue|subtitles-drawer|subtitles-profile-picker|book-duplicates|books-library|book-kindle-staff|music-library|music-artwork|music-naming|music-editions|music-matcher|music-search|first-run|watch-links|jellyfin-signin|media-server-settings|setup-media-server|version-badge|usenet|library-folders|arr-import|book-naming|book-edit|two-factor|api-keys|palette|display-titles|kobo-kepub|request-profile|library-order|series-searching-off|library-cast|series-naming|manual-import-picker)\.spec\.ts/,
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
    },
    {
      // A phone: touch only, so (hover: hover) is false and hover-revealed controls
      // must not exist. Chromium on purpose (Pixel, not iPhone) to keep CI to one browser.
      name: "phone",
      testMatch: /(requester-mobile|activity-mobile|activity-bulk|blocklist-bulk|movies-mobile|movie-other-files|movie-delete-folder|admin-mobile|series-phone|series-searching-off|book-formats|seasons|insights-mobile|phone-shell|me-page|calendar|audiobook-places|discover-depth-mobile|audiobook-player|connections-mobile|people-mobile|series-detail|issues-report|subtitles-queue|subtitles-drawer|subtitles-profile-picker|my-shelf|book-authors|books-phone|quality-formats|jellyfin-signin|setup-media-server|version-badge|usenet-mobile|palette-phone|request-profile|library-order-phone|library-cast|manual-import-picker|music-artwork)\.spec\.ts/,
      use: { ...devices["Pixel 7"], viewport: { width: 375, height: 812 } },
    },
    // The colour-contrast audit (e2e/contrast.spec.ts): every page in both themes at both
    // widths, checked against WCAG AA.
    { name: "contrast-1440", testMatch: /contrast\.spec\.ts/, use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } },
    { name: "contrast-375", testMatch: /contrast\.spec\.ts/, use: { ...devices["Pixel 7"], viewport: { width: 375, height: 812 } } },
    ...screens,
    ...readmeShots,
  ],
  webServer: {
    command: process.env.E2E_NO_BUILD ? preview : `npm run build && ${preview}`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
