import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("file navigation retains the editor and its observed scroll position", async ({ page, request }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const names = ["first.txt", "second.txt", "third.txt", "fourth.txt"];
  await openProjectFilesFixture(page, request, {
    prefix: "editor-scroll-retention", name: "Editor scroll retention",
    seed: (root) => {
      for (const name of names) writeFileSync(path.join(root, name),
        Array.from({ length: 5_000 }, (_, index) => `${name} line ${index + 1}`).join("\n"));
    },
  });
  const openFile = async (name: string) => {
    await page.getByTestId("files-tree-file").filter({ hasText: name }).dblclick();
    await expect(page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content")).toBeVisible();
    await expect(page.getByTestId("files-editor-host").filter({ visible: true }).locator(".cm-content")).toContainText(name);
  };
  await openFile(names[0]!);
  const host = page.getByTestId("files-editor-host").filter({ visible: true });
  const scroller = host.locator(".cm-scroller");
  await host.locator(".cm-editor").evaluate((element) => { element.setAttribute("data-retention-probe", "original"); });
  await scroller.hover();
  await page.mouse.wheel(0, 20_000);
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(10_000);
  const offset = await scroller.evaluate((element) => element.scrollTop);
  for (const name of names.slice(1)) await openFile(name);
  await openFile(names[0]!);
  await expect(host.locator(".cm-editor")).toHaveAttribute("data-retention-probe", "original");
  await expect.poll(() => scroller.evaluate((element, previous) => Math.abs(element.scrollTop - previous), offset)).toBeLessThanOrEqual(1);
  await expect.poll(() => scroller.evaluate((element) => {
    const viewport = element.getBoundingClientRect();
    const lines = [...element.querySelectorAll(".cm-line")].map((line) => line.getBoundingClientRect());
    return [viewport.top + 30, viewport.bottom - 30].every((y) => lines.some((line) => line.top <= y && line.bottom >= y));
  })).toBe(true);
});
