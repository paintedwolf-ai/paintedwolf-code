// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { bindScrollportMotion, unbindScrollportMotion } from "../../../platform/scrolling/scrollport-motion.ts";
import { scheduleScrollportFrame } from "../../../platform/scrolling/scrollport-frame.ts";
import { subscribeStreamScroll } from "../../stream/stream-scroll.ts";
import { observeTranscriptScrollOffset, type TranscriptVirtualizer } from "./transcript-virtualizer.ts";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const stop of cleanups.splice(0).reverse()) stop();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function fixture() {
  vi.useFakeTimers();
  const frames = new Map<number, FrameRequestCallback>();
  let id = 0;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frames.set(++id, callback);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (frame: number) => frames.delete(frame));
  const viewport = document.createElement("section");
  let top = 0;
  const readTop = vi.fn(() => top);
  Object.defineProperties(viewport, {
    scrollTop: { get: readTop, set: (value: number) => { top = value; } },
    scrollHeight: { value: 10_000 }, clientHeight: { value: 500 },
  });
  bindScrollportMotion(viewport, viewport, viewport);
  cleanups.push(() => unbindScrollportMotion(viewport));
  const changed = vi.fn();
  const stop = observeTranscriptScrollOffset({ scrollElement: viewport } as TranscriptVirtualizer, changed);
  cleanups.push(stop);
  const scroll = (offset: number) => {
    top = offset;
    viewport.dispatchEvent(new Event("scroll"));
  };
  const paint = () => {
    const batch = [...frames.values()];
    frames.clear();
    for (const callback of batch) callback(16);
  };
  readTop.mockClear();
  return { viewport, changed, readTop, frames, scroll, paint, stop };
}

describe("transcript scroll observation", () => {
  it("shares native capture with stream observers and delivers them before rendering", () => {
    const f = fixture();
    const order: string[] = [];
    f.changed.mockImplementation(() => order.push("render"));
    cleanups.push(subscribeStreamScroll(f.viewport, () => order.push("observe")));
    f.scroll(200);
    expect(f.readTop).toHaveBeenCalledOnce();
    f.paint();
    expect(order).toEqual(["observe", "render"]);
  });

  it("cancels queued stream delivery when the last subscriber leaves", () => {
    const f = fixture();
    const first = vi.fn();
    const stop = subscribeStreamScroll(f.viewport, first);
    f.scroll(200);
    stop();
    const second = vi.fn();
    cleanups.push(subscribeStreamScroll(f.viewport, second));
    f.paint();
    expect(first).not.toHaveBeenCalled();
    expect(second).not.toHaveBeenCalled();
    f.scroll(300);
    f.paint();
    expect(second).toHaveBeenCalledOnce();
  });

  it("runs stream observations before virtual rendering even when rendering queued first", () => {
    const f = fixture();
    const order: string[] = [];
    f.changed.mockImplementation(() => order.push("render"));
    f.scroll(200);
    scheduleScrollportFrame(f.viewport, "observe", () => order.push("observe"));
    f.paint();
    expect(order).toEqual(["observe", "render"]);
  });

  it("publishes the latest captured offset once per frame without rereading after DOM writers", () => {
    const f = fixture();
    expect(f.changed.mock.calls).toEqual([[0, false]]);
    // A target listener can update layout before later target listeners run.
    f.viewport.addEventListener("scroll", () => { f.viewport.scrollTop = 900; });
    f.scroll(100);
    f.scroll(200);
    f.scroll(300);
    expect(f.changed).toHaveBeenCalledTimes(1);
    expect(f.frames.size).toBe(1);
    expect(f.readTop).toHaveBeenCalledTimes(3);
    f.paint();
    expect(f.changed.mock.calls).toEqual([[0, false], [300, true]]);
    expect(f.readTop).toHaveBeenCalledTimes(3);
    vi.advanceTimersByTime(150);
    expect(f.changed).toHaveBeenLastCalledWith(300, false);
    expect(f.readTop).toHaveBeenCalledTimes(3);
  });

  it("ignores scrolling inside nested tool outputs", () => {
    const f = fixture();
    const nested = document.createElement("div");
    f.viewport.append(nested);
    nested.dispatchEvent(new Event("scroll"));
    expect(f.readTop).not.toHaveBeenCalled();
    expect(f.frames.size).toBe(0);
    expect(f.changed).toHaveBeenCalledTimes(1);
  });

  it("shares the sample after the motion binding is replaced", () => {
    const f = fixture();
    f.viewport.addEventListener("scroll", () => { f.viewport.scrollTop = 900; });
    unbindScrollportMotion(f.viewport);
    bindScrollportMotion(f.viewport, f.viewport, f.viewport);
    f.readTop.mockClear();
    f.scroll(200);
    f.paint();
    expect(f.changed).toHaveBeenLastCalledWith(200, true);
    expect(f.readTop).toHaveBeenCalledOnce();
  });

  it("takes a new sample when a synthetic event object is dispatched again", () => {
    const f = fixture();
    const event = new Event("scroll");
    f.viewport.scrollTop = 200;
    f.viewport.dispatchEvent(event);
    f.viewport.scrollTop = 400;
    f.viewport.dispatchEvent(event);
    f.paint();
    expect(f.changed).toHaveBeenLastCalledWith(400, true);
    expect(f.readTop).toHaveBeenCalledTimes(2);
  });

  it("settles the latest offset when a background frame has not run", () => {
    const f = fixture();
    f.scroll(400);
    vi.advanceTimersByTime(150);
    expect(f.changed.mock.calls).toEqual([[0, false], [400, false]]);
    f.paint();
    expect(f.changed).toHaveBeenCalledTimes(2);
  });

  it("cancels frame and settle delivery when the view detaches", () => {
    const f = fixture();
    f.scroll(400);
    f.stop();
    f.paint();
    vi.advanceTimersByTime(200);
    expect(f.changed.mock.calls).toEqual([[0, false]]);
  });
});
