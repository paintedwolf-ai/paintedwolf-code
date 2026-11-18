import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import * as Y from "yjs";
import type { EditorDocument } from "../src/api/types.ts";
import { apiJson, apiPostPrompt, openProjectFilesFixture, webE2e } from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

/** The accepted text a wire document carries inside its CRDT state. */
function documentText(document: EditorDocument): string {
  const state = new Y.Doc();
  Y.applyUpdate(state, new Uint8Array(Buffer.from(document.crdt_update, "base64")));
  const text = state.getText("text").toString();
  state.destroy();
  return text;
}

webE2e("a process the agent launches reads the person's unsaved text", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-launch", name: "Editor agent launch",
    seed: (root) => writeFileSync(path.join(root, "shared.txt"), "First line\nLast line\n"),
  });
  const onDisk = () => readFileSync(path.join(root, "shared.txt"), "utf8");
  const sessionRow = page.locator('[data-testid="focused-session-list"] [data-session-id]');
  await expect(sessionRow).toHaveCount(1);
  const sessionId = (await sessionRow.getAttribute("data-session-id"))!;
  expect(sessionId).toBeTruthy();
  await page.getByTestId("files-tree-file").filter({ hasText: "shared.txt" }).click();
  const editor = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
  await expect(editor).toBeEditable({ timeout: 15_000 });
  const documentUrl = `/v1/projects/${project.id}/editor-documents`;
  const readDocument = () => apiJson<EditorDocument>(request, "POST", documentUrl,
    { path: "shared.txt", root_id: project.roots[0]!.id, client_id: "fixture-observer" });
  await apiJson(request, "POST", "/harness/llm/auto", { enabled: false });
  try {
    const prompt = await apiPostPrompt(request, sessionId, { text: "Track shared.txt in Git and report what changed." });
    expect(prompt.ok(), await prompt.text()).toBe(true);
    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("update_progress");
    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [ ] Track shared.txt in Git\n- [ ] Report what changed\n",
    }));
    await driver.completed(await driver.invoke("command", { command: "git init && git add shared.txt" }));

    await editor.click();
    await page.keyboard.press("ControlOrMeta+Home");
    await page.keyboard.insertText("Unsaved person 🐺\n");
    await expect.poll(async () => documentText(await readDocument())).toContain("Unsaved person 🐺");

    // A native read serves the draft and leaves it unsaved.
    const read = await driver.completed(await driver.invoke("command", { command: "cat shared.txt" }));
    expect(read).toContain("Unsaved person 🐺");
    expect(onDisk()).toBe("First line\nLast line\n");

    // Git reads the working tree from disk, so its launch saves the draft first.
    const diff = await driver.completed(await driver.invoke("command", { command: "git diff -- shared.txt" }));
    expect(diff).toContain("+Unsaved person 🐺");
    expect(diff).not.toContain("unsaved_documents");
    expect(onDisk()).toBe("Unsaved person 🐺\nFirst line\nLast line\n");
    await expect(editor).toContainText("Unsaved person 🐺");
    expect(documentText(await readDocument())).toBe("Unsaved person 🐺\nFirst line\nLast line\n");

    // Text typed after one launch is the next launch's to save.
    await editor.click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.insertText("Typed after the launch 世界\n");
    await expect.poll(async () => documentText(await readDocument())).toContain("Typed after the launch 世界");
    expect(onDisk()).not.toContain("Typed after the launch");
    const later = await driver.completed(await driver.invoke("command", { command: "git diff -- shared.txt" }));
    expect(later).toContain("+Typed after the launch 世界");
    expect(onDisk()).toBe("Unsaved person 🐺\nFirst line\nLast line\nTyped after the launch 世界\n");
    await expect(editor).toContainText("Typed after the launch 世界");
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Launch verification complete." });
    const pending = await apiJson<{ pending: boolean; id: string }>(request, "GET", "/harness/llm/pending");
    if (pending.pending) await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Launch verification complete." });
  }
});
