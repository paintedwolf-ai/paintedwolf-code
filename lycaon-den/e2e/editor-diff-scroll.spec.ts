import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

type MeasuredEditor = {
  contentDOM: HTMLElement;
  dispatch(...args: unknown[]): void;
  measure(...args: unknown[]): void;
  posAtCoords(...args: unknown[]): unknown;
  requestMeasure(request: { read(): void; write(): void }): void;
};
type ContentElement = HTMLElement & { cmTile: { root: { view: MeasuredEditor } } };

for (const scenario of ["scattered replacements", "dense replacements", "whole-file attribution", "large removal"] as const) {
  modelIndependentWebE2e(`${scenario} stays painted while scrolling and refreshing`, async ({ page, request }, testInfo) => {
    await openProjectFilesFixture(page, request, {
      prefix: "diff-scroll", name: "Diff scroll",
      seed: root => writeFileSync(path.join(root, "changes.js"), Array.from({ length: 1200 }, (_, index) =>
        `export const value${index} = ${index}; // ${"source context ".repeat(8)}`).join("\n")),
    });
    await page.getByTestId("files-tree-file").filter({ hasText: "changes.js" }).click();
    const host = page.getByTestId("files-editor-host").filter({ visible: true });
    await expect(host.locator(".cm-content")).toContainText("value0");
    const wrap = page.getByTestId("files-editor-wrap-toggle").filter({ visible: true });
    if (await wrap.getAttribute("aria-pressed") !== "true") await wrap.click();
    await expect(wrap).toHaveAttribute("aria-pressed", "true");
    await host.locator(".cm-content").evaluate(async (node, scenario) => {
      const moduleUrl = "/src/components/source/diff/scope-diff.ts";
      const { applyEditorScopeDiff, setScopeDeletedLines } = await import(/* @vite-ignore */ moduleUrl);
      const view = (node as ContentElement).cmTile.root.view;
      const current = Array.from({ length: 1200 }, (_, index) =>
        `export const value${index} = ${index}; // ${"source context ".repeat(8)}`).join("\n");
      const original = scenario === "whole-file attribution" ? "" : scenario === "large removal"
        ? Array.from({ length: 1200 }, (_, index) => `export const removed${index} = ${index};`).join("\n") + "\n" + current
        : Array.from({ length: 1200 }, (_, index) =>
        `export const value${index} = ${index % (scenario === "dense replacements" ? 3 : 30) === 0 ? -index - 1 : index}; // ${"source context ".repeat(8)}`).join("\n");
      const length = current.length;
      const input = { original, ...(scenario === "whole-file attribution" ? { attribution: { before: [],
        after: [{ index: 0, length, selected: true, visible: true, contributors: [] }] } } : {}) };
      Object.assign(node, { comparisonInput: input });
      applyEditorScopeDiff(view, input);
      setScopeDeletedLines(view, "inplace");
    }, scenario);
    await expect(host.locator(scenario === "large removal" ? ".cm-deletedLine" : ".cm-changedLine").first()).toBeVisible();
    const result = await host.locator(".cm-scroller").evaluate(async element => {
      const moduleUrl = "/src/components/source/diff/scope-diff.ts";
      const { applyEditorScopeDiff } = await import(/* @vite-ignore */ moduleUrl);
      const content = element.querySelector<ContentElement>(".cm-content")!;
      const view = content.cmTile.root.view;
      const frame = () => new Promise<number>(resolve => requestAnimationFrame(resolve));
      await new Promise<void>(resolve => view.requestMeasure({ read: () => undefined, write: resolve }));
      element.dispatchEvent(new Event("scroll"));
      await frame();
      const originalCoords = view.posAtCoords;
      const originalDispatch = view.dispatch;
      const originalMeasure = view.measure;
      const measurementDurations: number[] = [];
      view.measure = function (...args) {
        const start = performance.now();
        try { return originalMeasure.apply(this, args); }
        finally { measurementDurations.push(performance.now() - start); }
      };
      let refreshing = false, refreshTransactions = 0;
      view.dispatch = function (...args) {
        if (refreshing) refreshTransactions++;
        return originalDispatch.apply(this, args);
      };
      let coordinateReads = 0;
      view.posAtCoords = function (...args) { coordinateReads++; return originalCoords.apply(this, args); };
      const durations: number[] = [];
      const intervals: number[] = [];
      let previous: number | undefined;
      try {
        for (let i = 0; i < 120; i++) {
          const at = await frame();
          if (previous !== undefined) intervals.push(at - previous);
          previous = at;
          const start = performance.now();
          refreshing = true;
          applyEditorScopeDiff(view, structuredClone((content as ContentElement & { comparisonInput: unknown }).comparisonInput));
          refreshing = false;
          // A resting pointer receives moves as different source lines pass underneath.
          const lines = content.querySelectorAll(".cm-line, .cm-deletedLine");
          const line = lines[i % Math.min(10, lines.length)]!;
          for (let event = 0; event < 4; event++) line.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
          element.scrollTop += 16;
          element.dispatchEvent(new Event("scroll"));
          durations.push(performance.now() - start);
        }
        await frame();
        const viewport = element.getBoundingClientRect();
        const rows = [...content.querySelectorAll(".cm-line, .cm-deletedLine")].map(row => row.getBoundingClientRect());
        return { coordinateReads, refreshTransactions, durations, intervals, measurementDurations, offset: element.scrollTop,
          covered: [viewport.top + 30, viewport.bottom - 30].every(y => rows.some(row => row.top <= y && row.bottom >= y)),
          rows: rows.length };
      } finally { view.posAtCoords = originalCoords; view.dispatch = originalDispatch; view.measure = originalMeasure; }
    });
    const measurementsPath = testInfo.outputPath("diff-scroll-measurements.json");
    writeFileSync(measurementsPath, JSON.stringify(result, null, 2));
    await testInfo.attach("diff-scroll-measurements", { path: measurementsPath, contentType: "application/json" });
    expect(result.coordinateReads, JSON.stringify(result)).toBe(0);
    expect(result.refreshTransactions, JSON.stringify(result)).toBe(0);
    expect(result.offset).toBeGreaterThan(1000);
    expect(result.covered, JSON.stringify(result)).toBe(true);
    const rowLimit = scenario === "large removal" ? 1500 : 500;
    expect(result.rows).toBeLessThan(rowLimit);
    const scroller = host.locator(".cm-scroller");
    await scroller.hover();
    for (const movement of [1600, 5000, -3600]) {
      const previous = await scroller.evaluate(element => element.scrollTop);
      await page.mouse.wheel(0, movement);
      await expect.poll(() => scroller.evaluate(element => element.scrollTop)).not.toBe(previous);
      await expect.poll(() => scroller.evaluate(element => {
        const box = element.getBoundingClientRect();
        const rows = [...element.querySelectorAll(".cm-line, .cm-deletedLine")].map(row => row.getBoundingClientRect());
        return [box.top + 30, box.bottom - 30].every(y => rows.some(row => row.top <= y && row.bottom >= y));
      })).toBe(true);
      expect(await host.locator(".cm-line, .cm-deletedLine").count()).toBeLessThan(rowLimit);
    }
    await page.screenshot({ path: testInfo.outputPath("diff-scroll.png") });
  });
}
