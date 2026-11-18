import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import * as Y from "yjs";
import type { EditorDocument } from "../src/api/types.ts";
import { apiConfig, apiJson, apiPostPrompt, modelIndependentWebE2e, openProjectFilesFixture, webE2e } from "./helpers.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

function documentText(document: EditorDocument): string {
  const state = new Y.Doc();
  Y.applyUpdate(state, new Uint8Array(Buffer.from(document.crdt_update, "base64")));
  const text = state.getText("text").toString();
  state.destroy();
  return text;
}

// The tool result may prefix its JSON payload with display text.
function programResult(content: string): { status: number; stdout: string; stderr: string } {
  return JSON.parse(content.slice(content.indexOf("{")));
}

modelIndependentWebE2e("source writes refresh clean editors and preserve unsaved typing", async ({ page, request }) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "editor-source-refresh", name: "Editor source refresh",
    seed: (root) => writeFileSync(path.join(root, "sample.txt"), "initial text\n"),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "sample.txt" }).click();
  const host = page.getByTestId("files-editor-host").filter({ visible: true });
  const content = host.locator(".cm-content");
  await expect(content).toContainText("initial text");
  await expect(content).toHaveAttribute("contenteditable", "true");
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const rootId = project.roots[0]!.id;
  const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source?path=sample.txt&root_id=${rootId}`, { headers });
  expect(response.ok()).toBe(true);
  let sha = (await response.json()).sha256 as string;
  const write = async (text: string) => {
    const result = await request.put(`${apiUrl}/v1/projects/${project.id}/source`, {
      headers,
      data: { operation_id: crypto.randomUUID(), root_id: rootId, path: "sample.txt", content: text, encoding: "utf-8", base_sha256: sha },
    });
    expect(result.ok(), await result.text()).toBe(true);
    sha = (await result.json()).sha256;
  };
  await write("updated by a source action\n");
  await expect(content).toContainText("updated by a source action", { timeout: 15_000 });
  await content.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText("unsaved local work\n");
  await expect(page.getByTestId("files-editor-save")).toBeVisible();
  await write("another disk update\n");
  await expect(content).toContainText("unsaved local work");
  await expect(content).toContainText("another disk update", { timeout: 15_000 });
  await expect(page.getByTestId("files-editor-save")).toBeEnabled();
});

modelIndependentWebE2e("outside write immediately after a host write reaches the open editor", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-immediate-outside", name: "Immediate outside edit",
    seed: (root) => writeFileSync(path.join(root, "sample.txt"), "initial text\n"),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "sample.txt" }).click();
  const content = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
  await expect(content).toContainText("initial text");
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const rootId = project.roots[0]!.id;
  const read = await request.get(`${apiUrl}/v1/projects/${project.id}/source?path=sample.txt&root_id=${rootId}`, { headers });
  expect(read.ok()).toBe(true);
  const before = await read.json();
  // The open editor's document can hold the path briefly; the host answers that with a retryable source_path_busy.
  await expect.poll(async () => {
    const write = await request.put(`${apiUrl}/v1/projects/${project.id}/source`, {
      headers,
      data: { operation_id: crypto.randomUUID(), root_id: rootId, path: "sample.txt", content: "host text\n", encoding: "utf-8", base_sha256: before.sha256 },
    });
    if (write.ok()) return "written";
    const refusal = await write.json() as { code?: string; retryable?: boolean };
    return refusal.code === "source_path_busy" && refusal.retryable ? "busy" : JSON.stringify(refusal);
  }, { timeout: 15_000 }).toBe("written");
  writeFileSync(path.join(root, "sample.txt"), "outside edit immediately after save\n");
  await expect(content).toContainText("outside edit immediately after save", { timeout: 15_000 });
});

webE2e("a process the agent launches reads the person's unsaved text", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-launch", name: "Editor agent launch",
    seed: (root) => {
      writeFileSync(path.join(root, "shared.txt"), "First line\nLast line\n");
      const git = (...args: string[]) => execFileSync("git", ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", ...args], { cwd: root });
      git("init", "--quiet");
      git("add", "shared.txt");
      git("commit", "--quiet", "--message", "Seed");
    },
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
    const prompt = await apiPostPrompt(request, sessionId, { text: "Report what changed in shared.txt." });
    expect(prompt.ok(), await prompt.text()).toBe(true);
    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("update_progress");
    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [ ] Report what changed in shared.txt\n",
    }));
    const run = async (command: string) => {
      const content = await driver.completed(await driver.invoke("command", { command }));
      const result = programResult(content);
      expect(result.status, content).toBe(0);
      return { content, ...result };
    };
    expect((await run("git ls-files")).stdout).toContain("shared.txt");

    await editor.click();
    await page.keyboard.press("ControlOrMeta+Home");
    await page.keyboard.insertText("Unsaved person 🐺\n");
    await expect.poll(async () => documentText(await readDocument())).toContain("Unsaved person 🐺");

    expect((await run("cat shared.txt")).stdout).toContain("Unsaved person 🐺");
    expect(onDisk()).toBe("First line\nLast line\n");

    const diff = await run("git diff -- shared.txt");
    expect(diff.stdout, diff.content).toContain("+Unsaved person 🐺");
    expect(diff.content).not.toContain("unsaved_documents");
    expect(onDisk()).toBe("Unsaved person 🐺\nFirst line\nLast line\n");
    await expect(editor).toContainText("Unsaved person 🐺");
    expect(documentText(await readDocument())).toBe("Unsaved person 🐺\nFirst line\nLast line\n");

    await editor.click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.insertText("Typed after the launch 世界\n");
    await expect.poll(async () => documentText(await readDocument())).toContain("Typed after the launch 世界");
    expect(onDisk()).not.toContain("Typed after the launch");
    expect((await run("git diff -- shared.txt")).stdout).toContain("+Typed after the launch 世界");
    expect(onDisk()).toBe("Unsaved person 🐺\nFirst line\nLast line\nTyped after the launch 世界\n");
    await expect(editor).toContainText("Typed after the launch 世界");
  } finally {
    await apiJson(request, "POST", "/harness/llm/auto", { enabled: true, text: "Launch verification complete." });
    const pending = await apiJson<{ pending: boolean; id: string }>(request, "GET", "/harness/llm/pending");
    if (pending.pending) await apiJson(request, "POST", "/harness/llm/respond", { id: pending.id, content: "Launch verification complete." });
  }
});
