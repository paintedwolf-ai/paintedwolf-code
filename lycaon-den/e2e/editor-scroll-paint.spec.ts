import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { openProjectFilesFixture, modelIndependentWebE2e } from "./helpers.ts";

type WheelPaintSample = { offset: number; covered: boolean; lines: number };
type WheelPaintProbe = HTMLElement & { wheelPaintSamples: WheelPaintSample[] };
type EditorLineGeometry = {
  lineBlockAtHeight(height: number): { from: number; top: number };
  lineBlockAt(position: number): { top: number };
};
type EditorContent = HTMLElement & { cmTile: { root: { view: EditorLineGeometry } } };

modelIndependentWebE2e("ordinary editor scrolling batches measurements and stays painted through resize", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "editor-frame-measurement", name: "Editor frame measurement",
    seed: (root) => writeFileSync(path.join(root, "scroll.go"),
      `package sample\n${Array.from({ length: 2000 }, (_, i) => `func value${i}() int {\n  return ${i}\n}\n`).join("\n")}`),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "scroll.go" }).click();
  const host = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(host.locator(".cm-content")).toContainText("package sample");
  const scroller = host.locator(".cm-scroller");
  const result = await scroller.evaluate(async (element) => {
    const frame = () => new Promise<number>((resolve) => requestAnimationFrame(resolve));
    element.scrollTop = 10;
    element.dispatchEvent(new Event("scroll"));
    await frame();
    await frame();
    const view = element.querySelector<EditorContent>(".cm-content")!.cmTile.root.view as EditorLineGeometry & {
      requestMeasure(request: { read(): void; write(): void }): void;
      dispatch(transaction: { selection: { anchor: number } }): void;
    };
    await new Promise<void>((resolve) => view.requestMeasure({ read: () => undefined, write: () => resolve() }));
    // Initial layout settles before scroll measurement begins.
    element.dispatchEvent(new Event("scroll"));
    let synchronous = false;
    let forcedMeasurements = 0;
    const forcedStacks: string[] = [];
    const durations: number[] = [];
    const frameIntervals: number[] = [];
    let previousFrame: number | undefined;
    const original = window.getComputedStyle;
    window.getComputedStyle = function (target, pseudo) {
      if (synchronous && target === element.querySelector(".cm-content")) {
        forcedMeasurements++;
        if (forcedStacks.length < 2) forcedStacks.push(new Error().stack ?? "");
      }
      return original.call(this, target, pseudo);
    };
    try {
      for (let batch = 0; batch < 24; batch++) {
        // Frame measurement settles state before the next scroll event.
        view.dispatch({ selection: { anchor: batch % 2 } });
        await new Promise<void>((resolve) => view.requestMeasure({ read: () => undefined, write: resolve }));
        const timestamp = await frame();
        if (previousFrame !== undefined) frameIntervals.push(timestamp - previousFrame);
        previousFrame = timestamp;
        synchronous = true;
        const start = performance.now();
        for (let event = 0; event < 3; event++) {
          element.scrollTop += 4;
          element.dispatchEvent(new Event("scroll"));
        }
        durations.push(performance.now() - start);
        synchronous = false;
      }
      await frame();
      return { forcedMeasurements, forcedStacks, durations, frameIntervals, offset: element.scrollTop };
    } finally { window.getComputedStyle = original; }
  });
  expect(result.forcedMeasurements, JSON.stringify(result)).toBe(0);
  expect(result.offset).toBeGreaterThan(250);
  // Timing is diagnostic because frame cadence depends on host load.
  console.log(`editor scroll batches: ${JSON.stringify(result)}`);
  for (const [width, height] of [[1320, 850], [1180, 740], [1440, 920], [1100, 700], [1320, 850]] as const) {
    await page.setViewportSize({ width, height });
    await expect.poll(() => scroller.evaluate((element) => {
      const viewport = element.getBoundingClientRect();
      const lines = [...element.querySelectorAll(".cm-line")].map((line) => line.getBoundingClientRect());
      return [viewport.top + 30, viewport.bottom - 30].every((y) => lines.some((line) => line.top <= y && line.bottom >= y));
    })).toBe(true);
  }
  await expect(host.locator(".cm-line")).not.toHaveCount(0);
  expect(await host.locator(".cm-line").count()).toBeLessThan(500);
});

modelIndependentWebE2e("large editor paints native wheel destinations", async ({
  page,
  request,
}, testInfo) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await openProjectFilesFixture(page, request, {
    prefix: "editor-scroll-paint",
    name: "Large editor scroll",
    seed: (root) => {
      writeFileSync(
        path.join(root, "large.txt"),
        Array.from({ length: 100_000 }, (_, i) => `Line ${i + 1}: scroll coverage`).join("\n"),
      );
    },
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "large.txt" }).click();
  const host = page.getByTestId("files-editor-host");
  await expect(host.locator(".cm-content")).toBeVisible();
  await expect(host.locator(".cm-content")).toContainText("Line 1: scroll coverage");
  await host.locator(".cm-content").focus();
  await page.keyboard.press("ArrowRight");
  await expect(page.getByText("Ln 1, Col 2", { exact: true })).toBeVisible();

  const scroller = host.locator(".cm-scroller");
  await scroller.hover();
  await scroller.evaluate((element) => {
    const view = element.querySelector<EditorContent>(".cm-content")!.cmTile.root.view as import("@codemirror/view").EditorView;
    const dispatch = view.dispatch;
    const selections: unknown[] = [];
    Object.assign(element, { wheelSelections: selections });
    view.dispatch = function (...args) {
      const before = this.state.selection.toJSON();
      const doc = this.state.doc;
      const stack = new Error().stack;
      Reflect.apply(dispatch, this, args);
      if (JSON.stringify(before) !== JSON.stringify(this.state.selection.toJSON())) {
        selections.push({ before, after: this.state.selection.toJSON(), docChanged: doc !== this.state.doc,
          offset: element.scrollTop, stack });
      }
    };
    const samples: WheelPaintSample[] = [];
    Object.assign(element, { wheelPaintSamples: samples });
    let previousOffset = element.scrollTop;
    element.addEventListener("scroll", () => {
      if (element.scrollTop === previousOffset) return;
      previousOffset = element.scrollTop;
      const box = element.getBoundingClientRect();
      const lines = Array.from(element.querySelectorAll(".cm-line"));
      const rects = lines.map((line) => line.getBoundingClientRect());
      // This offset precedes the next animation frame.
      samples.push({
        offset: element.scrollTop,
        covered: [box.top + 30, box.bottom - 30].every((y) =>
          rects.some((rect) => rect.top <= y && rect.bottom >= y)),
        lines: lines.length,
      });
    }, { passive: true });
  });
  for (const delta of [15_000, 30_000, 60_000, -30_000, -60_000]) {
    const destination = await scroller.evaluate((element, movement) => {
      const view = element.querySelector<EditorContent>(".cm-content")!.cmTile.root.view;
      const target = element.scrollTop + movement;
      // Height-map refinements preserve the destination inset and eight-pixel anchor probe.
      const line = view.lineBlockAtHeight(target + 8);
      return { from: line.from, inset: target - line.top };
    }, delta);
    await page.mouse.wheel(0, delta);
    await expect.poll(() => scroller.evaluate((element, expected) => {
      const view = element.querySelector<EditorContent>(".cm-content")!.cmTile.root.view;
      const samples = (element as WheelPaintProbe).wheelPaintSamples;
      const reported = samples[samples.length - 1]?.offset;
      const actual = element.scrollTop;
      const inset = actual - view.lineBlockAt(expected.from).top;
      const error = reported === undefined ? Number.POSITIVE_INFINITY : Math.max(
        Math.abs(inset - expected.inset), Math.abs(reported - actual),
      );
      return error <= 1 ? "aligned" : JSON.stringify({ expected, actual, inset, reported, samples });
    }, destination)).toBe("aligned");
  }
  const samples = await scroller.evaluate((element) =>
    (element as WheelPaintProbe).wheelPaintSamples);
  expect(samples.length).toBeGreaterThanOrEqual(5);
  expect(Math.max(...samples.map((sample) => sample.offset))).toBeGreaterThan(100_000);
  expect(samples.every((sample) => sample.covered), JSON.stringify(samples)).toBe(true);
  expect(samples.every((sample) => sample.lines < 500)).toBe(true);
  const selectionPath = testInfo.outputPath("wheel-selections.json");
  writeFileSync(selectionPath, JSON.stringify(await scroller.evaluate(element =>
    (element as HTMLElement & { wheelSelections: unknown[] }).wheelSelections), null, 2));
  await expect(page.getByText("Ln 1, Col 2", { exact: true })).toBeVisible();
});

for (const interaction of ["track", "thumb"] as const) {
  modelIndependentWebE2e(`large editor keeps text painted during scrollbar ${interaction} jumps`, async ({
    page,
    request,
  }) => {
    await page.addInitScript(() => {
      Object.defineProperty(window, "ScrollTimeline", { value: undefined, configurable: true });
    });
    await openProjectFilesFixture(page, request, {
      prefix: `editor-scroll-${interaction}`,
      name: `Large editor ${interaction}`,
      seed: (root) => {
        writeFileSync(
          path.join(root, "large.txt"),
          Array.from({ length: 100_000 }, (_, i) => `Line ${i + 1}: scroll coverage`).join("\n"),
        );
      },
    });
    await page.getByTestId("files-tree-file").filter({ hasText: "large.txt" }).click();
    const host = page.getByTestId("files-editor-host");
    await expect(host.locator(".cm-content")).toBeVisible();
    await expect(host.locator(".cm-content")).toContainText("Line 1: scroll coverage");
    const scrollbar = host.locator(".cm-editor > .os-scrollbar-vertical");
    await expect(scrollbar).toHaveClass(/os-scrollbar-visible/);
    if (interaction === "thumb") {
      await host.locator(".cm-scroller").hover();
      await expect(scrollbar).not.toHaveClass(/den-scrollbar-idle/);
      const handle = scrollbar.locator(".os-scrollbar-handle");
      await handle.evaluate((element) => {
        element.addEventListener("pointerdown", (event) => {
          element.setAttribute("data-test-pointer-id", String((event as PointerEvent).pointerId));
        }, { once: true });
      });
      await handle.hover();
      await page.mouse.down();
      await expect(host.locator(".cm-editor")).toHaveAttribute("data-den-scrollbar-dragging", "y");
    }
    try {
      const samples = await scrollbar.evaluate(async (bar, input) => {
        const editor = bar.parentElement!;
        const scroller = editor.querySelector<HTMLElement>(".cm-scroller")!;
        const track = bar.querySelector<HTMLElement>(".os-scrollbar-track")!;
        const handle = bar.querySelector<HTMLElement>(".os-scrollbar-handle")!;
        const trackBox = track.getBoundingClientRect();
        const results: Array<{ offset: number; covered: boolean; lines: number; thumbTop: number }> = [];
        const sample = () => {
          const box = scroller.getBoundingClientRect();
          const lines = Array.from(scroller.querySelectorAll(".cm-line"));
          const rects = lines.map((line) => line.getBoundingClientRect());
          results.push({
            offset: scroller.scrollTop,
            thumbTop: handle.getBoundingClientRect().top,
            covered: [box.top + 30, box.bottom - 30].every((y) =>
              rects.some((rect) => rect.top <= y && rect.bottom >= y)),
            lines: lines.length,
          });
        };
        for (const fraction of [0.25, 0.7, 0.4, 0.85, 0.1]) {
          const event = new PointerEvent(input === "thumb" ? "pointermove" : "pointerdown", {
            pointerId: Number(handle.getAttribute("data-test-pointer-id") ?? 1),
            isPrimary: true,
            button: 0,
            clientY: trackBox.top + trackBox.height * fraction,
            bubbles: true,
            cancelable: true,
          });
          if (input === "thumb") {
            handle.dispatchEvent(event);
            // The thumb's queued write runs first in this same frame.
            await new Promise<void>((resolve) => requestAnimationFrame(() => {
              sample();
              resolve();
            }));
          } else {
            await new Promise<void>((resolve) => requestAnimationFrame(() => {
              track.dispatchEvent(event);
              sample();
              resolve();
            }));
          }
        }
        return results;
      }, interaction);
      expect(samples.every((sample) => sample.covered), JSON.stringify(samples)).toBe(true);
      expect(samples.every((sample) => sample.lines < 500)).toBe(true);
      expect(Math.min(...samples.map((sample) => sample.offset))).toBeGreaterThan(50_000);
      expect(samples[1]!.offset).toBeGreaterThan(samples[0]!.offset);
      expect(samples[2]!.offset).toBeLessThan(samples[1]!.offset);
      expect(new Set(samples.map((sample) => Math.round(sample.thumbTop))).size, JSON.stringify(samples)).toBeGreaterThan(2);
    } finally {
      if (interaction === "thumb") await page.mouse.up();
    }
  });
}


modelIndependentWebE2e("editor scrollbars track live content growth and contraction", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "editor-content-resize", name: "Editor content resize",
    seed: (root) => writeFileSync(path.join(root, "resize.txt"), "Short text\n"),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "resize.txt" }).click();
  const host = page.getByTestId("files-editor-host");
  const content = host.locator(".cm-content");
  const scrollbar = host.locator(".cm-editor > .os-scrollbar-vertical");
  await expect(content).toBeVisible();
  await expect(content).toHaveAttribute("contenteditable", "true");
  await content.focus();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.insertText(Array.from({ length: 400 }, (_, i) => `Line ${i}\n`).join(""));
  await expect(scrollbar).toHaveClass(/os-scrollbar-visible/);
  await expect.poll(() => host.locator(".cm-scroller").evaluate((el) => el.scrollTop)).toBeGreaterThan(1000);
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText("Short again\n");
  // Scroll-past-end leaves a line of intentional overflow in this two-line document.
  await expect.poll(() => host.locator(".cm-scroller").evaluate((el) => el.scrollHeight - el.clientHeight)).toBeLessThan(100);
  await expect.poll(() => scrollbar.evaluate((bar) => {
    const track = bar.querySelector(".os-scrollbar-track")!.getBoundingClientRect();
    const thumb = bar.querySelector(".os-scrollbar-handle")!.getBoundingClientRect();
    return thumb.height / track.height;
  })).toBeGreaterThan(0.9);
  await expect.poll(() => host.locator(".cm-scroller").evaluate((el) => el.scrollTop)).toBe(0);
  const horizontal = host.locator(".cm-editor > .os-scrollbar-horizontal");
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText("x".repeat(400));
  await expect(horizontal).toHaveClass(/os-scrollbar-visible/);
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.insertText("Short");
  await expect(horizontal).not.toHaveClass(/os-scrollbar-visible/);
});
