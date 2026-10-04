import { linkSync, mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page, type Request } from "@playwright/test";
import { modelIndependentWebE2e as test, openProjectFilesFixture } from "./helpers.ts";
import type { SourceTreeView, SourceTreeFrame } from "../src/api/types.ts";
import { installLargeTreeFrames } from "./source-tree-frame-fixture.ts";

test.use({ trace: "retain-on-failure" });

async function scrollTreeWindow(page: Page, direction: number, frames = 240) {
  return page.getByTestId("files-tree-scroll").evaluate(async (element, { direction, frames }) => {
    const blankFrames: number[] = [];
    const gaps: { frame: number; scrollTop: number; paint: string | undefined; top: number; bottom: number; rows: number[][] }[] = [];
    const started = performance.now();
    for (let frame = 0; frame < frames; frame++) {
      element.scrollTop += (frame < frames / 2 ? direction : -direction) * 96;
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
      const bounds = element.getBoundingClientRect();
      const inner = element.querySelector<HTMLElement>(".den-files-tree-virtual__inner");
      const painted = [...element.querySelectorAll(".den-files-tree-virtual__row")]
        .map(row => row.getBoundingClientRect()).filter(rect => rect.bottom > bounds.top && rect.top < bounds.bottom)
        .sort((left, right) => left.top - right.top);
      const contentBottom = element.querySelector(".den-files-tree-virtual")!.getBoundingClientRect().bottom;
      const requiredBottom = Math.min(bounds.bottom, contentBottom);
      let covered = bounds.top;
      for (const rect of painted) {
        if (rect.top > covered + 1) break;
        covered = Math.max(covered, rect.bottom);
      }
      if (covered < requiredBottom - 1) {
        blankFrames.push(frame);
        if (gaps.length < 5) gaps.push({ frame, scrollTop: element.scrollTop, paint: inner?.style.transform,
          top: covered - bounds.top, bottom: requiredBottom - bounds.top,
          rows: painted.slice(0, 4).map(rect => [rect.top - bounds.top, rect.bottom - bounds.top]) });
      }
    }
    return { blankFrames, gaps, elapsedMs: performance.now() - started, scrollTop: element.scrollTop };
  }, { direction, frames });
}

async function expand(page: Page, project: string): Promise<SourceTreeView> {
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().click({ button: "right" });
  const accepted = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/apply") && response.request().postDataJSON()?.command?.kind === "disclose" && response.url().includes(`/projects/${project}/source/views/`));
  await page.getByTestId("files-ctx-expand-all").click();
  const response = await accepted;
  expect(response.ok()).toBe(true);
  return response.json();
}
function observeFrames(page: Page, project: string) {
  const frames: { bytes: number; rows: number; extent: number; latencyMs: number; start: number; revision: string; presentation: string }[] = [];
  let rowRequests = 0;
  const count = (request: Request) => {
    if (request.url().includes(`/projects/${project}/source/views/`) && new URL(request.url()).pathname.endsWith("/rows")) rowRequests++;
  };
  const pending = new Set<Promise<void>>(), errors: unknown[] = [];
  const collect = (request: Request) => {
    if (!request.url().includes(`/projects/${project}/source/views/`) || !new URL(request.url()).pathname.endsWith("/rows")) return;
    const capture = (async () => {
      const response = await request.response();
      if (!response?.ok()) return;
      const text = await response.text();
      const frame = JSON.parse(text) as SourceTreeFrame;
      frames.push({ bytes: new TextEncoder().encode(text).length, rows: frame.rows.length, extent: frame.extent.rows, latencyMs: request.timing().responseEnd,
        start: frame.span.start, revision: frame.projection_revision, presentation: new URL(request.url()).pathname.split("/presentations/")[1]!.split("/")[0]! });
    })().catch(error => { errors.push(error); });
    pending.add(capture);
    void capture.finally(() => pending.delete(capture));
  };
  page.on("requestfinished", collect);
  page.on("request", count);
  return { frames, requests: () => rowRequests, async stop() {
    page.off("requestfinished", collect);
    page.off("request", count);
    await Promise.all(pending);
    expect(errors).toEqual([]);
  } };
}

async function dragTreeThumb(page: Page, fractions: number[]): Promise<void> {
  const bar = page.getByTestId("files-tree-scroll-frame").locator(".os-scrollbar-vertical");
  await page.getByTestId("files-tree-scroll").hover();
  await expect(bar).toBeVisible();
  const track = await bar.locator(".os-scrollbar-track").boundingBox();
  const handle = await bar.locator(".os-scrollbar-handle").boundingBox();
  expect(track).not.toBeNull(); expect(handle).not.toBeNull();
  const x = handle!.x + handle!.width / 2;
  const y = handle!.y + handle!.height / 2;
  const travel = track!.height - handle!.height;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await expect(page.getByTestId("files-tree-scroll-frame")).toHaveAttribute("data-den-scrollbar-dragging", "y");
  const dragTo = async (fraction: number) => {
    await page.mouse.move(x, track!.y + handle!.height / 2 + travel * fraction, { steps: 6 });
    await expect.poll(async () => {
      const sample = await bar.evaluate(element => {
        const track = element.querySelector(".os-scrollbar-track")!.getBoundingClientRect();
        const handle = element.querySelector(".os-scrollbar-handle")!.getBoundingClientRect();
        return (handle.top - track.top) / (track.height - handle.height);
      });
      return Math.abs(sample - fraction);
    }, { message: `thumb remains at ${fraction}` }).toBeLessThan(0.003);
  };
  for (const fraction of fractions) await dragTo(fraction);
  await page.mouse.up();
}

test("expand all opens a large tree in bounded frames and keeps rows virtualized", async ({ page, request }, info) => {
  info.setTimeout(90_000);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "tree-expansion", name: "Tree expansion",
    seed(root) {
      for (let index = 0; index < 600; index++) {
        const folder = path.join(root, `folder-${String(index).padStart(4, "0")}`, "nested");
        mkdirSync(folder, { recursive: true }); writeFileSync(path.join(folder, "file.ts"), "export {};\n");
      }
    },
  });
  const capture = observeFrames(page, project.id), { frames } = capture, started = Date.now();
  const painting = page.getByTestId("files-tree-scroll").evaluate(async element => {
    const heights: number[] = [], loading: number[] = [];
    for (let frame = 0; frame < 90; frame++) {
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
      heights.push(element.scrollHeight);
      const bounds = element.getBoundingClientRect();
      const missing = [...element.querySelectorAll(".den-files-tree__hint--loading")].some(row => {
        const rect = row.getBoundingClientRect();
        return rect.bottom > bounds.top && rect.top < bounds.bottom;
      });
      if (missing) loading.push(frame);
    }
    return { heights, loading };
  });
  const accepted = await expand(page, project.id);
  const painted = await painting;
  writeFileSync(info.outputPath("expansion-painting.json"), JSON.stringify(painted, null, 2));
  expect(painted.loading).toEqual([]);
  expect(painted.heights.at(-1)).toBeGreaterThanOrEqual(Math.min(4_000_000, accepted.extent.rows * 26));
  expect(new Set(painted.heights).size).toBeLessThanOrEqual(2);
  await expect(page.getByTestId("files-tree-expansion-status")).toHaveCount(0);
  expect(await page.getByTestId("files-tree-dir").count()).toBeLessThan(200);
  await page.getByTestId("files-tree-scroll").evaluate(element => { element.scrollTop = element.scrollHeight; });
  await expect(page.locator('.den-files-tree__label--file[data-path="folder-0599/nested/file.ts"]')).toBeVisible();
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().click({ button: "right" });
  await page.getByTestId("files-ctx-collapse-all").click();
  await expect(page.locator('.den-files-tree__label--dir[data-path="folder-0599"]')).toBeVisible();
  await expect.poll(() => page.getByTestId("files-tree-scroll").evaluate(element => element.scrollHeight)).toBe(601 * 26 + 16);
  await expect(page.locator('.den-files-tree__label--dir[data-path="folder-0000/nested"]')).toHaveCount(0);
  await expand(page, project.id);
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().press("End");
  await expect(page.locator('.den-files-tree__label--file[data-path="folder-0599/nested/file.ts"]')).toBeVisible();
  await capture.stop();
  expect(frames.length).toBeGreaterThan(0);
  expect(frames.every(frame => frame.rows <= 200 && frame.bytes <= 256 * 1024)).toBe(true);
  writeFileSync(info.outputPath("expansion-performance.json"), JSON.stringify({ elapsedMs: Date.now() - started, frames }, null, 2));
  await page.screenshot({ path: info.outputPath("expanded-tree.png") });
});


test("expands a generated repository beyond the directory and child cache limits", async ({ page, request }, info) => {
  info.setTimeout(240_000);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "large-tree-expansion", name: "Large tree expansion",
    seed(root) {
      const payload = path.join(root, ".payload.ts"); writeFileSync(payload, "export const value = 1;\n");
      writeFileSync(path.join(root, "z-last.ts"), "export {};\n");
      // ext4 caps one inode at 65,000 links, so each group links to its own first file.
      let source = "";
      for (let group = 0; group < 160; group++) for (let folder = 0; folder < 100; folder++) {
        const directory = path.join(root, `group-${String(group).padStart(4, "0")}`, `folder-${String(folder).padStart(4, "0")}`);
        mkdirSync(directory, { recursive: true });
        for (let file = 0; file < 10; file++) {
          const target = path.join(directory, `file-${String(file).padStart(2, "0")}.ts`);
          if (folder === 0 && file === 0) {
            writeFileSync(target, "export const value = 1;\n");
            source = target;
          } else {
            linkSync(source, target);
          }
        }
      }
    },
  });
  const capture = observeFrames(page, project.id), { frames } = capture, started = Date.now();
  await expand(page, project.id);
  let firstExpandedFrameMs: number | undefined;
  try {
    await expect(page.locator('.den-files-tree__label--file[data-path="group-0000/folder-0000/file-00.ts"]')).toBeVisible({ timeout: 30_000 });
    firstExpandedFrameMs = Date.now() - started;
    const idleRequests = capture.requests();
    const idle = await page.getByTestId("files-tree-scroll").evaluate(async element => {
      const started = performance.now();
      let missing = 0;
      while (performance.now() - started < 4_000) {
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
        const bounds = element.getBoundingClientRect();
        if ([...element.querySelectorAll(".den-files-tree__hint--loading")].some(row => {
          const rect = row.getBoundingClientRect();
          return rect.bottom > bounds.top && rect.top < bounds.bottom;
        })) missing++;
      }
      return { elapsedMs: performance.now() - started, missing };
    });
    const idleSamples = { ...idle, requests: capture.requests() - idleRequests };
    writeFileSync(info.outputPath("cold-tree-idle.json"), JSON.stringify(idleSamples, null, 2));
    expect(idle.missing).toBe(0);
    expect(idleSamples.requests).toBeLessThan(12 * idle.elapsedMs / 1_000);
    await page.getByTestId("files-tree").locator('.den-files-tree__label--dir[data-path="."]').press("End");
    await expect(page.locator('.den-files-tree__label--file[data-path="z-last.ts"]')).toBeVisible();
    await expect.poll(() => page.getByTestId("files-tree-scroll").evaluate(element => {
      const bounds = element.getBoundingClientRect();
      return [...element.querySelectorAll(".den-files-tree__hint--loading")].filter(row => {
        const rect = row.getBoundingClientRect();
        return rect.bottom > bounds.top && rect.top < bounds.bottom;
      }).length;
    })).toBe(0);
    await expect(page.getByTestId("files-tree-expansion-status")).toHaveCount(0);
    expect(await page.getByTestId("files-tree-dir").count()).toBeLessThan(200);
    await page.getByTestId("files-tree-scroll").evaluate(element => { element.scrollTop = element.scrollHeight; });
    const last = page.locator('.den-files-tree__label--file[data-path="group-0159/folder-0099/file-09.ts"]');
    await expect(last).toBeVisible();
    const upward = await scrollTreeWindow(page, -1);
    writeFileSync(info.outputPath("cold-tree-upward.json"), JSON.stringify(upward, null, 2));
    expect(upward.blankFrames).toEqual([]);
    await page.getByTestId("files-tree-scroll").evaluate(element => { element.scrollTop = element.scrollHeight; });
    await expect(last).toBeVisible();
    await page.getByTestId("project-search-entry").click();
    await expect(page.getByTestId("global-search-view")).toBeVisible();
    await page.getByTestId("project-files-entry").click();
    await expect(last).toBeVisible();
    await last.press("Home");
    await expect(page.locator('.den-files-tree__label--file[data-path="group-0000/folder-0000/file-00.ts"]')).toBeVisible();
    await dragTreeThumb(page, [0.25, 0.5, 0.75, 0.95, 0.6, 0.3]);
    await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().press("Home");
    await expect(page.locator('.den-files-tree__label--file[data-path="group-0000/folder-0000/file-00.ts"]')).toBeVisible();
    const scrolling = await page.getByTestId("files-tree-scroll").evaluate(async element => {
      const blankFrames: number[] = [];
      const started = performance.now();
      for (let frame = 0; frame < 360; frame++) {
        element.scrollTop += frame < 180 ? 96 : -96;
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
        const bounds = element.getBoundingClientRect();
        const missing = [...element.querySelectorAll(".den-files-tree__hint--loading")].some(row => {
          const rect = row.getBoundingClientRect();
          return rect.bottom > bounds.top && rect.top < bounds.bottom;
        });
        if (missing) blankFrames.push(frame);
      }
      return { blankFrames, elapsedMs: performance.now() - started, scrollTop: element.scrollTop };
    });
    writeFileSync(info.outputPath("tree-scrolling-performance.json"), JSON.stringify(scrolling, null, 2));
    expect(scrolling.blankFrames).toEqual([]);
    await capture.stop();
    expect(frames.length).toBeGreaterThan(0);
    expect(frames.every(frame => frame.rows <= 200 && frame.bytes <= 256 * 1024)).toBe(true);
    expect(capture.requests()).toBeLessThan(64 * (1 + (Date.now() - started) / 1_000));
    await page.screenshot({ path: info.outputPath("large-expanded-tree.png") });
  } finally {
    writeFileSync(info.outputPath("large-expansion-performance.json"), JSON.stringify({ firstExpandedFrameMs, scenarioElapsedMs: Date.now() - started, rowRequests: capture.requests(), frames }, null, 2));
  }
});

test("expansion survives leaving Files and returning while a frame is pending", async ({ page, request }, info) => {
  info.setTimeout(90_000);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "tree-expansion-navigation", name: "Expansion navigation",
    seed(root) {
      writeFileSync(path.join(root, "README.md"), "# Expansion navigation\n");
      for (let index = 0; index < 600; index++) {
        const directory = path.join(root, `folder-${String(index).padStart(4, "0")}`, "nested");
        mkdirSync(directory, { recursive: true }); writeFileSync(path.join(directory, "file.ts"), "export {};\n");
      }
    },
  });
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().press("End");
  await page.locator('.den-files-tree__label--file[data-path="README.md"]').dblclick();
  await expect(page.getByTestId("files-editor-host").locator(".cm-content")).toBeVisible();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let held = false;
  await page.route(`**/projects/${project.id}/source/views/*/presentations/*/rows?*`, async route => {
    if (!held) { held = true; await gate; }
    await route.continue().catch(() => {});
  });
  try {
    await expand(page, project.id);
    await expect.poll(() => held).toBe(true);
    await page.getByTestId("project-search-entry").click();
    await expect(page.getByTestId("global-search-view")).toBeVisible();
    await page.getByTestId("project-files-entry").click();
    await expect(page.getByTestId("files-tree")).toBeVisible();
    release();
    await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().press("End");
    await expect(page.locator('.den-files-tree__label--file[data-path="folder-0599/nested/file.ts"]')).toBeVisible();
  } finally { release(); }
});

test("expand and collapse preserve row elements and contract the scrollbar from the bottom", async ({ page, request }, info) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "tree-collapse", name: "Tree collapse",
    seed(root) {
      for (let index = 0; index < 80; index++) {
        const folder = path.join(root, `folder-${String(index).padStart(4, "0")}`, "nested");
        mkdirSync(folder, { recursive: true });
        writeFileSync(path.join(folder, "file.ts"), "export {};\n");
      }
    },
  });
  const tree = page.getByTestId("files-tree"), scroll = page.getByTestId("files-tree-scroll");
  const root = await tree.locator('.den-files-tree__label--dir[data-path="."]').elementHandle();
  const folder = await tree.locator('.den-files-tree__label--dir[data-path="folder-0000"]').elementHandle();
  expect(root).not.toBeNull(); expect(folder).not.toBeNull();
  const collapsedHeight = await scroll.evaluate(element => element.scrollHeight);
  await expand(page, project.id);
  await expect(tree.locator('.den-files-tree__label--file[data-path="folder-0000/nested/file.ts"]')).toBeVisible();
  expect(await root!.evaluate(element => element.isConnected)).toBe(true);
  expect(await folder!.evaluate(element => element.isConnected)).toBe(true);
  await tree.locator('.den-files-tree__label--dir[data-path="."]').click({ button: "right" });
  await page.getByTestId("files-ctx-collapse-all").click();
  await expect.poll(() => scroll.evaluate(element => element.scrollHeight)).toBe(collapsedHeight);
  expect(await root!.evaluate(element => element.isConnected)).toBe(true);
  expect(await folder!.evaluate(element => element.isConnected)).toBe(true);
  await expand(page, project.id);
  await tree.locator('.den-files-tree__label--dir[data-path="."]').press("End");
  await expect(tree.locator('.den-files-tree__label--file[data-path="folder-0079/nested/file.ts"]')).toBeVisible();
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().click({ button: "right" });
  await page.getByTestId("files-ctx-collapse-all").click();
  await expect.poll(() => scroll.evaluate(element => element.scrollHeight)).toBe(collapsedHeight);
  await expect.poll(() => scroll.evaluate(element => element.scrollTop <= element.scrollHeight - element.clientHeight + 1)).toBe(true);
  await expect(tree.locator('.den-files-tree__label--dir[data-path="folder-0079"]')).toBeVisible();
  await scroll.evaluate(element => { element.scrollTop = 0; });
  await expect(tree.locator('.den-files-tree__label--dir[data-path="folder-0000"]')).toBeVisible();
  await page.screenshot({ path: info.outputPath("collapsed-tree.png") });
});

test("the thumb addresses a million-row tree and contracts after collapse", async ({ page, request }) => {
  await installLargeTreeFrames(page);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "segmented-tree", name: "Segmented tree",
    seed(root) { writeFileSync(path.join(root, "README.md"), "# Tree geometry\n"); },
  });
  await expand(page, project.id);
  await expect(page.locator('.den-files-tree__label--file[data-path="folder-000/file-0.ts"]')).toBeVisible();
  await expect.poll(() => page.getByTestId("files-tree-scroll").evaluate(element => element.scrollHeight)).toBe(4_000_016);
  await dragTreeThumb(page, [0.1, 0.3, 0.6, 0.95, 0.5, 0.3]);
  await expect.poll(() => page.getByTestId("files-tree-scroll").evaluate(element => {
    const file = element.querySelector<HTMLElement>('.den-files-tree__label--file');
    const match = file?.dataset.path?.match(/^folder-(\d+)\/file-(\d+)\.ts$/);
    const row = file?.closest<HTMLElement>(".den-files-tree-virtual__row");
    if (!match || !row) return Infinity;
    const rank = 2 + Number(match[1]) * 10_000 + Number(match[2]);
    const logical = rank * 26 - new DOMMatrix(row.style.transform).m42 + element.scrollTop;
    return Math.abs(logical / (1_000_001 * 26 - element.clientHeight) - 0.3);
  })).toBeLessThan(0.003);
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().press("End");
  await expect(page.locator('.den-files-tree__label--file[data-path="folder-099/file-9998.ts"]')).toBeVisible();
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().click({ button: "right" });
  await page.getByTestId("files-ctx-collapse-all").click();
  await expect.poll(() => page.getByTestId("files-tree-scroll").evaluate(element => element.scrollHeight)).toBe(101 * 26 + 16);
  await expect(page.locator('.den-files-tree__label--dir[data-path="folder-099"]')).toBeVisible();
  // Expansion keeps the row at the top of the viewport where it was.
  const topRow = () => page.getByTestId("files-tree-scroll").evaluate(element => {
    const top = element.getBoundingClientRect().top;
    const rows = [...element.querySelectorAll<HTMLElement>(".den-files-tree-virtual__row")]
      .map(row => ({ path: row.querySelector<HTMLElement>(".den-files-tree__label")?.dataset.path, top: row.getBoundingClientRect().top - top }))
      .filter(row => row.top > -26).sort((a, b) => a.top - b.top);
    return rows[0] && { path: rows[0].path, top: Math.round(rows[0].top) };
  });
  const anchored = await topRow();
  expect(anchored?.path).toMatch(/^folder-0\d\d$/);
  await expand(page, project.id);
  await expect.poll(topRow).toEqual(anchored);
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().press("End");
  await expect(page.locator('.den-files-tree__label--file[data-path="folder-099/file-9998.ts"]')).toBeVisible();
});


test("a distant thumb drag retains painted rows until its destination arrives", async ({ page, request }) => {
  let waiting: Promise<void> | undefined;
  let reads = 0;
  await installLargeTreeFrames(page, async () => { if (waiting) { reads++; await waiting; } });
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "tree-destination", name: "Tree destination",
    seed(root) { writeFileSync(path.join(root, "README.md"), "# Tree destination\n"); },
  });
  await expand(page, project.id);
  const first = page.locator('.den-files-tree__label--file[data-path="folder-000/file-0.ts"]');
  await expect(first).toBeVisible();
  const original = await first.elementHandle();
  let release!: () => void;
  waiting = new Promise<void>(resolve => { release = resolve; });
  try {
    await dragTreeThumb(page, [0.3, 0.85]);
    await expect.poll(() => reads).toBeGreaterThan(0);
    await expect(first).toBeVisible();
    expect(await original!.evaluate(element => element.isConnected)).toBe(true);
    await expect(page.locator('.den-files-tree__hint--loading:visible')).toHaveCount(0);
  } finally { waiting = undefined; release(); }
  await expect(first).not.toBeVisible();
  await expect(page.locator('.den-files-tree__label--file:visible').first()).toBeVisible();
  await expect(page.locator('.den-files-tree__hint--loading:visible')).toHaveCount(0);
});


test("native scrolling after expand all retains rows while destination reads wait", async ({ page, request }) => {
  let waiting: Promise<void> | undefined;
  await installLargeTreeFrames(page, async () => { await waiting; });
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "tree-native-scroll", name: "Tree native scroll",
    seed(root) { writeFileSync(path.join(root, "README.md"), "# Tree native scroll\n"); },
  });
  await expand(page, project.id);
  const first = page.locator('.den-files-tree__label--file[data-path="folder-000/file-0.ts"]');
  await expect(first).toBeVisible();
  let release!: () => void;
  waiting = new Promise<void>(resolve => { release = resolve; });
  try {
    await page.getByTestId("files-tree-scroll").evaluate(element => { element.scrollTop = 1_000_000; });
    const scrolling = await scrollTreeWindow(page, 1, 120);
    expect(scrolling.blankFrames, JSON.stringify(scrolling.gaps)).toEqual([]);
    await expect(first).toBeVisible();
  } finally { waiting = undefined; release(); }
  await expect(first).not.toBeVisible();
  await expect(page.locator('.den-files-tree__label--file:visible').first()).toBeVisible();
});

test("file reveal resumes through preparation and folder clicks remain usable", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "tree-preparation", name: "Tree preparation",
    seed(root) {
      mkdirSync(path.join(root, "src"));
      writeFileSync(path.join(root, "src", "child.ts"), "export const child = true;\n");
      writeFileSync(path.join(root, "entry.ts"), "export const entry = true;\n");
    },
  });
  const folder = page.locator('[data-testid="files-tree-dir-toggle"][data-dir="src"]');
  await folder.click();
  await page.locator('.den-files-tree__label--file[data-path="src/child.ts"]').click();
  await expect(page.getByTestId("files-editor-host").locator(".cm-content")).toContainText("child = true");
  await folder.click();
  await expect(folder).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator('.den-files-tree__label--file[data-path="src/child.ts"]')).toHaveCount(0);
  let attempts = 0;
  await page.route("**/source/views/*/presentations", async route => {
    attempts++;
    if (attempts === 1) await route.fulfill({ status: 409, contentType: "application/json",
      body: JSON.stringify({ code: "source_view_preparing", message: "The source view is preparing." }) });
    else await route.continue();
  });
  await page.getByRole("tab", { name: "child.ts", exact: true }).click({ button: "right" });
  await page.getByTestId("files-ctx-tab-reveal-tree").click();
  await expect.poll(() => attempts).toBeGreaterThanOrEqual(2);
  await expect(folder).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator('.den-files-tree__label--file[data-path="src/child.ts"]')).toBeVisible();
  await folder.click();
  await expect(folder).toHaveAttribute("aria-expanded", "false");
  await folder.click();
  await expect(folder).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator('.den-files-tree__label--file[data-path="src/child.ts"]')).toBeVisible();
  await expect(page.getByText("Preparing source view", { exact: true })).toHaveCount(0);
});

test("tabs remain usable while recursive tree preparation never finishes", async ({ page, request }) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "tree-tab-navigation", name: "Tree tab navigation",
    seed(root) {
      writeFileSync(path.join(root, "first.ts"), "export const first = true;\n");
      writeFileSync(path.join(root, "second.ts"), "export const second = true;\n");
    },
  });
  const editor = page.getByTestId("files-editor-host").locator(".cm-content:visible");
  await page.locator('.den-files-tree__label--file[data-path="first.ts"]').dblclick();
  await expect(editor).toContainText("first = true");
  let preparing = false;
  await page.route("**/source/views**", async route => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (request.method() === "POST" && pathname.endsWith("/apply")) {
      const command = request.postDataJSON().command;
      if (command.kind === "disclose") {
        for (const disclosure of command.disclosures) if (disclosure.recursive) preparing = disclosure.open;
      }
    }
    if (preparing && pathname.endsWith("/presentations") && request.method() === "POST") {
      await route.fulfill({ status: 409, json: { code: "source_view_preparing", message: "The source view is preparing." } });
      return;
    }
    if (preparing && ((request.method() === "GET" && /\/source\/views\/[^/]+$/.test(pathname)) || (request.method() === "POST" && pathname.endsWith("/apply")))) {
      const response = await route.fetch();
      const view = await response.json() as SourceTreeView;
      await route.fulfill({ response, json: { ...view, state: "preparing", extent: { rows: 0, complete: false } } });
      return;
    }
    await route.continue();
  });
  await expand(page, project.id);
  expect(preparing).toBe(true);
  await page.locator('.den-files-tree__label--file[data-path="second.ts"]').dblclick();
  await expect(editor).toContainText("second = true");
  await page.getByRole("tab", { name: "first.ts", exact: true }).click();
  await expect(editor).toContainText("first = true");
  await page.getByRole("tab", { name: "second.ts", exact: true }).click();
  await expect(editor).toContainText("second = true");
  await page.getByTestId("files-tab-close").last().click();
  await expect(editor).toContainText("first = true");
  expect(preparing).toBe(true);
  await page.locator('.den-files-tree__label--dir[data-path="."]:visible').first().click({ button: "right" });
  await page.getByTestId("files-ctx-collapse-all").click();
  await expect.poll(() => preparing).toBe(false);
  await expect(page.locator('.den-files-tree__label--file[data-path="first.ts"]')).toBeVisible();
  await expect(page.locator('.den-files-tree__hint--loading:visible')).toHaveCount(0);
});


test("restart restores expand all and its collapsed exception without replaying toggles", async ({ page, request }) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "tree-restoration", name: "Tree restoration",
    seed(root) {
      for (const name of ["src", "generated"]) {
        mkdirSync(path.join(root, name));
        writeFileSync(path.join(root, name, "entry.ts"), "export {};\n");
      }
    },
  });
  await expand(page, project.id);
  const generated = page.locator('[data-testid="files-tree-dir-toggle"][data-dir="generated"]');
  await expect(generated).toHaveAttribute("aria-expanded", "true");
  await generated.click();
  await expect(generated).toHaveAttribute("aria-expanded", "false");
  const restored = page.waitForRequest(request => request.method() === "POST" && request.url().endsWith(`/projects/${project.id}/source/views`));
  await page.reload();
  await page.getByTestId("project-files-entry").click();
  const opening = await restored;
  expect(opening.postDataJSON().intent.disclosures).toEqual(expect.arrayContaining([
    expect.objectContaining({ address: expect.objectContaining({ path: "." }), open: true, recursive: true }),
    expect.objectContaining({ address: expect.objectContaining({ path: "generated" }), open: false }),
  ]));
  await expect(page.locator('[data-testid="files-tree-dir-toggle"][data-dir="src"]')).toHaveAttribute("aria-expanded", "true");
  await expect(generated).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator('.den-files-tree__label--file[data-path="src/entry.ts"]')).toBeVisible();
  await expect(page.locator('.den-files-tree__label--file[data-path="generated/entry.ts"]')).toHaveCount(0);
});


test("expand all preserves a mixed middle viewport through jumps and reversals", async ({ page, request }, info) => {
  info.setTimeout(180_000);
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "mixed-tree-expansion", name: "Mixed tree expansion",
    seed(root) {
      const payload = path.join(root, ".payload.ts"); writeFileSync(payload, "export {};\n");
      for (let group = 0; group < 96; group++) for (let folder = 0; folder < 80; folder++) {
        const directory = path.join(root, `group-${String(group).padStart(2, "0")}`, `folder-${String(folder).padStart(2, "0")}`);
        mkdirSync(directory, { recursive: true });
        for (let file = 0; file < 3; file++) linkSync(payload, path.join(directory, `file-${file}.ts`));
      }
    },
  });
  await page.getByTestId("files-tree-scroll").evaluate(element => { element.scrollTop = 47 * 26; });
  const toggle = (path: string) => page.locator(`[data-testid="files-tree-dir-toggle"][data-dir="${path}"]`);
  await toggle("group-47").click();
  await toggle("group-47/folder-00").click();
  await toggle("group-47/folder-02").click();
  await expect(toggle("group-47/folder-02")).toHaveAttribute("aria-expanded", "true");
  await toggle("group-47/folder-02").click();
  await expect(toggle("group-47/folder-00")).toHaveAttribute("aria-expanded", "true");
  await expect(toggle("group-47/folder-02")).toHaveAttribute("aria-expanded", "false");
  await toggle("group-47").evaluate(element => {
    const viewport = document.querySelector('[data-testid="files-tree-scroll"]')!;
    viewport.scrollTop += element.getBoundingClientRect().top - viewport.getBoundingClientRect().top;
  });
  await expand(page, project.id);
  await expect(toggle("group-47/folder-02")).toHaveAttribute("aria-expanded", "true", { timeout: 30_000 });
  const samples = [];
  samples.push(await scrollTreeWindow(page, -1));
  for (const fraction of [0.71, 0.12, 0.94, 0.37]) {
    await dragTreeThumb(page, [fraction]);
    await expect(page.locator('.den-files-tree__hint--loading:visible')).toHaveCount(0);
    samples.push(await scrollTreeWindow(page, fraction > 0.5 ? -1 : 1, 120));
  }
  writeFileSync(info.outputPath("mixed-tree-scrolling.json"), JSON.stringify(samples, null, 2));
  expect(samples.flatMap(sample => sample.blankFrames)).toEqual([]);
});
