import { expect } from "@playwright/test";
import type { HarnessApi } from "../src/platform/harness/harness-driver.ts";
import {
  bootstrapChatSession,
  liveChatStage,
  openNewChatSession,
  openSessionRow,
  settledChatSessionId,
  waitForChatComposerReady,
  waitForSessionReady,
  webE2e,
} from "./helpers.ts";

webE2e.describe("queue send across a session switch", () => {
  webE2e("a reservation sent to the active turn keeps its seat after switching away and back", async ({ page }) => {
    await bootstrapChatSession(page);
    const sessionA = await settledChatSessionId(page);
    await waitForSessionReady(page.request, sessionA);
    const composer = await waitForChatComposerReady(page);
    await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.manual());
    try {
      // Keep the turn busy: the completion is held and never answered.
      await composer.fill("Start the first turn.");
      await composer.press("Enter");
      await expect(composer).toHaveValue("", { timeout: 30_000 });
      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.pending(30_000));

      const queuedPrompt = "Actually, just say PELICAN.";
      await composer.fill(queuedPrompt);
      await composer.press("Enter");

      const stage = liveChatStage(page);
      const card = stage.getByTestId("queue-popover-card");
      await expect(card).toBeVisible({ timeout: 30_000 });
      await expect(card).toContainText(queuedPrompt);
      await expect(stage.getByTestId("queue-pending-item")).toHaveCount(0, { timeout: 30_000 });

      await stage.getByTestId("queue-send").click();
      await expect(stage.getByTestId("queue-cancel-send")).toBeVisible({ timeout: 10_000 });
      await expect(stage.getByTestId("queue-status")).toHaveText("Sending");
      const seatBefore = stage
        .getByTestId("pending-send-bubble")
        .filter({ hasText: queuedPrompt });
      await expect(seatBefore).toHaveCount(1, { timeout: 10_000 });
      await expect(seatBefore).toHaveAttribute("data-pending-kind", "queue_send");

      // Switch to a fresh session, then back while the first turn still runs.
      const sessionB = await openNewChatSession(page);
      expect(sessionB).not.toBe(sessionA);
      await openSessionRow(page, sessionA);
      await expect(stage.getByTestId("chat-composer")).toBeVisible({ timeout: 60_000 });

      const seatAfter = stage
        .getByTestId("pending-send-bubble")
        .filter({ hasText: queuedPrompt });
      await expect(seatAfter).toHaveCount(1, { timeout: 10_000 });
      await expect(seatAfter).toHaveAttribute("data-pending-kind", "queue_send");
      await expect(card).toBeVisible({ timeout: 10_000 });
      await expect(stage.getByTestId("queue-status")).toHaveText("Sending");
      await expect(stage.getByTestId("queue-cancel-send")).toBeVisible();
      await expect(
        stage.getByTestId("transcript-article-user").filter({ hasText: queuedPrompt }),
      ).toHaveCount(0);
    } finally {
      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.auto("Queue fixture complete."));
    }
  });
});
