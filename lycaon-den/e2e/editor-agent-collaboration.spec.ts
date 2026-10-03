import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import * as Y from "yjs";
import type { EditorDocument } from "../src/api/types.ts";
import { apiJson, apiPostPrompt, openProjectFilesFixture, webE2e } from "./helpers.ts";

/** The accepted text a wire document carries inside its CRDT state. */
function documentText(document: EditorDocument): string {
  const state = new Y.Doc();
  Y.applyUpdate(state, new Uint8Array(Buffer.from(document.crdt_update, "base64")));
  const text = state.getText("text").toString();
  state.destroy();
  return text;
}
import { manualHarnessTools } from "./manual-harness-tools.ts";

webE2e("agent reads unsaved text and edits its pinned snapshot while the person keeps typing", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-agent", name: "Editor agent collaboration",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), "First line\nAgent target\nLast line\n"),
  });
  const sessionRow = page.locator('[data-testid="focused-session-list"] [data-session-id]');
  await expect(sessionRow).toHaveCount(1);
  const sessionId = (await sessionRow.getAttribute("data-session-id"))!;
  expect(sessionId).toBeTruthy();
  await page.getByTestId("files-tree-file").filter({ hasText: "shared.txt" }).click();
  const editor = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
  await expect(editor).toBeEditable({ timeout: 15_000 });
  await editor.click();
  await page.keyboard.press("ControlOrMeta+Home");
  await page.keyboard.insertText("Unsaved person 🐺\n");
  const documentUrl = `/v1/projects/${project.id}/editor-documents`;
  const readDocument = () => apiJson<EditorDocument>(request, "POST", documentUrl,
    { path: "shared.txt", root_id: project.roots[0]!.id, client_id: "fixture-observer" });
  await expect.poll(async () => documentText(await readDocument())).toContain("Unsaved person 🐺");
  expect(readFileSync(path.join(root, "shared.txt"), "utf8")).not.toContain("Unsaved person");
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  try {
    const prompt = await apiPostPrompt(request, sessionId, { text: "Read shared.txt and replace Agent target with Agent changed, preserving the person's edits." });
    expect(prompt.ok(), await prompt.text()).toBe(true);
    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("read", "edit", "write", "update_progress");
    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [ ] Update shared.txt while preserving the person's edits\n- [ ] Verify the shared document\n",
    }));
    const read = await driver.completed(await driver.invoke("read", { path: "shared.txt" }));
    expect(read).toContain("Unsaved person 🐺");
    await editor.click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.insertText("Typed after agent read 世界\n");
    await expect.poll(async () => documentText(await readDocument())).toContain("Typed after agent read 世界");
    const edit = await driver.completed(await driver.invoke("edit", {
      path: "shared.txt", old_string: "Agent target", new_string: "Agent changed",
    }));
    expect(edit).toContain("shared editor document");
    await expect(editor).toContainText("Agent changed", { timeout: 15_000 });
    await expect(editor).toContainText("Unsaved person 🐺");
    await expect(editor).toContainText("Typed after agent read 世界");
    await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8"))
      .toBe("Unsaved person 🐺\nFirst line\nAgent changed\nLast line\nTyped after agent read 世界\n");
    await driver.completed(await driver.invoke("read", { path: "shared.txt" }));
    const current = documentText(await readDocument());
    await driver.completed(await driver.invoke("write", {
      path: "shared.txt", content: current.replace("Last line", "Whole-file writer"),
    }));
    await expect(editor).toContainText("Whole-file writer");
    await expect(editor).toContainText("Unsaved person 🐺");
    await expect.poll(() => readFileSync(path.join(root, "shared.txt"), "utf8"))
      .toBe(current.replace("Last line", "Whole-file writer"));
    await editor.click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.insertText("Person after both agent changes\n");
    await page.getByRole("button", { name: "File version: Current", exact: true }).click();
    const history = page.getByRole("group", { name: "Recent edits", exact: true });
    await expect(history.getByRole("button", { name: "Undo", exact: true })).toHaveCount(2);
    await history.getByRole("button", { name: "Undo", exact: true }).first().click();
    await expect(editor).toContainText("Last line");
    await expect(editor).not.toContainText("Whole-file writer");
    await expect(editor).toContainText("Agent changed");
    await expect(editor).toContainText("Person after both agent changes");
    await expect(editor).toBeFocused();
    await page.keyboard.press("ControlOrMeta+z");
    await expect(editor).toContainText("Whole-file writer");
    await expect(editor).toContainText("Person after both agent changes");
    await editor.press("ControlOrMeta+Shift+z");
    await expect(editor).toContainText("Last line");
    await expect(editor).not.toContainText("Whole-file writer");
    await expect(editor).toContainText("Person after both agent changes");
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Collaboration verification complete." });
    const pending = await apiJson<{ pending: boolean; id: string }>(request, "GET", "/harness/llm/pending");
    if (pending.pending) await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Collaboration verification complete." });
  }
});
