import { mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { expect } from "@playwright/test";
import type { CheckpointListResponse, SourceComparison } from "../src/api/types.ts";
import {
  apiJson, apiPostPrompt, openNewChatSession, openProjectFilesFixture, webE2e,
} from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

webE2e("Review shows an outside edit and its retained versions in the chat that made it", async ({ page, request }) => {
  const folder = realpathSync(mkdtempSync(path.join(process.env.PW_TEST_HOST_HOME ?? homedir(), "paintedwolf-outside-review-")));
  const filename = "outside-note.txt";
  const destination = path.join(folder, filename);
  try {
    writeFileSync(destination, "Original notes\n");
    const { project } = await openProjectFilesFixture(page, request, {
      prefix: "outside-review",
      name: "Outside file review",
      seed: (root) => writeFileSync(path.join(root, "revised.txt"), "Updated notes\n"),
    });
    const session = page.locator('[data-testid="focused-session-list"] [data-session-id]');
    await expect(session).toHaveCount(1);
    const sessionId = (await session.getAttribute("data-session-id"))!;
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
        approval = response.checkpoints.find((item) => item.tool_approval?.plan?.subject?.kind === "write_root_set");
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
      const pending = await apiJson<{ pending: boolean; id: string }>(request, "GET", "/harness/llm/pending");
      if (pending.pending) await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: completion });
    }
    const openReview = () => page.evaluate((projectId) => {
      const harness = (window as unknown as { __harness: { openReviewLens(id: string, scope: { kind: string }): void } }).__harness;
      harness.openReviewLens(projectId, { kind: "session" });
    }, project.id);
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
    await expect(editor).not.toBeEditable();

    await page.getByRole("button", { name: "Back to chat", exact: true }).click();
    await openNewChatSession(page);
    await openReview();
    await expect(page.getByTestId("review-lens-empty")).toBeVisible();
    await expect(row).toHaveCount(0);
  } finally {
    rmSync(folder, { recursive: true, force: true });
  }
});
