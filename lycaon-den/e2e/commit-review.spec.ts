import { execFileSync } from "node:child_process";
import { mkdirSync, unlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { openProjectFilesFixture, modelIndependentWebE2e } from "./helpers.ts";

function git(root: string, ...args: string[]) {
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("GIT_")));
  execFileSync("git", ["-C", root, "-c", "user.name=Review test", "-c", "user.email=review@example.test", ...args], {
    env: { ...env, GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: "/dev/null", GIT_TERMINAL_PROMPT: "0" },
  });
}

modelIndependentWebE2e("commit review: unseen Git paths open and external staging refreshes without history", async ({ page, request }, testInfo) => {
  const { project, root } = await openProjectFilesFixture(page, request, {
    prefix: "commit-review", name: "Commit review", readyTestId: "files-pane-segment",
    seed: (root) => {
      git(root, "init", "-b", "main");
      writeFileSync(path.join(root, "changed.ts"), "export const value = 1;\n");
      writeFileSync(path.join(root, "deleted.ts"), "export const removed = true;\n");
      git(root, "add", "-A"); git(root, "commit", "-m", "baseline");
      writeFileSync(path.join(root, "changed.ts"), "export const value = 2;\n");
      unlinkSync(path.join(root, "deleted.ts"));
      mkdirSync(path.join(root, "new"));
      writeFileSync(path.join(root, "new", "one.ts"), "export const one = 1;\n");
      writeFileSync(path.join(root, "new", "two.ts"), "export const two = 2;\n");
    },
  });
  await page.evaluate((id) => {
    const h = (window as unknown as { __harness: { openReviewLens(id: string, scope: { kind: string }): unknown } }).__harness;
    h.openReviewLens(id, { kind: "commit" });
  }, project.id);
  const rows = page.getByTestId("changes-row-file");
  await expect(rows).toHaveCount(4, { timeout: 20_000 });
  await rows.filter({ hasText: "deleted.ts" }).click();
  await expect(page.getByTestId("file-deletion-document").filter({ visible: true })).toContainText("export const removed = true;", { timeout: 15_000 });
  await page.screenshot({ path: process.env.LYCAON_REVIEW_SCREENSHOT ?? testInfo.outputPath("commit-deleted-review.png"), fullPage: true });
  await rows.filter({ hasText: "changed.ts" }).click();
  await expect(page.getByTestId("files-editor-host").filter({ visible: true })).toContainText("export const value = 2;", { timeout: 15_000 });
  await expect(page.getByTestId("changes-row-detail")).toContainText("Unstaged");
  git(root, "add", "changed.ts");
  await expect(page.getByTestId("changes-row-detail")).toContainText("Staged", { timeout: 15_000 });
  await expect(page.getByTestId("changes-row-detail")).not.toContainText("Unstaged");
});
