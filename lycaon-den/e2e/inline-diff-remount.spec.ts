import { expect } from "@playwright/test";
import { modelIndependentWebE2e } from "./helpers.ts";

modelIndependentWebE2e("an inline diff remount keeps the first following image in place while loading", async ({ page }) => {
  await page.goto("/e2e/fixtures/inline-diff-remount.html");
  await expect(page.getByTestId("ready")).toHaveText("true");
  const image = page.getByTestId("image-0");
  await page.evaluate(async () => {
    await document.fonts.ready;
    for (let i = 0; i < 20; i++) await new Promise(resolve => requestAnimationFrame(resolve));
  });
  await image.evaluate(element => {
    const scroll = element.parentElement!;
    scroll.scrollTop += element.getBoundingClientRect().top - scroll.getBoundingClientRect().top - 80;
  });
  const sample = () => image.evaluate(element => element.getBoundingClientRect().top);
  // Let CodeMirror finish measuring the wrapped comparison before recycling it.
  await expect.poll(async () => {
    const before = await sample();
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    return Math.abs(await sample() - before);
  }).toBeLessThan(1);
  const before = await sample();
  const geometry = () => image.evaluate(element => ({ top: element.getBoundingClientRect().top,
    scroll: element.parentElement!.scrollTop, height: element.parentElement!.scrollHeight,
    reader: element.parentElement!.querySelector(".den-file-edit-diff")?.getBoundingClientRect().height,
    cm: element.parentElement!.querySelector(".cm-content")?.getBoundingClientRect().height }));
  const original = await geometry();
  for (let cycle = 0; cycle < 3; cycle++) {
    await page.getByRole("button", { name: "Unmount comparison", exact: true }).click();
    expect(Math.abs(await sample() - before), JSON.stringify({ phase: "unmounted", original, after: await geometry() })).toBeLessThan(2);
    await page.getByRole("button", { name: "Remount comparison", exact: true }).click();
    await expect(page.getByTestId("ready")).toHaveText("false");
    const drift = await image.evaluate(async element => {
      const positions: number[] = [];
      for (let i = 0; i < 12; i++) {
        await new Promise(resolve => requestAnimationFrame(resolve));
        positions.push(element.getBoundingClientRect().top);
      }
      return positions;
    });
    expect(Math.max(...drift.map(top => Math.abs(top - before))), JSON.stringify({ original, after: await geometry(), drift })).toBeLessThan(2);
    await expect(image).toBeVisible();
    await page.getByRole("button", { name: "Release comparison", exact: true }).click();
    await expect(page.getByTestId("ready")).toHaveText("true");
    await expect.poll(async () => Math.abs(await sample() - before)).toBeLessThan(2);
  }
});
