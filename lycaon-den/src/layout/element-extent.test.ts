// @vitest-environment jsdom
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  beginShellLayoutBusy,
  endShellLayoutBusy,
  flushShellLayoutSettleForTests,
  resetShellLayoutBusyForTests,
} from "../shell/shell-layout-busy.ts";
import { observeElementExtent } from "./element-extent.ts";
import { resetSharedResizeObserverForTests } from "./shared-resize-observer.ts";

type Observed = { target: Element; cb: ResizeObserverCallback };

const observed: Observed[] = [];
const unobserves = vi.fn();
let resizeFrames: FrameRequestCallback[] = [];
let resizeDeliveries: VoidFunction[] = [];

class FakeResizeObserver {
  constructor(private readonly cb: ResizeObserverCallback) {}
  observe(target: Element) {
    observed.push({ target, cb: this.cb });
  }
  disconnect() {}
  unobserve(target: Element) {
    unobserves();
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

afterEach(async () => {
  await flushShellLayoutSettleForTests();
  resetShellLayoutBusyForTests();
  resetSharedResizeObserverForTests();
  observed.length = 0;
  resizeFrames = [];
  resizeDeliveries = [];
  unobserves.mockClear();
});

describe("observeElementExtent", () => {
  it("reports the observed content box and never reads layout", () => {
    const el = document.createElement("div");
    // Throws if the extent falls back to a layout read.
    Object.defineProperty(el, "clientWidth", {
      get() {
        throw new Error("clientWidth read");
      },
    });

    createRoot((dispose) => {
      const extent = observeElementExtent(() => el);
      expect(extent.width()).toBeUndefined();
      expect(extent.height()).toBeUndefined();

      emit(el, 640, 480);
      flushResizeDelivery();
      expect(extent.width()).toBe(640);
      expect(extent.height()).toBe(480);

      emit(el, 320, 200);
      flushResizeDelivery();
      expect(extent.width()).toBe(320);
      dispose();
    });
  });

  it("clears the extent and unobserves when the element goes away", () => {
    const el = document.createElement("div");
    const [target, setTarget] = createSignal<HTMLElement | undefined>(el);
    // Signal writes inside the root callback stay batched until it returns, so
    // the re-observe has to be driven from outside it.
    const { extent, dispose } = createRoot((dispose) => ({
      extent: observeElementExtent(target),
      dispose,
    }));

    emit(el, 500, 300);
    flushResizeDelivery();
    expect(extent.width()).toBe(500);

    setTarget(undefined);
    expect(unobserves).toHaveBeenCalledTimes(1);
    // Stale geometry for a detached element would clamp panes against a box
    // that no longer exists.
    expect(extent.width()).toBeUndefined();
    dispose();
  });

  it("re-observes when the ref swaps to a new element", () => {
    const first = document.createElement("div");
    const second = document.createElement("div");
    const [target, setTarget] = createSignal<HTMLElement | undefined>(first);
    const { extent, dispose } = createRoot((dispose) => ({
      extent: observeElementExtent(target),
      dispose,
    }));

    emit(first, 800, 600);
    flushResizeDelivery();
    expect(extent.width()).toBe(800);

    setTarget(second);
    emit(second, 400, 300);
    flushResizeDelivery();
    expect(extent.width()).toBe(400);
    expect(unobserves).toHaveBeenCalledTimes(1);
    dispose();
  });

  it("publishes live boxes during a resize and remains current through settle", async () => {
    const el = document.createElement("div");
    const { extent, dispose } = createRoot((dispose) => ({
      extent: observeElementExtent(() => el),
      dispose,
    }));

    emit(el, 800, 600);
    flushResizeDelivery();
    expect(extent.width()).toBe(800);

    beginShellLayoutBusy();
    emit(el, 400, 500);
    flushResizeDelivery();
    expect(extent.width()).toBe(400);
    expect(extent.height()).toBe(500);
    emit(el, 300, 450);
    flushResizeDelivery();
    expect(extent.width()).toBe(300);

    endShellLayoutBusy();
    await flushShellLayoutSettleForTests();
    expect(extent.width()).toBe(300);
    dispose();
  });
});
