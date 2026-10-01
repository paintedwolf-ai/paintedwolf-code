import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  liveChatStage,
  openNewChatSession,
  waitActiveWorkflowRun,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

/**
 * ask_user artifacts=[a,b] compare → pick B → answered tool row.
 * Other/free-text is the escape hatch (no Neither option).
 */
webE2e(
  "den:harness — ask_user visual compare → B → answered tool result",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    const active = await waitActiveWorkflowRun(request, sessionId);

    const fixtureIds: string[] = [];
    for (const caption of ["variant A", "variant B"]) {
      const fixture = await request.post(`${apiUrl}/harness/visual_fixture`, {
        headers: { ...auth, "Content-Type": "application/json" },
        data: { session_id: sessionId, caption },
      });
      const fixtureBody = (await fixture.json()) as { artifact_id?: string };
      expect(fixture.ok(), `visual_fixture: ${JSON.stringify(fixtureBody)}`).toBeTruthy();
      const id = String(fixtureBody.artifact_id ?? "").trim();
      expect(id).toBeTruthy();
      fixtureIds.push(id);
    }

    const inject = await request.post(`${apiUrl}/harness/ask_user`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionId,
        prompt: "Which layout should we ship?",
        artifacts: fixtureIds,
      },
    });
    const injectBody = await inject.json();
    expect(inject.ok(), `harness ask_user: ${JSON.stringify(injectBody)}`).toBeTruthy();
    const phaseId = String((injectBody as { phase_id?: string }).phase_id ?? "").trim();
    expect(phaseId.startsWith("ask-"), `phase_id=${phaseId}`).toBeTruthy();

    const dock = page.getByTestId("ask-user-dock");
    await expect(dock).toBeVisible({ timeout: 30_000 });
    await expect(dock).toHaveAttribute("data-purpose", "compare");
    await expect(page.getByTestId("workflow-feedback-compare-grid")).toBeVisible();
    await expect(page.getByTestId("workflow-feedback-artifact")).toHaveCount(2);
    // A stage mid-cross-fade briefly keeps the leaving composer mounted.
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible();
    await expect(page.getByTestId("workflow-feedback-other")).toHaveCount(0);
    await expect(page.getByTestId("workflow-feedback-remaining")).toHaveCount(0);
    await expect(page.getByLabel("Neither")).toHaveCount(0);

    // Compare: select option B, then submit via the composer Send button.
    const variantB = page.locator(
      '[data-testid="workflow-feedback-artifact"][data-compare-label="B"]',
    );
    await variantB.locator("label").click();
    await expect(page.getByLabel("B", { exact: true })).toBeChecked();
    await liveChatStage(page).getByTestId("composer-send").click();

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
            body.response === "B" &&
            body.source === "tool_result"
          );
        },
        { timeout: 15_000 },
      )
      .toBeTruthy();

    await expect(page.getByTestId("ask-user-dock")).toHaveCount(0);
    expect(active.id).toBeTruthy();
  },
);
