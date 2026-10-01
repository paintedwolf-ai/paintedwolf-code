import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e.use({ browserName: "webkit" });

modelIndependentWebE2e("file tab clicks preserve drafts and dragging reorders tabs", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "editor-tab-navigation",
    name: "Editor tab navigation",
    seed: (root) => {
      writeFileSync(path.join(root, "first.md"), "# First document\n");
      writeFileSync(path.join(root, "second.md"), "# Second document\n");
    },
  });
  const first = page.getByTestId("files-tab").filter({ hasText: "first" });
  const second = page.getByTestId("files-tab").filter({ hasText: "second" });
  const editor = page.getByRole("textbox", { name: "", exact: true });
  await page.getByTestId("files-tree-file").filter({ hasText: "first.md" }).dblclick();
  await expect(editor).toContainText("First document");
  await expect(editor).toBeEditable();
  await editor.press("ControlOrMeta+End");
  await editor.press("Enter");
  await editor.pressSequentially("Retained draft.");
  await expect(editor).toContainText("Retained draft.");
  await page.getByTestId("files-tree-file").filter({ hasText: "second.md" }).dblclick();
  await expect(editor).toContainText("Second document");

  await first.click();
  await expect(first).toHaveAttribute("aria-selected", "true");
  await expect(editor).toContainText("Retained draft.");
  await second.click();
  await expect(second).toHaveAttribute("aria-selected", "true");
  await expect(editor).toContainText("Second document");

  const start = await first.boundingBox();
  const target = await second.boundingBox();
  expect(start).not.toBeNull();
  expect(target).not.toBeNull();
  await page.mouse.move(start!.x + start!.width / 2, start!.y + start!.height / 2);
  await page.mouse.down();
  await page.mouse.move(target!.x + target!.width - 2, target!.y + target!.height / 2, { steps: 8 });
  await page.mouse.up();
  await expect(page.getByTestId("files-tab").first()).toHaveAttribute("data-path", "second.md");
  await first.click();
  await expect(first).toHaveAttribute("aria-selected", "true");
  await expect(editor).toContainText("Retained draft.");
});
