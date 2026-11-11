import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("file navigation retains the editor and its observed scroll position", async ({ page, request }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const names = ["first.txt", "second.txt", "third.txt", "fourth.txt"];
  await openProjectFilesFixture(page, request, {
    prefix: "editor-scroll-retention", name: "Editor scroll retention",
    seed: (root) => {
      for (const name of names) writeFileSync(path.join(root, name),
        Array.from({ length: 5_000 }, (_, index) => `${name} line ${index + 1}`).join("\n"));
    },
  });
  const openFile = async (name: string) => {
    await page.getByTestId("files-tree-file").filter({ hasText: name }).dblclick();
    await expect(page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content")).toBeVisible();
    await expect(page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content")).toContainText(name);
  };
  await openFile(names[0]!);
  const host = page.getByTestId("files-editor-host").filter({ visible: true });
  const scroller = host.locator(".cm-scroller");
  await host.locator(".cm-editor").evaluate((element) => { element.setAttribute("data-retention-probe", "original"); });
  await scroller.hover();
  await page.mouse.wheel(0, 20_000);
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(10_000);
  const offset = await scroller.evaluate((element) => element.scrollTop);
  for (const name of names.slice(1)) await openFile(name);
  await openFile(names[0]!);
  await expect(host.locator(".cm-editor")).toHaveAttribute("data-retention-probe", "original");
  await expect.poll(() => scroller.evaluate((element, previous) => Math.abs(element.scrollTop - previous), offset)).toBeLessThanOrEqual(1);
  await expect.poll(() => scroller.evaluate((element) => {
    const viewport = element.getBoundingClientRect();
    const lines = [...element.querySelectorAll(".cm-line")].map((line) => line.getBoundingClientRect());
    return [viewport.top + 30, viewport.bottom - 30].every((y) => lines.some((line) => line.top <= y && line.bottom >= y));
  })).toBe(true);
});

for (const selection of ["caret", "forward", "backward"] as const) {
  modelIndependentWebE2e(`preserves ${selection} while text leaves the viewport`, async ({ page, request }) => {
    const { project } = await openProjectFilesFixture(page, request, {
      prefix: "editor-scroll-selection",
      name: "Editor scroll selection",
      seed: (root) => writeFileSync(path.join(root, "scroll.ts"),
        Array.from({ length: 5_000 }, (_, i) => `const value${i + 1} = "selection stays here";`).join("\n")),
    });
    await page.getByTestId("files-tree-file").filter({ hasText: "scroll.ts" }).click();
    const content = page.getByTestId("files-editor-host").locator(".cm-content");
    await expect(content).toContainText('const value8 = "selection stays here";');
    await expect(content).toBeEditable();
    await expect(page.getByTestId("files-editor-host").locator(".os-scrollbar-vertical")).toHaveClass(/os-scrollbar-visible/);
    await content.focus();
    await content.evaluate(async (_element, { projectId, kind }) => {
      const hostUrl = "/src/files/editor/files-editor-host.ts";
      const buffersUrl = "/src/files/documents/files-buffer-state.ts";
      const { getFilesEditorView } = await import(/* @vite-ignore */ hostUrl) as typeof import("../src/files/editor/files-editor-host.ts");
      const { projectFilesState } = await import(/* @vite-ignore */ buffersUrl) as typeof import("../src/files/documents/files-buffer-state.ts");
      const view = getFilesEditorView(projectId, projectFilesState(projectId).activeKey!)!;
      const start = view.state.doc.line(1200).from + 5;
      const end = kind === "caret" ? start : view.state.doc.line(1202).from + 12;
      view.dispatch({ scrollIntoView: true, selection: kind === "backward"
        ? { anchor: end, head: start } : { anchor: start, head: end } });
    }, { projectId: project.id, kind: selection });
    const expectedPosition = selection === "forward" ? "Ln 1202, Col 13" : "Ln 1200, Col 6";
    await expect(page.getByText(expectedPosition, { exact: true })).toBeVisible();

    await expect(content).toBeFocused();
    const snapshot = () => content.evaluate(async (_element, projectId) => {
      const hostUrl = "/src/files/editor/files-editor-host.ts";
      const buffersUrl = "/src/files/documents/files-buffer-state.ts";
      const { getFilesEditorView } = await import(/* @vite-ignore */ hostUrl) as typeof import("../src/files/editor/files-editor-host.ts");
      const { projectFilesState } = await import(/* @vite-ignore */ buffersUrl) as typeof import("../src/files/documents/files-buffer-state.ts");
      const view = getFilesEditorView(projectId, projectFilesState(projectId).activeKey!)!;
      return { selection: view.state.selection.toJSON(), viewport: view.viewport.from,
        offset: view.scrollDOM.scrollTop, text: window.getSelection()!.toString() };
    }, project.id);
    const scroller = page.getByTestId("files-editor-host").locator(".cm-scroller");
    await scroller.hover();
    // The initial caret reveal settles before the wheel gesture.
    await page.waitForTimeout(400);
    const before = await snapshot();
    for (const delta of [5_000, -10_000, 5_000]) {
      const start = await snapshot();
      await page.mouse.wheel(0, delta);
      await expect.poll(async () => Math.abs((await snapshot()).offset - start.offset))
        .toBeGreaterThan(2_000);
      await page.waitForTimeout(700);
      const current = await snapshot();
      expect(current.selection, JSON.stringify({ before, current })).toEqual(before.selection);
    }
    await scroller.evaluate(async (element, offset) => {
      const url = "/src/platform/scrolling/scrollport-motion.ts";
      const { scrollportMotionForHost } = await import(/* @vite-ignore */ url) as typeof import("../src/platform/scrolling/scrollport-motion.ts");
      scrollportMotionForHost(element.closest<HTMLElement>(".cm-editor")!)?.cancelApplicationMotion();
      element.scrollTop = offset;
    }, before.offset);
    await expect.poll(async () => Math.abs((await snapshot()).offset - before.offset)).toBeLessThan(2);
    await expect.poll(async () => (await snapshot()).text).toBe(before.text);
    await expect(page.getByText(expectedPosition, { exact: true })).toBeVisible();
  });
}
