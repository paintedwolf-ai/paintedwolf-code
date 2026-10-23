import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("a long editor keeps its native scroll viewport inside its host", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "editor-viewport-bounds", name: "Editor viewport bounds",
    seed: root => writeFileSync(path.join(root, "long.txt"),
      Array.from({ length: 1000 }, (_, index) => `Line ${index + 1}`).join("\n")),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "long.txt" }).click();
  const host = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(host.locator(".cm-content")).toContainText("Line 1");
  const scroller = host.locator(".cm-scroller");
  for (const [width, height] of [[1280, 900], [1100, 740], [1440, 960]]) {
    await page.setViewportSize({ width, height });
    await expect.poll(() => scroller.evaluate(element => {
      const frame = element.closest<HTMLElement>('[data-testid="files-editor-host"]')!;
      const outer = frame.getBoundingClientRect();
      const viewport = element.getBoundingClientRect();
      return viewport.height > 0 && Math.abs(viewport.height - outer.height) <= 1 &&
        viewport.top >= outer.top - 1 && viewport.bottom <= outer.bottom + 1 &&
        element.scrollHeight > element.clientHeight + 100;
    })).toBe(true);
  }
  const before = await scroller.evaluate(element => element.scrollTop);
  await scroller.hover();
  await page.mouse.wheel(0, 400);
  await expect.poll(() => scroller.evaluate(element => element.scrollTop)).toBeGreaterThan(before + 100);
});
