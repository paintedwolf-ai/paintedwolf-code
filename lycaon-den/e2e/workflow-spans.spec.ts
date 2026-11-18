import { expect } from "@playwright/test";
import {
  apiGetActiveWorkflowRun,
  bootstrapChatSession,
  liveChatStage,
  openWorkflowsTab,
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
    // Exit either resumes the ambient parent or respawns a fresh ambient run.
    await expect
      .poll(async () => (await apiGetActiveWorkflowRun(request, sessionId)).workflow_id, { timeout: 60_000 })
      .toBe(ambient.workflow_id);
    await openWorkflowsTab(page);
    await expect(page.getByTestId("workflows-tab-empty")).toBeVisible({
      timeout: 60_000,
    });
    // Catalog span stays in the transcript as history after /exit.
    await expect(span).toHaveCount(1);
  });
});
