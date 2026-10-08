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
    const animation = { animationName: "den-composer-activity-spin", pause: vi.fn(), play: vi.fn(), currentTime: 0 };
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
