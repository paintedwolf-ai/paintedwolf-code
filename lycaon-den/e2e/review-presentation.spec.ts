import { writeFileSync } from "node:fs";
import { randomUUID } from "node:crypto";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiConfig, modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("review clears on close, reopens cleanly, and keeps historical review separate from new work", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "review-presentation", name: "Review presentation",
    seed: (root) => writeFileSync(path.join(root, "review.ts"), "export const value = 1;\n"),
  });
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  await expect.poll(async () => {
    const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/workspace`, { headers });
    expect(response.ok()).toBe(true);
    return (await response.json()).inventory.complete;
  }, { timeout: 30_000 }).toBe(true);
  const sourceUrl = `${apiUrl}/v1/projects/${project.id}/source`;
  const params = { path: "review.ts", root_id: project.roots[0]!.id };
  expect((await request.get(sourceUrl, { headers, params })).ok()).toBe(true);
  await page.evaluate((id) => {
    const h = (window as unknown as { __harness: { openReviewLens(id: string, scope: { kind: string }): unknown } }).__harness;
    h.openReviewLens(id, { kind: "new" });
  }, project.id);
  writeFileSync(path.join(root, "review.ts"), "export const value = 2;\n");
  expect((await request.get(sourceUrl, { headers, params })).ok()).toBe(true);
  const newRows = page.getByTestId("review-lens").getByTestId("changes-row-file");
  await expect(newRows.filter({ hasText: "review.ts" })).toBeVisible({ timeout: 20_000 });
  await newRows.filter({ hasText: "review.ts" }).click();
  await expect(page.getByTestId("files-editor-host").filter({ visible: true })).toContainText("export const value = 2;");
  const completed = page.waitForResponse((response) =>
    response.url().endsWith(`/v1/projects/${project.id}/source/seen`) && response.request().method() === "POST");
  await page.getByTestId("files-tab-close").filter({ visible: true }).click();
  expect((await completed).ok()).toBe(true);
  await expect(page.getByTestId("review-lens-empty")).toContainText("Nothing new since you last looked");
  await expect(page.getByTestId("review-lens-seen-heading")).toBeVisible();

  await newRows.filter({ hasText: "review.ts" }).click();
  const current = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(current).toContainText("export const value = 2;", { timeout: 20_000 });
  await expect(current.locator(".cm-changedLine, .cm-changedText, .cm-deletedChunk")).toHaveCount(0);
  await newRows.filter({ hasText: "review.ts" }).click();
  await page.getByTestId("changes-row-view-reviewed").click();
  const history = page.getByTestId("file-version-file").filter({ visible: true });
  await expect(history).toContainText("export const value = 2;");
  await expect(history.locator(".cm-changedLine")).toHaveCount(1);

  writeFileSync(path.join(root, "review.ts"), "export const value = 3;\n");
  expect((await request.get(sourceUrl, { headers, params })).ok()).toBe(true);
  await expect(page.getByTestId("review-lens-empty")).toHaveCount(0);
  await expect(history).toContainText("export const value = 2;");
  await page.getByTestId("files-tab-close").filter({ visible: true }).click();
  await expect(newRows.filter({ hasText: "review.ts" })).toBeVisible();
  const read = await request.get(sourceUrl, { headers, params });
  const fileId = (await read.json()).file_id;
  const comparison = await request.get(`${sourceUrl}/comparison`, {
    headers, params: { file_id: fileId, baseline: "presentation" },
  });
  const unread = await comparison.json();
  expect(unread.before.content).toBe("export const value = 2;\n");
  expect(unread.after.content).toBe("export const value = 3;\n");

  await newRows.filter({ hasText: "review.ts" }).click();
  await expect(current).toContainText("export const value = 3;", { timeout: 20_000 });
  await expect(current.locator(".cm-changedLine")).toHaveCount(1);
  const reviewedAgain = page.waitForResponse((response) =>
    response.url().endsWith(`/v1/projects/${project.id}/source/seen`) && response.request().method() === "POST");
  await page.getByTestId("files-tab-close").filter({ visible: true }).click();
  expect((await reviewedAgain).ok()).toBe(true);
  await expect(page.getByTestId("review-lens-empty")).toContainText("Nothing new since you last looked");
  await page.getByTestId("changes-row-mark-unseen").click();
  await expect(page.getByTestId("review-lens-empty")).toHaveCount(0);
  const restored = await request.get(`${sourceUrl}/comparison`, {
    headers, params: { file_id: fileId, baseline: "presentation" },
  });
  expect((await restored.json()).before.content).toBe("export const value = 2;\n");

});

modelIndependentWebE2e("a newly added file has normal text in current and historical views", async ({ page, request }) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "review-added-file", name: "Added file review",
    seed: (root) => writeFileSync(path.join(root, "existing.txt"), "Existing file\n"),
  });
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  await expect.poll(async () => {
    const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/workspace`, { headers });
    return (await response.json()).inventory.complete;
  }, { timeout: 30_000 }).toBe(true);
  await page.evaluate((id) => {
    const h = (window as unknown as { __harness: { openReviewLens(id: string, scope: { kind: string }): unknown } }).__harness;
    h.openReviewLens(id, { kind: "new" });
  }, project.id);
  const text = "export const first = 1;\nexport const second = 2;\n";
  expect((await request.post(`${apiUrl}/v1/projects/${project.id}/source`, {
    headers,
    data: { operation_id: randomUUID(), root_id: project.roots[0]!.id, path: "added.ts", kind: "file" },
  })).status()).toBe(201);
  writeFileSync(path.join(root, "added.ts"), text);
  expect((await request.get(`${apiUrl}/v1/projects/${project.id}/source`, {
    headers, params: { path: "added.ts", root_id: project.roots[0]!.id },
  })).ok()).toBe(true);
  const row = page.getByTestId("review-lens").getByTestId("changes-row-file").filter({ hasText: "added.ts" });
  await expect(row).toBeVisible({ timeout: 20_000 });
  await row.click();
  const current = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(current).toContainText("export const second = 2;");
  await expect(current.locator(".cm-changedLine, .cm-changedText")).toHaveCount(0);
  const completed = page.waitForResponse((response) =>
    response.url().endsWith(`/v1/projects/${project.id}/source/seen`) && response.request().method() === "POST");
  await page.getByTestId("files-tab-close").filter({ visible: true }).click();
  expect((await completed).ok()).toBe(true);
  await expect(page.getByTestId("review-lens-seen-heading")).toBeVisible();
  await row.click();
  await expect(current).toContainText("export const second = 2;");
  await row.click();
  await page.getByTestId("changes-row-view-reviewed").click();
  const history = page.getByTestId("file-version-file").filter({ visible: true });
  await expect(history).toContainText("export const second = 2;");
  await expect(history.locator(".cm-changedLine, .cm-changedText")).toHaveCount(0);
});
