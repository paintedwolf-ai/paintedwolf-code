import { mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { expect } from "@playwright/test";
import type { OpenedOutsideFile } from "../src/api/types.ts";
import { apiJson, modelIndependentWebE2e, openNewChatSession, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("Outside locations follow the chat while a dirty file stays open", async ({ page, request }) => {
  const folder = realpathSync(mkdtempSync(path.join(process.env.PW_TEST_HOST_HOME ?? homedir(), "pw-outside-locations-")));
  const filename = "outside-notes.txt";
  const outside = path.join(folder, filename);
  try {
    writeFileSync(outside, "Original notes\n");
    const { project } = await openProjectFilesFixture(page, request, {
      prefix: "outside-locations",
      name: "Outside file locations",
      seed: (root) => writeFileSync(path.join(root, "project.txt"), "Project notes\n"),
    });
    const chat = page.locator('[data-testid="focused-session-list"] [data-session-id]');
    await expect(chat).toHaveCount(1);
    const session = await chat.getAttribute("data-session-id");
    expect(session).toBeTruthy();
    const picked = await apiJson<OpenedOutsideFile>(request, "POST", `/v1/projects/${project.id}/source/outside-files`, {
      session_id: session,
      path: outside,
    });

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

    await page.getByRole("button", { name: "Back to chat", exact: true }).click();
    expect(await openNewChatSession(page)).not.toBe(session);
    await page.getByTestId("project-files-entry").click();
    await expect(tree.getByTestId("files-tree-file").filter({ hasText: "project.txt" })).toBeVisible();
    await expect(outsideLocation).toHaveCount(0);
    await expect(content).toContainText("Unsaved notes");
    expect(readFileSync(outside, "utf8")).toBe("Original notes\n");

    await save.click();
    await expect.poll(() => readFileSync(outside, "utf8")).toBe("Unsaved notes");
  } finally {
    rmSync(folder, { recursive: true, force: true });
  }
});
