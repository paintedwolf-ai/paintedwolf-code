// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cancelStreamSpringScroll,
  isStreamSpringScrolling,
  streamSpringScrollTo,
} from "./stream-scroll-spring.ts";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";

const hosts = new Set<HTMLElement>();

afterEach(() => {
  for (const host of hosts) unbindScrollportMotion(host);
  hosts.clear();
  vi.restoreAllMocks();
});

function mockMetrics(scrollHeight: number, scrollTop: number, clientHeight: number) {
  const el = document.createElement("div");
  let top = scrollTop;
  Object.defineProperty(el, "scrollHeight", { value: scrollHeight, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: clientHeight, configurable: true });
  Object.defineProperty(el, "scrollTop", {
    get: () => top,
    set: (value: number) => {
      top = Math.max(0, Math.min(scrollHeight - clientHeight, value));
    },
    configurable: true,
  });
  bindScrollportMotion(el, el, el);
  hosts.add(el);
  return el;
}

/** Frames run as microtasks, one per request. */
function microtaskFrames(onFrame?: (frame: number) => void) {
  let frame = 0;
  vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
    frame += 1;
    const id = frame;
    queueMicrotask(() => {
      onFrame?.(id);
      cb(id * 16);
    });
    return id;
  });
  vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation(() => {});
}

describe("stream-scroll-spring", () => {
  it("glides to its target and reports arrival", async () => {
    microtaskFrames();
    const el = mockMetrics(1_000, 100, 100);

    await expect(streamSpringScrollTo(el, () => 400)).resolves.toBe(true);

    expect(el.scrollTop).toBeGreaterThanOrEqual(399);
    expect(isStreamSpringScrolling(el)).toBe(false);
  });

  it("follows a target that moves while content grows", async () => {
    let goal = 400;
    microtaskFrames((frame) => {
      if (frame === 5) goal = 700;
    });
    const el = mockMetrics(1_000, 100, 100);

    await expect(streamSpringScrollTo(el, () => goal)).resolves.toBe(true);

    expect(el.scrollTop).toBeGreaterThanOrEqual(699);
  });

  it("resolves as interrupted when canceled", async () => {
    const el = mockMetrics(1_000, 100, 100);
    const glide = streamSpringScrollTo(el, () => 400);
    expect(isStreamSpringScrolling(el)).toBe(true);

    cancelStreamSpringScroll(el);

    expect(isStreamSpringScrolling(el)).toBe(false);
    await expect(glide).resolves.toBe(false);
    expect(el.scrollTop).toBe(100);
  });

  it("lets a new glide supersede the one in flight", async () => {
    const el = mockMetrics(1_000, 100, 100);
    const first = streamSpringScrollTo(el, () => 400);
    const second = streamSpringScrollTo(el, () => 600);

    await expect(first).resolves.toBe(false);
    cancelStreamSpringScroll(el);
    await expect(second).resolves.toBe(false);
  });

  it("stops when its scrollport goes away", async () => {
    microtaskFrames();

    const removed = mockMetrics(1_000, 100, 100);
    const glide = streamSpringScrollTo(removed, () => 400);
    unbindScrollportMotion(removed);
    await expect(glide).resolves.toBe(false);
  });
});
