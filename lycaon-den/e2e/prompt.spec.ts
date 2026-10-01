import { expect } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, webE2e, sendChatPrompt } from "./helpers.ts";

webE2e.describe("prompt", () => {
  webE2e("new session → prompt → assistant visible", async ({ page }) => {
    await bootstrapChatSession(page);
    await sendChatPrompt(page, "Say hello in one word");
    await expect(liveChatStage(page).getByTestId("transcript-article-assistant").last()).toBeVisible({
      timeout: 90_000,
    });
  });
});
