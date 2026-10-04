import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiConfig, modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

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
