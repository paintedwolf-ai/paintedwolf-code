import { writeFileSync } from "node:fs";
import path from "node:path";
import type { EditorDocumentChanges, ProjectSourceReadResponse, ProjectSourceWriteResponse, PutProjectSourceRequest } from "../src/api/types.ts";
import { expect } from "@playwright/test";
import { apiConfig, modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("version history uses quiet loading and retains edits when reopened", async ({ page, request }, testInfo) => {
  await openProjectFilesFixture(page, request, {
    prefix: "version-loading", name: "Version loading",
    seed: root => writeFileSync(path.join(root, "example.txt"), "Example file\n"),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "example.txt" }).click();
  await expect(page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content")).toBeEditable();

  let release!: () => void;
  const response = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/editor-documents/*/changes", async route => {
    await response;
    await route.fulfill({ json: { changes: [{ actor_kind: "agent", session_id: "11111111-1111-4111-8111-111111111111", session_title: "Example change",
      turn: 1, revision: 2, epoch: 1, operation_id: "22222222-2222-4222-8222-222222222222", person_id: "", client_id: "", reverted_by: "",
      created_at: "2026-09-13T10:00:00Z" }] } satisfies EditorDocumentChanges });
  });
  await page.getByTestId("file-version-trigger").click();
  const menu = page.getByRole("menu", { name: "Version history" });
  const loader = menu.getByRole("status").filter({ hasText: "Loading edits" });
  await expect(loader).toBeVisible();
  const style = await loader.evaluate(node => ({
    fontSize: getComputedStyle(node).fontSize,
    expected: getComputedStyle(document.documentElement).getPropertyValue("--text-den-caption").trim(),
    rootSize: getComputedStyle(document.documentElement).fontSize,
  }));
  expect(parseFloat(style.fontSize)).toBeCloseTo(parseFloat(style.expected) * parseFloat(style.rootSize), 1);
  await page.screenshot({ path: testInfo.outputPath("version-history-waiting.png") });
  release();
  await expect(menu.getByText("From Example change · turn 1")).toBeVisible();
  await expect(loader).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(menu).toBeHidden();

  await page.unroute("**/editor-documents/*/changes");
  const refresh = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/editor-documents/*/changes", async route => {
    await refresh;
    await route.fulfill({ json: { changes: [] } satisfies EditorDocumentChanges });
  });
  await page.getByTestId("file-version-trigger").click();
  await expect(menu.getByText("From Example change · turn 1")).toBeVisible();
  await expect(loader).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("version-history-retained.png") });
  release();
  await expect(menu.getByRole("group", { name: "Recent edits" })).toHaveCount(0);
});

modelIndependentWebE2e("historical selections retain painted content through row preparation", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "version-presentation", name: "Version presentation",
    seed: root => writeFileSync(path.join(root, "history.txt"), "first contents\n"),
  });
  const { apiUrl, token } = apiConfig();
  const source = `${apiUrl}/v1/projects/${project.id}/source`;
  const options = { headers: { Authorization: `Bearer ${token}` }, params: { root_id: project.roots[0]!.id, path: "history.txt" } };
  const initial = await request.get(source, options);
  expect(initial.ok()).toBe(true);
  const read = await initial.json() as ProjectSourceReadResponse;
  if (!read.sha256) throw new Error("Historical fixture source has no content SHA.");
  let sha = read.sha256;
  for (const content of ["second contents\n", "current contents\n"]) {
    const saved = await request.put(source, { headers: options.headers, data: {
      operation_id: crypto.randomUUID(), root_id: project.roots[0]!.id, path: "history.txt",
      content, encoding: "utf-8", base_sha256: sha,
    } satisfies PutProjectSourceRequest });
    expect(saved.ok(), await saved.text()).toBe(true);
    sha = (await saved.json() as ProjectSourceWriteResponse).sha256;
  }
  await page.getByTestId("files-tree-file").filter({ hasText: "history.txt" }).click();
  const current = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
  await expect(current).toBeEditable();
  for (const fromEnd of [1, 2]) {
    let release!: () => void;
    const held = new Promise<void>(resolve => { release = resolve; });
    let requested = false;
    await page.route("**/source/views/*/presentations/*/rows?*", async route => {
      requested = true;
      await held;
      await route.continue();
    });
    const stage = page.locator(".den-files-editor__document-stage").filter({ visible: true });
    const probe = await stage.evaluateHandle(element => {
      const state = { frames: 0, failures: [] as string[], running: true };
      const sample = () => {
        if (!state.running) return;
        state.frames++;
        const content = [...element.querySelectorAll<HTMLElement>(".cm-content")].some(node =>
          node.textContent?.trim() && node.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }));
        if (!content) state.failures.push("No readable document painted");
        requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
      return state;
    });
    try {
      const outgoing = await stage.locator(".cm-content").filter({ visible: true }).elementHandle();
      await page.getByTestId("file-version-trigger").click();
      const versions = page.getByRole("menu", { name: "Version history" }).getByRole("menuitemradio");
      await expect.poll(() => versions.count()).toBeGreaterThan(2);
      await versions.nth(await versions.count() - fromEnd).click();
      await expect.poll(() => requested).toBe(true);
      await expect(stage).toHaveAttribute("aria-busy", "true");
      expect(await outgoing!.evaluate(node => node.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }))).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`history-${fromEnd}-pending.png`) });
      release();
      await expect(stage).toHaveAttribute("aria-busy", "false");
      await expect(stage.getByTestId("source-reader").filter({ visible: true })).toContainText(fromEnd === 1 ? "first contents" : "second contents");
      const result = await probe.evaluate(state => { state.running = false; return state; });
      expect(result.frames).toBeGreaterThan(0);
      expect(result.failures).toEqual([]);
    } finally {
      release();
      await probe.evaluate(state => { state.running = false; });
      await page.unrouteAll({ behavior: "wait" });
    }
  }
});
