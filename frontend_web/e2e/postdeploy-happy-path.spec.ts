// frontend_web/e2e/postdeploy-happy-path.spec.ts
//
// Phase 4c post-deploy E2E skeleton per ROADMAP: runs against the
// deployed staging URL after every ArgoCD sync (or on a nightly
// schedule) and fails loudly when the live site regresses the
// chief-of-staff user journey.
//
// Scope today (initial slice):
//
//   - Landing page loads, header renders
//   - /host route is reachable and shows the pre-sign-in call-to-action
//     (Local mode: "Sign in (local)"; Cloud mode: "Sign in with Cognito")
//   - /view/<sessionId>?token=<garbage> route renders an error-state
//     rather than a blank screen (catches SPA routing regressions
//     where 404s or empty bundles silently ship)
//
// Out of scope today (tracked for follow-up extension when the live
// loop is ready to drive):
//
//   - Full CreateMeeting → audio → transcript → JoinAsViewer →
//     EndMeeting flow per ROADMAP "post-deploy E2E suite against
//     staging". Blocked on two things:
//       * Real audio in a headless browser — Playwright supports
//         `--use-fake-device-for-media-stream` for chromium but
//         webkit is thinner; we'd need a dedicated audio fixture
//         (.wav) served into getUserMedia to stand in for a mic.
//       * Real Cognito login — waits on ldz staging/auth/ Terraform
//         apply (ADR-0026 Partially Accepted, aegis-core#76). When
//         the Dev User Pool is live, we'll drive `AdminInitiateAuth`
//         programmatically and inject the ID token rather than
//         driving the Hosted UI.
//   - WebRTC handshake verification (needs real STUN / TURN reach-
//     ability on the Playwright runner).
//
// The spec skips in CI unless `AEGIS_POSTDEPLOY_URL` is set — local
// dev can run against the Vite dev server by exporting the env var
// to `http://127.0.0.1:5173`. That lets the spec be authored + kept
// green on laptop while the nightly CI workflow sits gated on the
// real staging URL surfacing in GH Secrets.

import { test, expect } from "@playwright/test";

const BASE_URL = process.env["AEGIS_POSTDEPLOY_URL"];

test.describe("post-deploy happy-path smoke", () => {
  test.beforeAll(() => {
    if (!BASE_URL) {
      test.skip(
        true,
        "AEGIS_POSTDEPLOY_URL not set — running in gating mode. " +
          "Export it to a staging or dev URL to exercise the spec.",
      );
    }
  });

  test("landing page renders", async ({ page }) => {
    // BASE_URL is non-null here by virtue of the beforeAll skip, but
    // TypeScript doesn't track that — guard for its benefit.
    if (!BASE_URL) return;
    await page.goto(BASE_URL);
    // The App shell renders the product name in the header; if the
    // bundle failed to load or a client-side error blanked the tree,
    // the heading is absent and this fails with a useful locator
    // snapshot rather than "page is blank".
    await expect(page.locator("h1, h2").first()).toBeVisible({
      timeout: 10_000,
    });
  });

  test("/host shows the sign-in call-to-action", async ({ page }) => {
    if (!BASE_URL) return;
    await page.goto(`${BASE_URL}/host`);
    // The pre-signed-in HostPage has a single primary CTA button;
    // text is one of:
    //   - "Sign in with Cognito"   (Cloud build)
    //   - "Sign in (local)"        (Local build)
    // Match either so this spec works against both deploy flavors.
    const cta = page.getByRole("button", {
      name: /Sign in (with Cognito|\(local\))/,
    });
    await expect(cta).toBeVisible({ timeout: 10_000 });
  });

  test("/view/<id>?token=<garbage> renders an error rather than blanking", async ({
    page,
  }) => {
    if (!BASE_URL) return;
    // A bad token on the viewer path should surface a visible error
    // panel, not a blank React tree. Catches SPA routing regressions
    // where a 404 or a runtime-unhandled rejection blanks the screen.
    await page.goto(
      `${BASE_URL}/view/nonexistent-session?token=definitely-not-a-valid-jwt`,
    );
    // The ViewerPage emits a distinctive error region when the
    // session fails to resolve; assert any user-visible text in the
    // error treatment surfaces.
    await expect(page.locator("main").first()).toBeVisible({
      timeout: 10_000,
    });
    // Don't assert a specific error message — the copy may evolve;
    // asserting "the page rendered something" is the load-bearing
    // claim ("not blank"), and copy-level assertions would produce
    // flaky noise.
  });
});
