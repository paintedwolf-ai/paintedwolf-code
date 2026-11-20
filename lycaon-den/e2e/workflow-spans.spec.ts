import { expect } from "@playwright/test";
import {
  apiGetActiveWorkflowRun,
  apiGetWorkflowRun,
  apiTryGetActiveWorkflowRun,
  bootstrapChatSession,
  liveChatStage,
  openWorkflowsTab,
  sendChatPrompt,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

webE2e.describe("workflow spans", () => {
  webE2e("/plan stub phase then /exit restores ambient build span", async ({
    page,
    request,
  }) => {
    await bootstrapChatSession(page);
    const sessionId = await settledChatSessionId(page);
    const ambient = await apiGetActiveWorkflowRun(request, sessionId);
    const stage = liveChatStage(page);
    const span = page.getByTestId("den-chat-span-run");
    await expect(span).toHaveCount(0);
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflows-tab-empty")).toBeVisible();

    // Bare `/plan` arms only — seed an ask so the host starts the catalog leaf.
    await stage.getByTestId("chat-composer").fill("/plan draft a stub");
    await stage.getByTestId("composer-send").click();
    let planRunId = "";
    await expect
      .poll(async () => {
        const run = await apiGetActiveWorkflowRun(request, sessionId);
        planRunId = run.id;
        return run.workflow_id;
      }, { timeout: 120_000 })
      .toBe("plan");
    await expect(span).toHaveCount(1, { timeout: 120_000 });
    await expect(span).toHaveAttribute("data-workflow-run-id", planRunId);
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflow-active-row")).toBeVisible({ timeout: 120_000 });

    await stage.getByTestId("chat-composer").fill("/exit");
    await stage.getByTestId("composer-send").click();
    await expect(stage.getByTestId("chat-composer")).toHaveValue("");
    await expect
      .poll(async () => (await apiGetWorkflowRun(request, planRunId)).status, { timeout: 60_000 })
      .not.toMatch(/^(running|paused|paused_on_child)$/);
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflows-tab-empty")).toBeVisible({
      timeout: 60_000,
    });
    // Catalog span stays in the transcript as history after /exit.
    await expect(span).toHaveCount(1);

    // Exit respawns the ambient run, or the next request binds one when the plan ended first.
    await sendChatPrompt(page, "back to building");
    await expect
      .poll(async () => (await apiTryGetActiveWorkflowRun(request, sessionId))?.workflow_id, { timeout: 60_000 })
      .toBe(ambient.workflow_id);
    await expect(span).toHaveCount(1);
  });
});
