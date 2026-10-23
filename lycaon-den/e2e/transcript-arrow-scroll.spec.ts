import { expect } from "@playwright/test";
import {
  apiSeedSessionTranscript, bootstrapChatSession, liveChatStage,
  modelIndependentWebE2e, settledChatSessionId, transcriptMessages,
} from "./helpers.ts";

modelIndependentWebE2e("plain arrows scroll a focused conversation row normally", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages(
    Array.from({ length: 30 }, (_, index) => ({
      id: crypto.randomUUID(), role: index % 2 ? "assistant" as const : "user" as const,
      content: `Message ${index + 1}. ${"Conversation text for reading. ".repeat(60)}`,
      created_at: "2026-10-23T12:00:00Z", seq: index + 1, ord: index + 1,
    })),
  ));
  const stream = liveChatStage(page).locator(".den-chat-stream");
  const rows = stream.locator(".transcript-viewport-row[data-msg-id]");
  await expect(rows.first()).toBeAttached();
  await stream.focus();
  await page.keyboard.press("Home");
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeLessThan(2);
  await expect(rows.first()).toBeVisible();
  // Message rows are accessibility focus targets. Exercise native keyboard
  // scrolling from that focus state in each browser.
  await rows.first().focus();
  const start = await stream.evaluate(element => element.scrollTop);
  await page.keyboard.press("ArrowDown");
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeGreaterThan(start + 5);
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeLessThan(start + 200);
  await page.keyboard.press("ArrowUp");
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeLessThan(5);
});
