import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import { activateProject, gotoShell, modelIndependentWebE2e, openProjectFilesFixture, seedAppState } from "./helpers.ts";

const editor = (page: Page) => page.getByTestId("files-editor-host").filter({ visible: true });
async function openFile(page: Page) {
  await page.getByTestId("files-tree-file").filter({ hasText: "shared.txt" }).click();
  await expect(editor(page).locator(".cm-content")).toBeEditable({ timeout: 15_000 });
}
async function selectLines(page: Page, first: number, last = first) {
  await page.bringToFront();
  await editor(page).locator(".cm-content").click();
  await page.keyboard.press("ControlOrMeta+Home");
  for (let index = 1; index < first; index++) await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Home");
  for (let index = first; index < last; index++) await page.keyboard.press("Shift+ArrowDown");
  await page.keyboard.press("Shift+End");
}

async function setWrap(page: Page, enabled: boolean) {
  const toggle = page.getByTestId("files-editor-wrap-toggle");
  if (await toggle.getAttribute("aria-pressed") !== String(enabled)) await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", String(enabled));
}

// The local selection mixes its fill with color-mix, which serializes as color(srgb …) rather than rgba().
function colorChannels(color: string): number[] {
  const srgb = /^color\(srgb ([\d.]+) ([\d.]+) ([\d.]+)/.exec(color);
  if (srgb) return srgb.slice(1, 4).map(channel => Math.round(Number(channel) * 255));
  const rgb = /^rgba?\((\d+), (\d+), (\d+)/.exec(color);
  if (rgb) return rgb.slice(1, 4).map(Number);
  throw new Error(`Unrecognized color ${color}`);
}

async function rulerHasColor(page: Page, color: string): Promise<boolean> {
  return editor(page).getByTestId("files-overview-ruler").evaluate((canvas, hex) => {
    if (!(canvas instanceof HTMLCanvasElement)) return false;
    if (canvas.hidden) return false;
    const context = canvas.getContext("2d");
    if (!context) return false;
    const rgb = [1, 3, 5].map(offset => Number.parseInt(hex.slice(offset, offset + 2), 16));
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
    for (let index = 0; index < pixels.length; index += 4) {
      if (pixels[index + 3]! > 0 && rgb.every((channel, offset) => Math.abs(pixels[index + offset]! - channel) <= 3)) return true;
    }
    return false;
  }, color);
}

modelIndependentWebE2e("three windows share labeled selections and preserve color identities when a peer leaves", async ({ page, context, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "window-presence", name: "Window presence",
    seed: root => writeFileSync(path.join(root, "shared.txt"),
      "First window\nSecond window\n\n" + "A selected line that wraps across the editor. ".repeat(8)
      + "\nSecond window end\nThird window\n\nThird window end\n"
      + Array.from({ length: 120 }, (_, index) => `More content ${index + 1}\n`).join("")),
  });
  await openFile(page);
  const second = await context.newPage(), third = await context.newPage();
  try {
    for (const peer of [second, third]) {
      await seedAppState(peer); await gotoShell(peer); await activateProject(peer, project.id);
      await peer.getByTestId("project-files-entry").click(); await openFile(peer);
    }
    for (const window of [page, second, third]) await setWrap(window, false);
    await selectLines(page, 1); await selectLines(second, 2, 5); await selectLines(third, 6, 8);
    for (const window of [page, second, third]) await setWrap(window, true);
    await expect(page.getByTestId("document-windows")).toHaveAccessibleName("Open in 3 windows");
    await expect.poll(() => editor(page).locator(".cm-document-selection").evaluateAll(nodes =>
      new Set(nodes.map(node => (node as HTMLElement).dataset.windowClient)).size)).toBe(2);
    await expect.poll(() => editor(page).evaluate(host => {
      const blank = host.querySelectorAll(".cm-line")[2]?.getBoundingClientRect();
      if (!blank) return false;
      const center = (blank.top + blank.bottom) / 2;
      return [...host.querySelectorAll(".cm-document-selection")].some(node => {
        const rect = node.getBoundingClientRect();
        return rect.top <= center && rect.bottom >= center && rect.width > blank.width * 0.8;
      });
    })).toBe(true);
    await expect.poll(() => editor(page).locator(".cm-document-selection-outline").count()).toBe(2);
    expect(await editor(page).locator(".cm-document-selection-outline").evaluateAll(nodes => nodes.every(node => {
      const path = node.querySelector("path");
      if (!path) return false;
      const bounds = path.getBBox(), box = node.getBoundingClientRect();
      return Math.abs(bounds.x) < 0.01 && Math.abs(bounds.y) < 0.01
        && Math.abs(bounds.width - box.width) < 0.1 && Math.abs(bounds.height - box.height) < 0.1;
    }))).toBe(true);
    expect(await editor(page).locator(".cm-document-selection-outline svg").first()
      .evaluate(el => Number(getComputedStyle(el).strokeOpacity))).toBe(0.45);
    const secondColor = await second.evaluate(() => document.documentElement.style.getPropertyValue("--den-current-window-caret"));
    const currentColor = await page.evaluate(() => document.documentElement.style.getPropertyValue("--den-current-window-caret"));
    await expect.poll(() => rulerHasColor(page, currentColor)).toBe(true);
    await expect.poll(() => rulerHasColor(page, secondColor)).toBe(true);
    const thirdColor = await third.evaluate(() => document.documentElement.style.getPropertyValue("--den-current-window-caret"));
    expect(thirdColor).toMatch(/^#[0-9a-f]{6}$/);
    await expect.poll(() => rulerHasColor(page, thirdColor)).toBe(true);
    const peerCaret = editor(page).locator(".cm-document-caret").last();
    await expect.poll(() => peerCaret.evaluate(el => (el as HTMLElement).style.getPropertyValue("--den-window-caret"))).toBe(thirdColor);
    const caretColor = await peerCaret.evaluate(el => getComputedStyle(el).borderInlineStartColor);
    await expect.poll(() => third.locator(".den-files-tab--active:visible .den-files-tab__body").first()
      .evaluate(el => getComputedStyle(el).backgroundImage)).toContain(caretColor);
    const tabFill = await third.locator(".den-files-tab--active:visible .den-files-tab__body").first().evaluate(el => getComputedStyle(el).backgroundColor);
    const thirdClient = await peerCaret.getAttribute("data-window-client");
    const thirdRange = editor(page).locator(`.cm-document-selection[data-window-client="${thirdClient}"]`).first();
    await expect.poll(() => thirdRange.evaluate(el => getComputedStyle(el).backgroundColor)).toBe(tabFill);
    const localFill = await editor(third).locator(".cm-selectionBackground").first().evaluate(el => getComputedStyle(el).backgroundColor);
    expect(colorChannels(localFill)).toEqual(colorChannels(tabFill));
    const remoteFill = await editor(third).locator(".cm-document-selection").first().evaluate(el => getComputedStyle(el).backgroundColor);
    expect(localFill).not.toBe(remoteFill);
    const label = await peerCaret.getAttribute("aria-label");
    expect(label).toMatch(/Browser window \d+/);
    await page.getByTestId("document-windows").click();
    await expect(page.getByRole("menu", { name: "Windows with this file open" })).toBeVisible();
    await expect(page.getByRole("menu").filter({ hasText: "This window" })).toBeVisible();
    await page.screenshot({ path: process.env.PW_WINDOW_PRESENCE_SCREENSHOT ?? testInfo.outputPath("three-editor-windows.png") });
    await third.screenshot({ path: process.env.PW_WINDOW_PRESENCE_SCREENSHOT?.replace(/\.png$/, "-peer.png")
      ?? testInfo.outputPath("peer-editor-window.png") });
    await page.keyboard.press("Escape");
    await second.close();
    await expect(page.getByTestId("document-windows")).toHaveAccessibleName("Open in 2 windows", { timeout: 75_000 });
    await expect(editor(page).locator(".cm-document-caret")).toHaveCount(1);
    await expect.poll(() => rulerHasColor(page, secondColor)).toBe(false);
    await expect.poll(() => rulerHasColor(page, thirdColor)).toBe(true);
    await expect(editor(page).locator(".cm-document-caret")).toHaveAttribute("aria-label", label ?? "");
    expect(await third.evaluate(() => document.documentElement.style.getPropertyValue("--den-current-window-caret"))).toBe(thirdColor);
    await third.close();
    await expect(page.getByTestId("document-windows")).toHaveCount(0, { timeout: 75_000 });
    await expect.poll(() => rulerHasColor(page, thirdColor)).toBe(false);
  } finally {
    if (!second.isClosed()) await second.close();
    if (!third.isClosed()) await third.close();
  }
});
