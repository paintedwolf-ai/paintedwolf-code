import { expect } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

webE2e.describe("search replace across files", () => {
  webE2e(
    "den:harness — match toggles, filters, replace preview states",
    async ({ page }) => {
      await bootstrapChatSession(page);
      await page.keyboard.press("ControlOrMeta+k");
      const crossbar = page.getByTestId("crossbar");
      await expect(crossbar).toBeVisible({ timeout: 15_000 });
      await page.getByTestId("crossbar-input").fill("kind:code toolbar");
      await page.getByTestId("crossbar-escalate").click();
      const search = page.getByTestId("global-search-view");
      await expect(search).toBeVisible({ timeout: 15_000 });

      await expect(page.getByTestId("search-match-toggles")).toBeVisible();
      await page.getByTestId("search-toggle-case").click();
      await page.getByTestId("search-toggle-word").click();
      await page.getByTestId("search-toggle-regex").click();
      await page.getByTestId("search-filter").click();
      await expect(page.getByTestId("search-include-glob")).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(page.getByTestId("search-filter-menu")).toHaveCount(0);

      await page.getByTestId("search-toggle-replace").click();
      await expect(page.getByTestId("search-replace-row")).toBeVisible();
      await page.getByTestId("search-replace-input").fill("Toolbar");
      await expect(page.getByTestId("search-replace-preview")).toBeVisible({
        timeout: 15_000,
      });
    },
  );
});
