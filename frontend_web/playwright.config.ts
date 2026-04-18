// frontend_web/playwright.config.ts
//
// Phase 3c Slice 6 — live-browser smoke harness.
// Motivated by Incident 09 (Phase 3): loopback / fixture-based tests
// silently passed while every real-browser Opus frame failed to
// decode. Playwright runs the actual host page in chromium + webkit
// so UI-level regressions (missing <dialog> support, focus trap,
// ADR-0024 consent-modal appearance) cannot hide behind unit tests.
//
// Browsers are kept repo-local per CLAUDE.md Rule 6 (hermetic
// toolchains): set `PLAYWRIGHT_BROWSERS_PATH` to a repo-scoped
// directory before running tests. The `frontend.sh e2e` wrapper
// does that automatically.

import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  // Single worker keeps the dev server logs readable during a
  // failure and avoids port races on the shared 5173.
  workers: 1,
  fullyParallel: false,
  // Fail the CI run if a test accidentally ships with .only().
  forbidOnly: !!process.env["CI"],
  retries: process.env["CI"] ? 1 : 0,
  reporter: process.env["CI"] ? "github" : "list",
  use: {
    baseURL: "http://127.0.0.1:5173",
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
    // WebKit is the critical cross-WebView target per ADR-0002
    // Constraint 4 (Safari / WKWebView behaviour). Chromium alone
    // would let WKWebView-specific regressions hide.
    {
      name: "webkit",
      use: { ...devices["Desktop Safari"] },
    },
  ],
  // Spin up Vite in local deploy mode (dummy principal, no Cognito
  // redirect) for the duration of the test run. Playwright waits
  // on the URL before dispatching tests.
  //
  // We invoke Vite via `node node_modules/vite/bin/vite.js` rather
  // than `pnpm exec vite` because Playwright's webServer runs in a
  // plain `/bin/sh` subshell: the hermetic pnpm from the frontend.sh
  // wrapper is reached via an absolute path, so `pnpm` itself is
  // not on PATH. Node IS on PATH (the wrapper prepends it), which
  // is enough to reach the locally-installed vite.
  webServer: {
    command:
      "node node_modules/vite/bin/vite.js --host 127.0.0.1 --port 5173 --strictPort",
    url: "http://127.0.0.1:5173",
    timeout: 60_000,
    reuseExistingServer: !process.env["CI"],
    env: {
      VITE_AEGIS_DEPLOY_MODE: "local",
    },
  },
});
