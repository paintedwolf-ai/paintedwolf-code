import { expect } from "@playwright/test";
import {
  apiConfig, bootstrapChatSession, liveChatStage, settledChatSessionId, webE2e,
} from "./helpers.ts";
import type { CheckpointListResponse } from "../src/api/types.ts";

webE2e("file approval opens its proposed diff in Files without resolving the ask", async ({ page, request }) => {
  // A fresh project keeps earlier specs' sessions and approvals out of this chat.
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const injected = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
    headers,
    data: {
      session_id: sessionId,
      title: "Review instruction change",
      file_changes: [
        { path: "AGENTS.md", operation: "write", before: "Original instruction\n", after: "Proposed instruction\n", before_bytes: 21, after_bytes: 21 },
        { path: `${Array(8).fill("deeply-nested-folder").join("/")}/notes.md`, operation: "create", before: "", after: "Notes", before_bytes: 0, after_bytes: 5 },
      ],
    },
  });
  expect(injected.ok(), await injected.text()).toBeTruthy();
  const { checkpoint_id: checkpointId } = await injected.json() as { checkpoint_id: string };
  const card = () => liveChatStage(page).locator(`[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`);
  await expect(card()).toBeVisible();
  await expect(page.getByTestId("shell-workspace-veil")).toHaveAttribute("aria-hidden", "true");
  await expect(card().getByTestId("approval-approve-primary")).toBeVisible();
  await expect(card().getByTestId("approval-no")).toBeVisible();
  expect(await card().evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
  await page.screenshot({ path: "/tmp/approval-file-change-card.png", animations: "disabled" });
  await card().getByRole("button", { name: "View diff · AGENTS.md", exact: true }).click();
  await expect(page.getByTestId("project-files-stage-boundary")).toHaveAttribute("data-ready", "true");
  await expect(page.getByText("Proposed file change", { exact: true })).toBeVisible();
  await expect(page.locator(".cm-content").filter({ hasText: "Proposed instruction" }).first()).toBeVisible();
  const pending = await request.get(`${apiUrl}/v1/sessions/${sessionId}/checkpoints?kind=tool_approval`, { headers });
  expect(pending.ok()).toBeTruthy();
  expect((await pending.json() as CheckpointListResponse).checkpoints.map((checkpoint) => checkpoint.id)).toContain(checkpointId);
  await page.screenshot({ path: "/tmp/approval-file-change.png", animations: "disabled" });
  await page.getByRole("button", { name: "Back to chat", exact: true }).click();
  await expect(card()).toBeVisible();
  await card().getByTestId("approval-approve-primary").click();
  await expect(card()).toHaveCount(0);
  const resolved = await request.get(`${apiUrl}/v1/sessions/${sessionId}/checkpoints?kind=tool_approval`, { headers });
  expect(resolved.ok()).toBeTruthy();
  expect((await resolved.json() as CheckpointListResponse).checkpoints.map((checkpoint) => checkpoint.id)).not.toContain(checkpointId);
});
