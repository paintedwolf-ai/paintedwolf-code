import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("selected tabs meet the overflow divider at fractional widths", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "file-tab-divider", name: "File tab divider",
    seed: root => writeFileSync(path.join(root, "README.md"), "# Tab divider\n"),
  });
  await page.evaluate(async ({ projectId, rootId }) => {
    const url = "/src/files/documents/project-files-buffers.ts";
    const { openFilesBuffer } = await import(/* @vite-ignore */ url) as typeof import("../src/files/documents/project-files-buffers.ts");
    for (let index = 0; index < 10; index++) {
      openFilesBuffer(projectId, {
        rootId, rootLabel: "Project root", path: "", name: `Review step ${index + 1}`,
        kind: "walk", intent: "permanent",
        walkStep: { kind: "outside", key: `divider-step-${index}`, ordinal: index + 1,
          label: `Review step ${index + 1}`, toolCallId: null, effects: [] },
      });
    }
  }, { projectId: project.id, rootId: project.roots[0]!.id });

  const strip = page.getByTestId("files-tab-strip");
  const overflow = page.getByTestId("files-tabs-overflow");
  await expect(overflow).toBeVisible();
  await expect(page.getByRole("tab", { name: "Review step 10, Group summary", exact: true }))
    .toHaveAttribute("aria-selected", "true");
  await page.emulateMedia({ reducedMotion: "reduce" });

  for (const fontSize of [14, 21]) {
    await page.evaluate(size => { document.documentElement.style.fontSize = `${size}px`; }, fontSize);
    for (const width of [401.25, 402.5, 403.75]) {
      await strip.evaluate((el, size) => { el.style.width = `${size}px`; }, width);
      await expect.poll(async () => {
        await strip.locator(".den-files-tabs-scroller").evaluate(el => { el.scrollLeft = el.scrollWidth; });
        return strip.evaluate(el => {
          const body = el.querySelector<HTMLElement>(".den-files-tab:last-child .den-files-tab__body")!;
          const button = el.querySelector<HTMLElement>(".den-files-tabs-overflow")!;
          const selected = body.getBoundingClientRect();
          const divider = button.getBoundingClientRect();
          const topmost = document.elementFromPoint(divider.left + 0.5, divider.top + divider.height / 2);
          return {
            gap: divider.left > selected.right,
            dividerOnTop: topmost === button || button.contains(topmost),
            lastBorder: getComputedStyle(body).borderRightWidth,
            dividerWidth: getComputedStyle(button).borderLeftWidth,
            flushRight: Math.abs(divider.right - el.getBoundingClientRect().right) < 0.01,
          };
        });
      }).toEqual({ gap: false, dividerOnTop: true, lastBorder: "0px", dividerWidth: "1px", flushRight: true });
    }
  }
  await strip.screenshot({ path: testInfo.outputPath("selected-tab-divider.png") });
});
