import { expect, type Page } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiSeedSessionTranscript,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  openSessionRow,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

async function installNoteTranscript(
  request: NonNullable<Parameters<typeof bootstrapChatSession>[1]>,
  sessionId: string,
) {
  const assistantMessageId = crypto.randomUUID();
  await apiSeedSessionTranscript(
    request,
    sessionId,
    transcriptMessages([
      {
        id: crypto.randomUUID(),
        role: "user",
        content: "Where is auth?",
        created_at: "2026-07-23T20:00:00Z",
        seq: 1,
        ord: 1,
      },
      {
        id: assistantMessageId,
        role: "assistant",
        content: "",
        status: "complete",
        created_at: "2026-07-23T20:00:01Z",
        seq: 2,
        ord: 2,
        tool_calls: [
          {
            id: "call_read_1",
            name: "read",
            args: { path: "middleware.go" },
          },
        ],
      },
      {
        id: crypto.randomUUID(),
        role: "tool",
        content: "package auth",
        created_at: "2026-07-23T20:00:02Z",
        seq: 3,
        ord: 3,
        tool_result: {
          tool_call_id: "call_read_1",
          assistant_message_id: assistantMessageId,
          tool: "read",
          content: "package auth",
          outcome: "completed",
        },
      },
      {
        id: crypto.randomUUID(),
        role: "assistant",
        kind: "agent_note",
        content: "Auth middleware lives in middleware.go.",
        status: "complete",
        created_at: "2026-07-23T20:00:03Z",
        seq: 4,
        ord: 4,
        grounding: {
          traced: true,
          cited_evidence: [
            {
              handle: "read#1",
              path: "middleware.go",
              line: 1,
              excerpt: "package auth",
              verdict: "matched",
            },
          ],
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              kind: "citation",
              summary: "1 citation(s) matched",
            },
          ],
        },
      },
    ]),
  );
}

/** Open a chat, then append the note fixture through the real store path. */
async function openMockedNoteChat(
  page: Page,
  request: Parameters<typeof bootstrapChatSession>[1],
) {
  const project = await bootstrapChatSession(page, request);
  const sessionId = (
    (await liveChatStage(page).getByTestId("chat-stream").getAttribute("data-session-id")) ??
    ""
  ).trim();
  expect(sessionId).toBeTruthy();
  await installNoteTranscript(request ?? page.request, sessionId);
  return { project, sessionId };
}

async function ensureProjectFocused(page: Page, projectId: string) {
  if (await page.getByTestId("focused-project-nav").isVisible().catch(() => false)) {
    return;
  }
  await activateProject(page, projectId);
}

webE2e.describe("agent mid-turn note", () => {
  webE2e(
    "note appears once after its tool card with badge and grounding chicklet",
    async ({ page, request }, testInfo) => {
      const { project, sessionId } = await openMockedNoteChat(page, request);

      const stream = liveChatStage(page).locator(
        `[data-testid="chat-stream"][data-session-id="${sessionId}"]`,
      );
      await expect(stream).toBeVisible({ timeout: 30_000 });
      await expect(stream.getByTestId("transcript-article-assistant").getByText("Auth middleware lives in middleware.go.")).toBeVisible({
        timeout: 30_000,
      });

      const noteBadge = page.getByTestId("agent-note-badge");
      try {
        await expect(noteBadge).toHaveCount(1);
      } catch (error) {
        const { apiUrl, token } = apiConfig();
        const response = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        await testInfo.attach("note-wire-state", { body: await response.body(), contentType: "application/json" });
        await testInfo.attach("note-rendered-state", { body: await stream.evaluate((element) => element.outerHTML), contentType: "text/html" });
        throw error;
      }
      await expect(noteBadge).toHaveText("Note");
      await expect(noteBadge).toHaveAttribute("aria-label", "Note");

      const noteArticle = page.locator(
        '[data-testid="transcript-article-assistant"][data-kind="agent_note"]',
      );
      await expect(noteArticle).toHaveCount(1);
      await expect(
        noteArticle.getByTestId("citation-evidence-chicklet"),
      ).toBeVisible();

      await stream.getByTestId("activity-span-card").locator(":scope > summary").click();
      const toolCard = stream.getByTestId("tool-part-card").first();
      await expect(toolCard).toBeVisible();
      const order = await page.evaluate(() => {
        const tool = document.querySelector('[data-testid="tool-part-card"]');
        const note = document.querySelector(
          '[data-testid="transcript-article-assistant"][data-kind="agent_note"]',
        );
        if (!tool || !note) return -1;
        return tool.compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING
          ? 1
          : 0;
      });
      expect(order).toBe(1);

      // Hydration / reload: still exactly one note.
      await page.reload();
      await expectShellReady(page);
      await ensureProjectFocused(page, project.id);
      await openSessionRow(page, sessionId);
      await expect(liveChatStage(page).getByText("Auth middleware lives in middleware.go.", { exact: true })).toBeVisible({ timeout: 30_000 });
      await expect(page.getByTestId("agent-note-badge")).toHaveCount(1);
    },
  );
});
