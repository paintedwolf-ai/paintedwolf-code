import { expect } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

webE2e.describe("checkpoint edit review", () => {
  webE2e("default policy shows no content_apply card on session load", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await expect(page.getByTestId("content-apply-card")).toHaveCount(0);
  });
});

webE2e.describe("checkpoint tool approval", () => {
  webE2e("no pending approval card on fresh session", async ({ page }) => {
    await bootstrapChatSession(page);
    await expect(page.getByTestId("tool-approval-card")).toHaveCount(0);
  });
});
