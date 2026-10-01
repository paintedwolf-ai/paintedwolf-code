import { expect } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

webE2e.describe("desktop notifications settings", () => {
  webE2e(
    "General notifications toggles and test button explain browser unavailability",
    async ({ page }) => {
      await bootstrapChatSession(page);
      await page
        .getByTestId("shell-nav-dock")
        .getByRole("button", { name: "Settings" })
        .click();
      await expect(page.getByTestId("settings-view")).toBeVisible({
        timeout: 15_000,
      });
      await page.getByTestId("settings-nav-general").click();
      await page.getByTestId("general-tab-notifications").click();
      await expect(
        page.getByTestId("general-panel-notifications"),
      ).toBeVisible();

      const finished = page.getByTestId("notifications-finished");
      await expect(finished).toBeVisible();
      await expect(finished).toBeChecked();
      await finished.click();
      await expect(finished).not.toBeChecked();
      await finished.click();
      await expect(finished).toBeChecked();

      const denied = page.getByTestId("notifications-approval-denied");
      await expect(denied).toHaveCount(0);

      await page.getByTestId("notifications-send-test").click();
      await expect(page.getByTestId("notifications-shell-unavailable")).toBeVisible();
      await expect(denied).toHaveCount(0);
    },
  );
});
