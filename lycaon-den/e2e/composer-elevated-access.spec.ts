import { expect, test } from "@playwright/test";
import type { CheckpointListResponse } from "../src/api/types.ts";
import { activateProject, apiConfig, apiFindHarnessProject, apiSeedUntrustedContent, liveChatStage, openNewChatSession, waitHarnessConnected, webE2e } from "./helpers.ts";

webE2e("composer opens project approvals to review and revoke elevated access", async ({ page, request }, testInfo) => {
  test.setTimeout(180_000);
  await page.goto("/");
  await waitHarnessConnected(page);
  const project = await apiFindHarnessProject(request);
  await activateProject(page, project.id);
  const sessionId = await openNewChatSession(page);
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const stage = liveChatStage(page);
  const lock = stage.getByTestId("composer-elevated-access");
  await expect(lock).toHaveCount(0);

  const inject = async () => {
    const response = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers, data: { session_id: sessionId, command: "psql -h db.example.test", direct_ip: true, declared_destinations: ["db.example.test:5432"] },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    const { checkpoint_id: checkpointId } = await response.json() as { checkpoint_id: string };
    const pending = await request.get(`${apiUrl}/v1/sessions/${sessionId}/checkpoints`, { headers });
    expect(pending.ok(), await pending.text()).toBeTruthy();
    const checkpoints = await pending.json() as CheckpointListResponse;
    const checkpoint = checkpoints.checkpoints.find((entry) => entry.id === checkpointId);
    const optionId = checkpoint?.tool_approval?.plan?.recommended_option_id;
    expect(optionId).toBeTruthy();
    const card = stage.getByTestId("tool-approval-card");
    await expect(card).toBeVisible();
    await expect(card.getByTestId("approval-elevated-access")).toBeVisible();
    await card.screenshot({ path: testInfo.outputPath("approval-ask-expanded.png") });
    await card.getByTestId("approval-card-minimize").click();
    await expect(card.getByTestId("approval-elevated-access")).toBeVisible();
    await card.screenshot({ path: testInfo.outputPath("approval-ask-minimized.png") });
    await card.getByTestId("approval-card-expand").click();
    const approve = await request.post(`${apiUrl}/v1/sessions/${sessionId}/checkpoints/${checkpointId}`, {
      headers, data: { kind: "tool_approval", action: "approve", option_id: optionId },
    });
    expect(approve.ok(), await approve.text()).toBeTruthy();
    await expect(lock).toBeVisible({ timeout: 30_000 });
  };
  const readSummary = async () => {
    const response = await request.get(`${apiUrl}/v1/sessions/${sessionId}/elevated-access`, { headers });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json() as Promise<{ total: number; approvals_enabled: boolean }>;
  };
  const setNeverAsk = async (neverAsk: boolean) => {
    const response = await request.patch(`${apiUrl}/v1/settings/approvals`, { headers, data: { never_ask: neverAsk } });
    expect(response.ok(), await response.text()).toBeTruthy();
  };

  await inject();
  await expect(lock).toHaveAttribute("aria-label", /Review 1 elevated-access approval/);
  await apiSeedUntrustedContent(request, sessionId);
  // The provenance fixture changes storage without publishing a session update.
  await page.reload();
  await waitHarnessConnected(page);
  const globe = stage.getByTestId("external-content-badge");
  await expect(globe).toBeVisible();
  const cluster = stage.getByTestId("composer-status-cluster");
  expect(await cluster.locator("[data-status-id]").evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-status-id"))))
    .toEqual(["external", "elevated-access"]);
  const globeBox = await globe.boundingBox();
  const lockBox = await lock.boundingBox();
  expect(globeBox && lockBox && lockBox.x > globeBox.x && Math.abs(lockBox.y - globeBox.y) < 2).toBeTruthy();

  await lock.click({ trial: true });
  await expect(page.getByTestId("shell-workspace-veil")).toHaveCSS("opacity", "0");
  await page.screenshot({ path: testInfo.outputPath("elevated-access.png") });

  try {
    await setNeverAsk(true);
    await expect(lock).toHaveCount(0);
    expect((await readSummary()).approvals_enabled).toBe(false);
    await setNeverAsk(false);
    await expect(lock).toBeVisible();
    expect((await readSummary()).total).toBe(1);
    await lock.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByTestId("project-context-view")).toBeVisible();
    await expect(page.getByTestId("approvals-panel-saved")).toBeVisible();
    await expect(page.getByTestId("saved-approvals-band-elevated")).toBeVisible();
    expect((await readSummary()).total).toBe(1);
    await page.screenshot({ path: testInfo.outputPath("project-approvals-elevated.png") });
    await page.getByTestId("saved-approvals-revoke-elevated").click();
    await page.getByRole("alertdialog", { name: "Revoke elevated access" }).getByRole("button", { name: "Revoke" }).click();
    await expect(page.getByTestId("saved-approvals-action-notice")).toContainText("approvals revoked");
    expect((await readSummary()).total).toBe(0);
    await page.getByRole("button", { name: "Close project configuration" }).click();
    await expect(lock).toHaveCount(0);
  } finally {
    await setNeverAsk(false);
  }
});
