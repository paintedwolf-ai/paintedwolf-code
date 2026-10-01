import { expect } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, openWorkflowsTab, webE2e } from "./helpers.ts";

webE2e.describe("ambient implement default", () => {
  webE2e("new session prompt keeps build span without workflow header chrome", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflows-tab-empty")).toBeVisible();
    await liveChatStage(page).getByTestId("chat-composer").fill("List one file in the repo root");
    await page.getByRole("button", { name: /send/i }).click();

    // Before any navigation: a stage change remounts the transcript, which would
    // hide a first turn that never drew on its own.
    await expect(
      liveChatStage(page).getByText("List one file in the repo root"),
    ).toBeVisible({ timeout: 120_000 });

    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflows-tab-empty")).toBeVisible({
      timeout: 120_000,
    });
    await expect(page.getByTestId("den-chat-span-implement")).toHaveCount(1, {
      timeout: 120_000,
    });
    await expect(page.getByTestId("den-chat-span-run")).toHaveCount(0);
  });
});
