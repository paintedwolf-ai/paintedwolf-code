import { expect, type Page } from "@playwright/test";
import { modelIndependentWebE2e } from "./helpers.ts";

type Probe = {
  host: HTMLElement;
  content: HTMLElement;
  instance: {
    state(): { overflowAmount: { y: number }; overflowEdge: { x: number } };
    update(): void;
    sleep(on: boolean): void;
    destroy(): void;
  };
  counting: boolean;
  reads: number;
};

/**
 * Lets the layout observers deliver at a real rendering step, then runs the scrollbar's
 * debounce on the page clock.
 */
async function settle(page: Page, ms: number) {
  await page.evaluate(() => new Promise<void>((resolve) => {
    const marker = document.createElement("div");
    marker.style.cssText = "position:fixed;width:1px;height:1px";
    document.body.append(marker);
    const observer = new ResizeObserver(() => { observer.disconnect(); marker.remove(); resolve(); });
    observer.observe(marker);
  }));
  await page.clock.runFor(ms);
}

modelIndependentWebE2e("scrollbar mutation bursts defer geometry and publish the final extent", async ({ page }) => {
  await page.clock.install();
  await page.goto("/");
  await page.evaluate(async () => {
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
    const state: Probe = { host, content, instance, counting: false, reads: 0 };
    const height = Object.getOwnPropertyDescriptor(Element.prototype, "scrollHeight")!;
    Object.defineProperty(host, "scrollHeight", {
      configurable: true,
      get() { if (state.counting) state.reads++; return height.get!.call(host); },
    });
    Object.assign(window, { __scrollbarProbe: state });
  });
  try {
    await settle(page, 200);
    const duringMutations = await page.evaluate(async () => {
      const state = (window as unknown as { __scrollbarProbe: Probe }).__scrollbarProbe;
      const { content } = state;
      state.counting = true;
      for (let i = 0; i < 12; i++) {
        content.style.height = `${400 + i * 100}px`;
        await Promise.resolve();
        await Promise.resolve();
      }
      return state.reads;
    });
    await settle(page, 250);
    const batch = await page.evaluate(async () => {
      const state = (window as unknown as { __scrollbarProbe: Probe }).__scrollbarProbe;
      const { host, content, instance } = state;
      const afterBatch = state.reads;
      const extent = instance.state().overflowAmount.y;
      content.style.height = "1700px";
      instance.update();
      const explicitExtent = instance.state().overflowAmount.y;
      const beforeResize = state.reads;
      for (let i = 0; i < 12; i++) {
        host.style.width = `${240 + i}px`;
        window.dispatchEvent(new Event("resize"));
        await Promise.resolve();
      }
      return { afterBatch, extent, explicitExtent, duringResize: state.reads - beforeResize };
    });
    await settle(page, 250);
    const beforeSleepingMutation = await page.evaluate(() => {
      const state = (window as unknown as { __scrollbarProbe: Probe }).__scrollbarProbe;
      const { content, instance } = state;
      const resizedWidth = instance.state().overflowEdge.x;
      instance.sleep(true);
      const before = state.reads;
      content.style.height = "1900px";
      window.dispatchEvent(new Event("resize"));
      return { resizedWidth, before };
    });
    await settle(page, 250);
    const wake = await page.evaluate((before) => {
      const state = (window as unknown as { __scrollbarProbe: Probe }).__scrollbarProbe;
      const { content, instance } = state;
      const whileSleeping = state.reads - before;
      instance.sleep(false);
      const afterWake = instance.state().overflowAmount.y;
      content.style.height = "2100px";
      return { whileSleeping, afterWake };
    }, beforeSleepingMutation.before);
    await settle(page, 250);
    const atDestroy = await page.evaluate(async () => {
      const state = (window as unknown as { __scrollbarProbe: Probe }).__scrollbarProbe;
      const { content, instance } = state;
      const afterWakeMutation = instance.state().overflowAmount.y;
      content.style.height = "2300px";
      await Promise.resolve();
      instance.destroy();
      return { afterWakeMutation, reads: state.reads };
    });
    await settle(page, 250);
    const afterDestroy = await page.evaluate((reads) => (window as unknown as { __scrollbarProbe: Probe }).__scrollbarProbe.reads - reads, atDestroy.reads);
    const result = { duringMutations, ...batch, resizedWidth: beforeSleepingMutation.resizedWidth, ...wake,
      afterWakeMutation: atDestroy.afterWakeMutation, afterDestroy };
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
  } finally {
    await page.evaluate(() => {
      const { host, instance } = (window as unknown as { __scrollbarProbe: Probe }).__scrollbarProbe;
      instance.destroy();
      host.remove();
    });
  }
});
