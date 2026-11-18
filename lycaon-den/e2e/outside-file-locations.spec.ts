import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import type { OpenedOutsideFile, Session } from "../src/api/types.ts";
import {
  activateProject,
  apiCreateProjectWithRoot,
  apiJson,
  e2eTempDir,
  e2eUniqueLabel,
  gotoShell,
  modelIndependentWebE2e,
  openSessionRow,
} from "./helpers.ts";

modelIndependentWebE2e("Outside locations follow the chat while a dirty file stays open", async ({ page, request }) => {
  const filename = "outside-notes.txt";
  const outside = path.join(e2eTempDir(request, "outside-locations"), filename);
  const root = e2eTempDir(request, "outside-locations-project");
  writeFileSync(outside, "Original notes\n");
  writeFileSync(path.join(root, "project.txt"), "Project notes\n");
  const project = await apiCreateProjectWithRoot(request, root, e2eUniqueLabel("Outside file locations"));
  const createChat = () => apiJson<Session>(request, "POST", "/v1/sessions", {
    project_id: project.id,
    posture: "build",
  });
  const first = await createChat();
  const second = await createChat();
  const picked = await apiJson<OpenedOutsideFile>(request, "POST", `/v1/projects/${project.id}/source/outside-files`, {
    session_id: first.id,
    path: outside,
  });
  await gotoShell(page);
  await activateProject(page, project.id);
  await openSessionRow(page, first.id);
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

  await page.getByTestId("files-back").click();
  await openSessionRow(page, second.id);
  await page.getByTestId("project-files-entry").click();
  await expect(tree.getByTestId("files-tree-file").filter({ hasText: "project.txt" })).toBeVisible();
  await expect(outsideLocation).toHaveCount(0);
  await expect(content).toContainText("Unsaved notes");
  expect(readFileSync(outside, "utf8")).toBe("Original notes\n");

  await save.click();
  await expect.poll(() => readFileSync(outside, "utf8")).toBe("Unsaved notes");
});
