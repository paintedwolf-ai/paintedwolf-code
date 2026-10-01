import { expect } from "@playwright/test";
import { APP_STATE_STORAGE_SLICE_PREFIX } from "../shared/app-state-storage.ts";
import { webE2e, expectShellReady } from "./helpers.ts";

webE2e.describe("boot", () => {
  webE2e("sidecar connected with stable E2E state and no token field", async ({ page }) => {
    await page.goto("/");
    await expectShellReady(page);
    const state = await page.evaluate((prefix) => {
      const slices: Record<string, unknown> = {};
      for (let index = 0; index < localStorage.length; index += 1) {
        const key = localStorage.key(index);
        if (!key?.startsWith(prefix)) continue;
        const slice = decodeURIComponent(key.slice(prefix.length));
        slices[slice] = JSON.parse(localStorage.getItem(key) ?? "null");
      }
      return slices;
    }, APP_STATE_STORAGE_SLICE_PREFIX);
    expect(JSON.stringify(state)).not.toMatch(/api_token|apiToken/i);
    expect(state).toMatchObject({
      onboarding: { firstRunSetupCompleted: true },
      firstTimeTips: { enabled: false },
    });
  });
});
