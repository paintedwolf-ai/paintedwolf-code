import { writeFileSync, readFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("keyboard editing preserves the existing chrome and document history", async ({ page, request }) => {
  let file = "";
  await openProjectFilesFixture(page, request, {
    prefix: "keyboard-styles", name: "Keyboard editing",
    seed(root) { file = path.join(root, "sample.ts"); writeFileSync(file, "one two\nthree four\n"); },
  });
  const treeFile = page.locator('.den-files-tree__label--file[data-path="sample.ts"]');
  await treeFile.focus();
  await page.keyboard.press("Enter");
  const editor = page.locator(".den-files-editor:visible .cm-content");
  await expect(editor).toBeVisible();
  await editor.focus();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("Control+k");
  await expect(editor).not.toContainText("one two");
  await page.keyboard.press("Control+y");
  await expect(editor).toContainText("one two");
  await expect(page.locator(".den-files-editor:visible .cm-vimCursorLayer")).toHaveCount(0);
  await expect(page.locator(".den-files-editor:visible .den-editor-mode")).toHaveCount(0);
  await expect(page.getByTestId("files-editor-editing-state").filter({ visible: true })).toHaveCount(0);

  const wrap = page.getByTestId("files-editor-wrap-toggle").filter({ visible: true });
  const before = await wrap.evaluate(element => getComputedStyle(element).color);
  const pressed = await wrap.getAttribute("aria-pressed");
  await wrap.focus();
  await page.keyboard.press("Enter");
  await expect(wrap).toHaveAttribute("aria-pressed", pressed === "true" ? "false" : "true");
  expect(await wrap.evaluate(element => getComputedStyle(element).color)).toBe(before);

  await editor.focus();
  await page.keyboard.press("Control+Home");
  await page.keyboard.type("saved ");
  await page.keyboard.press("Control+x");
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(file, "utf8")).toContain("saved one two");

  await page.evaluate(async () => {
    const url = "/src/settings/editor/editor-prefs.ts";
    const prefs = await import(/* @vite-ignore */ url) as typeof import("../src/settings/editor/editor-prefs.ts");
    await prefs.saveEditorKeymap("vim");
  });
  const state = page.getByTestId("files-editor-editing-state").filter({ visible: true });
  await expect(state).toHaveText("Normal");
  await editor.focus();
  await page.keyboard.type("ggdw");
  await expect(editor).not.toContainText("saved ");
  await page.keyboard.press("u");
  await expect(editor).toContainText("saved one two");
  await page.keyboard.press("i");
  await expect(state).toHaveText("Insert");
  await page.keyboard.press("Escape");
  await expect(state).toHaveText("Normal");
  await page.keyboard.type(":");
  await expect(page.locator(".den-files-editor:visible .den-files-editor__status .cm-vim-panel input")).toBeVisible();
  await page.keyboard.press("Escape");
  await editor.focus();
  await page.keyboard.press("F6");
  await expect(editor).not.toBeFocused();
});
