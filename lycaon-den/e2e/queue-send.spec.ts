import { expect } from "@playwright/test";
import type { HarnessApi } from "../src/platform/harness/harness-driver.ts";
import {
  bootstrapChatSession,
  holdPromptRequest,
  liveChatStage,
  settledChatSessionId,
  waitForChatComposerReady,
  waitForSessionReady,
  webE2e,
} from "./helpers.ts";

webE2e.describe("queue send", () => {
  webE2e("a message sent while busy queues, and Send seats it in the transcript at once", async ({ page }) => {
    await bootstrapChatSession(page);
    const sessionId = await settledChatSessionId(page);
    await waitForSessionReady(page.request, sessionId);
    const stage = liveChatStage(page);
    const composer = await waitForChatComposerReady(page);
    await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.manual());
    try {
      // Keep the turn busy for queue admission.
      await composer.fill("Start the first turn.");
      await composer.press("Enter");
      await expect(composer).toHaveValue("", { timeout: 30_000 });
      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.pending(30_000));

      const queuedPrompt = "Actually, just say PELICAN.";
      const held = await holdPromptRequest(page, queuedPrompt);
      await composer.fill(queuedPrompt);
      await composer.press("Enter");

      // Hold admission to observe the submission's first visible surface.
      await held.intercepted;
      const optimistic = stage
        .getByTestId("pending-send-bubble")
        .filter({ hasText: queuedPrompt });
      try {
        await expect(stage.getByTestId("queue-pending-item")).toContainText(queuedPrompt);
        await expect(optimistic).toHaveCount(0);
        await expect(stage.getByTestId("queue-send")).toBeDisabled();
      } finally {
        held.release();
      }

      // Queue admission enables the existing queue's controls.
      const card = stage.getByTestId("queue-popover-card");
      await expect(card).toBeVisible({ timeout: 30_000 });
      await expect(card).toContainText(queuedPrompt);
      await expect(stage.getByTestId("queue-pending-item")).toHaveCount(0, { timeout: 30_000 });
      await expect(optimistic).toHaveCount(0);

      await stage.getByTestId("queue-send").click();

      await expect(stage.getByTestId("queue-cancel-send")).toBeVisible({ timeout: 10_000 });
      // The reservation holds a transcript seat until pickup.
      const seat = stage
        .getByTestId("pending-send-bubble")
        .filter({ hasText: queuedPrompt });
      await expect(seat).toHaveCount(1, { timeout: 10_000 });
      await expect(seat).toHaveAttribute("data-pending-kind", "queue_send");

      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.auto("Queue fixture complete."));
      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.respond({ text: "First turn complete." }));
      // The message echo replaces the reserved seat.
      const landed = stage
        .getByTestId("transcript-article-user")
        .filter({ hasText: queuedPrompt });
      await expect(landed).toHaveCount(1, { timeout: 120_000 });
      await expect(seat).toHaveCount(0, { timeout: 10_000 });
    } finally {
      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.auto("Queue fixture complete."));
    }
  });

  webE2e("a reservation can be taken back before the turn picks it up", async ({ page }) => {
    await bootstrapChatSession(page);
    const sessionId = await settledChatSessionId(page);
    await waitForSessionReady(page.request, sessionId);
    const stage = liveChatStage(page);
    const composer = await waitForChatComposerReady(page);
    await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.manual());
    try {
      await composer.fill("Start the first turn.");
      await composer.press("Enter");
      await expect(composer).toHaveValue("", { timeout: 30_000 });
      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.pending(30_000));

      await composer.fill("Wrong idea, ignore this.");
      await composer.press("Enter");
      const card = stage.getByTestId("queue-popover-card");
      await expect(card).toBeVisible({ timeout: 30_000 });

      await stage.getByTestId("queue-send").click();
      const cancel = stage.getByTestId("queue-cancel-send");
      await expect(cancel).toBeVisible({ timeout: 10_000 });
      const seat = stage
        .getByTestId("pending-send-bubble")
        .filter({ hasText: "Wrong idea, ignore this." });
      await expect(seat).toHaveCount(1, { timeout: 10_000 });
      await cancel.click();

      // Cancellation returns the item to the editable queue.
      await expect(stage.getByTestId("queue-send")).toBeVisible({ timeout: 15_000 });
      await expect(card).toContainText("Wrong idea, ignore this.");
      await expect(seat).toHaveCount(0, { timeout: 10_000 });
      await expect(
        stage.getByTestId("transcript-article-user").filter({ hasText: "Wrong idea, ignore this." }),
      ).toHaveCount(0);
    } finally {
      await page.evaluate(() => (window as unknown as { __harness: HarnessApi }).__harness.llm.auto("Queue fixture complete."));
    }
  });
});
