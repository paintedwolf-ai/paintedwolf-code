import { chmodSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { expect, type APIRequestContext, type Page } from "@playwright/test";
import type { CheckpointListResponse, SourceWalkResponse, SourceWorkspace, ToolResult } from "../../src/api/types.ts";
import {
  apiConfig, apiPostPrompt, bootstrapActiveProject, e2eTempDir, e2eUniqueLabel,
  liveChatStage, settledChatSessionId, waitForChatComposerReady, webE2e,
} from "../helpers.ts";
import { manualHarnessTools } from "../manual-harness-tools.ts";

function outsideFixture(request: APIRequestContext) {
  const folder = process.env.LYCAON_E2E_DOCKER_PROJECT
    ? e2eTempDir(request, "outside-command")
    : mkdtempSync(path.join(homedir(), "paintedwolf-outside-command-"));
  return realpathSync(folder);
}

function quotePath(value: string): string {
  return "'" + value.replaceAll("'", "'\\''") + "'";
}

async function allowOutsideCommand(request: APIRequestContext, sessionId: string, callId: string): Promise<ToolResult> {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const session = `${apiUrl}/v1/sessions/${sessionId}`;
  const approved = new Set<string>();
  let result: ToolResult | undefined;
  await expect.poll(async () => {
    const messages = await request.get(`${session}/messages`, { headers });
    expect(messages.ok(), await messages.text()).toBeTruthy();
    result = (await messages.json() as { messages: { tool_result?: ToolResult }[] }).messages
      .find(({ tool_result }) => tool_result?.tool_call_id === callId)?.tool_result;
    if (result) return true;
    const pending = await request.get(`${session}/checkpoints?kind=tool_approval`, { headers });
    expect(pending.ok(), await pending.text()).toBeTruthy();
    for (const checkpoint of (await pending.json() as CheckpointListResponse).checkpoints) {
      if (checkpoint.status !== "pending" || approved.has(checkpoint.id)) continue;
      const { tool_approval: approval } = checkpoint;
      if (approval?.tool_call_id !== callId || !approval.plan?.recommended_option_id) continue;
      expect(approval.plan.subject.kind).not.toBe("host_execution");
      const response = await request.post(`${session}/checkpoints/${checkpoint.id}`, {
        headers, data: { kind: "tool_approval", action: "approve", option_id: approval.plan.recommended_option_id },
      });
      expect(response.ok(), await response.text()).toBeTruthy();
      approved.add(checkpoint.id);
    }
    return false;
  }, { timeout: 60_000 }).toBe(true);
  expect(result?.outcome, result?.content).toBe("completed");
  return result!;
}

async function openChatReview(page: Page, projectId: string) {
  await page.evaluate((id) => {
    const h = (window as unknown as { __harness: { openReviewLens(id: string, scope: { kind: "session" }): unknown } }).__harness;
    h.openReviewLens(id, { kind: "session" });
  }, projectId);
  await expect(page.getByTestId("review-lens")).toBeVisible();
}

webE2e("outside command changes reach chat Files and Review with explicit coverage gaps", async ({ page, request }) => {
  const root = e2eTempDir(request, "outside-command-project");
  const observed = outsideFixture(request);
  const unreadable = outsideFixture(request);
  const file = "notes.txt";
  const changedPath = path.join(observed, file);
  const newContents = "Updated by a confined command.\n";
  writeFileSync(path.join(root, file), newContents);
  writeFileSync(changedPath, "Original contents.\n");
  writeFileSync(path.join(unreadable, "other.txt"), "Coverage fixture.\n");
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  try {
    const project = await bootstrapActiveProject(page, request, root, e2eUniqueLabel("Outside command review"));
    await waitForChatComposerReady(page);
    const sessionId = await settledChatSessionId(page);
    await expect.poll(async () => {
      const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/workspace`, { headers });
      expect(response.ok(), await response.text()).toBeTruthy();
      return (await response.json() as SourceWorkspace).inventory.complete;
    }, { timeout: 30_000 }).toBe(true);
    const manual = await request.post(`${apiUrl}/harness/llm/auto`, { headers, data: { enabled: false } });
    expect(manual.ok(), await manual.text()).toBeTruthy();
    const prompt = await apiPostPrompt(request, sessionId, { text: "Copy the project note to the outside fixture, then test observation of a second folder." });
    expect(prompt.ok(), await prompt.text()).toBeTruthy();
    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("command", "update_progress");
    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [ ] Copy the project note\n- [ ] Check observation of the second folder\n",
    }));
    const copy = await driver.invoke("command", {
      command: `external -- cp ${file} ${quotePath(changedPath)}`,
      capability_request: { write_root: observed },
    });
    await allowOutsideCommand(request, sessionId, copy);
    expect(readFileSync(changedPath, "utf8")).toBe(newContents);

    const chmod = await driver.invoke("command", {
      command: `external -- chmod 000 ${quotePath(unreadable)}`,
      capability_request: { write_root: unreadable },
    });
    const gap = await allowOutsideCommand(request, sessionId, chmod);
    expect(statSync(unreadable).mode & 0o777).toBe(0);
    const resume = await request.post(`${apiUrl}/harness/llm/auto`, { headers, data: { enabled: true } });
    expect(resume.ok(), await resume.text()).toBeTruthy();
    expect(gap.card_facts).toContainEqual(expect.objectContaining({
      kind: "source_observation",
      source_coverage: expect.arrayContaining([expect.objectContaining({
        folder: unreadable, coverage: "not_observed", reason: "io_error",
      })]),
    }));
    const activity = liveChatStage(page).getByTestId("activity-span-card").last();
    await expect(activity).toBeVisible();
    if (await activity.getAttribute("data-expanded") !== "true") {
      await activity.locator(":scope > .den-transcript-disclosure-header").click();
    }
    const action = activity.locator(`[data-testid="action-card"][data-tool-call-id="${chmod}"]`).last();
    await expect(action).toContainText("File observation incomplete");
    if (await action.getAttribute("data-expanded") !== "true") {
      await action.locator(":scope > .den-transcript-disclosure-header").click();
    }
    await expect(action).toContainText(`${unreadable} was not observed`);

    const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/walk`, {
      headers, params: { baseline: `session:${sessionId}`, limit: 100 },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    const review = await response.json() as SourceWalkResponse;
    const recorded = review.files.find((entry) => entry.path === changedPath.slice(1));
    expect(recorded?.effects).toContainEqual(expect.objectContaining({
      command_id: expect.any(String), cause: "command_window", tool_call_id: copy,
    }));
    expect(review.commands.find((window) => window.tool_call_id === chmod)?.coverage).toContainEqual(
      expect.objectContaining({ path: unreadable.slice(1), coverage: "not_observed", reason: "io_error" }),
    );

    await page.getByTestId("project-files-entry").click();
    await openChatReview(page, project.id);
    await expect(page.getByTestId("changes-row-file").filter({ hasText: file })).toBeVisible();
    await expect(page.getByTestId("review-command-coverage").filter({ hasText: unreadable })).toBeVisible();

    await page.getByTestId("files-pane-files").click();
    await expect(page.getByTestId("files-tree-outside-tag")).toBeVisible();
    const location = page.getByRole("button", { name: new RegExp(`^(Expand|Collapse) ${path.basename(observed)}$`) });
    const locationId = await location.getAttribute("data-root");
    expect(locationId).toBeTruthy();
    if (await location.getAttribute("aria-expanded") !== "true") await location.click();
    const row = page.locator(`[data-testid="files-tree-file"][data-root="${locationId}"]`).filter({ hasText: file });
    await expect(row.getByTestId("files-tree-scope-mark")).toHaveAttribute("data-kind", "changed");
    await row.click();
    await expect(page.getByTestId("files-editor-host").filter({ visible: true })).toContainText(newContents.trim());
  } finally {
    await request.post(`${apiUrl}/harness/llm/auto`, { headers, data: { enabled: true } });
    chmodSync(unreadable, 0o700);
    for (const folder of [observed, unreadable]) rmSync(folder, { recursive: true, force: true });
  }
});
