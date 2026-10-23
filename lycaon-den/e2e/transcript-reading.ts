import { expect, test, type Page } from "@playwright/test";
import { liveChatStage } from "./helpers.ts";

const scroller = (page: Page) => liveChatStage(page).locator(".den-chat-stream");

export async function visibleTranscriptSteps(page: Page): Promise<number[]> {
  return scroller(page).evaluate((host) => {
    const view = host.getBoundingClientRect();
    return [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")]
      .map((row) => ({ box: row.getBoundingClientRect(), text: row.textContent ?? "" }))
      .filter(({ box }) => box.bottom > view.top && box.top < view.bottom)
      .sort((a, b) => a.box.top - b.box.top)
      .map(({ text }) => Number(text.match(/Step (\d+)\./)?.[1] ?? Number.NaN))
      .filter(Number.isFinite);
  });
}

export function expectContiguousSteps(steps: readonly number[]) {
  expect(steps.length, "the transcript viewport contains readable rows").toBeGreaterThan(0);
  for (let i = 1; i < steps.length; i++) {
    expect(steps[i], `visible rows ${steps.join(",")}`).toBe((steps[i - 1] ?? 0) + 1);
  }
}

export async function wheelTranscript(page: Page, deltaY: number) {
  // The runner deadline also catches a blocked renderer event loop.
  await test.step(`Wheel transcript by ${deltaY}px`, async () => {
    const host = scroller(page);
    const box = await host.boundingBox();
    if (!box) throw new Error("chat scroller is missing");
    const observation = await host.evaluateHandle((element) => {
      let frame = 0;
      let timer: ReturnType<typeof setTimeout>;
      let previous = "";
      let stable = 0;
      const cleanup = () => {
        clearTimeout(timer);
        cancelAnimationFrame(frame);
        element.removeEventListener("wheel", start, true);
      };
      let start = () => {};
      const settled = new Promise<void>((resolve, reject) => {
        timer = setTimeout(() => {
          cleanup();
          reject(new Error("Transcript did not settle after its wheel gesture"));
        }, 5_000);
        start = () => {
          const sample = () => {
            const rows = element.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]");
            const first = rows.item(0);
            const last = rows.item(rows.length - 1);
            const signature = [element.scrollTop, element.scrollHeight,
              first?.dataset.msgId, first?.getBoundingClientRect().top,
              last?.dataset.msgId, last?.getBoundingClientRect().bottom].join(":");
            stable = signature === previous ? stable + 1 : 0;
            previous = signature;
            if (stable >= 3) {
              cleanup();
              resolve();
            } else {
              frame = requestAnimationFrame(sample);
            }
          };
          frame = requestAnimationFrame(sample);
        };
        element.addEventListener("wheel", start, { capture: true, once: true });
      });
      return { settled, cleanup };
    });
    try {
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.mouse.wheel(0, deltaY);
      await observation.evaluate(async (state) => state.settled);
    } finally {
      await observation.evaluate((state) => state.cleanup());
      await observation.dispose();
    }
  }, { timeout: 7_000 });
}

export async function readTranscriptBackTo(page: Page, step: number) {
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    await wheelTranscript(page, -3_000);
    const steps = await visibleTranscriptSteps(page);
    expectContiguousSteps(steps);
    if (Math.min(...steps) <= step) return;
  }
  throw new Error(`Transcript did not reach history row ${step}`);
}

export async function readTranscriptForward(page: Page, until: (steps: readonly number[]) => boolean | Promise<boolean>) {
  const deadline = Date.now() + 180_000;
  let previous = await visibleTranscriptSteps(page);
  expectContiguousSteps(previous);
  while (Date.now() < deadline) {
    await wheelTranscript(page, 500);
    const steps = await visibleTranscriptSteps(page);
    expectContiguousSteps(steps);
    // Every newly presented screen must overlap the reader's previous position.
    expect(Math.min(...steps), "reading forward cannot skip unseen rows").toBeLessThanOrEqual(Math.max(...previous) + 1);
    previous = steps;
    if (await until(steps)) return;
  }
  throw new Error("Transcript did not reach the requested reading position");
}
