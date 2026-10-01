import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

for (const automatic of [true, false]) {
  modelIndependentWebE2e(`tree reveal ${automatic ? "follows file publication" : "remains available when automatic reveal is disabled"}`, async ({ page, request }) => {
    await page.emulateMedia({ reducedMotion: "no-preference" });
    const { project } = await openProjectFilesFixture(page, request, {
      prefix: "tree-reveal", name: "Tree reveal",
      seed(root) {
        for (let index = 0; index < 90; index++) {
          writeFileSync(path.join(root, `file-${String(index).padStart(3, "0")}.ts`), `export const value = ${index};\n`);
        }
      },
    });
    await page.locator('.den-files-tree__label--file[data-path="file-000.ts"]').click();
    const pane = page.getByTestId("files-pane-presentation");
    await expect(pane).toHaveAttribute("data-pending", "false");
    const samples = await page.evaluate(async ({ projectId, rootId, automatic }) => {
      const prefsUrl = "/src/settings/editor/editor-prefs.ts";
      const navigationUrl = "/src/platform/navigation/open-source.ts";
      const prefs = await import(/* @vite-ignore */ prefsUrl) as typeof import("../src/settings/editor/editor-prefs.ts");
      const navigation = await import(/* @vite-ignore */ navigationUrl) as typeof import("../src/platform/navigation/open-source.ts");
      await prefs.saveEditorRevealInTree(automatic);
      const scroll = document.querySelector<HTMLElement>('[data-testid="files-tree-scroll"]')!;
      const pane = document.querySelector<HTMLElement>('[data-testid="files-pane-presentation"]')!;
      const origin = scroll.scrollTop;
      await navigation.openSourceLocation({ projectId, intent: "permanent", rootId, path: "file-089.ts" });
      const samples: { offset: number; pending: boolean }[] = [];
      const started = performance.now();
      while (performance.now() - started < 900) {
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
        samples.push({ offset: scroll.scrollTop - origin, pending: pane.dataset.pending === "true" });
      }
      return samples;
    }, { projectId: project.id, rootId: project.roots[0]!.id, automatic });
    await expect(pane).toHaveAttribute("data-pending", "false");
    expect(samples.filter((sample) => sample.pending).every((sample) => Math.abs(sample.offset) < 1)).toBe(true);
    if (automatic) {
      expect(samples[samples.length - 1]!.offset).toBeGreaterThan(0);
      expect(new Set(samples.filter((sample) => sample.offset > 0).map((sample) => Math.round(sample.offset))).size).toBeGreaterThan(1);
    } else {
      expect(samples.every((sample) => Math.abs(sample.offset) < 1)).toBe(true);
      await page.locator('[data-files-ctx="tab"][data-path="file-089.ts"]').click({ button: "right" });
      await page.getByTestId("files-ctx-tab-reveal-tree").click();
    }
    const row = page.locator('.den-files-tree__label--file[data-path="file-089.ts"]');
    await expect(row).toBeInViewport();
    await expect(row).toHaveAttribute("aria-current", "true");
  });
}

modelIndependentWebE2e("chat path actions reselect an active file after a folder and reveal without changing the editor", async ({ page, request }, testInfo) => {
  const { randomUUID } = await import("node:crypto");
  const { mkdirSync } = await import("node:fs");
  const { apiSeedSessionTranscript, settledChatSessionId, transcriptMessages } = await import("./helpers.ts");
  await page.setViewportSize({ width: 1700, height: 1000 });
  await page.emulateMedia({ reducedMotion: "no-preference" });
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "chat-tree-navigation", name: "Chat tree navigation",
    seed(root) {
      mkdirSync(path.join(root, "nested"));
      for (let index = 0; index < 90; index++) {
        writeFileSync(path.join(root, "nested", `a-${String(index).padStart(3, "0")}.ts`), "export {};\n");
      }
      writeFileSync(path.join(root, "nested", "destination.ts"), "export const destination = true;\n");
      writeFileSync(path.join(root, "first.ts"), "export const first = true;\n");
    },
  });
  await page.evaluate(async () => {
    const url = "/src/shell/layout-store.ts";
    const layout = await import(/* @vite-ignore */ url) as typeof import("../src/shell/layout-store.ts");
    await layout.saveStagePlacementMode({ mode: "split", companion: "files" });
    const prefsUrl = "/src/settings/editor/editor-prefs.ts";
    const prefs = await import(/* @vite-ignore */ prefsUrl) as typeof import("../src/settings/editor/editor-prefs.ts");
    await prefs.saveEditorRevealInTree(true);
  });
  const sessionId = await settledChatSessionId(page);
  const messageId = randomUUID();
  const rootId = project.roots[0]!.id;
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([{
    id: messageId, role: "assistant",
    content: `Open [destination](source://${rootId}/nested/destination.ts), inspect \`first.ts\`, or browse [nested](source://${rootId}/nested).`,
    source_context: { locations: [
      { project_id: project.id, root_id: rootId, path: "nested/destination.ts", entry_kind: "file" },
      { project_id: project.id, root_id: rootId, path: "first.ts", entry_kind: "file" },
      { project_id: project.id, root_id: rootId, path: "nested", entry_kind: "folder" },
    ], truncated: false },
    grounding: { traced: true, cited_evidence: [{ handle: "read#1", path: "first.ts", line: 1, excerpt: "export const first = true;", verdict: "matched" }] },
    created_at: new Date().toISOString(), seq: 1, ord: 1,
  }]));
  const prose = page.locator(`[data-msg-id="${messageId}"] .assistant-prose`);
  const destination = prose.getByRole("button", { name: "destination", exact: true });
  const folderLink = prose.getByRole("button", { name: "nested", exact: true });
  const firstLink = prose.getByRole("button", { name: "first.ts", exact: true });
  await page.getByTestId("citation-evidence-chicklet").locator("summary").click();
  const citation = page.getByTestId("citation-evidence-chicklet-panel").getByTestId("source-path-link");
  for (const link of [destination, folderLink, firstLink, citation]) {
    await link.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      window.getSelection()?.removeAllRanges();
      window.getSelection()?.addRange(range);
    });
    await link.click({ button: "right" });
    await expect(page.getByTestId("path-menu-reveal-tree")).toBeVisible();
    await page.keyboard.press("Escape");
  }
  await page.evaluate(() => window.getSelection()?.removeAllRanges());
  const movement = page.evaluate(async () => {
    const scroll = document.querySelector<HTMLElement>('[data-testid="files-tree-scroll"]')!;
    const origin = scroll.scrollTop;
    const samples: { offset: number; pending: boolean }[] = [];
    const started = performance.now();
    let stableFrames = 0;
    let previous = origin;
    while (performance.now() - started < 5000) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      const selected = document.querySelector('.den-files-tree__label--file[data-path="nested/destination.ts"][aria-current="true"]');
      const pending = document.querySelector<HTMLElement>('[data-testid="files-pane-presentation"]')?.dataset.pending === "true";
      samples.push({ offset: scroll.scrollTop - origin, pending });
      stableFrames = selected && !pending && scroll.scrollTop === previous ? stableFrames + 1 : 0;
      previous = scroll.scrollTop;
      if (stableFrames >= 8) break;
    }
    return samples;
  });
  await destination.click();
  const samples = await movement;
  expect(samples.filter((sample) => sample.pending).every((sample) => Math.abs(sample.offset) < 1)).toBe(true);
  expect(new Set(samples.filter((sample) => sample.offset > 0).map((sample) => Math.round(sample.offset))).size).toBeGreaterThan(1);
  const file = page.locator('.den-files-tree__label--file[data-path="nested/destination.ts"]');
  await expect(file).toHaveAttribute("aria-current", "true");
  await folderLink.click();
  const folder = page.locator('.den-files-tree__label--dir[data-path="nested"]').first();
  await expect(folder).toHaveAttribute("aria-current", "true");
  await destination.click();
  await expect(file).toHaveAttribute("aria-current", "true");
  await expect(file).toBeInViewport();
  await firstLink.click({ button: "right" });
  await page.getByTestId("path-menu-reveal-tree").click();
  await expect(page.locator('.den-files-tree__label--file[data-path="first.ts"]')).toHaveAttribute("aria-current", "true");
  await expect(page.locator('.project-files-view .cm-content[contenteditable="true"]').filter({ visible: true })).toContainText("destination = true");
  await citation.click();
  await expect(page.locator('.den-files-tree__label--file[data-path="first.ts"]')).toHaveAttribute("aria-current", "true");
  await folderLink.click();
  await expect(folder).toHaveAttribute("aria-current", "true");
  await citation.click();
  await expect(page.locator('.den-files-tree__label--file[data-path="first.ts"]')).toHaveAttribute("aria-current", "true");
  await expect(page.locator('.den-files-tree__label--file[data-path="first.ts"]')).toBeInViewport({ ratio: 1 });
  await prose.hover({ position: { x: 5, y: 5 } });
  await page.screenshot({ path: testInfo.outputPath("chat-tree-reveal.png"), animations: "disabled" });
});

modelIndependentWebE2e("Reveal in tree from chat mounts the Files view and selects a deeply nested file", async ({ page, request }) => {
  const { randomUUID } = await import("node:crypto");
  const { mkdirSync } = await import("node:fs");
  const { activateProject, apiCreateProjectWithRoot, apiSeedSessionTranscript, e2eTempDir, e2eUniqueLabel, gotoShell, settledChatSessionId, transcriptMessages } = await import("./helpers.ts");
  const root = e2eTempDir(request, "cold-tree-reveal");
  mkdirSync(path.join(root, "scripts", "fixtures"), { recursive: true });
  writeFileSync(path.join(root, "scripts", "fixtures", "controls.json"), "{}\n");
  const project = await apiCreateProjectWithRoot(request, root, e2eUniqueLabel("Cold tree reveal"));
  await gotoShell(page);
  await activateProject(page, project.id);
  const sessionId = await settledChatSessionId(page);
  const rootId = project.roots[0]!.id;
  const messageId = randomUUID();
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([{
    id: messageId, role: "assistant", content: "Inspect `scripts/fixtures/controls.json`.",
    source_context: { locations: [{ project_id: project.id, root_id: rootId, path: "scripts/fixtures/controls.json", entry_kind: "file" }], truncated: false },
    created_at: new Date().toISOString(), seq: 1, ord: 1,
  }]));
  const link = page.locator(`[data-msg-id="${messageId}"] .assistant-prose`).getByRole("button", { name: "scripts/fixtures/controls.json", exact: true });
  await link.click({ button: "right" });
  await page.getByTestId("path-menu-reveal-tree").click();
  const row = page.locator('.den-files-tree__label--file[data-path="scripts/fixtures/controls.json"]');
  await expect(row).toBeVisible();
  await expect(row).toHaveAttribute("aria-current", "true");
  await expect(row).toBeInViewport();
  await expect(page.getByTestId("files-tab")).toHaveCount(0);
});

modelIndependentWebE2e("source reveal activates the addressed project", async ({ page, request }) => {
  const { apiCreateProjectWithRoot, e2eTempDir, e2eUniqueLabel } = await import("./helpers.ts");
  await openProjectFilesFixture(page, request, {
    prefix: "reveal-origin", name: "Reveal origin",
    seed(root) { writeFileSync(path.join(root, "origin.ts"), "export {};\n"); },
  });
  const root = e2eTempDir(request, "reveal-destination");
  writeFileSync(path.join(root, "destination.ts"), "export {};\n");
  const project = await apiCreateProjectWithRoot(request, root, e2eUniqueLabel("Reveal destination"));
  await page.evaluate(async (project) => {
    const navigationUrl = "/src/platform/navigation/open-source.ts";
    const navigation = await import(/* @vite-ignore */ navigationUrl) as typeof import("../src/platform/navigation/open-source.ts");
    return navigation.openSourceLocation({ projectId: project.id, rootId: project.roots[0]!.id, path: "destination.ts", action: "reveal", intent: "permanent" }, {
      lookup: () => ({ roots: project.roots }),
    });
  }, project);
  const row = page.locator(`.den-files-tree__label--file[data-root="${project.roots[0]!.id}"][data-path="destination.ts"]`);
  await expect(row).toBeVisible();
  await expect(row).toHaveAttribute("aria-current", "true");
});
