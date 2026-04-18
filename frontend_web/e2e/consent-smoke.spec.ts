// frontend_web/e2e/consent-smoke.spec.ts
//
// Phase 3c Slice 6 — live-browser acceptance for ADR-0024 Decisions
// A and B. Runs in chromium + webkit (see playwright.config.ts) so
// the native <dialog> element and focus-trap behaviour are exercised
// in the real engines the app targets, not a jsdom approximation.

import { expect, test } from "@playwright/test";

test.describe("Audio processing consent — ADR-0024 Decision A", () => {
  // Each test starts from a clean localStorage so the consent gate
  // actually fires. The navigate-then-clear dance is necessary
  // because localStorage is origin-scoped and can't be cleared
  // before the first navigation.
  test.beforeEach(async ({ page }) => {
    await page.goto("/host");
    await page.evaluate(() => localStorage.clear());
    await page.goto("/host");
  });

  test("modal appears on first visit and Agree dismisses it", async ({
    page,
  }) => {
    const dialog = page.getByRole("dialog", {
      name: /audio processing consent/i,
    });
    await expect(dialog).toBeVisible();
    // ADR-0024 Decision A is an accept-only flow: no decline
    // button, no ESC-to-close (closable={false}).
    await expect(dialog.getByRole("button", { name: /agree/i })).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: /decline|cancel/i }),
    ).toHaveCount(0);

    await dialog.getByRole("button", { name: /agree/i }).click();
    await expect(dialog).toBeHidden();
  });

  test("accepted consent persists across reloads", async ({ page }) => {
    const dialog = page.getByRole("dialog", {
      name: /audio processing consent/i,
    });
    await dialog.getByRole("button", { name: /agree/i }).click();
    await expect(dialog).toBeHidden();

    await page.reload();
    // The gate reads localStorage synchronously in `useState`'s
    // initializer so the dialog must never be visible, not even for
    // one paint.
    await expect(dialog).toBeHidden();
  });
});

test.describe("Transcript panel opt-in — ADR-0024 Decision B", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/host");
    await page.evaluate(() => localStorage.clear());
    await page.goto("/host");
    // Dismiss the audio gate so the meeting form is reachable.
    await page
      .getByRole("dialog", { name: /audio processing consent/i })
      .getByRole("button", { name: /agree/i })
      .click();
  });

  test("transcript checkbox defaults OFF and reveals the GDPR notice when turned on", async ({
    page,
  }) => {
    const checkbox = page.getByRole("checkbox", {
      name: /show live transcript/i,
    });
    await expect(checkbox).toBeVisible();
    await expect(checkbox).not.toBeChecked();

    // GDPR notice should only appear after opt-in.
    await expect(page.getByText(/GDPR Art\. 6\(1\)\(f\)/i)).toHaveCount(0);
    await checkbox.check();
    await expect(page.getByText(/GDPR Art\. 6\(1\)\(f\)/i)).toBeVisible();
  });
});
