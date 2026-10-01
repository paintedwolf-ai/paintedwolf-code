import { expect, type Page } from "@playwright/test";
import {
  apiConfig,
  apiSeedUntrustedContent,
  apiSeedSessionTranscript,
  bootstrapActiveProject,
  liveChatStage,
  openNewChatSession,
  openSessionRow,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";


async function sessionIdFromChat(page: Page): Promise<string> {
  // Settled foreground id — an unscoped chat-stream read races the session
  // switch cross-fade (two stages mounted, strict-mode violation).
  const id = await settledChatSessionId(page);
  expect(id, "settled chat session id").toBeTruthy();
  return id;
}

webE2e.describe("untrusted content den render", () => {
  webE2e("fresh session has no External content badge", async ({ page, request }) => {
    await bootstrapActiveProject(page, request);
    await openNewChatSession(page);
    await expect(page.getByTestId("external-content-badge")).toHaveCount(0);
  });

  webE2e(
    "den:harness — External content badge when untrusted_content is true",
    async ({ page, request }) => {
      webE2e.setTimeout(180_000);
      await bootstrapActiveProject(page, request);
      const sessionId = await openNewChatSession(page);
      expect(sessionId, "settled chat session id").toBeTruthy();

      // Title the session so its focused-session-list row is identifiable.
      const { apiUrl, token } = apiConfig();
      const titled = await request.patch(`${apiUrl}/v1/sessions/${sessionId}`, {
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        data: { title: "Untrusted badge e2e" },
      });
      expect(titled.ok(), await titled.text()).toBeTruthy();

      await apiSeedUntrustedContent(request, sessionId);
      await apiSeedSessionTranscript(request, sessionId, transcriptMessages([{
        id: crypto.randomUUID(), role: "user", content: "Review external content.",
        created_at: new Date().toISOString(), seq: 1, ord: 1,
      }]));
      await expect(liveChatStage(page).getByRole("article", { name: "You", exact: true })).toContainText("Review external content.");

      // Re-select the target after session bootstrap.
      await openNewChatSession(page);
      const foregrounded = await sessionIdFromChat(page);
      expect(foregrounded).not.toBe(sessionId);
      await openSessionRow(page, sessionId);

      await expect(page.getByTestId("external-content-badge")).toBeVisible({
        timeout: 30_000,
      });
      await expect(page.getByTestId("external-content-badge")).toHaveAttribute(
        "data-tip",
        /External content in this session/i,
      );
      await expect(page.getByTestId("external-content-badge")).toHaveAttribute(
        "data-tip",
        /click to search/i,
      );
    },
  );
});
