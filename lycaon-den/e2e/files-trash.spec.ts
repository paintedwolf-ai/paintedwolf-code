import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

for (const isDir of [false, true]) {
  modelIndependentWebE2e(`native trash and file history restore a ${isDir ? "folder" : "file"}`, async ({ page, request }, testInfo) => {
    const name = isDir ? "recovery folder" : "résumé ' [1].txt";
    const content = Buffer.from([0, 1, 255, 42]);
    const { root } = await openProjectFilesFixture(page, request, {
      prefix: "files-trash", name: "File recovery",
      seed(root) {
        if (isDir) mkdirSync(path.join(root, name, "empty"), { recursive: true });
        writeFileSync(path.join(root, name, ...(isDir ? ["binary.dat"] : [])), content);
      },
    });
    const target = path.join(root, name);
    const row = page.locator(`[data-files-ctx="tree-row"][data-path=${JSON.stringify(name)}]`).first();
    await row.click({ button: "right" });
    await page.getByTestId("files-ctx-trash").click();
    await expect(page.getByTestId("files-trash-body")).toContainText(name);
    await page.getByTestId("files-trash-dialog").screenshot({ path: testInfo.outputPath("trash-confirmation.png"), animations: "disabled" });
    await page.getByTestId("files-trash-confirm").click();
    await expect(page.getByTestId("files-trash-dialog")).toBeHidden();
    await expect.poll(() => existsSync(target)).toBe(false);
    const history = page.getByRole("status").filter({ has: page.getByRole("button", { name: "Dismiss file history notice", exact: true }) });
    await history.getByRole("button", { name: "Undo", exact: true }).click();
    await expect.poll(() => existsSync(target)).toBe(true);
    expect(readFileSync(isDir ? path.join(target, "binary.dat") : target)).toEqual(content);
    if (isDir) expect(existsSync(path.join(target, "empty"))).toBe(true);
    await history.getByRole("button", { name: "Redo", exact: true }).click();
    await expect.poll(() => existsSync(target)).toBe(false);
    await history.getByRole("button", { name: "Undo", exact: true }).click();
    await expect.poll(() => existsSync(target)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath("trash-restored.png"), animations: "disabled" });
  });
}

modelIndependentWebE2e("retry retrieves an accepted trash result with its original operation identity", async ({ page, request }, testInfo) => {
  const { root, project } = await openProjectFilesFixture(page, request, {
    prefix: "files-trash-retry", name: "Trash retry",
    seed(root) { writeFileSync(path.join(root, "retry.txt"), "recover me"); },
  });
  const operations: string[] = [];
  const retries: string[] = [];
  await page.route(`**/v1/projects/${project.id}/source?*`, async (route) => {
    if (route.request().method() !== "DELETE") return route.continue();
    operations.push(new URL(route.request().url()).searchParams.get("operation_id") ?? "");
    const accepted = await route.fetch();
    expect([202, 204]).toContain(accepted.status());
    await expect.poll(() => existsSync(path.join(root, "retry.txt"))).toBe(false);
    return route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({
      code: "source_trash_failed", message: "The system could not move the item to trash.", retryable: true, suggested_action: "Close applications using this file, then retry.",
    }) });
  });
  await page.route(`**/v1/projects/${project.id}/source/operations/*/retry`, async (route) => {
    retries.push(new URL(route.request().url()).pathname.split("/").at(-2) ?? "");
    return route.continue();
  });
  await page.locator('[data-files-ctx="tree-row"][data-path="retry.txt"]').first().click({ button: "right" });
  await page.getByTestId("files-ctx-trash").click();
  await page.getByTestId("files-trash-confirm").click();
  await expect(page.getByRole("heading", { name: "Couldn’t move “retry.txt” to trash" })).toBeVisible();
  expect(existsSync(path.join(root, "retry.txt"))).toBe(false);
  await page.getByTestId("files-trash-dialog").screenshot({ path: testInfo.outputPath("trash-retry.png"), animations: "disabled" });
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByTestId("files-trash-dialog")).toBeHidden();
  await expect.poll(() => existsSync(path.join(root, "retry.txt"))).toBe(false);
  expect(operations).toHaveLength(1);
  expect(operations[0]).not.toBe("");
  expect(retries).toEqual(operations);
});
