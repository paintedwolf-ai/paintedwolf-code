import { expect } from "@playwright/test";
import { webE2e, expectShellReady } from "./helpers.ts";

/** Web tier: boot + backend attach. Full kill/reconnect cycle is desktop-only. */
webE2e.describe("reconnect web", () => {
  webE2e("shell loads with backend connected", async ({ page }) => {
    await page.goto("/");
    await expectShellReady(page);
    await expect(page.getByTestId("critical-stop")).toHaveCount(0);
  });
});
