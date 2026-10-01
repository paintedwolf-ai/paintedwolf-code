import { expect, type Page } from "@playwright/test";

import { waitHarnessConnected, webE2e } from "./helpers.ts";

/** Sidebar and main-pane dividers align after layout spacing accumulates. */
async function homeDividerDrift(page: Page): Promise<number | null> {
  return page.evaluate(() => {
    const search = document.querySelector<HTMLElement>(".home-nav__search");
    const doorsLine = document.querySelector<HTMLElement>(".home-view__doors-line");
    if (!search || !doorsLine) return null;
    const searchBottom = search.getBoundingClientRect().bottom;
    const after = getComputedStyle(search, "::after");
    // CSS `bottom: B` => element bottom edge Y = container_bottom_Y - B.
    const lineBottom = searchBottom - parseFloat(after.bottom);
    const hairlineCenter = lineBottom - parseFloat(after.height) / 2;
    const doors = doorsLine.getBoundingClientRect();
    const doorsCenter = doors.top + doors.height / 2;
    return doorsCenter - hairlineCenter;
  });
}

async function expectHomeDividerAligned(page: Page): Promise<void> {
  // Whole-pixel layout: exact alignment is achievable, so hold a tight bound.
  await expect.poll(async () => {
    const drift = await homeDividerDrift(page);
    return drift === null ? Infinity : Math.abs(drift);
  }, { message: "search hairline and doors divider must settle on the same pixel" }).toBeLessThanOrEqual(0.5);
}

webE2e.beforeEach(async ({ page }) => {
  // The invariant covers idle Home. Attention cards precede new work and can be
  // left by unrelated tests on the shared harness host.
  await page.route("**/v1/attention", (route) => route.fulfill({ json: { rows: [] } }));
  // Readiness notices also precede the composer. Their layout is exercised
  // separately; this check needs a stable, healthy host projection.
  await page.route("**/v1/preflight", (route) => route.fulfill({ json: { overall: "ok", probes: [] } }));
});

webE2e("home readiness notice can be dismissed without losing aligned entry controls", async ({ page }) => {
  await page.route("**/v1/preflight", (route) => route.fulfill({ json: {
    overall: "degraded",
    probes: [{
      id: "browser_engine", status: "degraded", tier: "non_catastrophic", scope: "app",
      code: "BROWSER_ENGINE_UNAVAILABLE", title: "Browser features unavailable",
      message: "Browser capture is unavailable. Other features remain available.",
    }],
  } }));
  await page.goto("/");
  await waitHarnessConnected(page);
  const notice = page.getByTestId("preflight-nudge");
  await expect(notice).toBeVisible();
  await expect(page.getByRole("button", { name: "Open a folder…", exact: true })).toBeEnabled();
  await notice.getByRole("button", { name: "Dismiss", exact: true }).click();
  await expect(notice).toBeHidden();
  await expectHomeDividerAligned(page);
});

webE2e("home doors divider aligns with the sidebar search hairline", async ({ page }) => {
  await page.goto("/");
  await waitHarnessConnected(page);
  await expect(page.getByTestId("shell")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByTestId("home-nav")).toBeVisible();
  await expect(page.getByTestId("home-view")).toBeVisible();
  await expect(page.locator(".home-view__doors-line").first()).toBeVisible();

  await expectHomeDividerAligned(page);
});

webE2e("home doors divider aligns in macOS custom chrome", async ({ page }) => {
  await page.goto("/");
  await waitHarnessConnected(page);
  await expect(page.getByTestId("shell")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByTestId("home-nav")).toBeVisible();
  await expect(page.getByTestId("home-view")).toBeVisible();
  await page.evaluate(() => {
    document.documentElement.classList.add("den-custom-chrome", "den-tauri-macos");
  });

  await expectHomeDividerAligned(page);
});
