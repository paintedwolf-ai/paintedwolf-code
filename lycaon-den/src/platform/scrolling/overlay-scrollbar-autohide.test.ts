// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Elements } from "overlayscrollbars";
import {
  DEN_SCROLLBAR_IDLE_CLASS,
  SCROLLBAR_IDLE_DELAY_MS,
  bindOverlayScrollbarAutoHide,
} from "./overlay-scrollbar-autohide.ts";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "./scrollport-motion.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "./scrollport-motion-types.ts";

function setSize(element: HTMLElement, name: string, value: number): void {
  Object.defineProperty(element, name, { value, configurable: true });
}

function pointer(
  type: string,
  pointerType = "mouse",
  at: { x: number; y: number } = { x: 10, y: 10 },
): PointerEvent {
  const event = new Event(type, { bubbles: true }) as PointerEvent;
  Object.assign(event, {
    pointerType,
    isPrimary: true,
    pointerId: 1,
    screenX: at.x,
    screenY: at.y,
  });
  return event;
}

function createFixture() {
  const host = document.createElement("div");
  const viewport = document.createElement("div");
  const content = document.createElement("div");
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
  viewport.appendChild(content);
  host.append(vertical.scrollbar, horizontal.scrollbar, viewport);
  document.body.appendChild(host);
  setSize(viewport, "clientHeight", 500);
  setSize(viewport, "scrollHeight", 5_000);
  const elements = {
    host,
    scrollbarVertical: vertical,
    scrollbarHorizontal: horizontal,
  } as unknown as Elements;
  const motion = bindScrollportMotion(host, viewport, content);
  const stop = bindOverlayScrollbarAutoHide(elements, motion);
  const bars = [vertical.scrollbar, horizontal.scrollbar];
  const idle = () => bars.map((bar) => bar.classList.contains(DEN_SCROLLBAR_IDLE_CLASS));
  return { host, viewport, vertical, bars, idle, motion, stop };
}

describe("overlay scrollbar auto-hide", () => {
  beforeEach(() => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "Date", "performance"],
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    document.body.replaceChildren();
  });

  it("starts idle and ignores application scrolls", () => {
    const { host, viewport, idle, motion, stop } = createFixture();
    expect(idle()).toEqual([true, true]);

    motion.commit(4_500, "jump");
    viewport.dispatchEvent(new Event("scroll"));
    motion.commit(4_500, "repin_tail");
    viewport.dispatchEvent(new Event("scroll"));
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS * 2);

    expect(idle()).toEqual([true, true]);
    stop();
    unbindScrollportMotion(host);
  });

  it("reveals for direct input and fades after the idle window", () => {
    const { host, idle, stop } = createFixture();

    host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
    expect(idle()).toEqual([false, false]);

    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS - 1);
    expect(idle()).toEqual([false, false]);
    vi.advanceTimersByTime(1);
    expect(idle()).toEqual([true, true]);
    stop();
    unbindScrollportMotion(host);
  });

  it("keeps the bar through a running gesture and fades once it settles", () => {
    const { host, idle, motion, stop } = createFixture();

    motion.input.beginThumbGesture();
    expect(idle()).toEqual([false, false]);
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS * 3);
    expect(idle()).toEqual([false, false]);

    motion.input.endThumbGesture();
    // The direct-input settle window outlives the gesture.
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS);
    expect(motion.input.isDirectInputActive()).toBe(false);
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS);
    expect(idle()).toEqual([true, true]);
    stop();
    unbindScrollportMotion(host);
  });

  it("reveals for hover moves and scroll keys only", () => {
    const { host, idle, stop } = createFixture();

    host.dispatchEvent(pointer("pointermove", "touch"));
    expect(idle()).toEqual([true, true]);
    host.dispatchEvent(pointer("pointermove"));
    expect(idle()).toEqual([false, false]);
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS);
    expect(idle()).toEqual([true, true]);

    host.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
    expect(idle()).toEqual([true, true]);
    const textarea = document.createElement("textarea");
    host.appendChild(textarea);
    textarea.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }),
    );
    expect(idle()).toEqual([true, true]);
    host.dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown", bubbles: true }));
    expect(idle()).toEqual([false, false]);
    stop();
    unbindScrollportMotion(host);
  });

  it("stays idle for moves raised under a resting pointer", () => {
    const { host, idle, stop } = createFixture();

    host.dispatchEvent(pointer("pointermove", "mouse", { x: 40, y: 80 }));
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS);
    expect(idle()).toEqual([true, true]);

    // Content scrolled beneath the pointer; it did not move.
    host.dispatchEvent(pointer("pointermove", "mouse", { x: 40, y: 80 }));
    expect(idle()).toEqual([true, true]);

    host.dispatchEvent(pointer("pointermove", "mouse", { x: 41, y: 80 }));
    expect(idle()).toEqual([false, false]);
    stop();
    unbindScrollportMotion(host);
  });

  it("keeps one timer through pointer traffic and fades at the last deadline", () => {
    const { host, idle, stop } = createFixture();

    host.dispatchEvent(pointer("pointermove", "mouse", { x: 1, y: 1 }));
    for (let n = 2; n <= 40; n += 1) {
      vi.advanceTimersByTime(10);
      host.dispatchEvent(pointer("pointermove", "mouse", { x: n, y: 1 }));
    }
    expect(vi.getTimerCount()).toBe(1);
    expect(idle()).toEqual([false, false]);

    // The idle window runs from the last move, not the first.
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS - 1);
    expect(idle()).toEqual([false, false]);
    vi.advanceTimersByTime(1);
    expect(idle()).toEqual([true, true]);
    stop();
    unbindScrollportMotion(host);
  });

  it("holds the bar while the pointer rests on it", () => {
    const { host, vertical, idle, stop } = createFixture();

    vertical.scrollbar.dispatchEvent(new Event("pointerenter"));
    expect(idle()).toEqual([false, false]);
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS * 3);
    expect(idle()).toEqual([false, false]);

    vertical.scrollbar.dispatchEvent(new Event("pointerleave"));
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS);
    expect(idle()).toEqual([true, true]);
    stop();
    unbindScrollportMotion(host);
  });

  it("releases its class and listeners with the binding", () => {
    const { host, idle, stop } = createFixture();

    host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
    stop();
    expect(idle()).toEqual([false, false]);
    vi.advanceTimersByTime(SCROLLBAR_IDLE_DELAY_MS);
    expect(idle()).toEqual([false, false]);
    host.dispatchEvent(pointer("pointermove"));
    expect(idle()).toEqual([false, false]);
    unbindScrollportMotion(host);
  });
});
