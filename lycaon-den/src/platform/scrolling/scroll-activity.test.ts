// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  DEN_SCROLLING_ATTR,
  elementUnderPointer,
  isScrollActive,
  resetScrollActivityForTests,
  setupScrollActivity,
  subscribeAnyScrollActivity,
  subscribeScrollActivity,
} from "./scroll-activity.ts";

afterEach(() => {
  resetScrollActivityForTests();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("scroll activity", () => {
  it("publishes one start and one settled phase for a scroll burst", () => {
    vi.useFakeTimers();
    setupScrollActivity();
    const target = document.createElement("div");
    document.body.append(target);
    const local = vi.fn();
    const any = vi.fn();
    subscribeScrollActivity(target, local);
    subscribeAnyScrollActivity(any);

    target.dispatchEvent(new Event("scroll"));
    target.dispatchEvent(new Event("scroll"));

    expect(local).toHaveBeenCalledTimes(1);
    expect(local).toHaveBeenCalledWith("start", target);
    expect(any).toHaveBeenCalledTimes(1);
    expect(target.hasAttribute(DEN_SCROLLING_ATTR)).toBe(true);
    expect(isScrollActive(target)).toBe(true);
    // A document-wide mark would restyle every element at each start and settle.
    expect(document.documentElement.hasAttribute(DEN_SCROLLING_ATTR)).toBe(false);

    vi.advanceTimersByTime(120);
    expect(local).toHaveBeenLastCalledWith("settle", target);
    expect(any).toHaveBeenLastCalledWith("settle", target);
    expect(target.hasAttribute(DEN_SCROLLING_ATTR)).toBe(false);
    expect(isScrollActive(target)).toBe(false);
  });

  it("settles immediately when the platform emits scrollend", () => {
    vi.useFakeTimers();
    setupScrollActivity();
    const target = document.createElement("div");
    document.body.append(target);
    const listener = vi.fn();
    subscribeScrollActivity(target, listener);

    target.dispatchEvent(new Event("scroll"));
    target.dispatchEvent(new Event("scrollend"));

    expect(listener.mock.calls.map(([phase]) => phase)).toEqual([
      "start",
      "settle",
    ]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("clears target subscriptions during test reset", () => {
    setupScrollActivity();
    const target = document.createElement("div");
    document.body.append(target);
    const listener = vi.fn();
    subscribeScrollActivity(target, listener);

    resetScrollActivityForTests();
    setupScrollActivity();
    target.dispatchEvent(new Event("scroll"));

    expect(listener).not.toHaveBeenCalled();
  });

  it("marks editor scrollers like every other scroller", () => {
    vi.useFakeTimers();
    setupScrollActivity();
    const editor = document.createElement("div");
    editor.className = "cm-editor";
    const target = document.createElement("div");
    editor.append(target);
    document.body.append(editor);
    const listener = vi.fn();
    subscribeScrollActivity(target, listener);

    target.dispatchEvent(new Event("scroll"));

    expect(listener).toHaveBeenCalledWith("start", target);
    expect(target.hasAttribute(DEN_SCROLLING_ATTR)).toBe(true);
    expect(editor.hasAttribute(DEN_SCROLLING_ATTR)).toBe(false);

    target.dispatchEvent(new Event("scrollend"));
    expect(listener).toHaveBeenLastCalledWith("settle", target);
    expect(target.hasAttribute(DEN_SCROLLING_ATTR)).toBe(false);
  });

  it("finds the element under the resting pointer, and nothing once the pointer leaves", () => {
    setupScrollActivity();
    const under = document.body.appendChild(document.createElement("div"));
    const at = vi.fn(() => under);
    Object.defineProperty(document, "elementFromPoint", { configurable: true, value: at });
    expect(elementUnderPointer()).toBeNull();

    window.dispatchEvent(new MouseEvent("pointermove", { clientX: 40, clientY: 70 }));
    expect(elementUnderPointer()).toBe(under);
    expect(at).toHaveBeenLastCalledWith(40, 70);

    window.dispatchEvent(new MouseEvent("pointerout", { relatedTarget: null }));
    expect(elementUnderPointer()).toBeNull();
    Reflect.deleteProperty(document, "elementFromPoint");
  });

  it("uses one settle clock while activity keeps extending the burst", () => {
    vi.useFakeTimers();
    const timeout = vi.spyOn(globalThis, "setTimeout");
    setupScrollActivity();
    const target = document.createElement("div");
    document.body.append(target);
    const listener = vi.fn();
    subscribeScrollActivity(target, listener);

    target.dispatchEvent(new Event("scroll"));
    vi.advanceTimersByTime(60);
    target.dispatchEvent(new Event("scroll"));

    expect(timeout).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(59);
    expect(listener).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(61);
    expect(listener.mock.calls.map(([phase]) => phase)).toEqual([
      "start",
      "settle",
    ]);
  });
});
