import { mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { expect } from "@playwright/test";
import type { CheckpointListResponse, OpenedOutsideFile } from "../../src/api/types.ts";
import {
  apiConfig,
  apiJson,
  apiPostPrompt,
  bootstrapActiveProject,
  e2eTempDir,
  e2eUniqueLabel,
  liveChatStage,
  openNewChatSession,
  sendChatPrompt,
  settledChatSessionId,
  waitForChatComposerReady,
  webE2e,
} from "../helpers.ts";
import { manualHarnessTools } from "../manual-harness-tools.ts";

webE2e("Outside locations follow the chat while a dirty file stays open", async ({ page, request }) => {
  const filename = "outside-notes.txt";
  const outside = path.join(e2eTempDir(request, "outside-locations"), filename);
  const root = e2eTempDir(request, "outside-locations-project");
  writeFileSync(outside, "Original notes\n");
  writeFileSync(path.join(root, "project.txt"), "Project notes\n");
  const project = await bootstrapActiveProject(page, request, root, e2eUniqueLabel("Outside file locations"));
  await sendChatPrompt(page, "Keep these notes handy.");
  await expect(liveChatStage(page).getByText("Harness reply.", { exact: true })).toBeVisible();
  const first = await settledChatSessionId(page);
  const picked = await apiJson<OpenedOutsideFile>(request, "POST", `/v1/projects/${project.id}/source/outside-files`, {
    session_id: first,
    path: outside,
  });
  await page.getByTestId("project-files-entry").click();

  const tree = page.getByTestId("files-tree");
  const outsideLocation = tree.locator(`[data-testid="files-tree-dir"][data-root="${picked.location.id}"]`);
  await expect(outsideLocation).toContainText("Outside project");
  await expect(outsideLocation.getByTestId("files-tree-dir-toggle")).toHaveAttribute("aria-expanded", "false");
  await outsideLocation.getByTestId("files-tree-dir-toggle").click();
  await tree.getByTestId("files-tree-file").filter({ hasText: filename }).dblclick();

  const content = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
  await expect(content).toContainText("Original notes");
  await content.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText("Unsaved notes");
  const save = page.getByTestId("files-editor-save");
  await expect(save).toBeEnabled();

  expect(await openNewChatSession(page)).not.toBe(first);
  await page.getByTestId("project-files-entry").click();
  await expect(tree.getByTestId("files-tree-file").filter({ hasText: "project.txt" })).toBeVisible();
  await expect(outsideLocation).toHaveCount(0);
  await expect(content).toContainText("Unsaved notes");
  expect(readFileSync(outside, "utf8")).toBe("Original notes\n");

  const { apiUrl, token } = apiConfig();
  const deleted = await request.delete(`${apiUrl}/v1/sessions/${first}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(deleted.status(), await deleted.text()).toBe(204);
  await expect(content).toContainText("Unsaved notes");

  await save.click();
  await expect.poll(() => readFileSync(outside, "utf8")).toBe("Unsaved notes");
});

webE2e("An outside text edit opens the recorded file from chat", async ({ page, request }) => {
  const folder = realpathSync(process.env.LYCAON_E2E_DOCKER_STACK === "1"
    ? e2eTempDir(request, "outside-diff-path")
    : mkdtempSync(path.join(process.env.PW_TEST_HOST_HOME ?? homedir(), "paintedwolf-outside-diff-")));
  const destination = path.join(folder, "outside-report.txt");
  try {
    writeFileSync(destination, "Original report\n");
    const root = e2eTempDir(request, "outside-diff-project");
    const project = await bootstrapActiveProject(page, request, root, e2eUniqueLabel("Outside edit link"));
    await waitForChatComposerReady(page);
    const sessionId = await settledChatSessionId(page);
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
    try {
      const prompt = await apiPostPrompt(request, sessionId, { text: "Update the report." });
      expect(prompt.ok(), await prompt.text()).toBe(true);
      const driver = manualHarnessTools(request, sessionId);
      await driver.loadTools("command");
      const quoted = "'" + destination.replaceAll("'", "'\\''") + "'";
      const call = await driver.invoke("command", {
        command: `cat > ${quoted} <<'REPORT'\nUpdated report\nREPORT`,
      });
      const checkpoints = `/v1/sessions/${sessionId}/checkpoints`;
      let approval: CheckpointListResponse["checkpoints"][number] | undefined;
      await expect.poll(async () => {
        const response = await apiJson<CheckpointListResponse>(request, "GET", `${checkpoints}?kind=tool_approval`);
        approval = response.checkpoints.find((item) =>
          item.tool_approval?.plan?.reasons.includes("outside_roots_write"));
        return approval?.tool_approval?.plan?.recommended_option_id;
      }, { timeout: 30_000 }).toBeTruthy();
      await apiJson(request, "POST", `${checkpoints}/${approval!.id}`, {
        kind: "tool_approval", action: "approve", option_id: approval!.tool_approval!.plan!.recommended_option_id,
      });
      await driver.completed(call);
    } finally {
      await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "The report is updated." });
    }

    const group = liveChatStage(page).getByTestId("diff-group");
    await group.getByTestId("diff-group-files").click();
    const card = group.getByTestId("file-edit-diff").filter({ hasText: destination });
    const disclosure = card.getByTestId("file-edit-diff-toggle");
    await expect(disclosure).toHaveAttribute("aria-expanded", "true");
    await disclosure.click();
    await expect(disclosure).toHaveAttribute("aria-expanded", "false");
    const open = page.waitForResponse((response) => {
      const request = response.request();
      return new URL(response.url()).pathname === `/v1/projects/${project.id}/editor-documents` &&
        request.method() === "POST" && Boolean(request.postDataJSON().file_id);
    });
    await card.getByTestId("source-path-link").click();
    expect((await open).ok()).toBe(true);
    const editor = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
    await expect(editor).toContainText("Updated report");
  } finally {
    rmSync(folder, { recursive: true, force: true });
  }
});
