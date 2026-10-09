import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import type { TurnClock } from "../../api/types.ts";
import {
  COMPOSER_ACTIVITY_LEAVING_ATTR,
  ComposerActivityIndicator,
} from "./ComposerActivityIndicator.tsx";

type FadeStub = Animation & { keyframes: Keyframe[]; timing: KeyframeAnimationOptions };

function stubFades() {
  const fades: FadeStub[] = [];
  Object.defineProperty(HTMLElement.prototype, "animate", {
    configurable: true,
    writable: true,
    value: vi.fn((keyframes: Keyframe[], timing: KeyframeAnimationOptions) => {
      const fade = { keyframes, timing, cancel: vi.fn(), onfinish: null } as unknown as FadeStub;
      fades.push(fade);
      return fade;
    }),
  });
  return fades;
}

function finish(animation: Animation) {
  animation.onfinish?.call(animation, new Event("finish") as AnimationPlaybackEvent);
}

/** Presence changes reconcile in a microtask, outside Solid's update batch. */
const settle = () => Promise.resolve();

function renderPresence(initial: boolean) {
  const [present, setPresent] = createSignal(initial);
  const mountedDuringResize: boolean[] = [];
  const resize = vi.fn((mutate: () => void) => {
    mutate();
    mountedDuringResize.push(screen.queryByTestId("thinking-indicator") !== null);
  });
  render(() => (
    <ComposerActivityIndicator present={present()} activityLabel="Reading files" resize={resize} />
  ));
  return {
    setPresent,
    resize,
    mountedDuringResize,
    line: () => screen.queryByTestId("thinking-indicator"),
  };
}

describe("ComposerActivityIndicator", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    delete (HTMLElement.prototype as { animate?: unknown }).animate;
  });

  it("releases spinner frames while its retained surface is idle", () => {
    const original = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "getAnimations");
    Object.defineProperty(HTMLElement.prototype, "getAnimations", { configurable: true, value: () => [] });
    const request = vi.fn(() => 71);
    const cancel = vi.fn();
    vi.stubGlobal("requestAnimationFrame", request);
    vi.stubGlobal("cancelAnimationFrame", cancel);
    try {
      const [presence, setPresence] = createSignal<ResidentPresence>("idle");
      const view = render(() => (
        <ResidentPresenceProvider presence={presence()}>
          <ComposerActivityIndicator activityLabel="Reading files" />
        </ResidentPresenceProvider>
      ));
      expect(request).not.toHaveBeenCalled();
      setPresence("active");
      expect(request).toHaveBeenCalledOnce();
      setPresence("idle");
      expect(cancel).toHaveBeenCalledWith(71);
      window.dispatchEvent(new Event("focus"));
      expect(request).toHaveBeenCalledOnce();
      setPresence("active");
      expect(request).toHaveBeenCalledTimes(2);
      view.unmount();
      expect(cancel).toHaveBeenCalledTimes(2);
    } finally {
      if (original) Object.defineProperty(HTMLElement.prototype, "getAnimations", original);
      else delete (HTMLElement.prototype as { getAnimations?: unknown }).getAnimations;
      vi.unstubAllGlobals();
    }
  });

  it("ticks, pauses, resumes, and resets from host clock edges", () => {
    vi.useFakeTimers();
    const start = Date.parse("2026-09-10T12:00:00.987Z");
    vi.setSystemTime(start);
    const [clock, setClock] = createSignal<TurnClock>({
      session_id: "root",
      active_ms: 0,
      work_ms: 0,
      running: true,
      running_at: new Date(start).toISOString(),
    });
    const view = render(() => <ComposerActivityIndicator turnClock={clock()} />);
    const elapsed = () => screen.getByTestId("thinking-elapsed").textContent;
    expect(elapsed()).toBe("0:00");
    vi.advanceTimersByTime(2500);
    expect(elapsed()).toBe("0:02");

    setClock({ session_id: "root", active_ms: 2500, work_ms: 2500, running: false });
    vi.advanceTimersByTime(60_000);
    expect(elapsed()).toBe("0:02");
    expect(vi.getTimerCount()).toBe(0);

    setClock({ ...clock(), running: true, running_at: new Date(Date.now()).toISOString() });
    vi.advanceTimersByTime(2000);
    expect(elapsed()).toBe("0:04");

    setClock({ ...clock(), active_ms: 0, running_at: new Date(Date.now()).toISOString() });
    expect(elapsed()).toBe("0:00");
    vi.advanceTimersByTime(2000);
    expect(elapsed()).toBe("0:02");
    expect(screen.getByTestId("thinking-elapsed").getAttribute("aria-hidden")).toBe("true");
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("resamples elapsed time when an active turn is reopened", () => {
    vi.useFakeTimers();
    const start = Date.parse("2026-09-10T12:00:00.987Z");
    vi.setSystemTime(start);
    const clock: TurnClock = {
      session_id: "root", active_ms: 1500, work_ms: 1500, running: true,
      running_at: new Date(start).toISOString(),
    };
    const view = render(() => <ComposerActivityIndicator turnClock={clock} />);
    view.unmount();
    vi.advanceTimersByTime(65_000);
    render(() => <ComposerActivityIndicator turnClock={clock} />);
    expect(screen.getByTestId("thinking-elapsed").textContent).toBe("1:06");
  });

  it("is a non-interactive named status line", () => {
    render(() => <ComposerActivityIndicator activityLabel="Planning" />);
    const status = screen.getByTestId("thinking-indicator");
    expect(status.tagName).toBe("P");
    expect(status.getAttribute("role")).toBe("status");
    expect(status.getAttribute("aria-live")).toBe("polite");
    expect(status.textContent).toBe("Planning");
    expect(status.hasAttribute("data-tip")).toBe(false);
    expect(status.classList.contains("den-composer-activity")).toBe(true);
    expect(screen.getByTestId("thinking-spinner")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByTestId("stop-coordinator")).toBeNull();
  });

  it("leaves the visible label as the accessible name", () => {
    render(() => <ComposerActivityIndicator activityLabel="Planning" />);
    // An aria-label on a live region replaces the text it exists to announce.
    expect(screen.getByTestId("thinking-indicator").hasAttribute("aria-label")).toBe(
      false,
    );
    expect(screen.getByTestId("thinking-spinner").getAttribute("aria-hidden")).toBe(
      "true",
    );
  });

  it("defaults activity copy to Thinking", () => {
    render(() => <ComposerActivityIndicator />);
    expect(screen.getByTestId("thinking-indicator").textContent).toBe("Thinking");
  });

  it("shows a line present at mount without motion", () => {
    const fades = stubFades();
    const { line, resize } = renderPresence(true);
    expect(line()?.textContent).toBe("Reading files");
    expect(resize).not.toHaveBeenCalled();
    expect(fades).toHaveLength(0);
  });

  it("mounts a new line inside the composer resize, then fades it in", async () => {
    const fades = stubFades();
    const { setPresent, resize, mountedDuringResize, line } = renderPresence(false);
    expect(line()).toBeNull();

    setPresent(true);
    await settle();

    expect(resize).toHaveBeenCalledOnce();
    // The composer measures with the line already in the DOM.
    expect(mountedDuringResize).toEqual([true]);
    expect(fades[0]!.keyframes.map((frame) => [frame.offset, frame.opacity])).toEqual([
      [0, "0"],
      [0.35, "0"],
      [1, "1"],
    ]);
  });

  it("leaves the flow through the composer resize, then unmounts after the fade", async () => {
    const fades = stubFades();
    const { setPresent, resize, line } = renderPresence(true);

    setPresent(false);
    await settle();

    expect(resize).toHaveBeenCalledOnce();
    expect(line()?.hasAttribute(COMPOSER_ACTIVITY_LEAVING_ATTR)).toBe(true);
    expect(fades[0]!.timing.fill).toBe("forwards");
    expect(fades[0]!.keyframes[1]?.offset).toBe(0.55);

    finish(fades[0]!);
    expect(line()).toBeNull();
  });

  it("returns a leaving line to its lane from its current pose", async () => {
    const fades = stubFades();
    const { setPresent, resize, line } = renderPresence(true);

    setPresent(false);
    await settle();
    const leaving = line();
    setPresent(true);
    await settle();

    expect(resize).toHaveBeenCalledTimes(2);
    expect(line()).toBe(leaving);
    expect(leaving?.hasAttribute(COMPOSER_ACTIVITY_LEAVING_ATTR)).toBe(false);
    expect(fades[0]!.cancel).toHaveBeenCalledOnce();
    const keyframes = fades[1]!.keyframes;
    expect(keyframes[keyframes.length - 1]?.opacity).toBe("1");

    // A cancelled exit preserves the line.
    finish(fades[0]!);
    expect(line()).toBe(leaving);
  });

  it("coalesces a flicker inside one task into no change", async () => {
    stubFades();
    const { setPresent, resize, line } = renderPresence(true);

    setPresent(false);
    setPresent(true);
    await settle();

    expect(resize).not.toHaveBeenCalled();
    expect(line()).not.toBeNull();
  });

  it("removes the line through the resize without animation support", async () => {
    const { setPresent, resize, line } = renderPresence(true);
    setPresent(false);
    await settle();
    expect(resize).toHaveBeenCalledOnce();
    expect(line()).toBeNull();
  });
});
