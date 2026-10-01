import { expect } from "@playwright/test";
import { gotoShell, webE2e } from "./helpers.ts";

for (const overflow of ["hidden", "auto", "clip"]) {
  for (const depth of [1, 3]) {
    webE2e(`wheel reaches the scrollport through ${depth} ${overflow} descendants`, async ({ page }) => {
      await gotoShell(page);
      await page.evaluate(({ overflow, depth }) => {
        const viewport = document.createElement("section");
        viewport.dataset.testid = "wheel-boundary-fixture";
        viewport.style.cssText = "position:fixed;inset:40px auto auto 40px;width:400px;height:240px;overflow:auto;overscroll-behavior:none;z-index:99999;background:white";
        for (let index = 0; index < 60; index += 1) {
          const row = document.createElement("div");
          row.style.cssText = "height:40px;width:100%;padding:4px";
          let parent = row;
          for (let level = 0; level < depth; level += 1) {
            const child = document.createElement("div");
            child.style.cssText = `width:180px;height:30px;white-space:nowrap;overflow:${overflow}`;
            parent.append(child);
            parent = child;
          }
          parent.textContent = `Row ${index}: a long clipped label inside a scrolling list`;
          if (index === 0) parent.dataset.testid = "wheel-boundary-target";
          viewport.append(row);
        }
        document.body.append(viewport);
      }, { overflow, depth });

      const viewport = page.getByTestId("wheel-boundary-fixture");
      await page.getByTestId("wheel-boundary-target").hover();
      await page.mouse.wheel(0, 400);
      await expect.poll(() => viewport.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    });
  }
}

for (const axis of ["x", "y"] as const) {
  webE2e(`first ${axis} wheel reaches newly published content while measurements are held`, async ({ page }) => {
    await gotoShell(page);
    await page.evaluate(async (axis) => {
      const { syncThemedScrollbar, beginScrollMeasureQuiet } = await import(/* @vite-ignore */ "/src/platform/scrolling/" + "themed-scrollbars.ts");
      const frame = document.createElement("section");
      frame.className = "den-scrollport";
      frame.setAttribute("data-den-scrollport", axis);
      frame.style.cssText = "position:fixed;inset:40px auto auto 40px;width:300px;height:200px;z-index:99999;background:white";
      frame.innerHTML = '<div class="den-scrollport__viewport"><div class="den-scrollport__content"></div></div>';
      const viewport = frame.firstElementChild as HTMLElement;
      viewport.dataset.testid = "growing-scrollport";
      document.body.append(frame);
      syncThemedScrollbar(frame);
      const release = beginScrollMeasureQuiet(frame);
      viewport.addEventListener("fixture-cleanup", release, { once: true });
      const content = document.createElement("div");
      content.style.width = axis === "x" ? "1500px" : "100%";
      content.style.height = axis === "y" ? "1500px" : "100px";
      content.textContent = "Newly published content";
      viewport.firstElementChild!.append(content);
    }, axis);
    const viewport = page.getByTestId("growing-scrollport");
    try {
      await expect(viewport).toContainText("Newly published content");
      await viewport.hover();
      await page.mouse.wheel(axis === "x" ? 400 : 0, axis === "y" ? 400 : 0);
      await expect.poll(() => viewport.evaluate((element, axis) => axis === "x" ? element.scrollLeft : element.scrollTop, axis)).toBeGreaterThan(0);
    } finally {
      await viewport.evaluate((element) => {
        element.dispatchEvent(new Event("fixture-cleanup"));
        element.closest("[data-den-scrollport]")?.remove();
      });
    }
  });
}
