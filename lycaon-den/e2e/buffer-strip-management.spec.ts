import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect } from "@playwright/test";
import {
  openProjectFilesFixture,
  webE2e,
} from "./helpers.ts";

/**
 * Harness screenshots for buffer strip management. Overflow geometry and the
 * multi-file dirty dialog stay covered in vitest; this captures the native strip
 * with several durable Explorer tabs and the open-files list.
 */
const screenshotDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../test-results/buffer-strip-management-screenshots",
);
mkdirSync(screenshotDir, { recursive: true });

webE2e("buffer strip: open tabs + open-files list screenshot", async ({
  page,
  request,
}) => {
  await openProjectFilesFixture(page, request, {
    prefix: "buffer-strip",
    name: "Strip",
    seed: (root) => {
      for (let i = 0; i < 8; i++) {
        writeFileSync(path.join(root, `file-${i}.ts`), `export const n = ${i};\n`);
      }
    },
  });
  const files = page.getByTestId("files-tree-file");
  for (let i = 0; i < 6; i++) {
    await files.nth(i).click();
    await expect(page.getByTestId("files-tab").nth(i)).toBeVisible({
      timeout: 15_000,
    });
  }
  await expect(page.locator(".den-files-tab--preview")).toHaveCount(0);


  // Open the list through its registered command; overflow is viewport-dependent.
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByTestId("crossbar")).toBeVisible({ timeout: 10_000 });
  await page.getByTestId("crossbar-input").fill("Show open files");
  await page
    .getByTestId(
      "crossbar-action-painted-wolf/platform:files-open-files-list",
    )
    .click();
  await expect(page.getByTestId("files-open-list")).toBeVisible({
    timeout: 10_000,
  });
});
