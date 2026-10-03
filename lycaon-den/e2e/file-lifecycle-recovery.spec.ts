import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import { openProjectFilesFixture, webE2e } from "./helpers.ts";

async function fileAction(page: Page, name: string, action: string) {
  // Row actions mount on hover.
  await page.locator(`[data-files-ctx="tree-row"][data-name="${name}"]`).first().hover();
  await page.getByRole("button", { name: `More actions for ${name}`, exact: true }).click();
  await page.getByRole("menuitem", { name: action, exact: true }).click();
}

webE2e("file lifecycle: retained editor follows rename and move, trash is reversible", async ({ page, request }) => {
  const folder = "file lifecycle café";
  const original = "draft 🐺.md";
  const renamed = "renamed café 🐺.md";
  const duplicate = "renamed café 🐺 copy.md";
  const content = "# File lifecycle\n\nSaved after rename 🐺\n";
  const seeded = await openProjectFilesFixture(page, request, {
    prefix: "file-lifecycle",
    name: "File lifecycle",
    seed: (root) => {
      mkdirSync(path.join(root, folder));
      writeFileSync(path.join(root, folder, original), "# Original\n");
    },
  });
  await page.getByRole("button", { name: `Expand ${folder}`, exact: true }).click();
  await page.getByTestId("files-tree-file").filter({ hasText: original }).click();
  await expect(page.locator(".den-files-editor__status:visible")).toHaveAttribute("data-editor-identity", "editing");
  await fileAction(page, original, "Rename");
  const rename = page.getByRole("textbox", { name: `Rename ${original}`, exact: true });
  await rename.fill(renamed);
  await rename.press("Enter");
  const crumb = page.getByTestId("files-editor-crumb");
  await expect(crumb.getByRole("button", { name: renamed, exact: true }))
    .toHaveAttribute("data-path", `${folder}/${renamed}`);
  await expect.poll(() => existsSync(path.join(seeded.root, folder, original))).toBe(false);
  await page.getByTestId("files-editor-host").locator(".cm-content").click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText(content);
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(path.join(seeded.root, folder, renamed), "utf8")).toBe(content);

  await fileAction(page, renamed, "Move to…");
  const move = page.getByRole("dialog", { name: `Move ${renamed}`, exact: true });
  await move.getByRole("button", { name: "Parent folder ..", exact: true }).click();
  await move.getByRole("button", { name: "Move here", exact: true }).click();
  await expect(crumb.getByRole("button", { name: renamed, exact: true })).toHaveAttribute("data-path", renamed);
  await expect(crumb.getByRole("button", { name: folder, exact: true })).toHaveCount(0);
  await expect.poll(() => existsSync(path.join(seeded.root, folder, renamed))).toBe(false);

  await fileAction(page, renamed, "Duplicate");
  await expect.poll(() => existsSync(path.join(seeded.root, duplicate)) ? readFileSync(path.join(seeded.root, duplicate), "utf8") : null).toBe(content);
  const secondDuplicate = "renamed café 🐺 copy 2.md";
  await fileAction(page, renamed, "Duplicate");
  await expect.poll(() => existsSync(path.join(seeded.root, secondDuplicate)) ? readFileSync(path.join(seeded.root, secondDuplicate), "utf8") : null).toBe(content);
  await fileAction(page, duplicate, "Move to trash");
  await page.getByRole("alertdialog", { name: "Move to trash?", exact: true })
    .getByRole("button", { name: "Move to trash", exact: true }).click();
  await expect.poll(() => existsSync(path.join(seeded.root, duplicate))).toBe(false);
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await expect.poll(() => existsSync(path.join(seeded.root, duplicate)) ? readFileSync(path.join(seeded.root, duplicate), "utf8") : null).toBe(content);
  expect(readFileSync(path.join(seeded.root, renamed), "utf8")).toBe(content);
});

webE2e("file lifecycle: first create in an unloaded folder retains its name field", async ({ page, request }) => {
  const seeded = await openProjectFilesFixture(page, request, {
    prefix: "first-folder-create",
    name: "First folder creation",
    seed: (root) => {
      mkdirSync(path.join(root, "file parent"));
      mkdirSync(path.join(root, "folder parent"));
    },
  });
  for (const kind of ["file", "folder"] as const) {
    const parent = `${kind} parent`;
    await page.locator(`[data-files-ctx="tree-row"][data-name="${parent}"]`).first().hover();
    await page.getByRole("button", { name: `New ${kind} in ${parent}`, exact: true }).click();
    await expect(page.getByText("Empty folder.", { exact: true }).first()).toBeVisible();
    const input = page.getByRole("textbox", { name: `New ${kind} name in ${parent}`, exact: true });
    await expect(input).toBeFocused();
    await input.fill("entry ü");
    await input.press("Enter");
    await expect.poll(() => existsSync(path.join(seeded.root, parent, "entry ü"))).toBe(true);
    if (kind === "file") expect(readFileSync(path.join(seeded.root, parent, "entry ü")).byteLength).toBe(0);
    await expect(input).toHaveCount(0);
    await fileAction(page, parent, "Move to trash");
    const confirm = page.getByRole("alertdialog", { name: "Move to trash?", exact: true });
    await expect(confirm).toContainText(`"${parent}" and its contents will move to the Trash.`);
    await confirm.getByRole("button", { name: "Cancel", exact: true }).click();
  }
});
