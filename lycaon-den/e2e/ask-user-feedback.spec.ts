import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  liveChatStage,
  openNewChatSession,
  seedAppState,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

/**
 * ask_user → AskUserDock + composer Other → answered tool row.
 */
webE2e(
  "den:harness — ask_user dock + composer → answered tool result",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await seedAppState(page, { debug: { verboseMode: true } });
    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    const inject = await request.post(`${apiUrl}/harness/ask_user`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionId,
        prompt: "REST or GraphQL?",
      },
    });
    const injectBody = await inject.json();
    expect(inject.ok(), `harness ask_user: ${JSON.stringify(injectBody)}`).toBeTruthy();
    const phaseId = String((injectBody as { phase_id?: string }).phase_id ?? "").trim();
    expect(phaseId.startsWith("ask-"), `phase_id=${phaseId}`).toBeTruthy();

    // Wire must persist the row even if SSE append races the open stream.
    await expect
      .poll(
        async () => {
          const res = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, {
            headers: auth,
          });
          if (!res.ok()) return false;
          const body = (await res.json()) as
            | Array<{
                kind?: string;
                workflow_feedback?: { phase_id?: string; prompt?: string };
              }>
            | {
                messages?: Array<{
                  kind?: string;
                  workflow_feedback?: { phase_id?: string; prompt?: string };
                }>;
              };
          const msgs = Array.isArray(body) ? body : (body.messages ?? []);
          return msgs.some(
            (m) =>
              m.kind === "workflow_feedback" &&
              m.workflow_feedback?.phase_id === phaseId &&
              (m.workflow_feedback?.prompt ?? "").includes("REST or GraphQL"),
          );
        },
        { timeout: 30_000 },
      )
      .toBeTruthy();

    const dock = page.getByTestId("ask-user-dock");
    await expect(dock).toBeVisible({ timeout: 30_000 });
    await expect(dock).toContainText("REST or GraphQL?");
    await expect(page.getByTestId("workflow-feedback-open-marker")).toBeVisible();
    // Append order keeps the marker after its ask_user chip.
    await expect
      .poll(async () =>
        page.evaluate(() => {
          const chip = document.querySelector('[data-tool="ask_user"]');
          const marker = document.querySelector(
            '[data-testid="workflow-feedback-open-marker"]',
          );
          if (!chip || !marker) return "missing";
          return chip.compareDocumentPosition(marker) &
            Node.DOCUMENT_POSITION_FOLLOWING
            ? "after"
            : "before";
        }),
      )
      .toBe("after");
    await expect(page.getByTestId("workflow-feedback-remaining")).toHaveCount(0);
    const composer = liveChatStage(page).getByTestId("chat-composer");
    await expect(composer).toBeFocused({ timeout: 5_000 });
    await composer.fill("REST");
    await composer.press("Enter");

    await expect
      .poll(
        async () => {
          const res = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, {
            headers: auth,
          });
          if (!res.ok()) return false;
          const body = (await res.json()) as {
            messages?: Array<{
              workflow_feedback?: { phase_id?: string; answer?: string };
            }>;
          };
          const msgs = body.messages ?? [];
          return msgs.some(
            (m) =>
              m.workflow_feedback?.phase_id === phaseId &&
              m.workflow_feedback?.answer === "REST",
          );
        },
        { timeout: 30_000 },
      )
      .toBeTruthy();

    await expect(page.getByTestId("ask-user-dock")).toHaveCount(0);
    await expect(page.getByTestId("workflow-feedback-chicklet")).toContainText(
      "REST",
      { timeout: 15_000 },
    );

    await expect
      .poll(
        async () => {
          const res = await request.get(
            `${apiUrl}/harness/ask_user/last_response?session_id=${encodeURIComponent(sessionId)}`,
            { headers: auth },
          );
          if (!res.ok()) return false;
          const body = (await res.json()) as {
            present?: boolean;
            response?: string;
            source?: string;
          };
          return (
            body.present === true &&
            body.response === "REST" &&
            body.source === "tool_result"
          );
        },
        { timeout: 15_000 },
      )
      .toBeTruthy();

  },
);
