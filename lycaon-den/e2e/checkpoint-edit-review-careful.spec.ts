import { expect } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

webE2e.describe("checkpoint edit review", () => {
  webE2e("project configuration exposes path-based review rules", async ({ page }) => {
    await bootstrapChatSession(page);
    await page.getByTestId("project-configuration-entry").click();
    await page.getByTestId("project-context-entry-edit-review").click();
    await expect(page.getByTestId("edit-review-settings")).toBeVisible();
    await expect(page.getByTestId("edit-review-settings")).toContainText(
      "pause for review",
    );
    await expect(page.getByTestId("edit-review-add-rule")).toBeVisible();
  });
});
