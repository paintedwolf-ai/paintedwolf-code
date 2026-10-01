// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cancelScrollportFrame, scheduleScrollportFrame } from "./scrollport-frame.ts";

const frames = new Map<number, FrameRequestCallback>();
let id = 0;
beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frames.set(++id, callback);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (frame: number) => frames.delete(frame));
});
afterEach(() => { frames.clear(); vi.unstubAllGlobals(); });
const paint = () => {
  const batch = [...frames.values()];
  frames.clear();
  for (const callback of batch) callback(16);
};

describe("scrollport frames", () => {
  it("deduplicates requests and measures before observing and rendering", () => {
    const viewport = document.createElement("div");
    const order: string[] = [];
    const render = () => order.push("render");
    scheduleScrollportFrame(viewport, "render", render);
    scheduleScrollportFrame(viewport, "render", render);
    scheduleScrollportFrame(viewport, "observe", () => order.push("observe"));
    scheduleScrollportFrame(viewport, "measure", () => order.push("measure"));
    expect(frames.size).toBe(1);
    paint();
    expect(order).toEqual(["measure", "observe", "render"]);
    expect(frames.size).toBe(0);
  });

  it("defers a canceled and replaced request made during delivery", () => {
    const viewport = document.createElement("div");
    const render = vi.fn();
    scheduleScrollportFrame(viewport, "render", render);
    scheduleScrollportFrame(viewport, "observe", () => {
      cancelScrollportFrame(viewport, render);
      scheduleScrollportFrame(viewport, "render", render);
    });
    paint();
    expect(render).not.toHaveBeenCalled();
    expect(frames.size).toBe(1);
    paint();
    expect(render).toHaveBeenCalledOnce();
  });

  it("cancels the frame when its last participant detaches", () => {
    const viewport = document.createElement("div");
    const render = vi.fn();
    scheduleScrollportFrame(viewport, "render", render);
    cancelScrollportFrame(viewport, render);
    expect(frames.size).toBe(0);
    paint();
    expect(render).not.toHaveBeenCalled();
  });

  it("keeps remaining work scheduled when an observer throws", () => {
    const viewport = document.createElement("div");
    const render = vi.fn();
    scheduleScrollportFrame(viewport, "observe", () => {
      throw new Error("observer failed");
    });
    scheduleScrollportFrame(viewport, "render", render);
    expect(paint).toThrow("observer failed");
    expect(frames.size).toBe(1);
    paint();
    expect(render).toHaveBeenCalledOnce();
  });
});
