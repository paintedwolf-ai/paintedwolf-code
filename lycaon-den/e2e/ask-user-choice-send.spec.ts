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
 * ask_user single_choice → pick an option → composer Send resolves it.
 *
 * Covers the two things a picked option has to do with an empty message field:
 * arm Send (and say so), and survive until the user actually presses it.
 */
webE2e(
  "den:harness — ask_user choice arms Send and resolves the pick",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);
    expect(sessionId).toBeTruthy();
    await waitActiveWorkflowRun(request, sessionId);

    const inject = await request.post(`${apiUrl}/harness/ask_user`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: {
        session_id: sessionId,
        prompt: "Which API should we ship?",
        response_type: "single_choice",
        options: ["REST", "GraphQL"],
      },
    });
    expect(inject.ok(), `harness ask_user: ${await inject.text()}`).toBeTruthy();
    const phaseId = String(
      ((await inject.json()) as { phase_id?: string }).phase_id ?? "",
    ).trim();
    expect(phaseId.startsWith("ask-"), `phase_id=${phaseId}`).toBeTruthy();

    const dock = page.getByTestId("ask-user-dock");
    await expect(dock).toBeVisible({ timeout: 30_000 });
    await expect(dock).toHaveAttribute("data-response-type", "single_choice", {
      timeout: 30_000,
    });

    await page.getByLabel("GraphQL", { exact: true }).click();
    // Empty message field: the pick alone arms Send, and the button says so.
    await expect(liveChatStage(page).getByTestId("chat-composer")).toHaveValue("");
    await expect(liveChatStage(page).getByTestId("composer-send")).toHaveAttribute(
      "data-armed",
      "",
    );
    await expect(dock).toContainText("Press Enter to submit your answer");

    // The pick lives in the dock's local state until Send commits it; the dock
    // must not have been remounted out from under it by unrelated wire churn.
    await expect(page.getByLabel("GraphQL", { exact: true })).toBeChecked();
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
            body.response === "GraphQL" &&
            body.source === "tool_result"
          );
        },
        { timeout: 30_000 },
      )
      .toBeTruthy();

    await expect(page.getByTestId("ask-user-dock")).toHaveCount(0);
  },
);
