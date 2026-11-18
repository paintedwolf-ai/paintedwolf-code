import { expect } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, webE2e, sendChatPrompt } from "./helpers.ts";

webE2e.describe("prompt", () => {
  webE2e("new session → prompt → settled assistant reply and idle composer", async ({ page }) => {
    await bootstrapChatSession(page);
    await sendChatPrompt(page, "Say hello in one word");
    const stage = liveChatStage(page);
    // The e2e fixture puts the manual model on auto-reply with this text.
    const reply = stage.getByTestId("transcript-article-assistant").last();
    await expect(reply).toContainText("Harness reply.", { timeout: 90_000 });
    await expect(reply).not.toHaveAttribute("aria-busy", /.*/, { timeout: 90_000 });
    await expect(stage.getByTestId("stop-coordinator")).toHaveCount(0);
    await expect(stage.getByTestId("thinking-indicator")).toHaveCount(0);
    await expect(stage.getByTestId("composer-send")).toBeVisible();
  });
});
