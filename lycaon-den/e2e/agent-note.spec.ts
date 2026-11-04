import { expect, type APIRequestContext, type Page } from "@playwright/test";
import { manualHarnessTools } from "./manual-harness-tools.ts";
import {
  activateProject,
  apiConfig,
  apiJson,
  apiPostPrompt,
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
  // A note commits with its carrier, before the batch's tools settle.
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
        role: "assistant",
        kind: "agent_note",
        content: "Auth middleware lives in middleware.go.",
        status: "complete",
        created_at: "2026-07-23T20:00:02Z",
        seq: 3,
        ord: 3,
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
      {
        id: crypto.randomUUID(),
        role: "tool",
        content: "package auth",
        created_at: "2026-07-23T20:00:03Z",
        seq: 4,
        ord: 4,
        tool_result: {
          tool_call_id: "call_read_1",
          assistant_message_id: assistantMessageId,
          tool: "read",
          content: "package auth",
          outcome: "completed",
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

function manualCompletions(request: APIRequestContext, sessionId: string) {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const nextRequest = async () => {
    const response = await request.get(`${apiUrl}/harness/llm/pending?wait=30000&session_id=${sessionId}`, { headers });
    expect(response.ok(), await response.text()).toBeTruthy();
    const pending = (await response.json()) as { pending: boolean; id: string; tools: string[] };
    expect(pending.pending).toBe(true);
    return pending;
  };
  return {
    waitForNextRequest: nextRequest,
    async respond(content: string, calls: Array<{ name: string; args: Record<string, unknown> }> = []) {
      const { id, tools } = await nextRequest();
      for (const call of calls) expect(tools).toContain(call.name);
      const toolCalls = calls.map((call) => ({ id: crypto.randomUUID(), ...call }));
      const reply = await request.post(`${apiUrl}/harness/llm/respond`, {
        headers,
        data: { id, content, tool_calls: toolCalls },
      });
      expect(reply.ok(), await reply.text()).toBeTruthy();
      return toolCalls.map((call) => call.id);
    },
  };
}

webE2e.describe("agent mid-turn note", () => {
  webE2e(
    "note appears once above its tool card with badge and grounding chicklet",
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

      await stream.getByTestId("activity-span-card").locator(":scope > .den-transcript-disclosure-header").click();
      const toolCard = stream.getByTestId("tool-part-card").first();
      await expect(toolCard).toBeVisible();
      const order = await page.evaluate(() => {
        const tool = document.querySelector('[data-testid="tool-part-card"]');
        const note = document.querySelector(
          '[data-testid="transcript-article-assistant"][data-kind="agent_note"]',
        );
        if (!tool || !note) return -1;
        return tool.compareDocumentPosition(note) & Node.DOCUMENT_POSITION_PRECEDING
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

  webE2e(
    "assistant text beside a tool call publishes without a tool, exempt then cited",
    async ({ page, request }) => {
      await bootstrapChatSession(page, request);
      const stream = liveChatStage(page).getByTestId("chat-stream");
      await expect.poll(async () => (await stream.getAttribute("data-session-id")) ?? "", { timeout: 30_000 }).not.toBe("");
      const sessionId = (await stream.getAttribute("data-session-id"))!;
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
      try {
        const prompt = await apiPostPrompt(request, sessionId, { text: "Read README.md and tell me where auth is." });
        expect(prompt.ok(), await prompt.text()).toBe(true);
        const driver = manualHarnessTools(request, sessionId);
        const llm = manualCompletions(request, sessionId);
        const notes = stream.locator('[data-testid="transcript-article-assistant"][data-kind="agent_note"]');
        const read = { name: "command", args: { command: "cat README.md" } };

        // Before any observation: published, and not presented as a checked source.
        const [firstRead] = await llm.respond("I'll look at the auth path.", [read]);
        await expect(notes.filter({ hasText: "I'll look at the auth path." })).toHaveCount(1, { timeout: 30_000 });
        await expect(notes.first().getByTestId("citation-evidence-chicklet")).toHaveCount(0);
        const handle = (await driver.completed(firstRead!)).match(/\[([a-z_-]+#\d+)\]/)?.[1];
        expect(handle).toBeTruthy();

        // After an observation: uncited text stays out of the transcript.
        await llm.respond("This uncited claim stays private.", [read]);
        await llm.waitForNextRequest();
        await expect(stream.getByText("This uncited claim stays private.")).toHaveCount(0);
        await expect(notes).toHaveCount(1);

        // A valid citation publishes, with its evidence and no protocol syntax left.
        await llm.respond(`The readme is recorded [README.md:1-2].`, [read]);
        const cited = notes.filter({ hasText: "The readme is recorded" });
        await expect(cited).toHaveCount(1, { timeout: 30_000 });
        await expect(cited).not.toContainText("[README.md");
        await expect(cited.getByTestId("citation-evidence-chicklet")).toBeVisible();
        const opened = cited.locator('.den-source-path-link[data-den-source-end-line="2"]');
        await expect(opened).toHaveAttribute("data-den-source-line", "1");

        await llm.respond(`Auth is described in the readme [${handle}].`);
        await expect(stream.getByText("Auth is described in the readme")).toBeVisible({ timeout: 30_000 });
        await expect(stream.getByTestId("agent-note-badge")).toHaveCount(2);
      } finally {
        await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Done." });
      }
    },
  );

  webE2e(
    "a stored surface_note call in an old session keeps its note",
    async ({ page, request }) => {
      const project = await bootstrapChatSession(page, request);
      const sessionId = (
        (await liveChatStage(page).getByTestId("chat-stream").getAttribute("data-session-id")) ?? ""
      ).trim();
      expect(sessionId).toBeTruthy();
      const carrier = crypto.randomUUID();
      await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
        { id: crypto.randomUUID(), role: "user", content: "Where is auth?", created_at: "2026-07-23T20:00:00Z", seq: 1, ord: 1 },
        {
          id: carrier, role: "assistant", content: "", status: "complete",
          created_at: "2026-07-23T20:00:01Z", seq: 2, ord: 2,
          tool_calls: [{ id: "call_note_1", name: "surface_note", args: { summary: "Auth is in middleware.go." } }],
        },
        {
          id: crypto.randomUUID(), role: "tool", content: '{"status":"noted"}',
          created_at: "2026-07-23T20:00:02Z", seq: 3, ord: 3,
          tool_result: {
            tool_call_id: "call_note_1", assistant_message_id: carrier, tool: "surface_note",
            content: '{"status":"noted"}', outcome: "completed",
          },
        },
        {
          id: crypto.randomUUID(), role: "assistant", kind: "agent_note",
          content: "Auth is in middleware.go.", status: "complete",
          created_at: "2026-07-23T20:00:03Z", seq: 4, ord: 4,
          grounding: { traced: true },
        },
      ]));
      await page.reload();
      await expectShellReady(page);
      await ensureProjectFocused(page, project.id);
      await openSessionRow(page, sessionId);
      await expect(liveChatStage(page).getByText("Auth is in middleware.go.", { exact: true })).toBeVisible({ timeout: 30_000 });
      await expect(page.getByTestId("agent-note-badge")).toHaveCount(1);
    },
  );
});
