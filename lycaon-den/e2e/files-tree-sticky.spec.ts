import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e as test, openProjectFilesFixture } from "./helpers.ts";

type ThumbTrace = { frame: number; samples: number; failures: unknown[] };
type WheelTrace = { frame: number; previous: number; times: number[] };
declare global { interface Window { stickyThumbTrace?: ThumbTrace; stickyWheelTrace?: WheelTrace } }

test("sticky tree rows stay joined and fold indentation during scrolling at every size", async ({ page, request }, info) => {
  info.setTimeout(120_000);
  await page.setViewportSize({ width: 1400, height: 1000 });
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "sticky-tree-levels", name: "Sticky tree levels",
    seed(root) {
      const parent = path.join(root, ...Array.from({ length: 12 }, (_, index) => `folder-${index + 1}`));
      mkdirSync(parent, { recursive: true });
      for (let group = 0; group < 6; group++) {
        const directory = path.join(parent, `group-${group}`);
        mkdirSync(directory);
        for (let file = 0; file < 12; file++) writeFileSync(path.join(directory, `file-${file}.ts`), "export {};\n");
      }
      const following = path.join(root, "z-after");
      mkdirSync(following);
      for (let file = 0; file < 100; file++) writeFileSync(path.join(following, `file-${file}.ts`), "export {};\n");
    },
  });
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().click({ button: "right" });
  const accepted = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/apply") &&
    response.request().postDataJSON()?.command?.kind === "disclose" && response.url().includes(`/projects/${project.id}/source/views/`));
  await page.getByTestId("files-ctx-expand-all").click();
  expect((await accepted).ok()).toBe(true);
  await expect(page.getByTestId("files-tree-file").first()).toBeVisible({ timeout: 30_000 });
  const results = await page.evaluate(async () => {
    const prefsUrl = "/src/settings/editor/editor-prefs.ts";
    const prefs = await import(/* @vite-ignore */ prefsUrl) as typeof import("../src/settings/editor/editor-prefs.ts");
    const scroller = document.querySelector<HTMLElement>('[data-testid="files-tree-scroll"]')!;
    const tree = document.querySelector<HTMLElement>('[data-testid="files-tree"]')!;
    const frame = scroller.closest('[data-testid="files-tree-scroll-frame"]')!;
    const findings: { levels: number; tick: number; kind: string; actual: number; expected: number }[] = [];
    const nextFrame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
    let foldedFrames = 0;
    for (const levels of [3, 4, 5, 6, 7, 8, 9, 10, 0, 5]) {
      await prefs.saveEditorTreeVisibleLevels(levels);
      scroller.scrollTop = 0;
      await nextFrame(); await nextFrame();
      for (let tick = 0; tick < 100; tick++) {
        scroller.scrollTop += tick < 50 ? 65 : -65;
        await nextFrame();
        const band = frame.querySelector<HTMLElement>('[data-testid="files-tree-sticky"]');
        const fold = Number(tree.dataset.foldLevels);
        if (fold > 0) foldedFrames++;
        if (levels === 0 && (band || fold)) findings.push({ levels, tick, kind: "disabled", actual: fold, expected: 0 });
        if (!band) continue;
        const bounds = [...band.querySelectorAll('.den-files-tree-sticky__slot')]
          .map(slot => slot.getBoundingClientRect()).filter(rect => rect.bottom > band.getBoundingClientRect().top)
          .sort((a, b) => a.top - b.top);
        for (let index = 1; index < bounds.length; index++) {
          const gap = bounds[index]!.top - bounds[index - 1]!.bottom;
          if (gap > 1) findings.push({ levels, tick, kind: "sticky gap", actual: gap, expected: 0 });
        }
        const floor = Number(tree.dataset.foldFloor);
        for (const row of frame.querySelectorAll<HTMLElement>('.den-files-tree__row[data-isdir]')) {
          const depth = row.dataset.path === "." ? 0 : row.dataset.path!.split('/').length;
          const expected = Math.max(Math.min(depth, floor), depth - fold) * 14 + (row.dataset.isdir === "true" ? 0 : 16);
          const actual = parseFloat(getComputedStyle(row).paddingLeft);
          if (Math.abs(actual - expected) > 1) findings.push({ levels, tick, kind: "indentation", actual, expected });
        }
      }
    }
    return { findings: findings.slice(0, 30), foldedFrames };
  });
  await info.attach("sticky scrolling", { body: JSON.stringify(results), contentType: "application/json" });
  expect(results.foldedFrames).toBeGreaterThan(0);
  expect(results.findings).toEqual([]);
  await page.getByTestId("files-tree-scroll").evaluate(element => { element.scrollTop = 520; });
  await expect(page.getByTestId("files-tree-sticky-elision")).toBeVisible();
  await page.getByTestId("files-tree-scroll-frame").screenshot({ path: info.outputPath("sticky-tree.png") });
});

test("deep paged trees keep sticky geometry through thumb jumps and reversals", async ({ page, request }, info) => {
  info.setTimeout(120_000);
  const depth = 72;
  const parent = Array.from({ length: depth }, (_, index) => `d${index + 1}`).join("/");
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "paged-sticky-tree", name: "Paged sticky tree",
    seed(root) {
      for (let group = 0; group < 120; group++) {
        const directory = path.join(root, parent, `group-${String(group).padStart(3, "0")}`);
        mkdirSync(directory, { recursive: true });
        for (let file = 0; file < 20; file++) writeFileSync(path.join(directory, `file-${file}.ts`), "export {};\n");
      }
    },
  });
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().click({ button: "right" });
  const accepted = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/apply") &&
    response.request().postDataJSON()?.command?.kind === "disclose" && response.url().includes(`/projects/${project.id}/source/views/`));
  await page.getByTestId("files-ctx-expand-all").click();
  expect((await accepted).ok()).toBe(true);
  const viewport = page.getByTestId("files-tree-scroll");
  await expect.poll(() => viewport.evaluate(element => element.scrollHeight), { timeout: 30_000 })
    .toBeGreaterThanOrEqual((depth + 1 + 120 * 21) * 26);
  const frame = page.getByTestId("files-tree-scroll-frame");
  const bar = frame.locator(".os-scrollbar-vertical");
  const samples: unknown[] = [];
  for (const levels of [3, 5, 10, 0]) {
    await page.evaluate(async levels => {
      const prefsUrl = "/src/settings/editor/editor-prefs.ts";
      const prefs = await import(/* @vite-ignore */ prefsUrl) as typeof import("../src/settings/editor/editor-prefs.ts");
      await prefs.saveEditorTreeVisibleLevels(levels);
    }, levels);
    await viewport.hover();
    const track = await bar.locator(".os-scrollbar-track").boundingBox();
    const thumb = await bar.locator(".os-scrollbar-handle").boundingBox();
    expect(track).not.toBeNull(); expect(thumb).not.toBeNull();
    const x = thumb!.x + thumb!.width / 2;
    await page.mouse.move(x, thumb!.y + thumb!.height / 2);
    await page.mouse.down();
    await page.evaluate(({ levels, depth }) => {
      const trace: ThumbTrace = { frame: 0, samples: 0, failures: [] };
      window.stickyThumbTrace = trace;
      const sample = () => {
        const tree = document.querySelector<HTMLElement>('[data-testid="files-tree"]')!;
        const viewport = document.querySelector<HTMLElement>('[data-testid="files-tree-scroll"]')!;
        if (viewport.scrollTop > (depth + 10) * 26) {
          trace.samples++;
          const cap = Math.min(levels, Math.floor(viewport.clientHeight * 0.4 / 26));
          const expected = levels === 0 ? 0 : depth + 2 - cap;
          const actual = Number(tree.dataset.foldLevels);
          if (actual !== expected && trace.failures.length < 20) trace.failures.push({ scrollTop: viewport.scrollTop, actual, expected });
        }
        trace.frame = requestAnimationFrame(sample);
      };
      trace.frame = requestAnimationFrame(sample);
    }, { levels, depth });
    for (const fraction of [0.2, 0.8, 0.4, 0.95, 0.12]) {
      await page.mouse.move(x, track!.y + thumb!.height / 2 + (track!.height - thumb!.height) * fraction, { steps: 5 });
      // Pointer coordinates are quantized in viewport pixels, which can span many tree rows.
      await expect.poll(() => viewport.evaluate((element, { fraction, travel }) =>
        Math.abs(element.scrollTop / (element.scrollHeight - element.clientHeight) - fraction) * travel,
      { fraction, travel: track!.height - thumb!.height }))
        .toBeLessThan(1);
      await expect(frame.locator('.den-files-tree__hint--loading:visible')).toHaveCount(0);
      const sample = await frame.evaluate(host => {
        const tree = host.querySelector<HTMLElement>('[data-testid="files-tree"]')!;
        const band = host.querySelector<HTMLElement>('[data-testid="files-tree-sticky"]');
        const fold = Number(tree.dataset.foldLevels);
        const floor = Number(tree.dataset.foldFloor);
        const slots = [...host.querySelectorAll('.den-files-tree-sticky__slot')].map(row => row.getBoundingClientRect())
          .filter(rect => rect.bottom > (band?.getBoundingClientRect().top ?? 0)).sort((a, b) => a.top - b.top);
        const gaps = slots.slice(1).map((rect, index) => rect.top - slots[index]!.bottom).filter(gap => gap > 1);
        const indents = [...tree.querySelectorAll<HTMLElement>('.den-files-tree__row[data-isdir="false"]')].map(row => {
          const depth = row.dataset.path!.split('/').length;
          return { actual: parseFloat(getComputedStyle(row).paddingLeft), expected: Math.max(Math.min(depth, floor), depth - fold) * 14 + 16 };
        });
        const viewport = host.querySelector<HTMLElement>('[data-testid="files-tree-scroll"]')!;
        return { gaps, indents, sticky: !!band, fold, viewportHeight: viewport.clientHeight, scrollTop: viewport.scrollTop };
      });
      samples.push({ levels, fraction, ...sample });
      expect(sample.gaps).toEqual([]);
      expect(sample.indents.every(indent => Math.abs(indent.actual - indent.expected) < 1)).toBe(true);
      if (levels === 0) expect(sample.sticky).toBe(false);
    }
    await page.mouse.up();
    const trace = await page.evaluate(() => {
      const trace = window.stickyThumbTrace!;
      cancelAnimationFrame(trace.frame);
      delete window.stickyThumbTrace;
      return trace;
    });
    await info.attach(`thumb frames at ${levels} levels`, { body: JSON.stringify(trace), contentType: "application/json" });
    expect(trace.samples).toBeGreaterThan(0);
    expect(trace.failures).toEqual([]);
  }
  await info.attach("paged sticky geometry", { body: JSON.stringify(samples), contentType: "application/json" });
  await page.evaluate(async () => {
    const prefsUrl = "/src/settings/editor/editor-prefs.ts";
    const prefs = await import(/* @vite-ignore */ prefsUrl) as typeof import("../src/settings/editor/editor-prefs.ts");
    await prefs.saveEditorTreeVisibleLevels(5);
  });
  await viewport.hover();
  await page.evaluate(() => {
    const trace: WheelTrace = { frame: 0, previous: performance.now(), times: [] };
    window.stickyWheelTrace = trace;
    const sample = (now: number) => {
      trace.times.push(now - trace.previous); trace.previous = now;
      trace.frame = requestAnimationFrame(sample);
    };
    trace.frame = requestAnimationFrame(sample);
  });
  for (let tick = 0; tick < 60; tick++) {
    await page.mouse.wheel(0, tick < 30 ? 32 : -32);
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())));
  }
  const timing = await page.evaluate(() => {
    const trace = window.stickyWheelTrace!;
    cancelAnimationFrame(trace.frame); delete window.stickyWheelTrace;
    const frames = trace.times.sort((a, b) => a - b);
    return { samples: frames.length, medianMs: frames[Math.floor(frames.length / 2)],
      p95Ms: frames[Math.floor(frames.length * 0.95)], maxMs: frames.at(-1) };
  });
  const timingPath = info.outputPath("wheel-frame-timing.json");
  writeFileSync(timingPath, JSON.stringify(timing));
  await info.attach("wheel frame timing", { path: timingPath, contentType: "application/json" });
});
