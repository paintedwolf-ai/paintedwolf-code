import { expect, type Locator, type Page } from "@playwright/test";

/** Resolves after two animation frames: the last change has been laid out and painted. */
export async function settleFrames(page: Page): Promise<void> {
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
  );
}

/** Resolves when every finite animation on the element and its descendants has ended. */
export async function animationsFinished(scope: Locator): Promise<void> {
  await scope.evaluate(async (element) => {
    // A transition starts in the frame after the style change that triggers it.
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    const finite = element
      .getAnimations({ subtree: true })
      .filter((animation) => Number.isFinite(animation.effect?.getComputedTiming().endTime));
    // A canceled animation rejects, and has ended all the same.
    await Promise.allSettled(finite.map((animation) => animation.finished));
  });
}

/** Resolves when the scroller's offset and range hold across four frames. */
export async function scrollSettled(scroller: Locator): Promise<void> {
  await scroller.evaluate(
    (element) =>
      new Promise<void>((resolve) => {
        let previous = "";
        let stable = 0;
        const tick = () => {
          const next = `${element.scrollTop}:${element.scrollLeft}:${element.scrollHeight}:${element.scrollWidth}`;
          stable = next === previous ? stable + 1 : 0;
          previous = next;
          if (stable >= 4) resolve();
          else requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      }),
  );
}

/** Resolves when the element's painted box holds across four frames. */
export async function boxSettled(target: Locator): Promise<void> {
  await target.evaluate(
    (element) =>
      new Promise<void>((resolve) => {
        let previous = "";
        let stable = 0;
        const tick = () => {
          const box = element.getBoundingClientRect();
          const next = `${box.left}:${box.top}:${box.width}:${box.height}`;
          stable = next === previous ? stable + 1 : 0;
          previous = next;
          if (stable >= 4) resolve();
          else requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      }),
  );
}

/** Resolves when the live transcript's rows and scroll geometry hold across four frames with no animation running. */
export async function settleTranscriptLayout(page: Page): Promise<void> {
  await page.evaluate(async () => {
    await document.fonts.ready;
    await new Promise<void>((resolve) => {
      let previous = "";
      let stableFrames = 0;
      const sample = () => {
        const viewport = document.querySelector<HTMLElement>(
          ".den-resident-surface:not([data-resident=idle]) .den-shell-stage--chat .den-chat-stream",
        );
        if (!viewport) return "";
        const rows = [...viewport.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-index]")];
        return [
          viewport.scrollHeight,
          viewport.clientHeight,
          viewport.scrollTop,
          ...rows.flatMap((row) => {
            const rect = row.getBoundingClientRect();
            return [row.dataset.msgId ?? row.dataset.timeRow ?? "", rect.top, rect.height];
          }),
        ].join(":");
      };
      const tick = () => {
        const next = sample();
        const animating = document.getAnimations().some((animation) =>
          (animation.playState === "running" || animation.pending) &&
          Number.isFinite(animation.effect?.getComputedTiming().endTime),
        );
        stableFrames = !animating && next && next === previous ? stableFrames + 1 : 0;
        previous = next;
        if (stableFrames >= 4) resolve();
        else requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    });
  });
}

/** Resolves when the scrollport around the element no longer counts the reader's last wheel, key or drag as active. */
export async function directInputSettled(scroller: Locator): Promise<void> {
  await expect
    .poll(() =>
      scroller.evaluate(async (element) => {
        const url = "/src/platform/scrolling/scrollport-motion.ts";
        const { scrollportMotionContaining } = (await import(/* @vite-ignore */ url)) as
          typeof import("../../src/platform/scrolling/scrollport-motion.ts");
        return scrollportMotionContaining(element)?.input.isDirectInputActive();
      }),
    )
    .toBe(false);
}
