// @vitest-environment jsdom
import { watchStreamReaderIntent } from "./reader-intent/reader-intent.ts";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("overlayscrollbars", () => ({
  OverlayScrollbars: () => undefined,
}));

import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";
import {
  setStreamTailPin,
  streamTailOffset,
} from "./stream-scroll.ts";
import {
  bindScrollportMotion,
  scrollportMotionForHost,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";

const hosts = new Set<HTMLElement>();

afterEach(() => {
  for (const host of hosts) unbindScrollportMotion(host);
  hosts.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

const CLIENT_HEIGHT = 600;

/** Separates painted content from the virtual runway; the reader is not following. */
function createStreamFixture(opts?: {
  contentHeight?: number;
  scrollTop?: number;
  runwayFloorPx?: number;
  attachBeforeBind?: boolean;
}) {
  let contentHeight = opts?.contentHeight ?? 2_000;
  const runwayFloorPx = opts?.runwayFloorPx ?? 0;
  let scrollTop = opts?.scrollTop ?? 0;

  const host = document.createElement("div");
  host.className = "den-chat-stream";
  const body = document.createElement("div");
  body.className = "den-chat-stream-body";
  const inner = document.createElement("section");
  inner.className = "den-chat-stream-inner";
  const row = document.createElement("div");
  row.className = "transcript-viewport-row";
  row.dataset.msgId = "row-a";
  const transcriptEnd = document.createElement("div");
  transcriptEnd.dataset.transcriptEnd = "";
  inner.append(row, transcriptEnd);
  body.append(inner);
  host.append(body);
  document.body.append(host);

  const holdPx = () => {
    let total = 0;
    for (const el of host.querySelectorAll<HTMLElement>(
      "[data-scrollport-extent-hold]",
    )) {
      total += Number.parseFloat(el.style.height) || 0;
    }
    return total;
  };
  /** Published height includes the floor and any spacer. */
  const publishedHeight = () => Math.max(contentHeight, runwayFloorPx) + holdPx();

  Object.defineProperties(host, {
    clientHeight: { value: CLIENT_HEIGHT, configurable: true },
    clientWidth: { value: 400, configurable: true },
    scrollWidth: { value: 400, configurable: true },
    scrollLeft: { value: 0, writable: true, configurable: true },
    scrollHeight: { get: publishedHeight, configurable: true },
    scrollTop: {
      get: () => scrollTop,
      set: (value: number) => {
        // The scrollport clamps to its published range.
        scrollTop = Math.max(
          0,
          Math.min(publishedHeight() - CLIENT_HEIGHT, value),
        );
      },
      configurable: true,
    },
  });
  Object.defineProperty(host.style, "scrollBehavior", {
    value: "auto",
    writable: true,
    configurable: true,
  });
  vi.spyOn(host, "getBoundingClientRect").mockReturnValue({
    top: 0,
    bottom: CLIENT_HEIGHT,
  } as DOMRect);
  vi.spyOn(body, "getBoundingClientRect").mockImplementation(
    () => ({ top: -scrollTop, bottom: contentHeight - scrollTop }) as DOMRect,
  );
  vi.spyOn(transcriptEnd, "getBoundingClientRect").mockImplementation(
    () => ({ top: contentHeight - scrollTop, bottom: contentHeight - scrollTop }) as DOMRect,
  );

  // The painted boundary can precede the scrollport binding.
  if (opts?.attachBeforeBind) setStreamTailPin(host, null);
  bindScrollportMotion(host, host, host);
  hosts.add(host);
  if (!opts?.attachBeforeBind) setStreamTailPin(host, null);
  const stop = watchStreamReaderIntent(host, {
    following: () => false,
    stopFollowing: () => {},
    resumeFollowing: () => {},
    onReaderInput: () => {},
  });

  return {
    host,
    motion: scrollportMotionForHost(host)!,
    holdPx,
    publishedHeight,
    stop,
    /** The last offset that still shows transcript. */
    tail: () => Math.max(0, contentHeight - CLIENT_HEIGHT),
    setContentHeight(value: number) {
      contentHeight = value;
      // Contraction clamps the native offset and emits a scroll event.
      const before = scrollTop;
      host.scrollTop = scrollTop;
      if (scrollTop !== before) host.dispatchEvent(new Event("scroll"));
    },
    /** Applies a scroll from outside this module. */
    scrollNatively(value: number) {
      host.scrollTop = value;
      host.dispatchEvent(new Event("scroll"));
    },
  };
}

async function paint(frames = 4): Promise<void> {
  for (let frame = 0; frame < frames; frame += 1) {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  }
}

/** Paints the given frames, then until any glide back to the tail comes to rest. */
async function settleStreamScroll(host: HTMLElement, frames = 4): Promise<void> {
  flushScrollportFrameForTests(host);
  await paint(frames);
  let still = 0;
  for (let frame = 0; frame < 60 && still < 2; frame += 1) {
    const before = host.scrollTop;
    await paint(1);
    still = host.scrollTop === before ? still + 1 : 0;
  }
}

describe("the chat viewport cannot rest below the transcript", () => {
  it("shows a scrollbar only for range the reader can reach", async () => {
    const fixture = createStreamFixture({ contentHeight: 400, scrollTop: 0 });

    expect(fixture.tail()).toBe(0);
    expect(Math.max(0, fixture.publishedHeight() - CLIENT_HEIGHT)).toBe(0);

    fixture.scrollNatively(300);
    await settleStreamScroll(fixture.host, 6);

    expect(fixture.host.scrollTop).toBe(0);
    expect(fixture.holdPx()).toBe(0);
    fixture.stop();
  });

  it("holds the reader through unconfirmed contractions, then rests on the tail", async () => {
    const fixture = createStreamFixture({ contentHeight: 3_000, scrollTop: 2_400 });

    // Remeasurement repeatedly overshoots the height the transcript settles at.
    for (const height of [2_600, 3_100, 1_800, 2_200, 1_200]) {
      fixture.setContentHeight(height);
      fixture.motion.notifyLayoutMutated();
      expect(fixture.host.scrollTop).toBe(2_400);
      // The range stays within the content or the offset it holds.
      expect(fixture.publishedHeight() - CLIENT_HEIGHT).toBe(
        Math.max(fixture.tail(), 2_400),
      );
    }
    await settleStreamScroll(fixture.host, 8);

    expect(fixture.publishedHeight() - CLIENT_HEIGHT).toBe(fixture.tail());
    expect(fixture.host.scrollTop).toBe(fixture.tail());
    expect(fixture.holdPx()).toBe(0);
    fixture.stop();
  });

  it("returns a scroll it never issued out of range published past the end", async () => {
    // A host that publishes a floor beyond its painted rows.
    const fixture = createStreamFixture({
      contentHeight: 1_400,
      runwayFloorPx: 2_600,
      scrollTop: 700,
    });

    expect(streamTailOffset(fixture.host)).toBe(800);
    fixture.scrollNatively(2_000);
    expect(fixture.host.scrollTop).toBe(2_000);

    await settleStreamScroll(fixture.host, 6);

    expect(fixture.host.scrollTop).toBe(800);
    fixture.stop();
  });

  it("does not yank the reader's own gesture, and returns it once settled", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const now = vi.spyOn(performance, "now");
    now.mockReturnValue(1_000);
    const fixture = createStreamFixture({ contentHeight: 2_000, scrollTop: 1_400 });

    fixture.motion.input.noteNativeInput("wheel");
    fixture.setContentHeight(1_000);
    fixture.motion.notifyLayoutMutated();
    expect(fixture.host.scrollTop).toBe(1_400);
    // The held range is exactly the reader's offset; input cannot pass it.
    expect(fixture.publishedHeight() - CLIENT_HEIGHT).toBe(1_400);
    fixture.scrollNatively(fixture.publishedHeight());
    expect(fixture.host.scrollTop).toBe(1_400);

    // The clock keeps running once the input window closes, so the glide back can advance.
    let clock = 3_000;
    now.mockImplementation(() => (clock += 16));
    vi.advanceTimersByTime(700);
    await settleStreamScroll(fixture.host, 8);

    expect(fixture.host.scrollTop).toBe(400);
    expect(fixture.holdPx()).toBe(0);
    fixture.stop();
    vi.useRealTimers();
  });

  it("keeps the painted boundary when the scrollport binds after the viewport attaches", () => {
    const fixture = createStreamFixture({
      contentHeight: 2_000,
      scrollTop: 1_400,
      attachBeforeBind: true,
    });

    fixture.setContentHeight(1_000);
    fixture.motion.notifyLayoutMutated();

    // The boundary belongs to the host, so bind order cannot lose it.
    expect(streamTailOffset(fixture.host)).toBe(400);
    fixture.stop();
  });

  it("stays out of published runway while the transcript grows into it", async () => {
    const fixture = createStreamFixture({
      contentHeight: 1_000,
      runwayFloorPx: 2_400,
      scrollTop: 900,
    });

    let height = 1_000;
    let growing = true;
    const grow = () => {
      if (!growing) return;
      height += 150;
      fixture.setContentHeight(height);
      requestAnimationFrame(grow);
    };
    requestAnimationFrame(grow);

    fixture.scrollNatively(900);
    flushScrollportFrameForTests(fixture.host);
    for (let frame = 0; frame < 8; frame += 1) {
      await paint(1);
      // Never left below the painted content, however fast it grows.
      expect(fixture.host.scrollTop).toBeLessThanOrEqual(fixture.tail());
    }
    growing = false;

    await settleStreamScroll(fixture.host, 6);
    expect(fixture.host.scrollTop).toBeLessThanOrEqual(fixture.tail());
    expect(fixture.holdPx()).toBe(0);
    fixture.stop();
  });

  it("reconciles after direct input settles with no further events", async () => {
    // The settle window closes on a clock. A flick that ends with the reader
    // below the transcript has no later scroll or layout event to ride.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const now = vi.spyOn(performance, "now");
    now.mockReturnValue(1_000);
    const fixture = createStreamFixture({ contentHeight: 2_000, scrollTop: 1_400 });

    fixture.motion.input.noteNativeInput("touch");
    fixture.setContentHeight(1_100);
    fixture.motion.notifyLayoutMutated();
    await settleStreamScroll(fixture.host, 6);

    // The claim outlives the frames, so the offset is still held.
    expect(fixture.host.scrollTop).toBe(1_400);

    let clock = 3_000;
    now.mockImplementation(() => (clock += 16));
    vi.advanceTimersByTime(700);
    await settleStreamScroll(fixture.host, 6);
    expect(fixture.host.scrollTop).toBe(500);
    fixture.stop();
    vi.useRealTimers();
  });

  it("does not spend geometry retries while direct input causes the overrun", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let currentTime = 1_000;
    vi.spyOn(performance, "now").mockImplementation(() => currentTime);
    const fixture = createStreamFixture({ contentHeight: 2_000, scrollTop: 1_400 });

    fixture.motion.input.noteNativeInput("touch");
    fixture.setContentHeight(1_100);
    fixture.motion.notifyLayoutMutated();

    // The direct-input lease outlives the 30-frame geometry budget.
    for (let step = 0; step < 40; step += 1) {
      currentTime += 16;
      vi.advanceTimersByTime(16);
    }
    vi.mocked(performance.now).mockImplementation(() => (currentTime += 16));
    await settleStreamScroll(fixture.host, 6);

    expect(fixture.host.scrollTop).toBe(500);
    expect(fixture.holdPx()).toBe(0);
    fixture.stop();
    vi.useRealTimers();
  });
});
