import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("editor reconnects during wheel input and refreshes after holds settle", async ({ page, request }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await openProjectFilesFixture(page, request, {
    prefix: "scrollbar-reconnect",
    name: "Scrollbar reconnect",
    seed: (root) => writeFileSync(path.join(root, "scroll.txt"),
      Array.from({ length: 4_000 }, (_, i) => `Line ${i + 1}: reconnect coverage`).join("\n")),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "scroll.txt" }).click();
  const editor = page.getByTestId("files-editor-host").locator(".cm-editor");
  await expect(editor.locator(".cm-content")).toBeVisible();
  await expect(editor.locator(".cm-content")).toBeEditable();
  await expect(editor.locator(".cm-content")).toContainText("Line 8: reconnect coverage");
  await expect(editor.locator(".os-scrollbar-vertical")).toHaveClass(/os-scrollbar-visible/);

  const scroller = editor.locator(".cm-scroller");
  await scroller.hover();
  await page.mouse.wheel(0, 800);
  await expect.poll(() => scroller.evaluate((node) => node.scrollTop)).toBeGreaterThan(100);

  const result = await editor.evaluate(async (element) => {
    const scrollbarUrl = "/src/platform/scrolling/themed-scrollbars.ts";
    const motionUrl = "/src/platform/scrolling/scrollport-motion.ts";
    const { beginScrollMeasureQuiet, updateThemedViewportScrollbar } =
      await import(/* @vite-ignore */ scrollbarUrl) as typeof import("../src/platform/scrolling/themed-scrollbars.ts");
    const { scrollportMotionForHost } =
      await import(/* @vite-ignore */ motionUrl) as typeof import("../src/platform/scrolling/scrollport-motion.ts");
    const host = element as HTMLElement;
    const scroller = host.querySelector<HTMLElement>(".cm-scroller")!;
    const motion = scrollportMotionForHost(host)!;
    const parent = host.parentNode!;
    const sibling = host.nextSibling;
    const release = beginScrollMeasureQuiet(host);
    const nativeAdd = Set.prototype.add;
    let requeues = 0;
    // The iteration cap turns a synchronous loop into a test failure.
    Set.prototype.add = function(value) {
      if (value === host && ++requeues > 50) throw new Error("Scrollbar reconnect did not yield");
      return nativeAdd.call(this, value);
    };
    const samples: Array<{ active: boolean; sameMotion: boolean; offset: number }> = [];
    try {
      for (let i = 0; i < 3; i += 1) {
        await new Promise<void>((resolve) => requestAnimationFrame(() => {
          scroller.dispatchEvent(new WheelEvent("wheel", {
            deltaY: 800, bubbles: true, cancelable: true,
          }));
          updateThemedViewportScrollbar(host, `reconnect-${i}`);
          parent.insertBefore(host, sibling);
          resolve();
        }));
        await new Promise<void>((resolve) => requestAnimationFrame(() => {
          samples.push({
            active: motion.input.isDirectInputActive(),
            sameMotion: motion === scrollportMotionForHost(host),
            offset: scroller.scrollTop,
          });
          resolve();
        }));
      }
    } finally {
      Set.prototype.add = nativeAdd;
      release();
    }
    return { samples, requeues };
  });
  expect(result.samples.every((sample) => sample.active && sample.sameMotion), JSON.stringify(result)).toBe(true);
  expect(result.requeues).toBeLessThan(50);
  // Synthetic wheel events only mark input; exercise native scrolling after the DOM move.
  await scroller.hover();
  await page.mouse.wheel(0, 800);
  await expect.poll(() => editor.locator(".cm-scroller").evaluate((node) => node.scrollTop), {
    message: JSON.stringify(result),
  }).toBeGreaterThan(100);
  await expect.poll(() => editor.evaluate(async (host) => {
    const url = "/src/platform/scrolling/scrollport-motion.ts";
    const { scrollportMotionForHost } = await import(/* @vite-ignore */ url) as typeof import("../src/platform/scrolling/scrollport-motion.ts");
    return scrollportMotionForHost(host as HTMLElement)?.input.isDirectInputActive();
  })).toBe(false);
  await expect(editor.locator(".os-scrollbar-vertical")).toHaveClass(/os-scrollbar-visible/);
  await editor.locator(".cm-content").click();
  await page.keyboard.type("Still responsive");
  await expect(editor.locator(".cm-content")).toContainText("Still responsive");
  expect(errors).toEqual([]);
});
