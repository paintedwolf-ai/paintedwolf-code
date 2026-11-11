import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import {
  E2E_FIXTURE_PROJECT,
  activateProject,
  apiCreateProjectWithRoot,
  bootstrapActiveProject,
  e2eUniqueLabel,
  gotoShell,
  liveChatStage,
  modelIndependentWebE2e,
  openNewChatSession,
  webE2e,
} from "./helpers.ts";

// Adding a root restores file access after the last root is detached.
webE2e.describe("nofolder banner", () => {
  webE2e("den:harness — NoFolderBanner → Add folder → rooted project", async ({
    page,
    request,
  }) => {
    const project = await bootstrapActiveProject(page, request);
    const rootId = project.roots?.[0]?.id;
    expect(rootId).toBeTruthy();

    await openNewChatSession(page);
    const chat = liveChatStage(page);
    await expect(chat.getByTestId("nofolder-banner")).toHaveCount(0);

    // Detach lives in the expanded rows; the facepile is the collapsed summary.
    await page.getByTestId("project-folder-summary").click();
    await page.getByTestId(`project-folder-remove-${rootId}`).click();
    await page.getByRole("button", { name: "Remove folder", exact: true }).click();
    await expect(page.locator(".project-folders--empty")).toBeVisible({
      timeout: 30_000,
    });
    await expect(chat.getByTestId("nofolder-banner")).toBeVisible({ timeout: 30_000 });
    await expect(chat.getByTestId("nofolder-add")).toBeVisible();

    await chat.getByTestId("nofolder-add").click();
    await page.getByRole("textbox", { name: "Folder path", exact: true }).fill(E2E_FIXTURE_PROJECT);
    await page.getByRole("button", { name: "Choose folder", exact: true }).click();

    await expect(chat.getByTestId("nofolder-banner")).toHaveCount(0, { timeout: 30_000 });
    await expect(page.locator(".project-folders--empty")).toHaveCount(0);
    await expect(page.getByTestId("project-folder-zone")).toContainText(
      path.basename(E2E_FIXTURE_PROJECT),
      { timeout: 30_000 },
    );
    await expect(page.getByTestId("project-files-entry")).toBeEnabled({
      timeout: 30_000,
    });
  });
});

// The harness state directory holds the engine's own state, so the host
// reports it as an editor-only root: the person edits there, agents don't.
modelIndependentWebE2e("an editor-only root is fully editable and says agents don't work there", async ({ page, request }, testInfo) => {
  const stateDir = process.env.LYCAON_E2E_STATE_DIR?.trim();
  if (!stateDir) throw new Error("LYCAON_E2E_STATE_DIR is required for this spec");
  const folder = mkdtempSync(path.join(stateDir, "editor-only-"));
  testInfo.annotations.push({ type: "fixture", description: folder });
  try {
    writeFileSync(path.join(folder, "notes.txt"), "Original\n");
    const project = await apiCreateProjectWithRoot(request, stateDir, e2eUniqueLabel("Editor-only root"));
    const root = project.roots[0];
    expect(root?.admitted).toBe(false);
    expect(root?.admission_code).toBe("write_root_contains_control_plane");

    await gotoShell(page);
    await activateProject(page, project.id);
    await page.getByTestId("project-files-entry").click();
    await expect(page.getByTestId("files-root-summary-editor-only")).toBeVisible({ timeout: 15_000 });

    const name = path.basename(folder);
    await page.locator(`[data-files-ctx="tree-row"][data-path=${JSON.stringify(name)}]`).first().click();
    const file = page.getByTestId("files-tree-file").filter({ hasText: "notes.txt" });
    await file.dblclick();
    const editor = page.locator('[data-testid="files-editor-host"] .cm-content:visible');
    await expect(editor).toBeEditable();
    await editor.click();
    await editor.press("ControlOrMeta+End");
    await page.keyboard.insertText("Edited in the editor\n");
    await editor.press("ControlOrMeta+s");
    await expect.poll(() => readFileSync(path.join(folder, "notes.txt"), "utf8")).toBe("Original\nEdited in the editor\n");

    await page.locator(`[data-files-ctx="tree-row"][data-path=${JSON.stringify(`${name}/notes.txt`)}]`).first().click({ button: "right" });
    await page.getByTestId("files-ctx-trash").click();
    await page.getByTestId("files-trash-confirm").click();
    await expect.poll(() => existsSync(path.join(folder, "notes.txt"))).toBe(false);
    await page.screenshot({ path: testInfo.outputPath("editor-only-root.png"), animations: "disabled" });
  } finally {
    rmSync(folder, { recursive: true, force: true });
  }
});
