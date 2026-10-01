// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  observeSharedContentBox,
  resetSharedResizeObserverForTests,
} from "./shared-resize-observer.ts";

type Observed = { target: Element; cb: ResizeObserverCallback };

const observed: Observed[] = [];
let resizeFrames: FrameRequestCallback[] = [];
let resizeDeliveries: VoidFunction[] = [];

class FakeResizeObserver {
  constructor(private readonly cb: ResizeObserverCallback) {}
  observe(target: Element) {
    observed.push({ target, cb: this.cb });
  }
  disconnect() {
    for (let i = observed.length - 1; i >= 0; i--) {
      if (observed[i]?.cb === this.cb) observed.splice(i, 1);
    }
  }
  unobserve(target: Element) {
    for (let i = observed.length - 1; i >= 0; i--) {
      if (observed[i]?.target === target && observed[i]?.cb === this.cb) {
        observed.splice(i, 1);
      }
    }
  }
}

function emit(target: Element, width: number, height: number): void {
  const entry = {
    target,
    contentRect: { width, height },
  } as ResizeObserverEntry;
  for (const o of observed) {
    if (o.target === target) o.cb([entry], {} as ResizeObserver);
  }
}

vi.stubGlobal("ResizeObserver", FakeResizeObserver);
vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
  resizeFrames.push(callback);
  return resizeFrames.length;
});
vi.stubGlobal("cancelAnimationFrame", (handle: number) => {
  resizeFrames[handle - 1] = () => undefined;
});

vi.stubGlobal("queueMicrotask", (callback: VoidFunction) => {
  resizeDeliveries.push(callback);
});

function flushResizeDelivery(): void {
  const deliveries = resizeDeliveries;
  resizeDeliveries = [];
  for (const deliver of deliveries) deliver();
}

afterEach(() => {
  resetSharedResizeObserverForTests();
  observed.length = 0;
  resizeFrames = [];
  resizeDeliveries = [];
});

describe("shared-resize-observer", () => {
  it("delivers the content box and never reads layout", () => {
    const el = document.createElement("div");
    Object.defineProperty(el, "clientWidth", {
      get() {
        throw new Error("clientWidth read");
      },
    });
    const boxes: Array<{ width: number; height: number }> = [];
    const stop = observeSharedContentBox(el, (box) => boxes.push(box));
    emit(el, 640, 480);
    expect(boxes).toEqual([]);
    flushResizeDelivery();
    expect(boxes).toEqual([{ width: 640, height: 480 }]);
    stop();
  });

  it("publishes the latest live size before another frame without replaying intermediate boxes", () => {
    const el = document.createElement("div");
    const boxes: Array<{ width: number; height: number }> = [];
    const stop = observeSharedContentBox(el, (box) => boxes.push(box));

    emit(el, 320, 200);
    emit(el, 300, 200);
    flushResizeDelivery();
    expect(boxes).toEqual([{ width: 300, height: 200 }]);
    expect(resizeFrames).toHaveLength(0);
    emit(el, 300, 200);
    flushResizeDelivery();
    expect(boxes).toHaveLength(1);
    emit(el, 280, 200);
    flushResizeDelivery();
    expect(boxes).toHaveLength(2);
    stop();
  });

  it("delivers a pending box once to a late subscriber and drops disposed deliveries", () => {
    const el = document.createElement("div");
    const first = vi.fn();
    const late = vi.fn();
    const stopFirst = observeSharedContentBox(el, first);
    emit(el, 300, 200);
    const stopLate = observeSharedContentBox(el, late);
    expect(late).not.toHaveBeenCalled();
    flushResizeDelivery();
    expect(late).toHaveBeenCalledTimes(1);
    emit(el, 280, 200);
    stopFirst();
    stopLate();
    flushResizeDelivery();
    expect(first).toHaveBeenCalledTimes(1);
    expect(late).toHaveBeenCalledTimes(1);
  });

  it("unobserves when the last listener leaves", () => {
    const el = document.createElement("div");
    const stop = observeSharedContentBox(el, () => {});
    expect(observed.some((o) => o.target === el)).toBe(true);
    stop();
    expect(observed.some((o) => o.target === el)).toBe(false);
  });
});
