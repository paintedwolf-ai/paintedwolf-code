import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  liveChatStage,
  openNewChatSession,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

/**
 * ask_user artifacts=[id] review card → Approve → answered tool row.
 */
webE2e(
  "den:harness — ask_user visual review card → Approve → answered tool result",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    const fixture = await request.post(`${apiUrl}/harness/visual_fixture`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: { session_id: sessionId, caption: "layout preview" },
    });
    const fixtureBody = (await fixture.json()) as { artifact_id?: string };
    expect(fixture.ok(), `visual_fixture: ${JSON.stringify(fixtureBody)}`).toBeTruthy();
    const artifactId = String(fixtureBody.artifact_id ?? "").trim();
    expect(artifactId).toBeTruthy();

    const inject = await request.post(`${apiUrl}/harness/ask_user`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionId,
        prompt: "Approve this layout?",
        artifacts: [artifactId],
      },
    });
    const injectBody = await inject.json();
    expect(inject.ok(), `harness ask_user: ${JSON.stringify(injectBody)}`).toBeTruthy();
    const phaseId = String((injectBody as { phase_id?: string }).phase_id ?? "").trim();
    expect(phaseId.startsWith("ask-"), `phase_id=${phaseId}`).toBeTruthy();

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
                workflow_feedback?: { phase_id?: string; artifact_id?: string };
              }>
            | {
                messages?: Array<{
                  kind?: string;
                  workflow_feedback?: { phase_id?: string; artifact_id?: string };
                }>;
              };
          const msgs = Array.isArray(body) ? body : (body.messages ?? []);
          return msgs.some(
            (m) =>
              m.kind === "workflow_feedback" &&
              m.workflow_feedback?.phase_id === phaseId &&
              m.workflow_feedback?.artifact_id === artifactId,
          );
        },
        { timeout: 30_000 },
      )
      .toBeTruthy();

    const dock = page.getByTestId("ask-user-dock");
    await expect(dock).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("workflow-feedback-artifact")).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByTestId("workflow-feedback-remaining")).toHaveCount(0);
    await page.getByLabel("Approve", { exact: true }).click();
    await liveChatStage(page).getByTestId("composer-send").click();
    await expect(dock).toHaveCount(0);

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
            body.response === "Approve" &&
            body.source === "tool_result"
          );
        },
        { timeout: 15_000 },
      )
      .toBeTruthy();
  },
);
