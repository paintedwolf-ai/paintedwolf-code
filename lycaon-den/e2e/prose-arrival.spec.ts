import { expect } from "@playwright/test";
import { apiSeedSessionTranscript, bootstrapChatSession, settledChatSessionId, transcriptMessages, modelIndependentWebE2e } from "./helpers.ts";

modelIndependentWebE2e("prose arrival follows a long answer without stopping at its beginning", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const userId = crypto.randomUUID();
  const answerId = crypto.randomUUID();
  const user = { id: userId, role: "user" as const, content: "Explain the result.", created_at: "2026-01-01T00:00:00Z", seq: 1, ord: 1 };
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([user]));
  await expect(page.locator(`[data-msg-id="${userId}"]`)).toBeVisible();
  const content = "## Start reading here\n\n" + Array.from({ length: 35 }, (_, index) => `Paragraph ${index + 1}. This answer is fully available, with enough detail to extend beyond the visible conversation.`).join("\n\n") + "\n\nEnd of complete answer.";
  const answer = { id: answerId, role: "assistant" as const, content, created_at: "2026-01-01T00:00:01Z", seq: 2, ord: 2 };
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([answer]));
  const prose = page.locator(`[data-msg-id="${answerId}"] .assistant-prose`);
  await expect(prose).toContainText("End of complete answer.");
  const atTail = async () => {
    await expect.poll(() => page.locator(".den-chat-stream").evaluate((stream) => {
      const end = stream.querySelector<HTMLElement>("[data-transcript-end]")!;
      const body = stream.querySelector<HTMLElement>(".den-chat-stream-body")!;
      return Math.abs(end.getBoundingClientRect().bottom +
        Number.parseFloat(getComputedStyle(body).paddingBottom) -
        stream.getBoundingClientRect().bottom);
    })).toBeLessThan(2);
  };
  await atTail();
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([{
    id: crypto.randomUUID(), role: "assistant", content: "Additional available text.",
    created_at: "2026-01-01T00:00:02Z", seq: 3, ord: 3,
  }]));
  await expect(page.getByText("Additional available text.", { exact: true })).toBeAttached();
  await atTail();
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");
});
