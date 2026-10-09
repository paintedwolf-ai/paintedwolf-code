// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { driveActivitySpinner } from "./activity-spinner-motion.ts";

const { reduced } = vi.hoisted(() => ({ reduced: vi.fn(() => false) }));
vi.mock("../../platform/interaction/reduced-motion.ts", () => ({ prefersReducedMotion: reduced }));

afterEach(() => { vi.unstubAllGlobals(); reduced.mockReturnValue(false); });

describe("activity spinner frames", () => {
  it("seeks CSS keyframes, re-arms on focus, and releases its own frame", () => {
    let callback: FrameRequestCallback | undefined;
    let handle = 0;
    const request = vi.fn((next: FrameRequestCallback) => { callback = next; return ++handle; });
    const cancel = vi.fn();
    vi.stubGlobal("requestAnimationFrame", request);
    vi.stubGlobal("cancelAnimationFrame", cancel);
    const animation = { animationName: "den-composer-activity-spin", pause: vi.fn(), play: vi.fn(), cancel: vi.fn(), currentTime: 0 };
    const element = document.createElement("span");
    element.getAnimations = () => [animation as unknown as Animation];
    const stop = driveActivitySpinner(element);
    callback?.(performance.now() + 100);
    expect(animation.pause).toHaveBeenCalledOnce();
    expect(animation.currentTime).toBeGreaterThanOrEqual(100);
    window.dispatchEvent(new Event("focus"));
    expect(cancel).toHaveBeenCalledWith(2);
    expect(animation.play).toHaveBeenCalledOnce();
    stop();
    expect(cancel).toHaveBeenCalledWith(3);
    const before = request.mock.calls.length;
    window.dispatchEvent(new Event("focus"));
    expect(request).toHaveBeenCalledTimes(before);
  });

  it("does not revive the CSS animation removed by a live reduced-motion change", () => {
    const media = new EventTarget();
    vi.stubGlobal("matchMedia", () => media);
    let callback: FrameRequestCallback | undefined;
    const request = vi.fn((next: FrameRequestCallback) => { callback = next; return 1; });
    vi.stubGlobal("requestAnimationFrame", request);
    vi.stubGlobal("cancelAnimationFrame", vi.fn());
    const animation = { animationName: "den-composer-activity-spin", pause: vi.fn(), play: vi.fn(), cancel: vi.fn(), currentTime: 0 };
    const element = document.createElement("span");
    let animations = [animation as unknown as Animation];
    element.getAnimations = () => animations;
    const stop = driveActivitySpinner(element);
    callback?.(100);
    animations = [];
    reduced.mockReturnValue(true);
    media.dispatchEvent(new Event("change"));
    expect(animation.play).not.toHaveBeenCalled();
    expect(animation.cancel).toHaveBeenCalledOnce();
    const requests = request.mock.calls.length;
    window.dispatchEvent(new Event("focus"));
    expect(request).toHaveBeenCalledTimes(requests);
    reduced.mockReturnValue(false);
    animations = [animation as unknown as Animation];
    media.dispatchEvent(new Event("change"));
    expect(request).toHaveBeenCalledTimes(requests + 1);
    stop();
  });

  it("keeps reduced motion static", () => {
    reduced.mockReturnValue(true);
    const request = vi.fn();
    vi.stubGlobal("requestAnimationFrame", request);
    const element = document.createElement("span");
    element.getAnimations = () => [];
    const stop = driveActivitySpinner(element);
    expect(request).not.toHaveBeenCalled();
    stop();
  });
});
