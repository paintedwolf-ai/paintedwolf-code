import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { activateProject, openProjectFilesFixture, webE2e } from "./helpers.ts";

webE2e("editor opening: restored YAML draft survives a failed join and retry", async ({ page, request }) => {
  const { root } = await openProjectFilesFixture(page, request, {
    prefix: "editor-opening", name: "Editor opening recovery",
    seed: (root) => { mkdirSync(path.join(root, "data")); writeFileSync(path.join(root, "data/customization.yaml"), "theme: dark\n"); },
  });
  await page.getByRole("button", { name: "Expand data", exact: true }).click();
  await page.getByTestId("files-tree-file").filter({ hasText: "customization.yaml" }).dblclick();
  const editor = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
  await expect(editor).toBeEditable({ timeout: 15_000 });
  const accepted = page.waitForResponse((response) => response.url().endsWith("/updates") && response.ok());
  await editor.fill("theme: light\n");
  await accepted;
  await expect(page.getByTestId("files-editor-save")).toBeVisible();
  // Hold the next initial join at a permanent failure, as with unavailable storage.
  let unavailable = true;
  await page.route("**/editor-documents/*/sync", async (route) => {
    if (unavailable) await route.fulfill({ status: 500, json: { code: "internal_error", message: "Document storage is unavailable.", retryable: false } });
    else await route.continue();
  });
  await page.reload();
  await page.getByTestId("project-files-entry").click();
  await page.getByTestId("files-tree-file").filter({ hasText: "customization.yaml" }).dblclick();
  await expect(page.getByTestId("files-editor-opening-error")).toContainText("Document storage is unavailable", { timeout: 30_000 });
  unavailable = false;
  await page.getByRole("button", { name: "Retry editing", exact: true }).click();
  await expect(editor).toBeEditable({ timeout: 15_000 });
  await expect(editor).toContainText("theme: light");
  expect(readFileSync(path.join(root, "data/customization.yaml"), "utf8")).toBe("theme: dark\n");
  await page.getByTestId("files-editor-save").click();
  await expect.poll(() => readFileSync(path.join(root, "data/customization.yaml"), "utf8")).toBe("theme: light\n");
});

for (const failure of ["transport", "workspace"] as const) {
  webE2e(`editor opening: automatically recovers a ${failure} race`, async ({ page, request }) => {
    const { project } = await openProjectFilesFixture(page, request, {
      prefix: "editor-opening", name: "Editor opening race",
      seed: (root) => writeFileSync(path.join(root, "ready.txt"), "ready to edit\n"),
    });
    await page.getByTestId("files-tree-file").filter({ hasText: "ready.txt" }).dblclick();
    const editor = page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content");
    await expect(editor).toBeEditable();
    await page.getByTestId("nav-brand").click();
    let requests = 0;
    await page.route(/\/editor-documents(?:\?.*)?$/, async (route) => {
      requests++;
      if (requests !== 1) { await route.continue(); return; }
      if (failure === "transport") { await route.abort("connectionreset"); return; }
      const response = await route.fetch();
      await route.fulfill({ response, json: { ...await response.json(), workspace_id: "ws_obsolete" } });
    });
    await activateProject(page, project.id);
    await page.getByTestId("project-files-entry").click();
    await expect(editor).toBeEditable({ timeout: 20_000 });
    expect(requests).toBe(2);
    await expect(page.getByTestId("files-editor-opening-error")).toHaveCount(0);
    await editor.fill("editing recovered\n");
    await expect(editor).toContainText("editing recovered");
  });
}
