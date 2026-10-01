import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect } from "@playwright/test";
import {
  openProjectFilesFixture,
  webE2e,
} from "./helpers.ts";

/**
 * Minimal harness screenshots for the three buffer kinds. Each test seeds only
 * the file it opens so a 4 MiB fixture cannot destabilize the tree. Menus,
 * zoom-100%, and hot-exit restore stay in vitest.
 */
const screenshotDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../test-results/heavy-binary-hot-exit-screenshots",
);
mkdirSync(screenshotDir, { recursive: true });

const PNG_1X1 = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);

webE2e("heavy binary: image tab screenshot", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "heavy",
    name: "Heavy",
    seed: (root) => writeFileSync(path.join(root, "logo.png"), PNG_1X1),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "logo.png" }).click();
  await expect(page.getByTestId("files-image-viewer")).toBeVisible({
    timeout: 15_000,
  });
});

webE2e("heavy binary: over-limit info card screenshot", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "heavy",
    name: "Heavy",
    seed: (root) => writeFileSync(
      path.join(root, "huge.txt"),
      Buffer.alloc(4 * 1024 * 1024 + 1, 0x61),
    ),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "huge.txt" }).click();
  await expect(page.getByTestId("files-info-card")).toBeVisible({
    timeout: 15_000,
  });
});

webE2e("heavy binary: binary info card screenshot", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "heavy",
    name: "Heavy",
    seed: (root) => writeFileSync(
      path.join(root, "tool.bin"),
      Buffer.from([0x7f, 0x45, 0x4c, 0x46, 0, 1, 1, 0]),
    ),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "tool.bin" }).click();
  await expect(page.getByTestId("files-info-card")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("files-info-explain")).toContainText(
    "binary file",
  );
});
