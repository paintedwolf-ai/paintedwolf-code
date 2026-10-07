import { copyFileSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Locator, type Page } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";
import { largeMarkdownPreviewFixture, MARKDOWN_PREVIEW_TITLE } from "./markdown-preview-fixture.ts";

async function sampleNativeWheel(page: Page, preview: Locator, deltaY: number) {
  // The sampler records its starting offset before the wheel is dispatched;
  // the result promise stays wrapped so the handle resolves without awaiting it.
  const sampler = await preview.evaluateHandle((frame) => ({ result: new Promise<{ offset: number; painted: boolean; thumb: number }>((resolve, reject) => {
    const viewport = frame.querySelector<HTMLElement>(":scope > .den-scrollport__viewport");
    if (!viewport) {
      reject(new Error("The preview frame has no viewport"));
      return;
    }
    const before = viewport.scrollTop;
    const deadline = performance.now() + 15_000;
    const measure = () => {
      if (viewport.scrollTop === before) {
        if (performance.now() >= deadline) reject(new Error("Native wheel did not move the preview"));
        else requestAnimationFrame(measure);
        return;
      }
      const box = viewport.getBoundingClientRect();
      const bodies = Array.from(viewport.querySelectorAll(".den-markdown-preview-block .markdown-body"));
      const handle = frame.querySelector(":scope > .os-scrollbar-vertical .os-scrollbar-handle");
      resolve({
        offset: viewport.scrollTop,
        painted: bodies.some((body) => {
          const rect = body.getBoundingClientRect();
          return rect.top < box.bottom - 30 && rect.bottom > box.top + 30;
        }),
        thumb: handle?.getBoundingClientRect().top ?? 0,
      });
    };
    requestAnimationFrame(measure);
  }) }));
  await preview.hover();
  await page.mouse.wheel(0, deltaY);
  try {
    return await sampler.evaluate((pending) => pending.result);
  } finally {
    await sampler.dispose();
  }
}

modelIndependentWebE2e("large Markdown preview retains text and scroll direction while sections arrive", async ({ page, request }) => {
  let openingLine = `# ${MARKDOWN_PREVIEW_TITLE}`;
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.addInitScript(() => {
    Object.defineProperty(window, "ScrollTimeline", { value: undefined, configurable: true });
    const NativeWorker = window.Worker;
    window.Worker = class extends NativeWorker {
      override postMessage(message: unknown, options?: StructuredSerializeOptions | Transferable[]) {
        const send = () => Array.isArray(options)
          ? super.postMessage(message, options)
          : super.postMessage(message, options);
        if (typeof message === "object" && message !== null && "type" in message && message.type === "block") setTimeout(send, 80);
        else send();
      }
    };
  });
  await openProjectFilesFixture(page, request, {
    prefix: "preview-scroll",
    name: "Markdown preview scroll",
    seed: (root) => {
      const fixture = process.env.LYCAON_E2E_MARKDOWN_FIXTURE;
      const destination = path.join(root, "notices.md");
      if (fixture) {
        copyFileSync(fixture, destination);
        openingLine = readFileSync(fixture, "utf8").split("\n", 1)[0]?.slice(0, 80) ?? "";
      }
      else writeFileSync(destination, largeMarkdownPreviewFixture());
    },
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "notices.md" }).click();
  await expect(page.getByTestId("files-editor-host").locator(".cm-content")).toContainText(openingLine);
  await page.getByTestId("files-editor-md-preview").click();
  const preview = page.getByTestId("files-editor-preview");
  const opening = preview.locator(".den-markdown-preview-block .markdown-body").first();
  await expect(opening).toBeVisible();
  await expect(preview).not.toHaveAttribute("aria-hidden", "true");
  const openingText = (await opening.textContent() ?? "").slice(0, 80);
  expect(openingText.length).toBeGreaterThan(0);

  await preview.hover();
  const samples: Awaited<ReturnType<typeof sampleNativeWheel>>[] = [];
  for (let step = 0; step < 6; step++) {
    samples.push(await sampleNativeWheel(page, preview, 12_000));
  }
  expect(samples.every((sample) => sample.painted), JSON.stringify(samples)).toBe(true);
  expect(samples[0]!.offset).toBeGreaterThan(10_000);
  expect(samples.slice(1).every((sample, index) => sample.offset > samples[index]!.offset), JSON.stringify(samples)).toBe(true);
  expect(new Set(samples.map((sample) => Math.round(sample.thumb))).size, JSON.stringify(samples)).toBeGreaterThan(2);
  await expect(preview.locator('.den-markdown-preview-window [data-retained="true"]')).toHaveCount(0);
  expect(await preview.locator(".den-markdown-preview-block").count()).toBeLessThan(30);

  const returned = await sampleNativeWheel(page, preview, -await preview.locator(".den-scrollport__viewport").first().evaluate((element) => element.scrollTop));
  expect(returned.offset).toBe(0);
  expect(returned.painted).toBe(true);
  await expect(preview).toContainText(openingText);
  await expect(preview.locator('.den-markdown-preview-window [data-retained="true"]')).toHaveCount(0);
});
