// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createEventDeliveryScheduler, EVENT_DELIVERY_MAX_WAIT_MS } from "./event-delivery-scheduler.ts";

let hidden = false;
let frames: FrameRequestCallback[];
beforeEach(() => {
  hidden = false;
  frames = [];
  vi.useFakeTimers();
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  vi.stubGlobal("requestAnimationFrame", vi.fn((callback: FrameRequestCallback) => frames.push(callback)));
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });

function hide() {
  hidden = true;
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("event delivery scheduling", () => {
  it("batches foreground work on a frame and removes its other wakeups", () => {
    const remove = vi.spyOn(document, "removeEventListener");
    const apply = vi.fn();
    createEventDeliveryScheduler().request(apply);
    expect(apply).not.toHaveBeenCalled();
    frames[0]!(0);
    expect(apply).toHaveBeenCalledOnce();
    expect(cancelAnimationFrame).toHaveBeenCalledWith(1);
    expect(vi.getTimerCount()).toBe(0);
    expect(remove).toHaveBeenCalledWith("visibilitychange", expect.any(Function));
    frames[0]!(0);
    vi.advanceTimersByTime(EVENT_DELIVERY_MAX_WAIT_MS);
    expect(apply).toHaveBeenCalledOnce();
  });

  it("bounds foreground waiting when frames stop but JavaScript still runs", () => {
    const apply = vi.fn();
    createEventDeliveryScheduler().request(apply);
    vi.advanceTimersByTime(EVENT_DELIVERY_MAX_WAIT_MS - 1);
    expect(apply).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(apply).toHaveBeenCalledOnce();
    frames[0]!(0);
    expect(apply).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("promotes an already pending frame when the document becomes hidden", async () => {
    const apply = vi.fn();
    createEventDeliveryScheduler().request(apply);
    hide();
    expect(vi.getTimerCount()).toBe(0);
    await Promise.resolve();
    expect(apply).toHaveBeenCalledOnce();
    frames[0]!(0);
    expect(apply).toHaveBeenCalledOnce();
  });

  it.each(["hidden", "no frames"])("delivers without a timer or frame for %s", async mode => {
    if (mode === "hidden") hidden = true;
    else vi.stubGlobal("requestAnimationFrame", undefined);
    const apply = vi.fn();
    createEventDeliveryScheduler().request(apply);
    expect(apply).not.toHaveBeenCalled();
    await Promise.resolve();
    expect(apply).toHaveBeenCalledOnce();
    expect(frames).toHaveLength(0);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("cancels a promoted microtask without letting stale callbacks drain a new job", async () => {
    const scheduler = createEventDeliveryScheduler();
    const old = vi.fn();
    const fresh = vi.fn();
    const handle = scheduler.request(old);
    hide();
    scheduler.cancel(handle);
    hidden = false;
    scheduler.request(fresh);
    await Promise.resolve();
    frames[0]!(0);
    expect(old).not.toHaveBeenCalled();
    expect(fresh).not.toHaveBeenCalled();
    frames[1]!(0);
    expect(fresh).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("releases every scheduled resource when canceled while visible", async () => {
    const remove = vi.spyOn(document, "removeEventListener");
    const apply = vi.fn();
    const scheduler = createEventDeliveryScheduler();
    scheduler.cancel(scheduler.request(apply));
    hide();
    frames[0]!(0);
    await Promise.resolve();
    expect(apply).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
    expect(remove).toHaveBeenCalledWith("visibilitychange", expect.any(Function));
  });
});
