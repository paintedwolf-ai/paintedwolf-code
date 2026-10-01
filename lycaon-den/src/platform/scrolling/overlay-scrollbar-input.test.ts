// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Elements } from "overlayscrollbars";
import {
  DEN_SCROLLBAR_DRAGGING_ATTR,
  bindOverlayScrollbarInput,
  type ScrollbarAxisModel,
} from "./overlay-scrollbar-input.ts";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "./scrollport-motion.ts";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function pointer(type: string, init: Partial<PointerEvent>): PointerEvent {
  const event = new Event(type, { bubbles: true, cancelable: true }) as PointerEvent;
  Object.assign(event, { isPrimary: true, button: 0, pointerId: 7 }, init);
  return event;
}

function setSize(element: HTMLElement, name: string, value: number): void {
  Object.defineProperty(element, name, { value, configurable: true });
}

function setRect(element: HTMLElement, rect: Partial<DOMRect>): void {
  vi.spyOn(element, "getBoundingClientRect").mockReturnValue({
    x: 0,
    y: 0,
    top: 0,
    right: 0,
    bottom: 0,
    left: 0,
    width: 0,
    height: 0,
    toJSON: () => ({}),
    ...rect,
  });
}

function createScrollbarFixture(model?: ScrollbarAxisModel) {
  const host = document.createElement("div");
  const viewport = document.createElement("div");
  const vertical = {
    scrollbar: document.createElement("div"),
    track: document.createElement("div"),
    handle: document.createElement("div"),
  };
  const horizontal = {
    scrollbar: document.createElement("div"),
    track: document.createElement("div"),
    handle: document.createElement("div"),
  };
  vertical.track.appendChild(vertical.handle);
  vertical.scrollbar.appendChild(vertical.track);
  horizontal.track.appendChild(horizontal.handle);
  horizontal.scrollbar.appendChild(horizontal.track);
  host.append(vertical.scrollbar, horizontal.scrollbar, viewport);
  setSize(viewport, "clientHeight", 500);
  setSize(viewport, "scrollHeight", 5_000);
  setSize(vertical.track, "clientHeight", 500);
  setSize(vertical.handle, "clientHeight", 50);
  const elements = {
    host,
    scrollOffsetElement: viewport,
    scrollbarVertical: vertical,
    scrollbarHorizontal: horizontal,
  } as unknown as Elements;
  const motion = bindScrollportMotion(host, viewport, viewport);
  const stop = bindOverlayScrollbarInput(elements, motion, model);
  return { host, viewport, vertical, motion, stop: stop.dispose, refreshGeometry: stop.refreshGeometry };
}

describe("overlay scrollbar input", () => {
  it("keeps the thumb fraction when extent changes under a held pointer", () => {
    vi.stubGlobal("requestAnimationFrame", undefined);
    let offset = 0, extent = 5_500;
    const targets: number[] = [];
    const { host, vertical, stop, refreshGeometry } = createScrollbarFixture({
      read: () => ({ offset, viewport: 500, extent }),
      scrollTo: next => { offset = next; targets.push(next); },
    });
    vertical.handle.dispatchEvent(pointer("pointerdown", { clientY: 100 }));
    vertical.handle.dispatchEvent(pointer("pointermove", { clientY: 325 }));
    extent = 10_500;
    expect(refreshGeometry()).toBe(true);
    expect(refreshGeometry()).toBe(false);
    extent = 2_500;
    expect(refreshGeometry()).toBe(true);
    expect(targets).toEqual([2_500, 5_000, 1_000]);
    vertical.handle.dispatchEvent(pointer("pointerup", { clientY: 325 }));
    extent = 20_500;
    expect(refreshGeometry()).toBe(false);
    stop();
    unbindScrollportMotion(host);
  });

  it("keeps a thumb gesture in logical coordinates across physical recentering", () => {
    vi.stubGlobal("requestAnimationFrame", undefined);
    let logical = 2_000_000;
    const targets: number[] = [];
    const model: ScrollbarAxisModel = {
      read: () => ({ offset: logical, viewport: 500, extent: 10_000_500 }),
      scrollTo: offset => { logical = offset; targets.push(offset); },
    };
    const { host, viewport, vertical, stop } = createScrollbarFixture(model);
    viewport.scrollTop = 2_000;
    vertical.handle.dispatchEvent(pointer("pointerdown", { clientY: 100 }));
    vertical.handle.dispatchEvent(pointer("pointermove", { clientY: 190 }));
    viewport.scrollTop = 1_000;
    vertical.handle.dispatchEvent(pointer("pointermove", { clientY: 280 }));
    expect(targets).toHaveLength(2);
    expect(targets[0]).toBeCloseTo(4_000_000);
    expect(targets[1]).toBeCloseTo(6_000_000);
    vertical.handle.dispatchEvent(pointer("pointerup", { clientY: 280 }));
    stop();
    unbindScrollportMotion(host);
  });

  it("maps track clicks to the full logical range", () => {
    const scrollTo = vi.fn();
    const { host, vertical, stop } = createScrollbarFixture({
      read: () => ({ offset: 0, viewport: 500, extent: 10_000_500 }), scrollTo,
    });
    setRect(vertical.track, { top: 100, height: 500 });
    setRect(vertical.handle, { top: 100, height: 50 });
    vertical.track.dispatchEvent(pointer("pointerdown", { clientY: 350 }));
    expect(scrollTo).toHaveBeenCalledWith(5_000_000, "track_click");
    stop();
    unbindScrollportMotion(host);
  });

  it("writes the latest thumb position once per animation frame", () => {
    const frames: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.push(callback);
      return frames.length;
    });
    const { host, viewport, vertical, stop } = createScrollbarFixture();

    vertical.handle.dispatchEvent(pointer("pointerdown", { clientY: 20 }));
    vertical.handle.dispatchEvent(pointer("pointermove", { clientY: 40 }));
    vertical.handle.dispatchEvent(pointer("pointermove", { clientY: 70 }));

    expect(frames).toHaveLength(1);
    expect(viewport.scrollTop).toBe(0);
    expect(host.getAttribute(DEN_SCROLLBAR_DRAGGING_ATTR)).toBe("y");
    frames[0]!(0);
    expect(viewport.scrollTop).toBe(500);

    vertical.handle.dispatchEvent(pointer("pointerup", { clientY: 70 }));
    expect(host.hasAttribute(DEN_SCROLLBAR_DRAGGING_ATTR)).toBe(false);
    stop();
    unbindScrollportMotion(host);
  });

  it("routes track clicks through the motion writer", () => {
    const { host, viewport, vertical, motion, stop } = createScrollbarFixture();
    setRect(vertical.track, { top: 100, bottom: 600, height: 500 });
    setRect(vertical.handle, { top: 200, bottom: 250, height: 50 });
    const commit = vi.spyOn(motion, "commit");
    const event = pointer("pointerdown", { clientY: 475 });

    expect(vertical.scrollbar.classList.contains("den-scrollbar-track-interactive"))
      .toBe(true);
    expect(vertical.scrollbar.classList.contains("os-scrollbar-track-interactive"))
      .toBe(false);
    vertical.track.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(commit).toHaveBeenCalledWith(2_500, "track_click", {
      axis: "y",
      measuredMaxOffset: 4_500,
    });
    expect(viewport.scrollTop).toBe(2_500);
    stop();
    expect(vertical.scrollbar.classList.contains("den-scrollbar-track-interactive"))
      .toBe(false);
    unbindScrollportMotion(host);
  });

});
