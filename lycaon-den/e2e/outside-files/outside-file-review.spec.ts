import { mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { expect } from "@playwright/test";
import type { CheckpointListResponse, SourceComparison } from "../../src/api/types.ts";
import {
  apiJson, apiPostPrompt, bootstrapActiveProject, e2eTempDir, e2eUniqueLabel,
  settledChatSessionId, waitForChatComposerReady, webE2e,
} from "../helpers.ts";
import { manualHarnessTools } from "../manual-harness-tools.ts";

webE2e("Review shows an outside edit and its retained versions in the chat that made it", async ({ page, request }) => {
  const folder = realpathSync(process.env.LYCAON_E2E_DOCKER_STACK === "1"
    ? e2eTempDir(request, "outside-review")
    : mkdtempSync(path.join(process.env.PW_TEST_HOST_HOME ?? homedir(), "paintedwolf-outside-review-")));
  const filename = "outside-note.txt";
  const destination = path.join(folder, filename);
  try {
    writeFileSync(destination, "Original notes\n");
    const root = e2eTempDir(request, "outside-review-project");
    writeFileSync(path.join(root, "revised.txt"), "Updated notes\n");
    const project = await bootstrapActiveProject(page, request, root, e2eUniqueLabel("Outside file review"));
    await waitForChatComposerReady(page);
    const sessionId = await settledChatSessionId(page);
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
    try {
      const prompt = await apiPostPrompt(request, sessionId, { text: "Update the notes using revised.txt." });
      expect(prompt.ok(), await prompt.text()).toBe(true);
      const driver = manualHarnessTools(request, sessionId);
      await driver.loadTools("command");
      const quoted = "'" + destination.replaceAll("'", "'\\''") + "'";
      const callId = await driver.invoke("command", { command: `cp revised.txt ${quoted}` });
      let approval: CheckpointListResponse['checkpoints'][number] | undefined;
      const checkpoints = `/v1/sessions/${sessionId}/checkpoints`;
      await expect.poll(async () => {
        const response = await apiJson<CheckpointListResponse>(request, "GET", `${checkpoints}?kind=tool_approval`);
        approval = response.checkpoints.find((item) =>
          item.tool_approval?.plan?.reasons.includes("outside_roots_write"));
        return approval?.tool_approval?.plan?.recommended_option_id;
      }, { timeout: 30_000 }).toBeTruthy();
      await apiJson(request, "POST", `${checkpoints}/${approval!.id}`, {
        kind: "tool_approval", action: "approve", option_id: approval!.tool_approval!.plan!.recommended_option_id,
      });
      await driver.completed(callId);
      expect(readFileSync(destination, "utf8")).toBe("Updated notes\n");
    } finally {
      const completion = "The notes are updated.";
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: completion });
    }
    const openReview = async () => {
      await page.getByTestId("project-files-entry").click();
      await expect(page.getByTestId("project-files-stage")).toBeVisible();
      await page.evaluate((projectId) => {
        const harness = (window as unknown as { __harness: { openReviewLens(id: string, scope: { kind: string }): void } }).__harness;
        harness.openReviewLens(projectId, { kind: "session" });
      }, project.id);
    };
    await openReview();

    const row = page.getByTestId("review-lens").getByTestId("changes-row-file").filter({ hasText: filename });
    await expect(row).toContainText(folder);
    const comparisonResponse = page.waitForResponse((response) =>
      response.url().includes(`/v1/projects/${project.id}/source/comparison?`) &&
      response.url().includes("file_id="));
    await row.click();
    const response = await comparisonResponse;
    expect(response.ok()).toBe(true);
    const comparison = await response.json() as SourceComparison;
    expect(comparison.before?.content).toBe("Original notes\n");
    expect(comparison.after?.content).toBe("Updated notes\n");
    const editor = page.locator(".cm-content").filter({ hasText: "Updated notes" }).filter({ visible: true });
    await expect(editor).toBeVisible();
    await page.getByTestId("file-version-trigger").click();
    const history = page.getByRole("menu", { name: "File history" });
    await expect.poll(() => history.getByRole("menuitemradio").count()).toBeGreaterThan(1);
    await history.getByRole("menuitemradio").last().click();
    const retained = page.getByTestId("file-version-file").locator(".cm-content").filter({ hasText: "Original notes" });
    await expect(retained).toBeVisible();
    await expect(retained).not.toBeEditable();

    await page.getByTestId("new-chat-btn").click();
    await expect.poll(() => settledChatSessionId(page), { timeout: 90_000 }).not.toBe(sessionId);
    await openReview();
    await expect(page.getByTestId("review-lens-empty")).toBeVisible();
    await expect(row).toHaveCount(0);
  } finally {
    rmSync(folder, { recursive: true, force: true });
  }
});
