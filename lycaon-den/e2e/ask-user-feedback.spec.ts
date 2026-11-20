import { expect, test, type APIRequestContext } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  liveChatStage,
  openNewChatSession,
  seedAppState,
  waitActiveWorkflowRun,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

type WorkflowFeedbackRow = {
  kind?: string;
  workflow_feedback?: {
    phase_id?: string;
    prompt?: string;
    answer?: string;
    artifact_id?: string;
  };
};

function authHeaders() {
  const { token } = apiConfig();
  return { Authorization: `Bearer ${token}` };
}

async function sessionMessages(
  request: APIRequestContext,
  sessionId: string,
): Promise<WorkflowFeedbackRow[] | null> {
  const { apiUrl } = apiConfig();
  const res = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, {
    headers: authHeaders(),
  });
  if (!res.ok()) return null;
  const body = (await res.json()) as
    | WorkflowFeedbackRow[]
    | { messages?: WorkflowFeedbackRow[] };
  return Array.isArray(body) ? body : (body.messages ?? []);
}

async function injectAskUser(
  request: APIRequestContext,
  data: Record<string, unknown>,
): Promise<string> {
  const { apiUrl } = apiConfig();
  const inject = await request.post(`${apiUrl}/harness/ask_user`, {
    headers: { ...authHeaders(), "Content-Type": "application/json" },
    data,
  });
  const injectBody = await inject.json();
  expect(inject.ok(), `harness ask_user: ${JSON.stringify(injectBody)}`).toBeTruthy();
  const phaseId = String((injectBody as { phase_id?: string }).phase_id ?? "").trim();
  expect(phaseId.startsWith("ask-"), `phase_id=${phaseId}`).toBeTruthy();
  return phaseId;
}

async function createVisualFixture(
  request: APIRequestContext,
  sessionId: string,
  caption: string,
): Promise<string> {
  const { apiUrl } = apiConfig();
  const fixture = await request.post(`${apiUrl}/harness/visual_fixture`, {
    headers: { ...authHeaders(), "Content-Type": "application/json" },
    data: { session_id: sessionId, caption },
  });
  const fixtureBody = (await fixture.json()) as { artifact_id?: string };
  expect(fixture.ok(), `visual_fixture: ${JSON.stringify(fixtureBody)}`).toBeTruthy();
  const id = String(fixtureBody.artifact_id ?? "").trim();
  expect(id).toBeTruthy();
  return id;
}

async function expectToolResultResponse(
  request: APIRequestContext,
  sessionId: string,
  response: string,
  timeout: number,
) {
  const { apiUrl } = apiConfig();
  await expect
    .poll(
      async () => {
        const res = await request.get(
          `${apiUrl}/harness/ask_user/last_response?session_id=${encodeURIComponent(sessionId)}`,
          { headers: authHeaders() },
        );
        if (!res.ok()) return false;
        const body = (await res.json()) as {
          present?: boolean;
          response?: string;
          source?: string;
        };
        return (
          body.present === true &&
          body.response === response &&
          body.source === "tool_result"
        );
      },
      { timeout },
    )
    .toBeTruthy();
}

/**
 * ask_user feedback journeys, each in its own chat so per-session docks and
 * last responses stay independent: free text through the composer, a single
 * choice, a visual compare, and a visual review.
 */
webE2e(
  "den:harness — ask_user dock answers resolve as tool results",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await seedAppState(page, { debug: { verboseMode: true } });
    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    await activateProject(page, project.id);
    const dock = page.getByTestId("ask-user-dock");
    const composer = liveChatStage(page).getByTestId("chat-composer");
    const send = liveChatStage(page).getByTestId("composer-send");

    await test.step("free text answer through the composer", async () => {
      const sessionId = await openNewChatSession(page);
      const phaseId = await injectAskUser(request, {
        session_id: sessionId,
        prompt: "REST or GraphQL?",
      });

      // Wire must persist the row even if SSE append races the open stream.
      await expect
        .poll(
          async () =>
            (await sessionMessages(request, sessionId))?.some(
              (m) =>
                m.kind === "workflow_feedback" &&
                m.workflow_feedback?.phase_id === phaseId &&
                (m.workflow_feedback?.prompt ?? "").includes("REST or GraphQL"),
            ) ?? false,
          { timeout: 30_000 },
        )
        .toBeTruthy();

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
      await expect(composer).toBeFocused({ timeout: 30_000 });
      await composer.fill("REST");
      await composer.press("Enter");

      await expect
        .poll(
          async () =>
            (await sessionMessages(request, sessionId))?.some(
              (m) =>
                m.workflow_feedback?.phase_id === phaseId &&
                m.workflow_feedback?.answer === "REST",
            ) ?? false,
          { timeout: 30_000 },
        )
        .toBeTruthy();

      await expect(dock).toHaveCount(0);
      await expect(page.getByTestId("workflow-feedback-chicklet")).toContainText(
        "REST",
        { timeout: 15_000 },
      );
      await expectToolResultResponse(request, sessionId, "REST", 15_000);
    });

    await test.step("single choice arms Send and resolves the pick", async () => {
      const sessionId = await openNewChatSession(page);
      expect(sessionId).toBeTruthy();
      await waitActiveWorkflowRun(request, sessionId);
      await injectAskUser(request, {
        session_id: sessionId,
        prompt: "Which API should we ship?",
        response_type: "single_choice",
        options: ["REST", "GraphQL"],
      });

      await expect(dock).toBeVisible({ timeout: 30_000 });
      await expect(dock).toHaveAttribute("data-response-type", "single_choice", {
        timeout: 30_000,
      });

      await page.getByLabel("GraphQL", { exact: true }).click();
      // Empty message field: the pick alone arms Send, and the button says so.
      await expect(composer).toHaveValue("");
      await expect(send).toHaveAttribute("data-armed", "");
      await expect(dock).toContainText("Press Enter to submit your answer");

      // The pick lives in the dock's local state until Send commits it; the dock
      // must not have been remounted out from under it by unrelated wire churn.
      await expect(page.getByLabel("GraphQL", { exact: true })).toBeChecked();
      await send.click();

      await expectToolResultResponse(request, sessionId, "GraphQL", 30_000);
      await expect(dock).toHaveCount(0);
    });

    await test.step("visual compare picks B; Other is the only escape hatch", async () => {
      const sessionId = await openNewChatSession(page);
      const active = await waitActiveWorkflowRun(request, sessionId);
      expect(active.id).toBeTruthy();

      const fixtureIds: string[] = [];
      for (const caption of ["variant A", "variant B"]) {
        fixtureIds.push(await createVisualFixture(request, sessionId, caption));
      }
      await injectAskUser(request, {
        session_id: sessionId,
        prompt: "Which layout should we ship?",
        purpose: "compare",
        artifacts: fixtureIds,
      });

      await expect(dock).toBeVisible({ timeout: 30_000 });
      await expect(dock).toHaveAttribute("data-purpose", "compare");
      await expect(page.getByTestId("workflow-feedback-compare-grid")).toBeVisible();
      await expect(page.getByTestId("workflow-feedback-artifact")).toHaveCount(2);
      // A stage mid-cross-fade briefly keeps the leaving composer mounted.
      await expect(composer).toBeVisible();
      await expect(page.getByTestId("workflow-feedback-other")).toHaveCount(0);
      await expect(page.getByTestId("workflow-feedback-remaining")).toHaveCount(0);
      await expect(page.getByLabel("Neither")).toHaveCount(0);

      const variantB = page.locator(
        '[data-testid="workflow-feedback-artifact"][data-compare-label="B"]',
      );
      await variantB.locator("label").click();
      await expect(page.getByLabel("B", { exact: true })).toBeChecked();
      await send.click();

      await expectToolResultResponse(request, sessionId, "B", 15_000);
      await expect(dock).toHaveCount(0);
    });

    await test.step("visual review approves the artifact", async () => {
      const sessionId = await openNewChatSession(page);
      const artifactId = await createVisualFixture(request, sessionId, "layout preview");
      const phaseId = await injectAskUser(request, {
        session_id: sessionId,
        prompt: "Approve this layout?",
        artifacts: [artifactId],
      });

      await expect
        .poll(
          async () =>
            (await sessionMessages(request, sessionId))?.some(
              (m) =>
                m.kind === "workflow_feedback" &&
                m.workflow_feedback?.phase_id === phaseId &&
                m.workflow_feedback?.artifact_id === artifactId,
            ) ?? false,
          { timeout: 30_000 },
        )
        .toBeTruthy();

      await expect(dock).toBeVisible({ timeout: 30_000 });
      await expect(page.getByTestId("workflow-feedback-artifact")).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByTestId("workflow-feedback-remaining")).toHaveCount(0);
      await page.getByLabel("Approve", { exact: true }).click();
      await send.click();
      await expect(dock).toHaveCount(0);

      await expectToolResultResponse(request, sessionId, "Approve", 15_000);
    });
  },
);
