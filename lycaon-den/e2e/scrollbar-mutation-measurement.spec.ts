import { expect } from "@playwright/test";
import { modelIndependentWebE2e } from "./helpers.ts";

modelIndependentWebE2e("scrollbar mutation bursts defer geometry and publish the final extent", async ({ page }) => {
  await page.goto("/");
  const result = await page.evaluate(async () => {
    const moduleUrl = "/node_modules/overlayscrollbars/overlayscrollbars.esm.js";
    const { OverlayScrollbars } = await import(/* @vite-ignore */ moduleUrl);
    const host = document.createElement("div");
    host.style.cssText = "position:fixed;left:0;top:0;width:200px;height:100px;overflow:auto";
    host.setAttribute("data-overlayscrollbars-direction", "ltr");
    const content = document.createElement("div");
    content.style.height = "300px";
    host.append(content);
    document.body.append(host);
    const instance = OverlayScrollbars({ target: host, elements: { viewport: host } }, {
      update: { debounce: { mutation: [80, 160], resize: 0, env: [80, 160] } },
    });
    const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
    await wait(200);
    const height = Object.getOwnPropertyDescriptor(Element.prototype, "scrollHeight")!;
    let reads = 0;
    Object.defineProperty(host, "scrollHeight", {
      configurable: true,
      get() { reads++; return height.get!.call(host); },
    });
    try {
      for (let i = 0; i < 12; i++) {
        content.style.height = `${400 + i * 100}px`;
        await Promise.resolve();
        await Promise.resolve();
      }
      const duringMutations = reads;
      await wait(250);
      const afterBatch = reads;
      const extent = instance.state().overflowAmount.y;
      content.style.height = "1700px";
      instance.update();
      const explicitExtent = instance.state().overflowAmount.y;
      const beforeResize = reads;
      for (let i = 0; i < 12; i++) {
        host.style.width = `${240 + i}px`;
        window.dispatchEvent(new Event("resize"));
        await Promise.resolve();
      }
      const duringResize = reads - beforeResize;
      await wait(250);
      const resizedWidth = instance.state().overflowEdge.x;
      instance.sleep(true);
      const beforeSleepingMutation = reads;
      content.style.height = "1900px";
      window.dispatchEvent(new Event("resize"));
      await wait(250);
      const whileSleeping = reads - beforeSleepingMutation;
      instance.sleep(false);
      const afterWake = instance.state().overflowAmount.y;
      content.style.height = "2100px";
      await wait(250);
      const afterWakeMutation = instance.state().overflowAmount.y;
      content.style.height = "2300px";
      await Promise.resolve();
      instance.destroy();
      const atDestroy = reads;
      await wait(250);
      return { duringMutations, afterBatch, extent, explicitExtent, duringResize, resizedWidth, whileSleeping, afterWake, afterWakeMutation, afterDestroy: reads - atDestroy };
    } finally {
      instance.destroy();
      host.remove();
    }
  });
  expect(result.duringMutations).toBe(0);
  expect(result.afterBatch).toBeGreaterThan(0);
  expect(result.extent).toBe(1400);
  expect(result.explicitExtent).toBe(1600);
  expect(result.duringResize, JSON.stringify(result)).toBe(0);
  expect(result.resizedWidth).toBe(251);
  expect(result.whileSleeping, JSON.stringify(result)).toBe(0);
  expect(result.afterWake).toBe(1800);
  expect(result.afterWakeMutation).toBe(2000);
  expect(result.afterDestroy).toBe(0);
});
