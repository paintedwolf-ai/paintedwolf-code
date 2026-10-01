import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { openProjectFilesFixture, modelIndependentWebE2e } from "./helpers.ts";
import { largeMarkdownPreviewFixture, MARKDOWN_PREVIEW_TITLE } from "./markdown-preview-fixture.ts";

modelIndependentWebE2e("large Markdown preview keeps the app responsive and bounds mounted content", async ({ page, request }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.addInitScript(() => {
    const NativeWorker = window.Worker;
    window.Worker = class extends NativeWorker {
      override postMessage(message: unknown, options?: StructuredSerializeOptions | Transferable[]) {
        const send = () => Array.isArray(options)
          ? super.postMessage(message, options)
          : super.postMessage(message, options);
        if (typeof message === "object" && message !== null && "type" in message && message.type === "prepare") {
          setTimeout(send, 1000);
        } else send();
      }
    };
  });
  await openProjectFilesFixture(page, request, {
    prefix: "markdown-preview",
    name: "Large Markdown preview",
    seed: (root) => {
      writeFileSync(path.join(root, "notices.md"), largeMarkdownPreviewFixture());
      writeFileSync(path.join(root, "small.md"), "# Small preview\n\n```text\n" + "Wide code line ".repeat(40) + "\n```\n");
    },
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "notices.md" }).click();
  await expect(page.getByTestId("files-editor-host").locator(".cm-content")).toContainText(MARKDOWN_PREVIEW_TITLE);
  const sampling = page.evaluate(async () => {
    const delays: number[] = [];
    let previous = performance.now();
    const timer = setInterval(() => {
      const now = performance.now();
      delays.push(now - previous);
      previous = now;
    }, 16);
    document.querySelector<HTMLButtonElement>('[data-testid="files-editor-md-preview"]')!.click();
    const deadline = performance.now() + 15_000;
    while (!document.querySelector(".den-markdown-preview-block .markdown-body") && performance.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 16));
    }
    await new Promise((resolve) => setTimeout(resolve, 200));
    clearInterval(timer);
    return { delays, mounted: document.querySelectorAll(".den-markdown-preview-block").length };
  });
  const waiting = page.getByRole("status", { name: "", exact: true }).filter({ hasText: "Preparing preview…" });
  await expect(waiting).toBeVisible();
  await expect(waiting).toHaveClass("den-presentation-wait");
  const noteStyle = await waiting.evaluate((note) => {
    const probe = document.createElement("span");
    probe.style.color = "var(--den-text)";
    note.append(probe);
    const normalColor = getComputedStyle(probe).color;
    probe.remove();
    let undimmed = true;
    for (let element: Element | null = note; element; element = element.parentElement) {
      const style = getComputedStyle(element);
      if (Number(style.opacity) < 1 || style.filter !== "none") undimmed = false;
    }
    return { normalColor, color: getComputedStyle(note).color, undimmed };
  });
  expect(noteStyle.color).toBe(noteStyle.normalColor);
  expect(noteStyle.undimmed).toBe(true);
  const waitingBox = await waiting.boundingBox();
  const previewBox = await page.locator(".den-files-editor__document-stage").boundingBox();
  expect(Math.abs(waitingBox!.x + waitingBox!.width / 2 - previewBox!.x - previewBox!.width / 2)).toBeLessThan(2);
  expect(Math.abs(waitingBox!.y + waitingBox!.height / 2 - previewBox!.y - previewBox!.height / 2)).toBeLessThan(2);
  const retainedEditor = page.locator(".den-files-editor__host-wrap");
  await expect(retainedEditor).toBeVisible();
  await expect(retainedEditor).toHaveAttribute("data-retained", "true");
  await expect(retainedEditor).toHaveAttribute("inert", "");
  await page.screenshot({ path: path.resolve("../.task/markdown-preview-waiting-qa.png") });
  const samples = await sampling;
  expect(samples.mounted).toBeGreaterThan(0);
  expect(samples.mounted).toBeLessThan(20);
  expect(Math.max(...samples.delays), JSON.stringify(samples)).toBeLessThan(250);
  const preview = page.getByTestId("files-editor-preview");
  await expect(preview).toContainText(MARKDOWN_PREVIEW_TITLE);
  const largeWidth = await preview.locator(".den-files-editor__preview-body").evaluate((element) => element.getBoundingClientRect().width);
  const code = preview.locator(".markdown-code-scroll").first();
  await expect(code).toBeVisible();
  const codeWidth = await code.evaluate((element) => element.getBoundingClientRect().width);
  expect(codeWidth).toBeGreaterThan(largeWidth - 40);
  await page.screenshot({ path: path.resolve("../.task/markdown-preview-qa.png") });
  await preview.locator(".den-scrollport__viewport").first().evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await expect(preview.locator(".den-markdown-preview-block .markdown-body").last()).toContainText("End of preview fixture.");
  expect(await preview.locator(".den-markdown-preview-block").count()).toBeLessThan(20);
  await page.getByTestId("files-editor-md-code").click();
  await expect(preview).toHaveCount(0);
  await expect(page.getByTestId("files-editor-host").locator(".cm-content")).toBeVisible();
  await page.getByTestId("files-tree-file").filter({ hasText: "small.md" }).click();
  await expect(page.getByRole("textbox", { name: "", exact: true })).toContainText("Small preview");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(preview).toContainText("Small preview");
  const smallWidth = await preview.locator(".den-files-editor__preview-body").evaluate((element) => element.getBoundingClientRect().width);
  expect(Math.abs(smallWidth - largeWidth)).toBeLessThan(2);
  const reachable = await preview.locator(".markdown-code-scroll > .den-scrollport__viewport").evaluate((element) => {
    element.scrollLeft = element.scrollWidth;
    return element.scrollLeft > 0;
  });
  expect(reachable).toBe(true);
});
