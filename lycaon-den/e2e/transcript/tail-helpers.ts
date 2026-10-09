import { expect, type Page } from "@playwright/test";
import { LIVE_CHAT_STAGE_SELECTOR } from "../../shared/stage-selectors.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "../../src/platform/scrolling/scrollport-motion-types.ts";

type TailBoundaryTrace = {
  active: boolean;
  overflow: number[];
  samples: Array<{
    phase: string;
    overflow: number;
    scrollTop: number;
    scrollHeight: number;
    clientHeight: number;
    viewportWidth: number;
    transcriptEnd: number;
    afterRunway: number;
  }>;
};

export async function startTailBoundaryTrace(page: Page) {
  await page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => {
      const body = host.querySelector<HTMLElement>(".den-chat-stream-body");
      if (!body) throw new Error("transcript body is missing");
      const trace: TailBoundaryTrace = {
        active: true,
        overflow: [],
        samples: [],
      };
      (
        window as Window & { __denTailBoundaryTrace?: TailBoundaryTrace }
      ).__denTailBoundaryTrace = trace;
      const sample = () => {
        if (!trace.active) return;
        const transcriptEnd = host.querySelector<HTMLElement>(
          "[data-transcript-end]",
        );
        if (transcriptEnd) {
          const clearance =
            Number.parseFloat(getComputedStyle(body).paddingBottom) || 0;
          const overflow =
            host.getBoundingClientRect().bottom -
              transcriptEnd.getBoundingClientRect().bottom -
              clearance;
          trace.overflow.push(overflow);
          trace.samples.push({
            phase:
              (window as Window & { __denTailBoundaryPhase?: string })
                .__denTailBoundaryPhase ?? "",
            overflow,
            scrollTop: host.scrollTop,
            scrollHeight: host.scrollHeight,
            clientHeight: host.clientHeight,
            viewportWidth: host.clientWidth,
            transcriptEnd: transcriptEnd.getBoundingClientRect().bottom,
            afterRunway:
              Number.parseFloat(
                host.querySelector<HTMLElement>(
                  '[data-transcript-runway="after"]',
                )?.style.height ?? "",
              ) || 0,
          });
        }
        // ResizeObserver delivery follows rAF; sample after the render callbacks.
        requestAnimationFrame(() => setTimeout(sample, 0));
      };
      requestAnimationFrame(() => setTimeout(sample, 0));
    });
}

export async function stopTailBoundaryTrace(page: Page): Promise<TailBoundaryTrace> {
  return page.evaluate(() => {
    const target = window as Window & {
      __denTailBoundaryTrace?: TailBoundaryTrace;
    };
    const trace = target.__denTailBoundaryTrace;
    if (!trace) throw new Error("tail boundary trace is missing");
    trace.active = false;
    delete target.__denTailBoundaryTrace;
    return trace;
  });
}

export async function setTailBoundaryTracePhase(page: Page, phase: string) {
  await page.evaluate((nextPhase) => {
    (
      window as Window & { __denTailBoundaryPhase?: string }
    ).__denTailBoundaryPhase = nextPhase;
  }, phase);
}

export async function transcriptTailGap(page: Page) {
  return page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => {
      const body = host.querySelector<HTMLElement>(".den-chat-stream-body");
      const transcriptEnd = host.querySelector<HTMLElement>(
        "[data-transcript-end]",
      );
      const afterRunway = host.querySelector<HTMLElement>(
        '[data-transcript-runway="after"]',
      );
      const heldExtent = host.querySelector<HTMLElement>(
        "[data-scrollport-extent-hold]",
      );
      if (!body || !transcriptEnd) {
        throw new Error("transcript tail is unavailable");
      }
      return {
        gap:
          host.getBoundingClientRect().bottom -
          transcriptEnd.getBoundingClientRect().bottom,
        clearance: Number.parseFloat(getComputedStyle(body).paddingBottom) || 0,
        afterRunway: Number.parseFloat(afterRunway?.style.height ?? "") || 0,
        heldExtent: Number.parseFloat(heldExtent?.style.height ?? "") || 0,
        scrollTop: host.scrollTop,
        scrollHeight: host.scrollHeight,
        clientHeight: host.clientHeight,
      };
    });
}

export async function expectTranscriptTailBounded(page: Page) {
  await expect.poll(async () => {
    const state = await transcriptTailGap(page);
    return (
      state.heldExtent <= 1 &&
      state.gap <= state.clearance + 2
    )
      ? "bounded"
      : JSON.stringify(state);
  }).toBe("bounded");
}

export async function placeTranscriptAtTail(page: Page) {
  await page.evaluate(() => document.fonts.ready.then(() => undefined));
  await expect.poll(() => page.locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => host.scrollHeight > host.clientHeight ? "scrollable" : JSON.stringify({
      scrollHeight: host.scrollHeight,
      clientHeight: host.clientHeight,
      rows: host.querySelectorAll(".transcript-viewport-row").length,
      height: host.getBoundingClientRect().height,
      overflow: getComputedStyle(host).overflowY,
    }))).toBe("scrollable");
  await page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host) => {
      host.scrollTop = host.scrollHeight;
      host.dispatchEvent(new Event("scroll"));
    });
  // The initial scroll mounts rows that still need measurement.
  await page.evaluate(() => new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  ));
  await expectTranscriptTailBounded(page);
}

export async function placeTranscriptNearTail(page: Page) {
  await page
    .locator(`${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`)
    .evaluate((host, inputEvent) => {
      host.scrollTop = Math.max(
        0,
        host.scrollHeight - host.clientHeight - 36,
      );
      host.dispatchEvent(new Event("scroll"));
      host.dispatchEvent(new CustomEvent(inputEvent));
    }, DEN_SCROLLPORT_INPUT_EVENT);
}
