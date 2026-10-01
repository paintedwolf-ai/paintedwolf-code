import { expect } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, openWorkflowsTab, webE2e } from "./helpers.ts";

webE2e.describe("workflow spans", () => {
  webE2e("fresh session has no catalog workflow chrome for ambient implement", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    await expect(page.getByTestId("den-chat-span-run")).toHaveCount(0);
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflows-tab-empty")).toBeVisible();
  });

  webE2e("/plan stub phase then /exit restores ambient build span", async ({
    page,
  }) => {
    await bootstrapChatSession(page);
    // Bare `/plan` arms only — seed an ask so the host starts the catalog leaf.
    await liveChatStage(page).getByTestId("chat-composer").fill("/plan draft a stub");
    await page.getByRole("button", { name: /send/i }).click();
    await expect(page.getByTestId("den-chat-span-run")).toHaveCount(1, {
      timeout: 120_000,
    });
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflow-active-row")).toContainText(
      /Understanding the request|Researching|Drafting the plan/i,
      { timeout: 120_000 },
    );

    await liveChatStage(page).getByTestId("chat-composer").fill("/exit");
    await page.getByRole("button", { name: /send/i }).click();
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflows-tab-empty")).toBeVisible({
      timeout: 60_000,
    });
    // Catalog span stays in the transcript as history after /exit.
    await expect(page.getByTestId("den-chat-span-run")).toHaveCount(1);
  });
});
