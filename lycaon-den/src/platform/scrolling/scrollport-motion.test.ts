// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  DEN_SCROLLPORT_INPUT_EVENT,
  bindScrollportNativeInput,
  requireScrollportMotionForViewport,
  unbindScrollportMotion,
} from "./scrollport-motion.ts";
import {
  bindScrollportMotion,
  createScrollportFixture,
  extentHoldPx,
  flushRetainedExtentReclaim,
  stubAnimationFrames,
  unbindTrackedScrollportMotion,
  wheel,
} from "./scrollport-motion-test-harness.ts";

afterEach(() => {
  unbindTrackedScrollportMotion();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ScrollportMotion layout notification cost", () => {
  it("skips the scrollHeight read while nothing claims range past the content", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 0 });
    const held = Object.getOwnPropertyDescriptor(
      fixture.viewport,
      "scrollHeight",
    )!;
    let reads = 0;
    Object.defineProperty(fixture.viewport, "scrollHeight", {
      configurable: true,
      get() {
        reads += 1;
        return held.get!.call(this);
      },
    });
    // Idle mutation tracking takes no layout reads.
    fixture.motion.notifyLayoutMutated();
    fixture.motion.notifyLayoutMutated();
    expect(reads).toBe(0);
    unbindScrollportMotion(fixture.host);
  });
});

describe("ScrollportMotion native input", () => {
  it("claims direct input without consuming or moving the native scroll", () => {
    stubAnimationFrames();
    const { host, viewport, motion } = createScrollportFixture();
    const stop = bindScrollportNativeInput(motion);
    let claims = 0;
    host.addEventListener(DEN_SCROLLPORT_INPUT_EVENT, () => {
      claims += 1;
    });

    const event = wheel({ deltaY: 3 });
    viewport.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    expect(viewport.scrollTop).toBe(1200);
    expect(motion.isDirectInputActive()).toBe(true);
    expect(claims).toBe(1);
    stop();
  });

  it("lets the innermost scrollport claim a nested wheel", () => {
    stubAnimationFrames();
    const outer = createScrollportFixture();
    const nestedHost = document.createElement("div");
    const nestedViewport = document.createElement("div");
    nestedHost.appendChild(nestedViewport);
    outer.viewport.firstElementChild?.appendChild(nestedHost);
    const nested = bindScrollportMotion(nestedHost, nestedViewport, nestedViewport);
    const stopOuter = bindScrollportNativeInput(outer.motion);
    const stopNested = bindScrollportNativeInput(nested);

    nestedViewport.dispatchEvent(wheel({ deltaY: 40 }));

    expect(nested.isDirectInputActive()).toBe(true);
    expect(outer.motion.isDirectInputActive()).toBe(false);
    stopNested();
    stopOuter();
  });

  it.each([
    { deltaX: 0, deltaY: 40, railReceivesInput: false },
    { deltaX: 40, deltaY: 0, railReceivesInput: true },
  ])("routes a filmstrip wheel ($deltaX, $deltaY) to the scrolling axis", ({ deltaX, deltaY, railReceivesInput }) => {
    stubAnimationFrames();
    const outer = createScrollportFixture();
    const rail = document.createElement("div");
    rail.style.overflowY = "hidden";
    rail.style.overscrollBehaviorY = "auto";
    rail.style.overscrollBehaviorX = "none";
    outer.viewport.appendChild(rail);
    const nested = bindScrollportMotion(rail, rail, rail);
    const stopOuter = bindScrollportNativeInput(outer.motion);
    const stopNested = bindScrollportNativeInput(nested);

    const event = wheel({ deltaX, deltaY });
    rail.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    expect(nested.isDirectInputActive()).toBe(railReceivesInput);
    expect(outer.motion.isDirectInputActive()).toBe(!railReceivesInput);
    stopNested();
    stopOuter();
  });

  it("ignores a pinch, which zooms rather than scrolls", () => {
    stubAnimationFrames();
    const { viewport, motion } = createScrollportFixture();
    const stop = bindScrollportNativeInput(motion);

    viewport.dispatchEvent(wheel({ deltaY: 3, ctrlKey: true }));

    expect(motion.isDirectInputActive()).toBe(false);
    stop();
  });

  it("releases the claim when its settle window closes", () => {
    const now = vi.spyOn(performance, "now");
    now.mockReturnValue(1_000);
    const { motion } = createScrollportFixture();

    motion.noteNativeInput("wheel");
    expect(motion.isDirectInputActive()).toBe(true);

    now.mockReturnValue(1_601);
    expect(motion.isDirectInputActive()).toBe(false);
  });

  it("notifies settlement after the last input even when the offset never changes", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    stubAnimationFrames();
    const { motion, host } = createScrollportFixture();
    const settled = vi.fn();
    const stop = motion.subscribeInputSettled(settled);
    try {
      motion.noteNativeInput("wheel");
      vi.advanceTimersByTime(400);
      motion.noteNativeInput("wheel");
      vi.advanceTimersByTime(599);
      expect(settled).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1);
      expect(settled).toHaveBeenCalledOnce();
      motion.beginThumbGesture();
      vi.advanceTimersByTime(1_000);
      expect(settled).toHaveBeenCalledOnce();
      motion.endThumbGesture();
      vi.advanceTimersByTime(600);
      expect(settled).toHaveBeenCalledTimes(2);
      motion.noteNativeInput("wheel");
      stop();
      vi.advanceTimersByTime(600);
      expect(settled).toHaveBeenCalledTimes(2);
    } finally { stop(); unbindScrollportMotion(host); vi.useRealTimers(); }
  });

  it("releases the claim when input unbinds", () => {
    stubAnimationFrames();
    const { viewport, motion } = createScrollportFixture();
    const stop = bindScrollportNativeInput(motion);
    viewport.dispatchEvent(wheel({ deltaY: 3 }));

    stop();

    expect(motion.isDirectInputActive()).toBe(false);
  });

  it("keeps a scrolling reader's offset addressable while content contracts", () => {
    stubAnimationFrames();
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 650,
    });

    motion.noteNativeInput("wheel");
    setNaturalScrollHeight(800);
    motion.notifyLayoutMutated();

    expect(viewport.scrollTop).toBe(650);
    expect(extentHoldPx(viewport)).toBe(150);
  });
});

describe("ScrollportMotion extent", () => {
  it.each(["idle", "wheel", "restore"] as const)("does not reclaim a released tail until new %s intent", async (intent) => {
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 300, initialScrollHeight: 1_000, initialScrollTop: 650,
    });
    setNaturalScrollHeight(900);
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(650);
    await flushRetainedExtentReclaim(viewport);
    expect(viewport.scrollTop).toBe(600);
    if (intent === "wheel") motion.noteNativeInput("wheel");
    if (intent === "restore") motion.commit(600, "restore_anchor");
    const observed: number[] = [];
    for (const height of [800, 700]) {
      setNaturalScrollHeight(height);
      observed.push(viewport.scrollTop);
      motion.notifyLayoutMutated();
      observed.push(viewport.scrollTop);
    }
    expect(observed).toEqual(intent === "idle" ? [500, 500, 400, 400] : [600, 600, 600, 600]);
  });

  it("samples a moving tail once per frame until two samples agree", () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 300, initialScrollHeight: 1_000, initialScrollTop: 700,
    });
    let tail = 500;
    fixture.motion.setTailOffsetResolver(() => tail);
    fixture.resetScrollHeightReads();

    tail = 480;
    frames.flush(1, 16);
    expect(fixture.scrollHeightReads()).toBe(1);
    expect(fixture.viewport.scrollTop).toBe(700);

    frames.flush(1, 32);
    expect(fixture.viewport.scrollTop).toBe(480);
    unbindScrollportMotion(fixture.host);
  });

  it("allows an explicit disclosure collapse to contract without restoring the old maximum", () => {
    stubAnimationFrames();
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 300, initialScrollHeight: 1_000, initialScrollTop: 650,
    });
    motion.releaseTailRange();
    for (const height of [900, 800, 700]) {
      setNaturalScrollHeight(height);
      motion.notifyLayoutMutated();
      expect(viewport.scrollTop).toBe(height - 300);
      expect(extentHoldPx(viewport)).toBe(0);
    }
  });

  it("publishes range for layout compensation ahead of runway geometry", () => {
    const { viewport, motion } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 650,
    });

    motion.commit(750, "layout_compensation");

    expect(viewport.scrollTop).toBe(750);
    expect(viewport.scrollHeight - viewport.clientHeight).toBe(750);
    expect(extentHoldPx(viewport)).toBe(50);
  });

  it("relinquishes retained range for an explicit tail transition", () => {
    const { viewport, motion } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 650,
    });

    motion.commit(750, "layout_compensation");
    expect(extentHoldPx(viewport)).toBe(50);

    motion.commit(motion.tailOffsetY(), "repin_tail");
    expect(viewport.scrollTop).toBe(700);
    expect(extentHoldPx(viewport)).toBe(0);
  });

  it("tells extent subscribers each time the hold changes", () => {
    const { viewport, motion } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 650,
    });
    const changed = vi.fn();
    motion.subscribeExtent(changed);

    motion.commit(750, "layout_compensation");
    expect(extentHoldPx(viewport)).toBe(50);
    expect(changed).toHaveBeenCalledTimes(1);

    motion.commit(motion.tailOffsetY(), "repin_tail");
    expect(extentHoldPx(viewport)).toBe(0);
    expect(changed.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("contracts retained extent when a thumb drag ends", async () => {
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1000,
      initialScrollTop: 400,
    });
    motion.beginThumbGesture();
    setNaturalScrollHeight(700);
    motion.commit(500, "thumb_drag");
    expect(viewport.scrollTop).toBe(400);
    motion.endThumbGesture();
    await flushRetainedExtentReclaim();
    expect(extentHoldPx(viewport)).toBe(0);
    expect(viewport.scrollTop).toBe(400);

    motion.beginThumbGesture();
    motion.commit(350, "thumb_drag");
    motion.endThumbGesture();
    await flushRetainedExtentReclaim();
    expect(extentHoldPx(viewport)).toBe(0);
  });

  it("publishes retained extent in the bound content element", () => {
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    const decoy = document.createElement("div");
    const content = document.createElement("div");
    decoy.className = "den-chat-stream-inner";
    host.append(viewport);
    viewport.append(decoy, content);
    let naturalScrollHeight = 1000;
    // Block content adds the spacer's height to the range.
    const spacerPx = () =>
      Number.parseFloat(
        content.querySelector<HTMLElement>("[data-scrollport-extent-hold]")?.style.height ?? "",
      ) || 0;
    Object.defineProperties(viewport, {
      clientHeight: { value: 300, configurable: true },
      scrollHeight: {
        get: () => naturalScrollHeight + spacerPx(),
        configurable: true,
      },
      scrollTop: { value: 400, writable: true, configurable: true },
      scrollLeft: { value: 0, writable: true, configurable: true },
      scrollWidth: { value: 0, configurable: true },
      clientWidth: { value: 0, configurable: true },
    });
    Object.defineProperty(viewport.style, "scrollBehavior", {
      value: "auto",
      writable: true,
      configurable: true,
    });
    const motion = bindScrollportMotion(host, viewport, content);

    motion.beginThumbGesture();
    // Offset 400 requires retained range.
    naturalScrollHeight = 600;
    motion.notifyLayoutMutated();

    expect(content.querySelector("[data-scrollport-extent-hold]")).not.toBeNull();
    expect(decoy.childElementCount).toBe(0);
    unbindScrollportMotion(host);
  });

  it("holds only the range its offset needs through a layout shrink", async () => {
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 675,
      initialScrollHeight: 2000,
      initialScrollTop: 1200,
    });

    setNaturalScrollHeight(1500);
    motion.notifyLayoutMutated();

    // Retain only the range needed to keep offset 1200 addressable.
    expect(extentHoldPx(viewport)).toBe(375);
    expect(viewport.scrollHeight - viewport.clientHeight).toBe(viewport.scrollTop);

    viewport.scrollTop = 900;
    motion.notifyLayoutMutated();
    expect(extentHoldPx(viewport)).toBe(75);

    viewport.scrollTop = 800;
    motion.notifyLayoutMutated();
    expect(extentHoldPx(viewport)).toBe(0);

    await flushRetainedExtentReclaim();
    expect(extentHoldPx(viewport)).toBe(0);
  });

  it("keeps a canceled motion's offset addressable, then returns it to content", async () => {
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 700,
    });

    setNaturalScrollHeight(602);
    motion.notifyLayoutMutated();

    expect(extentHoldPx(viewport)).toBe(398);
    expect(viewport.scrollTop).toBe(700);

    // Cancellation preserves range for the pending restore.
    motion.cancelApplicationMotion();
    motion.commit(700, "restore_anchor");
    expect(viewport.scrollTop).toBe(700);
    expect(extentHoldPx(viewport)).toBe(398);

    // Reclaim removes unsupported blank extent.
    await flushRetainedExtentReclaim(viewport);
    expect(viewport.scrollTop).toBe(302);
    expect(extentHoldPx(viewport)).toBe(0);

  });

  it("reconciles a pinned tail immediately when the content contracts", async () => {
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 700,
    });
    motion.setTailPin(() => true);

    setNaturalScrollHeight(602);
    motion.notifyLayoutMutated();

    expect(extentHoldPx(viewport)).toBe(0);
    expect(viewport.scrollTop).toBe(302);

    await flushRetainedExtentReclaim();
    expect(viewport.scrollTop).toBe(302);
    expect(extentHoldPx(viewport)).toBe(0);
  });

  it("keeps a pinned tail within half a device pixel while the viewport eases", () => {
    const { viewport, motion } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 700,
    });
    vi.stubGlobal("devicePixelRatio", 2);
    let box = 300;
    Object.defineProperties(viewport, {
      clientHeight: { get: () => Math.round(box), configurable: true },
      offsetHeight: { get: () => Math.round(box), configurable: true },
    });
    viewport.getBoundingClientRect = () => ({ height: box }) as DOMRect;
    motion.setTailPin(() => true);

    // A dock easing open shrinks the viewport by fractions of a pixel per frame.
    for (const height of [299.6, 298.7, 297.35, 296.2]) {
      box = height;
      motion.notifyLayoutMutated();
      expect(Math.abs(viewport.scrollTop - (1_000 - height))).toBeLessThanOrEqual(0.25);
    }

    // Movement under half a device pixel is already where the platform can place it.
    box = 296.1;
    const settled = viewport.scrollTop;
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(settled);
  });

  it("absorbs incoming row growth while a layout extent hold is active", async () => {
    const { viewport, motion, setNaturalScrollHeight } = createScrollportFixture({
      clientHeight: 675,
      initialScrollHeight: 2000,
      initialScrollTop: 1200,
    });

    setNaturalScrollHeight(1500);
    motion.notifyLayoutMutated();
    expect(extentHoldPx(viewport)).toBe(375);

    setNaturalScrollHeight(1700);
    motion.notifyLayoutMutated();
    expect(extentHoldPx(viewport)).toBe(175);
    expect(viewport.scrollTop).toBe(1200);

    await flushRetainedExtentReclaim(viewport);
    // Reclaim removes the remaining blank extent.
    expect(extentHoldPx(viewport)).toBe(0);
    expect(viewport.scrollTop).toBe(1025);
  });
});

describe("ScrollportMotion extent spacer", () => {
  /** A scrollport whose content adds `spacerAdds(height)` for a spacer of that height. */
  function contentFixture(spacerAdds: (heightPx: number) => number) {
    let natural = 2_000;
    let top = 1_400;
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    const content = document.createElement("div");
    viewport.append(content);
    host.append(viewport);
    const spacer = () =>
      content.querySelector<HTMLElement>("[data-scrollport-extent-hold]");
    const spacerHeight = () =>
      Number.parseFloat(spacer()?.style.height ?? "") || 0;
    Object.defineProperties(viewport, {
      clientHeight: { value: 600, configurable: true },
      clientWidth: { value: 400, configurable: true },
      scrollWidth: { value: 400, configurable: true },
      scrollLeft: { value: 0, writable: true, configurable: true },
      scrollHeight: {
        get: () => natural + (spacer() ? spacerAdds(spacerHeight()) : 0),
        configurable: true,
      },
      scrollTop: {
        get: () => top,
        set: (value: number) => {
          top = value;
        },
        configurable: true,
      },
    });
    const motion = bindScrollportMotion(host, viewport, content);
    return {
      viewport,
      motion,
      spacerHeight,
      /** How far the reader's view reaches past real content. */
      overrun: () => Math.max(0, top + 600 - natural),
      contract(to: number) {
        natural = to;
        motion.notifyLayoutMutated();
      },
    };
  }

  it("never grows a hold its content does not add to the scroll range", () => {
    stubAnimationFrames();
    // A row flex scroller lays the spacer beside taller content.
    const f = contentFixture(() => 0);
    f.motion.noteNativeInput("wheel");

    for (const height of [1_950, 1_900, 1_800, 1_700, 1_600, 1_500]) {
      f.contract(height);
      expect(f.spacerHeight()).toBeLessThanOrEqual(f.overrun());
      expect(f.motion.contentHeight()).toBe(height);
    }
    expect(f.spacerHeight()).toBe(0);
    expect(f.motion.maxOffsetY()).toBe(1_400);
  });

  it("retains range through a spacer that adds its height plus a layout gap", () => {
    stubAnimationFrames();
    const f = contentFixture((height) => height + 8);
    f.motion.noteNativeInput("wheel");

    // The gap counts toward the held range, so the range ends at the held offset.
    f.contract(1_700);
    expect(f.spacerHeight()).toBe(292);
    expect(f.motion.contentHeight()).toBe(1_700);
    expect(f.viewport.scrollHeight - 600).toBe(1_400);

    f.contract(1_600);
    expect(f.spacerHeight()).toBe(392);
    expect(f.motion.contentHeight()).toBe(1_600);
    expect(f.viewport.scrollHeight - 600).toBe(1_400);
  });

  it("does not expand a content-sized viewport to preserve an old offset", () => {
    stubAnimationFrames();
    const f = contentFixture((height) => height);
    Object.defineProperty(f.viewport, "clientHeight", {
      configurable: true,
      get: () => 600 + f.spacerHeight(),
    });
    f.motion.noteNativeInput("wheel");

    for (const height of [1_700, 1_600, 1_500]) {
      f.contract(height);
      expect(f.spacerHeight()).toBe(0);
      expect(f.viewport.clientHeight).toBe(600);
      expect(f.motion.contentHeight()).toBe(height);
    }
  });
});

describe("ScrollportMotion content shift", () => {
  it.each([false, true])("counts a one-pixel native tail clamp only once during content compensation (input=%s)", (input) => {
    const fixture = createScrollportFixture({ initialScrollTop: 1_200, initialScrollHeight: 1_875 });
    const { motion, viewport } = fixture;
    if (input) motion.noteNativeInput("wheel");
    const fromOffset = viewport.scrollTop;
    fixture.setNaturalScrollHeight(1_874, { notify: false });
    expect(viewport.scrollTop).toBe(1_199);

    expect(motion.shiftContent(-0.75, fromOffset)).toBe(-0.75);
    expect(viewport.scrollTop).toBe(1_199.25);
  });

  it.each([0, -400])("carries the pre-layout offset when native contraction precedes a %s correction", (delta) => {
    const fixture = createScrollportFixture({ initialScrollTop: 1_200, initialScrollHeight: 4_000 });
    const { motion, viewport } = fixture;
    const fromOffset = viewport.scrollTop;
    // Contraction clamps the measured offset away before its scroll event is delivered.
    fixture.setNaturalScrollHeight(900 + 675, { notify: false });
    expect(viewport.scrollTop).toBe(900);
    expect(motion.shiftContent(delta, fromOffset)).toBe(delta);
    expect(viewport.scrollTop).toBe(1_200 + delta);
    expect(motion.offsetY()).toBe(1_200 + delta);
  });

  it("restores the pre-layout offset when native contraction clamps scrollTop even if scrollHeight recovers before shift", () => {
    const fixture = createScrollportFixture({ initialScrollTop: 1_750, initialScrollHeight: 4_000 });
    const { motion, viewport } = fixture;
    const fromOffset = viewport.scrollTop;
    // Transient contraction clamps scrollTop down to 1180.
    fixture.setNaturalScrollHeight(1_180 + 675, { notify: false });
    expect(viewport.scrollTop).toBe(1_180);
    // Before shiftContent is committed, runway or surrounding layout expands scrollHeight back up.
    fixture.setNaturalScrollHeight(4_000, { notify: false });
    expect(viewport.scrollTop).toBe(1_180);
    expect(motion.shiftContent(0, fromOffset)).toBe(0);
    expect(viewport.scrollTop).toBe(1_750);
    expect(motion.offsetY()).toBe(1_750);
  });

  it("keeps travel a scrolling thread made after the caller read its offset", () => {
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 1_200, initialScrollHeight: 8_000 });
    const writes: string[] = [];
    // The scrolling thread holds the live position; the main thread reads the one it last saw.
    let live = 1_200;
    let seen = 1_200;
    Object.defineProperties(viewport, {
      scrollTop: {
        configurable: true,
        get: () => seen,
        set: (value: number) => { live = value; seen = value; writes.push(`set ${value}`); },
      },
      scrollBy: {
        configurable: true,
        value: (_x: number, y: number) => { live += y; seen = live; writes.push(`by ${y}`); },
      },
    });
    // The gesture travels 200 px the main thread has not observed yet.
    live = 1_400;

    expect(motion.shiftContent(120, 1_200)).toBe(120);

    expect(writes).toEqual(["by 120"]);
    expect(live).toBe(1_520);
  });

  it.each(["wheel", "touch", "thumb"] as const)("discards old fractional debt when the reader moves by %s", (input) => {
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 500 });
    let offset = viewport.scrollTop;
    Object.defineProperty(viewport, "scrollTop", {
      configurable: true,
      get: () => offset,
      set: (value: number) => { offset = Math.round(value); },
    });
    motion.shiftContent(0.4, viewport.scrollTop);
    if (input === "thumb") motion.beginThumbGesture();
    else motion.noteNativeInput(input);
    viewport.scrollTop = 800;
    if (input === "thumb") motion.endThumbGesture();
    motion.shiftContent(0.4, viewport.scrollTop);
    expect(offset).toBe(800);
  });

  it.each([-0.125, -0.4, 0.125, 0.4])("carries fractional movement through quantized native offsets (%s)", (delta) => {
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 500 });
    let offset = viewport.scrollTop;
    Object.defineProperty(viewport, "scrollTop", {
      configurable: true,
      get: () => offset,
      set: (value: number) => { offset = Math.round(value); },
    });
    let applied = 0;
    for (let index = 0; index < 40; index += 1) applied += motion.shiftContent(delta, viewport.scrollTop);
    expect(Math.abs(applied - delta * 40)).toBeLessThanOrEqual(0.5);
    expect(offset).toBe(500 + applied);
  });

  it("discards fractional movement when the reader jumps", () => {
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 500 });
    let offset = viewport.scrollTop;
    Object.defineProperty(viewport, "scrollTop", {
      configurable: true,
      get: () => offset,
      set: (value: number) => { offset = Math.round(value); },
    });
    motion.shiftContent(0.4, viewport.scrollTop);
    motion.commit(800, "jump");
    motion.shiftContent(0.4, viewport.scrollTop);
    expect(offset).toBe(800);
  });

  it("moves the offset with the content without ending direct input", () => {
    stubAnimationFrames();
    const { viewport, motion } = createScrollportFixture({
      initialScrollTop: 1_200,
      initialScrollHeight: 4_000,
    });

    motion.noteNativeInput("wheel");
    expect(motion.shiftContent(400, viewport.scrollTop)).toBe(400);

    expect(viewport.scrollTop).toBe(1_600);
    expect(motion.isDirectInputActive()).toBe(true);
  });

  it("publishes range for a shift ahead of runway geometry", () => {
    const { viewport, motion } = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 650,
    });

    expect(motion.shiftContent(400, viewport.scrollTop)).toBe(400);

    expect(viewport.scrollTop).toBe(1_050);
    expect(extentHoldPx(viewport)).toBe(350);
  });

  it("leaves the offset to an active thumb drag", () => {
    const { viewport, motion } = createScrollportFixture({
      initialScrollTop: 2_175,
      initialScrollHeight: 8_000,
    });

    motion.beginThumbGesture();
    expect(motion.shiftContent(-1_340, viewport.scrollTop)).toBe(0);
    expect(viewport.scrollTop).toBe(2_175);

    motion.endThumbGesture();
    expect(motion.shiftContent(-1_340, viewport.scrollTop)).toBe(-1_340);
    expect(viewport.scrollTop).toBe(835);
  });

  it("keeps a pinned viewport on the tail instead of carrying a shift past it", () => {
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { viewport, motion } = fixture;
    motion.setTailPin(() => true);

    // A row above grew by 300 and the pin already reached the new tail.
    fixture.setNaturalScrollHeight(2_300);
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(1_700);

    expect(motion.shiftContent(300, viewport.scrollTop)).toBe(0);
    expect(viewport.scrollTop).toBe(1_700);
    expect(extentHoldPx(viewport)).toBe(0);

    unbindScrollportMotion(fixture.host);
  });

  it("reports the shift the top of the range allowed", () => {
    const { viewport, motion } = createScrollportFixture({ initialScrollTop: 100 });

    expect(motion.shiftContent(-300, viewport.scrollTop)).toBe(-100);
    expect(viewport.scrollTop).toBe(0);
  });
});

describe("scrollport bindings", () => {
  it("rejects a viewport shared by multiple hosts", () => {
    const firstHost = document.createElement("div");
    const secondHost = document.createElement("div");
    const viewport = document.createElement("div");

    bindScrollportMotion(firstHost, viewport, viewport);

    expect(() => bindScrollportMotion(secondHost, viewport, viewport)).toThrow(
      "A scroll viewport can belong to only one host.",
    );
  });

  it("removes the old viewport binding when a host changes viewport", () => {
    const host = document.createElement("div");
    const firstViewport = document.createElement("div");
    const secondViewport = document.createElement("div");

    bindScrollportMotion(host, firstViewport, firstViewport);
    const current = bindScrollportMotion(host, secondViewport, secondViewport);

    expect(() => requireScrollportMotionForViewport(firstViewport)).toThrow();
    expect(requireScrollportMotionForViewport(secondViewport)).toBe(current);
  });
});

describe("ScrollportMotion retained extent sizing", () => {
  it("sizes the spacer past an extent that fills a short viewport", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ clientHeight: 675, initialScrollTop: 0, initialScrollHeight: 500 });
    // The extent fills the viewport, so a spacer adds range only past that fill.
    Object.defineProperty(fixture.viewport, "scrollHeight", {
      configurable: true,
      get: () => Math.max(675, 500 + extentHoldPx(fixture.viewport)),
    });

    fixture.motion.commit(100, "layout_compensation");

    expect(fixture.viewport.scrollTop).toBe(100);
    expect(fixture.viewport.scrollHeight - 675).toBe(100);
    // Retention follows the claim down and stays available for the next one.
    fixture.motion.commit(40, "layout_compensation");
    expect(fixture.viewport.scrollHeight - 675).toBe(40);
    fixture.motion.commit(160, "layout_compensation");
    expect(fixture.viewport.scrollHeight - 675).toBe(160);
    expect(fixture.viewport.scrollTop).toBe(160);
    unbindScrollportMotion(fixture.host);
  });

  it("gives up retention only when growth never reaches the range", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ clientHeight: 675, initialScrollTop: 0, initialScrollHeight: 1000 });
    // A row-flex extent lays the spacer beside its content.
    Object.defineProperty(fixture.viewport, "scrollHeight", { configurable: true, get: () => 1000 });

    fixture.motion.commit(500, "layout_compensation");
    expect(extentHoldPx(fixture.viewport)).toBe(0);
    // The binding stops trying, so later claims add no spacer.
    fixture.motion.commit(450, "layout_compensation");
    expect(extentHoldPx(fixture.viewport)).toBe(0);
    unbindScrollportMotion(fixture.host);
  });
});

describe("ScrollportMotion landed commits", () => {
  it.each([0, 375.5])("reports the actual position when the browser clamps an absolute write to %s", limit => {
    stubAnimationFrames();
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 0 });
    let top = 0, left = 0;
    Object.defineProperties(viewport, {
      scrollTop: { configurable: true, get: () => top, set: (value: number) => { top = Math.min(limit, value); } },
      scrollLeft: { configurable: true, get: () => left, set: (value: number) => { left = Math.min(limit, value); } },
      scrollWidth: { configurable: true, value: 2_000 },
    });
    const committed = vi.fn();
    motion.subscribeCommits(committed);
    motion.commit(900, "reveal", { axis: "both" });
    expect(motion.offsetY()).toBe(limit);
    expect(committed).toHaveBeenLastCalledWith("reveal", { top: limit, left: limit });
  });
});
