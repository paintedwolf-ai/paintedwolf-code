import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import type { OpenedOutsideFile } from "../../src/api/types.ts";
import {
  apiConfig,
  apiJson,
  bootstrapActiveProject,
  e2eTempDir,
  e2eUniqueLabel,
  liveChatStage,
  sendChatPrompt,
  settledChatSessionId,
  waitForChatComposerReady,
  webE2e,
} from "../helpers.ts";

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

  await page.getByTestId("new-chat-btn").click();
  await waitForChatComposerReady(page);
  expect(await settledChatSessionId(page)).not.toBe(first);
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
