import { expect, test, type Page } from "@playwright/test";
import type { CheckpointListResponse } from "../src/api/types.ts";
import {
  activateProject,
  apiConfig,
  bootstrapChatSession,
  expectShellReady,
  liveChatStage,
  openSessionRow,
  seedAppState,
  settledChatSessionId,
} from "./helpers.ts";

async function reloadToSession(page: Page, projectId: string, sessionId: string) {
  await page.reload();
  await expect(page.getByTestId("shell")).toHaveAttribute("data-sidecar-status", "connected", { timeout: 60_000 });
  await expectShellReady(page);
  if (await page.getByTestId("home-nav").isVisible().catch(() => false)) {
    await activateProject(page, projectId);
  }
  const current = await settledChatSessionId(page).catch(() => "");
  if (current !== sessionId) {
    await openSessionRow(page, sessionId);
  }
  await expect.poll(() => settledChatSessionId(page), { timeout: 30_000 }).toBe(sessionId);
}

test.describe("portable command M12 desktop smoke", () => {
  test("M12: pending command tool approval checkpoint survives reload and clears cleanly when approved", async ({ page, request }) => {
    test.setTimeout(180_000);
    await seedAppState(page);
    const project = await bootstrapChatSession(page, request);
    const sessionId = await settledChatSessionId(page);
    const { apiUrl, token } = apiConfig();
    const headers = { Authorization: `Bearer ${token}` };

    const created = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers,
      data: { session_id: sessionId, command: "printf 'm12 desktop fixture'" },
    });
    expect(created.ok(), await created.text()).toBeTruthy();
    const { checkpoint_id: checkpointId } = (await created.json()) as { checkpoint_id: string };
    const card = () => liveChatStage(page).locator(`[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`);
    await expect(card()).toBeVisible({ timeout: 30_000 });

    await reloadToSession(page, project.id, sessionId);
    await expect(card()).toBeVisible({ timeout: 30_000 });

    const pendingRes = await request.get(`${apiUrl}/v1/sessions/${sessionId}/checkpoints?include_children=true`, { headers });
    expect(pendingRes.ok()).toBeTruthy();
    expect(((await pendingRes.json()) as CheckpointListResponse).checkpoints.map((cp) => cp.id)).toEqual([checkpointId]);

    const attentionRes = await request.get(`${apiUrl}/v1/attention`, { headers });
    expect(attentionRes.ok()).toBeTruthy();
    expect((await attentionRes.json()).rows).toEqual(expect.arrayContaining([
      expect.objectContaining({ session_id: sessionId, class: "needs_you", reason: "checkpoint" }),
    ]));

    await card().getByTestId("approval-approve-primary").click();
    await expect(card()).toHaveCount(0, { timeout: 30_000 });

    await reloadToSession(page, project.id, sessionId);
    await expect(card()).toHaveCount(0);

    const bootstrapRes = await request.get(`${apiUrl}/v1/sessions/${sessionId}/bootstrap`, { headers });
    expect(bootstrapRes.ok()).toBeTruthy();
    expect((await bootstrapRes.json()).checkpoints).toEqual([]);

    console.log("M12_DESKTOP_EVIDENCE:" + JSON.stringify({
      sessionId,
      checkpointId,
      survivedReload: true,
      clearedAfterApproveAndReload: true,
    }));
  });
});
