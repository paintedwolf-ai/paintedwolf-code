import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { activateProject, modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("a restored project joins its selected file without hydrating every cached tab", async ({ page, request }, testInfo) => {
  const names = Array.from({ length: 20 }, (_, index) => `file-${String(index).padStart(2, "0")}.ts`);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "project-tab-restoration", name: "Project tab restoration",
    seed: root => names.forEach(name => writeFileSync(path.join(root, name), `// ${name}\nexport const value = 1;\n`)),
  });
  for (const name of names) {
    await page.getByTestId("files-tree-file").filter({ hasText: name }).dblclick();
    await expect(page.locator('[data-testid="files-editor-host"] .cm-content:visible')).toContainText(name);
  }
  await expect(page.getByTestId("files-tab")).toHaveCount(20);
  await page.getByTestId("nav-brand").click();
  const joins: string[] = [];
  await page.route(/\/(?:editor-documents|source\/open)(?:\?.*)?$/, async route => {
    if (route.request().method() === "POST") joins.push(route.request().postDataJSON().path);
    await route.continue();
  });
  await activateProject(page, project.id);
  await page.getByTestId("project-files-entry").click();
  const editor = page.locator('[data-testid="files-editor-host"] .cm-content:visible');
  await expect(editor).toContainText("file-19.ts");
  await expect(editor).toBeEditable();
  await expect.poll(() => joins.includes("file-18.ts")).toBe(true);
  expect(joins[0]).toBe("file-19.ts");
  expect(new Set(joins)).toEqual(new Set(["file-19.ts", "file-18.ts"]));
  await expect(page.getByTestId("files-tab")).toHaveCount(20);
  await testInfo.attach("restored-document-joins", { body: JSON.stringify(joins), contentType: "application/json" });
});

modelIndependentWebE2e("duplicate file names and a pending destination retain unambiguous identity", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "file-identity", name: "File identity",
    seed: root => {
      mkdirSync(path.join(root, "src"));
      writeFileSync(path.join(root, "AGENTS.md"), "Root document\n");
      writeFileSync(path.join(root, "src/AGENTS.md"), "Source document\n");
    },
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "AGENTS.md" }).dblclick();
  await expect(page.locator('[data-testid="files-editor-host"] .cm-content:visible')).toContainText("Root document");
  const presentation = page.getByTestId("files-pane-presentation");
  const originalBounds = await presentation.boundingBox();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  await page.route(/\/source\/open(?:\?.*)?$/, async route => {
    if (route.request().postDataJSON()?.path === "src/AGENTS.md") await gate;
    await route.continue();
  });
  try {
    await page.evaluate(async ({ projectId, rootId }) => {
      const moduleUrl = "/src/platform/navigation/open-source.ts";
      const { openSourceLocation } = await import(/* @vite-ignore */ moduleUrl) as typeof import("../src/platform/navigation/open-source.ts");
      void openSourceLocation({ projectId, rootId, path: "src/AGENTS.md", intent: "permanent" });
    }, { projectId: project.id, rootId: project.roots[0]!.id });
    const rootTab = page.locator('[data-testid="files-tab"][data-path="AGENTS.md"]');
    const sourceTab = page.locator('[data-testid="files-tab"][data-path="src/AGENTS.md"]');
    await expect(sourceTab).toHaveAccessibleName("AGENTS.md — src");
    expect(await rootTab.locator(".den-files-tab__name-start").evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
    await expect(sourceTab).toHaveAttribute("aria-busy", "true");
    await expect(presentation.locator('[role="status"]')).toHaveCount(0);
    await expect(presentation.locator('.cm-content:visible')).toContainText("Root document");
    expect(await presentation.boundingBox()).toEqual(originalBounds);
    expect(await rootTab.getAttribute("aria-label")).not.toBe(await sourceTab.getAttribute("aria-label"));
    await page.screenshot({ path: testInfo.outputPath("pending-file-identity.png") });
    release();
    await expect(page.locator('[data-testid="files-editor-host"] .cm-content:visible')).toContainText("Source document");
    await expect(sourceTab).toHaveAttribute("aria-busy", "false");
    await page.screenshot({ path: testInfo.outputPath("duplicate-file-names.png") });
  } finally {
    release();
    await page.unroute(/\/source\/open(?:\?.*)?$/);
  }
});

modelIndependentWebE2e("file bytes paint during tab motion and joining keeps the mounted editor", async ({ page, request }, testInfo) => {
  await openProjectFilesFixture(page, request, {
    prefix: "file-opening",
    name: "File opening",
    seed: (root) => writeFileSync(path.join(root, "first.ts"), "export const first = 1;\n".repeat(400)),
  });
  let release!: () => void;
  const barrier = new Promise<void>((resolve) => { release = resolve; });
  let joining = false;
  await page.route("**/editor-documents/*/sync", async (route) => {
    joining = true;
    await barrier;
    await route.continue();
  });
  await page.evaluate(() => {
    const state = { attachments: new Set<Element>(), editor: null as Element | null, animation: null as Animation | null };
    Object.assign(window, { fileOpeningProbe: state });
    new MutationObserver((records) => {
      for (const record of records) for (const node of record.addedNodes) {
        if (!(node instanceof Element)) continue;
        const editors = [...node.querySelectorAll(".cm-editor")];
        if (node.matches(".cm-editor")) editors.push(node);
        for (const editor of editors) { state.editor ??= editor; state.attachments.add(editor); }
      }
      const tab = document.querySelector('[data-testid="files-tab"]');
      if (tab && !state.animation) state.animation = tab.animate([{ opacity: 1 }, { opacity: 0.99 }], { duration: 30_000 });
    }).observe(document.body, { childList: true, subtree: true });
  });
  try {
    await page.getByTestId("files-tree-file").filter({ hasText: "first.ts" }).click();
    const content = page.locator('[data-testid="files-editor-host"] .cm-content:visible');
    await expect(content).toContainText("export const first = 1;");
    await expect.poll(() => joining).toBe(true);
    await expect(content).not.toBeEditable();
    await expect.poll(() => page.evaluate(() => {
      const probe = (window as unknown as { fileOpeningProbe: { animation: Animation } }).fileOpeningProbe;
      return probe.animation.playState;
    })).toBe("running");
    release();
    await expect(content).toBeEditable();
    const probe = await page.evaluate(() => {
      const state = (window as unknown as { fileOpeningProbe: { attachments: Set<Element>; editor: Element; animation: Animation } }).fileOpeningProbe;
      state.animation.cancel();
      return { attachments: state.attachments.size, retained: state.editor === document.querySelector('[data-testid="files-editor-host"] .cm-editor') };
    });
    expect(probe).toEqual({ attachments: 1, retained: true });
    await content.press("ControlOrMeta+Home");
    await content.pressSequentially("// editable\n");
    await expect(content).toContainText("// editable");
    await page.screenshot({ path: testInfo.outputPath("file-opened.png") });
  } finally {
    release();
    await page.unroute("**/editor-documents/*/sync");
  }
});

modelIndependentWebE2e("opening and revisiting files keeps navigation work bounded", async ({ page, request }, testInfo) => {
  const names = Array.from({ length: 12 }, (_, index) => `file-${String(index).padStart(2, "0")}.ts`);
  await openProjectFilesFixture(page, request, {
    prefix: "file-navigation",
    name: "File navigation",
    seed: (root) => {
      for (const name of names) writeFileSync(path.join(root, name), `// ${name}\n` + "export const value = 1;\n".repeat(400));
    },
  });
  const samples: { path: string; kind: string; duration: number }[] = [];
  const measure = async (name: string, kind: string) => {
    const target = kind === "open"
      ? page.getByTestId("files-tree-file").filter({ hasText: name })
      : page.getByTestId("files-tab").filter({ hasText: name.replace(".ts", "") });
    // Page timing excludes automation polling and pointer travel.
    const duration = await target.evaluate((element, filename) => new Promise<number>((resolve, reject) => {
      const started = performance.now();
      const timeout = setTimeout(() => reject(new Error(`File never painted: ${filename}`)), 10_000);
      const check = () => {
        const host = [...document.querySelectorAll('[data-testid="files-editor-host"]')].find(node =>
          node.getClientRects().length && getComputedStyle(node).visibility === "visible" &&
          node.closest('[data-resident="active"]') && node.textContent?.includes(`// ${filename}`));
        const pending = document.querySelector('[data-testid="files-pane-presentation"]')?.getAttribute("data-pending");
        if (host && pending === "false") { clearTimeout(timeout); resolve(performance.now() - started); }
        else requestAnimationFrame(check);
      };
      element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      requestAnimationFrame(check);
    }), name);
    samples.push({ path: name, kind, duration });
  };
  for (const name of names) await measure(name, "open");
  for (const name of names) await measure(name, "revisit");
  const timingPath = testInfo.outputPath("file-navigation-timing.json");
  writeFileSync(timingPath, JSON.stringify(samples, null, 2));
  await testInfo.attach("file-navigation-timing", { path: timingPath, contentType: "application/json" });
  // The ceiling detects stalls; the artifact records timing distributions.
  expect(Math.max(...samples.map(sample => sample.duration))).toBeLessThan(1500);
  expect(await page.getByTestId("files-tab").count()).toBe(names.length);
});

modelIndependentWebE2e("outbox inventory follows atomic pending-work transitions", async ({ page }) => {
  await page.goto("/");
  const result = await page.evaluate(async () => {
    const moduleUrl = "/src/files/documents/document-outbox.ts";
    const { documentOutbox } = await import(/* @vite-ignore */ moduleUrl);
    const outbox = documentOutbox();
    const address = { projectId: crypto.randomUUID(), documentId: crypto.randomUUID(), rootId: "root", fileId: "file", path: "test.ts" };
    const checkpoint = { ...address, kind: "checkpoint", clientId: "window", epoch: 1, replicaId: 1, incarnation: "fixture", confirmed: { id: address.documentId, project_id: address.projectId, crdt_update: "AAA=" }, state: "AAA=", synchronized: true, pendingOperations: [] };
    const pending = { ...address, kind: "update", clientId: "window", replicaId: 1, epoch: 1, operationId: crypto.randomUUID(), update: "AAA=", acknowledged: false, sequence: 1, sessionId: "", turn: 0 };
    const inventory: unknown[] = [];
    const capture = async () => inventory.push((await outbox.list(address.projectId))[0]?.synchronized);
    await outbox.put(checkpoint);
    await capture();
    await outbox.commit([{ ...checkpoint, synchronized: false, pendingOperations: [pending.operationId] }, pending]);
    await capture();
    await outbox.put({ ...pending, acknowledged: true });
    await capture();
    await outbox.remove(pending);
    await capture();
    const retained = await outbox.read(address.documentId);
    return { inventory, retained: retained.map((record: { kind: string; synchronized?: boolean }) => ({ kind: record.kind, synchronized: record.synchronized })) };
  });
  expect(result).toEqual({ inventory: [true, false, false, true], retained: [{ kind: "checkpoint", synchronized: true }] });
});
