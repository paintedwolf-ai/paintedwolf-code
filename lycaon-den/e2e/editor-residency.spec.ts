import { linkSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

const diagnostics = new WeakMap<Page, string[]>();
test.beforeEach(({ page }) => {
  const entries: string[] = [];
  diagnostics.set(page, entries);
  page.on("pageerror", error => entries.push(error.stack ?? error.message));
  page.on("response", response => {
    if (!response.url().includes("/source/views")) return;
    void response.json().then(value => entries.push(JSON.stringify({ url: response.url(), method: response.request().method(),
      status: response.status(), kind: value.kind, state: value.state, extent: value.extent, intent: value.intent, summary: value.comparison?.summary,
      rows: value.rows?.length, firstRow: value.rows?.[0], failure: value.failure }))).catch(() => undefined);
  });
});
test.afterEach(async ({ page }, testInfo) => {
  if (testInfo.status === testInfo.expectedStatus || page.isClosed()) return;
  const state = await page.evaluate(async () => {
    const buffersURL = "/src/files/documents/files-buffer-state.ts", residencyURL = "/src/files/documents/document-residency.ts";
    const { projectFilesState, openFilesProjectIds } = await import(/* @vite-ignore */ buffersURL) as typeof import("../src/files/documents/files-buffer-state.ts");
    const { documentResidency } = await import(/* @vite-ignore */ residencyURL) as typeof import("../src/files/documents/document-residency.ts");
    return { residency: documentResidency.snapshot, projects: openFilesProjectIds().map(id => {
      const state = projectFilesState(id);
      return { id, active: state.activeKey, pending: state.pendingKey, buffers: Object.values(state.byKey).map(buffer => ({ path: buffer.path,
        loading: buffer.loading, error: buffer.loadError, opening: buffer.editorOpening, content: buffer.content.state, documentId: buffer.documentId })) };
    }), surfaces: Array.from(document.querySelectorAll<HTMLElement>("[data-resident-key]")).map(element => ({ ...element.dataset })) };
  });
  const diagnosticPath = testInfo.outputPath("residency-failure.json");
  writeFileSync(diagnosticPath, JSON.stringify({ state, events: diagnostics.get(page) }, null, 2));
  await testInfo.attach("residency-failure", { path: diagnosticPath, contentType: "application/json" });
});

modelIndependentWebE2e("hundreds of tabs retain only the admitted document working set", async ({ page, request, browserName }, testInfo) => {
  testInfo.setTimeout(180_000);
  const names = Array.from({ length: 300 }, (_, index) => `large-${index}.txt`);
  const text = "A bounded document working set.\n".repeat(65536);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "editor-residency", name: "Editor residency",
    seed: root => {
      const first = names[0];
      if (!first) throw new Error("Missing source fixture.");
      writeFileSync(path.join(root, first), text);
      for (const name of names.slice(1)) linkSync(path.join(root, first), path.join(root, name));
    },
  });
  const rootId = project.roots[0]?.id;
  if (!rootId) throw new Error("Missing fixture root.");
  await page.evaluate(async ({ projectId, rootId, names }) => {
    const url = "/src/files/documents/project-files-buffers.ts";
    const { openFilesBuffer } = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/project-files-buffers.ts");
    for (const name of names) openFilesBuffer(projectId, { rootId, rootLabel: "root", path: name, intent: "permanent" });
  }, { projectId: project.id, rootId, names });
  await expect(page.getByTestId("files-tab")).toHaveCount(300);
  await expect(page.locator('[data-testid="files-editor-host"] .cm-content:visible')).toContainText("A bounded document");
  const heap = browserName === "chromium" ? await page.context().newCDPSession(page) : undefined;
  const samples: unknown[] = [];
  await page.evaluate(async () => {
    const url = "/src/files/documents/document-residency.ts";
    const { documentResidency } = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/document-residency.ts");
    documentResidency.setBudget(96 * 1024 * 1024);
  });
  for (let index = 0; index < 18; index++) {
    const name = names[index];
    if (!name) throw new Error("Missing document fixture.");
    await page.evaluate(async ({ projectId, rootId, name }) => {
      const url = "/src/files/documents/project-files-buffers.ts";
      const { openFilesBuffer } = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/project-files-buffers.ts");
      openFilesBuffer(projectId, { rootId, rootLabel: "root", path: name, intent: "permanent" });
    }, { projectId: project.id, rootId, name });
    await expect(page.locator(`[data-testid="files-tab"][data-path="${name}"]`)).toHaveAttribute("aria-busy", "false");
    const sample = await page.evaluate(async projectId => {
      const residencyURL = "/src/files/documents/document-residency.ts", buffersURL = "/src/files/documents/files-buffer-state.ts";
      const { documentResidency } = await import(/* @vite-ignore */ residencyURL) as typeof import("../src/files/documents/document-residency.ts");
      const { projectFilesState } = await import(/* @vite-ignore */ buffersURL) as typeof import("../src/files/documents/files-buffer-state.ts");
      const buffers = Object.values(projectFilesState(projectId).byKey);
      return { ...documentResidency.snapshot, bodies: buffers.filter(b => b.content.state === "document" || b.content.state === "source").length,
        suspended: buffers.filter(b => b.content.state === "suspended").length };
    }, project.id);
    if (heap) await heap.send("HeapProfiler.collectGarbage");
    const memory = heap ? await heap.send("Runtime.getHeapUsage") : undefined;
    samples.push({ ...sample, heap: memory });
    expect(sample.acquiring).toBeLessThanOrEqual(2);
    expect(sample.bytes).toBeLessThanOrEqual(sample.budget);
    expect(sample.bodies).toBeLessThan(12);
    if (index >= 6) expect(sample.suspended).toBeGreaterThan(0);
  }
  // Returning to the first visited tab resumes a suspended document under the same budget.
  const first = names[0];
  if (!first) throw new Error("Missing document fixture.");
  const residencyOf = async (name: string) => page.evaluate(async ({ projectId, name }) => {
    const residencyURL = "/src/files/documents/document-residency.ts", buffersURL = "/src/files/documents/files-buffer-state.ts";
    const { documentResidency } = await import(/* @vite-ignore */ residencyURL) as typeof import("../src/files/documents/document-residency.ts");
    const { projectFilesState } = await import(/* @vite-ignore */ buffersURL) as typeof import("../src/files/documents/files-buffer-state.ts");
    const buffers = Object.values(projectFilesState(projectId).byKey);
    return { ...documentResidency.snapshot, state: buffers.find(buffer => buffer.path === name)?.content.state,
      bodies: buffers.filter(b => b.content.state === "document" || b.content.state === "source").length };
  }, { projectId: project.id, name });
  expect((await residencyOf(first)).state).toBe("suspended");
  await page.locator(`[data-testid="files-tab"][data-path="${first}"]`).click();
  await expect(page.locator(`[data-testid="files-tab"][data-path="${first}"]`)).toHaveAttribute("aria-busy", "false");
  await expect(page.locator('[data-testid="files-editor-host"] .cm-content:visible')).toContainText("A bounded document");
  // The source paints first; the resumed host document follows its adoption.
  await expect.poll(async () => (await residencyOf(first)).state).toBe("document");
  const resumed = await residencyOf(first);
  expect(resumed.bytes).toBeLessThanOrEqual(resumed.budget);
  expect(resumed.bodies).toBeLessThan(12);
  samples.push({ ...resumed, resumed: first });
  await expect(page.getByTestId("files-tab")).toHaveCount(300);
  await heap?.detach();
  const measurementPath = testInfo.outputPath("document-residency.json");
  writeFileSync(measurementPath, JSON.stringify(samples, null, 2));
  await testInfo.attach("document-residency", { path: measurementPath, contentType: "application/json" });
});

modelIndependentWebE2e("a huge current file uses paged reading and restores its suspended viewport", async ({ page, request }, testInfo) => {
  testInfo.setTimeout(90_000);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "huge-current-file", name: "Huge current file",
    seed: root => {
      writeFileSync(path.join(root, "huge.txt"), Array.from({ length: 200000 }, (_, i) => `Line ${i + 1}: bounded current source reading.\n`).join(""));
      writeFileSync(path.join(root, "small.txt"), "Small file\n");
    },
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "huge.txt" }).dblclick();
  const reader = page.getByTestId("source-reader").filter({ visible: true });
  await expect(reader.locator(".cm-content")).toContainText("Line 1:");
  await expect(reader.locator(".cm-content")).toHaveAttribute("contenteditable", "false");
  await reader.getByRole("button", { name: "Go to line", exact: true }).click();
  await page.getByRole("textbox", { name: "Go to line", exact: true }).fill("190000");
  await page.getByRole("button", { name: "Go", exact: true }).click();
  await expect(reader.locator(".cm-content")).toContainText("Line 190000:");
  expect(await reader.locator(".cm-line").count()).toBeLessThan(200);
  const number = reader.locator(".files-line-gutter__number").filter({ hasText: /^190000$/ });
  await expect(number).toBeVisible();
  expect(await number.evaluate(element => {
    const column = element.closest(".cm-gutter")?.getBoundingClientRect();
    const label = element.getBoundingClientRect();
    return !!column && label.left >= column.left && label.right <= column.right;
  })).toBe(true);
  expect(await reader.evaluate(element => {
    const actions = element.querySelector(".den-source-reader__actions")?.getBoundingClientRect();
    const scroller = element.querySelector(".cm-scroller")?.getBoundingClientRect();
    return !!actions && !!scroller && actions.bottom <= scroller.top;
  })).toBe(true);
  const line = reader.locator(".cm-line").filter({ hasText: /^Line 190000:/ });
  const linePosition = () => line.evaluate(element => {
    const viewport = element.closest(".cm-scroller")!.getBoundingClientRect();
    const rect = element.getBoundingClientRect();
    return { top: rect.top - viewport.top, height: rect.height, visible: rect.bottom > viewport.top && rect.top < viewport.bottom };
  });
  await expect.poll(async () => (await linePosition()).visible).toBe(true);
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  const before = await linePosition();
  await page.getByTestId("files-tree-file").filter({ hasText: "small.txt" }).dblclick();
  await expect(page.locator('[data-testid="files-editor-host"] .cm-content:visible')).toContainText("Small file");
  const rootId = project.roots[0]?.id;
  if (!rootId) throw new Error("Missing fixture root.");
  expect(await page.evaluate(async ({ projectId, rootId }) => {
    const url = "/src/files/documents/files-residency.ts";
    const { suspendFileDocument } = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/files-residency.ts");
    return suspendFileDocument(projectId, rootId, "huge.txt");
  }, { projectId: project.id, rootId })).toBe(true);
  await page.locator('[data-testid="files-tab"][data-path="huge.txt"]').click();
  await expect(reader.locator(".cm-content")).toContainText("Line 190000:");
  try {
    await expect.poll(async () => Math.abs((await linePosition()).top - before.top)).toBeLessThanOrEqual(2);
  } finally {
    writeFileSync(testInfo.outputPath("reader-viewport.json"), JSON.stringify({ before, after: await linePosition() }));
  }
  await page.screenshot({ path: testInfo.outputPath("huge-current-reader.png") });
});

modelIndependentWebE2e("a dirty suspended tab resumes offline with undo and later synchronizes", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "suspended-draft", name: "Suspended draft",
    seed: root => {
      writeFileSync(path.join(root, "draft.txt"), "Original text\n");
      writeFileSync(path.join(root, "other.txt"), "Another file\n");
    },
  });
  const editor = page.locator('[data-testid="files-editor-host"] .cm-content:visible');
  await page.getByTestId("files-tree-file").filter({ hasText: "draft.txt" }).dblclick();
  await expect(editor).toBeEditable();
  let deliveries = 0;
  await page.route("**/editor-documents/*/updates", async route => { deliveries++; await route.abort("failed"); });
  await editor.click();
  await editor.press("ControlOrMeta+End");
  await page.keyboard.insertText("Preserved before suspension\n");
  await expect.poll(() => deliveries).toBeGreaterThan(0);
  await page.getByTestId("files-tree-file").filter({ hasText: "other.txt" }).dblclick();
  await expect(editor).toContainText("Another file");
  await page.route("**/editor-documents/*/sync", route => route.abort("failed"));
  const rootId = project.roots[0]?.id;
  if (!rootId) throw new Error("Missing fixture root.");
  expect(await page.evaluate(async ({ projectId, rootId }) => {
    const url = "/src/files/documents/files-residency.ts";
    const { suspendFileDocument } = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/files-residency.ts");
    return suspendFileDocument(projectId, rootId, "draft.txt");
  }, { projectId: project.id, rootId })).toBe(true);
  await page.route("**/health", route => route.abort("failed"));
  let hostOpens = 0;
  await page.route(/\/source\/open(?:\?.*)?$/, async route => { hostOpens++; await route.abort("failed"); });
  await page.locator('[data-testid="files-tab"][data-path="draft.txt"]').click();
  await expect(editor).toContainText("Preserved before suspension");
  await expect(editor).toBeEditable();
  expect(hostOpens).toBeGreaterThan(0);
  await editor.click();
  await editor.press("ControlOrMeta+End");
  await page.keyboard.insertText("Typed after resume\n");
  await expect(editor).toContainText("Typed after resume");
  await editor.press("ControlOrMeta+z");
  await expect(editor).not.toContainText("Typed after resume");
  await expect(editor).toContainText("Preserved before suspension");
  await editor.press("ControlOrMeta+Shift+z");
  await page.unroute("**/editor-documents/*/updates");
  await page.unroute("**/editor-documents/*/sync");
  await page.unroute(/\/source\/open(?:\?.*)?$/);
  await page.unroute("**/health");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(path.join(root, "draft.txt"), "utf8"), { timeout: 15000 })
    .toBe("Original text\nPreserved before suspension\nTyped after resume\n");
  await expect(page.getByTestId("files-editor-saved").filter({ visible: true })).toBeVisible();
});

modelIndependentWebE2e("save and close all includes a suspended dirty document", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "suspended-save", name: "Suspended save",
    seed: root => {
      writeFileSync(path.join(root, "draft.txt"), "Original text\n");
      writeFileSync(path.join(root, "other.txt"), "Another file\n");
    },
  });
  const editor = page.locator('[data-testid="files-editor-host"] .cm-content:visible');
  await page.getByTestId("files-tree-file").filter({ hasText: "draft.txt" }).dblclick();
  await expect(editor).toBeEditable();
  await editor.click();
  await editor.press("ControlOrMeta+End");
  await page.keyboard.insertText("Saved from a cold tab\n");
  await expect(editor).toContainText("Saved from a cold tab");
  await page.getByTestId("files-tree-file").filter({ hasText: "other.txt" }).dblclick();
  await expect(editor).toContainText("Another file");
  const rootId = project.roots[0]?.id;
  if (!rootId) throw new Error("Missing fixture root.");
  const suspended = await page.evaluate(async ({ projectId, rootId }) => {
    const residencyURL = "/src/files/documents/files-residency.ts", buffersURL = "/src/files/documents/files-buffer-state.ts";
    const { suspendFileDocument } = await import(/* @vite-ignore */ residencyURL) as typeof import("../src/files/documents/files-residency.ts");
    const { projectFilesState } = await import(/* @vite-ignore */ buffersURL) as typeof import("../src/files/documents/files-buffer-state.ts");
    await suspendFileDocument(projectId, rootId, "draft.txt");
    const buffer = Object.values(projectFilesState(projectId).byKey).find(buffer => buffer.path === "draft.txt");
    return { state: buffer?.content.state, dirty: buffer?.dirty };
  }, { projectId: project.id, rootId });
  expect(suspended).toEqual({ state: "suspended", dirty: true });
  await page.locator('[data-testid="files-tab"][data-path="other.txt"]').click({ button: "right" });
  await page.getByTestId("files-ctx-tab-close-tabs").click();
  await page.getByTestId("files-ctx-tab-close-all").click();
  await expect(page.getByTestId("editor-unsaved-save")).toBeEnabled();
  await page.getByTestId("editor-unsaved-save").click();
  await expect(page.getByTestId("files-tab")).toHaveCount(0);
  expect(readFileSync(path.join(root, "draft.txt"), "utf8")).toBe("Original text\nSaved from a cold tab\n");
});
