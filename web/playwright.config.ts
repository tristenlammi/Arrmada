import { defineConfig, devices } from "@playwright/test";

// Browser smoke tests for the web UI (FE-10). They run against the production build
// served by `vite preview`, with every /api/v1 call answered from typed fixtures
// (e2e/mockApi.ts), so no Go backend, database or network is needed.
//
// `npm run e2e` builds first so it never tests a stale bundle. CI's web job has just
// built, so it sets E2E_NO_BUILD=1 to skip a second build.
const PORT = 4179;
const preview = `npx vite preview --port ${PORT} --strictPort`;

export default defineConfig({
  testDir: "./e2e",
  // Every request is mocked, so a test that runs long is stuck, not slow.
  timeout: 20_000,
  expect: { timeout: 5_000 },
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : [["list"]],
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
      testMatch: /(admin-smoke|wanted)\.spec\.ts/,
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
    },
    {
      // A phone: touch only, so (hover: hover) is false and hover-revealed controls
      // must not exist. Chromium on purpose (Pixel, not iPhone) to keep CI to one browser.
      name: "phone",
      testMatch: /requester-mobile\.spec\.ts/,
      use: { ...devices["Pixel 7"], viewport: { width: 375, height: 812 } },
    },
  ],
  webServer: {
    command: process.env.E2E_NO_BUILD ? preview : `npm run build && ${preview}`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
