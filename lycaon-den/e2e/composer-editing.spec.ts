import { expect } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, webE2e } from "./helpers.ts";

webE2e.describe("composer editing", () => {
  webE2e("preserves the caret while typing in the middle", async ({ page }) => {
    await bootstrapChatSession(page);
    const composer = liveChatStage(page).getByTestId("chat-composer");
    const send = liveChatStage(page).getByTestId("composer-send");

    await expect(composer).toHaveValue("");
    await composer.fill("hello world");
    for (let index = 0; index < 5; index += 1) {
      await composer.press("ArrowLeft");
    }
    await composer.pressSequentially("brave ");

    await expect(composer).toHaveValue("hello brave world");
    await expect(send).toBeEnabled();

    await composer.fill("   ");
    await expect(composer).toHaveValue("   ");
  });
});
