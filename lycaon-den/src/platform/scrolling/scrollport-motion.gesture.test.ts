// @vitest-environment jsdom
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { flushScrollportFrameForTests } from "./scrollport-frame.ts";
import {
  bindScrollportNativeInput,
  unbindScrollportMotion,
} from "./scrollport-motion.ts";
import {
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

describe("ScrollportMotion layout reads under direct input", () => {
  it("commits a thumb offset within a measured range without reading the tail", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ clientHeight: 300, initialScrollHeight: 1_000, initialScrollTop: 100 });
    const box = vi.spyOn(fixture.viewport, "getBoundingClientRect");
    fixture.motion.beginThumbGesture();
    fixture.resetScrollHeightReads();

    fixture.motion.commit(500, "thumb_drag", { measuredMaxOffset: 700 });

    expect(fixture.viewport.scrollTop).toBe(500);
    expect(fixture.scrollHeightReads()).toBe(0);
    expect(box).not.toHaveBeenCalled();
    fixture.motion.endThumbGesture();
  });

  it("takes no layout reads for wheel input while nothing holds range", async () => {
    const { flush } = stubAnimationFrames();
    const fixture = createScrollportFixture({ clientHeight: 300, initialScrollHeight: 1_000, initialScrollTop: 100 });
    fixture.resetScrollHeightReads();

    fixture.motion.noteNativeInput("wheel");
    // The reclaim frame follows a microtask.
    await Promise.resolve();
    flush(4, 0);

    expect(fixture.scrollHeightReads()).toBe(0);
  });

  it("keeps one settle timer and one quiet timer through a wheel stream", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    stubAnimationFrames();
    const { motion, host } = createScrollportFixture();
    const settled = vi.fn();
    const stop = motion.subscribeInputSettled(settled);
    try {
      for (let n = 0; n < 30; n += 1) {
        motion.noteNativeInput("wheel");
        vi.advanceTimersByTime(16);
      }
      expect(vi.getTimerCount()).toBe(2);
      expect(settled).not.toHaveBeenCalled();
      vi.advanceTimersByTime(600);
      expect(settled).toHaveBeenCalledOnce();
      expect(vi.getTimerCount()).toBe(0);
    } finally { stop(); unbindScrollportMotion(host); vi.useRealTimers(); }
  });
});

describe("ScrollportMotion press anchor", () => {
  /** A shell at `documentTop` in the scrolled content. */
  function shellAt(fixture: ReturnType<typeof createScrollportFixture>, documentTop: () => number) {
    const shell = document.createElement("details");
    const press = document.createElement("summary");
    shell.appendChild(press);
    fixture.viewport.querySelector("section")!.prepend(shell);
    // A press anchors only to controls that are in the document.
    if (!fixture.host.isConnected) {
      document.body.append(fixture.host);
      onTestFinished(() => fixture.host.remove());
    }
    const rect = () => ({ top: documentTop() - fixture.viewport.scrollTop }) as DOMRect;
    shell.getBoundingClientRect = rect;
    press.getBoundingClientRect = rect;
    return { shell, press };
  }

  function pointerClick(target: Element): void {
    target.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 1 }));
  }

  it("keeps a collapsing card under the pointer at the tail", () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 1325, initialScrollHeight: 2000 });
    const pinned = vi.fn(() => true);
    fixture.motion.setTailPin(pinned);
    const { shell, press } = shellAt(fixture, () => 1500);

    pointerClick(press);
    const change = fixture.motion.declareHeightChange(shell);
    fixture.setNaturalScrollHeight(1700, { notify: false });
    frames.flush(1, 16);

    expect(fixture.viewport.scrollTop).toBe(1325);
    expect(extentHoldPx(fixture.viewport)).toBe(300);

    change.end();
    frames.flush(1, 32);
    // The pin waits while the pointer rests over the held view.
    fixture.motion.notifyLayoutMutated();
    expect(fixture.viewport.scrollTop).toBe(1325);
    unbindScrollportMotion(fixture.host);
  });

  // WebKit can land a collapse's contraction in the layout after every frame callback, so a
  // repair from the next frame is one painted frame too late. Nothing may run between the layout
  // and the paint here: the offset must already hold when the layout lands.
  it.each([
    ["in one step", [1700]],
    ["frame by frame", [1950, 1850, 1760, 1710, 1700]],
  ])("a pressed collapse at the tail never paints a clamped offset, landing %s", (_how, heights) => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 1325, initialScrollHeight: 2000 });
    fixture.motion.setTailPin(() => true);
    const { shell, press } = shellAt(fixture, () => 1500);

    pointerClick(press);
    const change = fixture.motion.declareHeightChange(shell, { contraction: 300 });
    for (const height of heights) {
      fixture.setNaturalScrollHeight(height, { notify: false });
      expect(fixture.viewport.scrollTop, `layout at ${height}`).toBe(1325);
    }
    change.end();
    expect(fixture.viewport.scrollTop).toBe(1325);
    unbindScrollportMotion(fixture.host);
  });

  it("reserves nothing for a collapse the reader did not press", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 1325, initialScrollHeight: 2000 });
    const { shell } = shellAt(fixture, () => 1500);
    fixture.motion.declareHeightChange(shell, { contraction: 300 }).end();
    expect(extentHoldPx(fixture.viewport)).toBe(0);
    unbindScrollportMotion(fixture.host);
  });

  it("carries the offset when a sibling above the pressed control collapses", () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 400 });
    let siblingHeight = 200;
    const { shell: target, press } = shellAt(fixture, () => 800 + siblingHeight);
    const sibling = document.createElement("details");
    target.before(sibling);

    pointerClick(press);
    const closing = fixture.motion.declareHeightChange(sibling);
    const opening = fixture.motion.declareHeightChange(target);
    siblingHeight = 50;
    frames.flush(1, 16);

    expect(fixture.viewport.scrollTop).toBe(250);
    closing.end();
    opening.end();
    frames.flush(1, 32);
    // Nothing past the tail remains to hold.
    expect(fixture.viewport.scrollTop).toBe(250);
    unbindScrollportMotion(fixture.host);
  });

  it("leaves keyboard toggles to the tail policy", () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 1325 });
    const { shell, press } = shellAt(fixture, () => 1500);

    press.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail: 0 }));
    const change = fixture.motion.declareHeightChange(shell);
    fixture.setNaturalScrollHeight(1700, { notify: false });
    frames.flush(1, 16);
    change.end();

    expect(fixture.viewport.scrollTop).toBe(1025);
    unbindScrollportMotion(fixture.host);
  });

  it("returns the held range by gliding once the click sequence can no longer continue", async () => {
    const fixture = createScrollportFixture({ initialScrollTop: 1325 });
    const { shell, press } = shellAt(fixture, () => 1500);
    pointerClick(press);
    const change = fixture.motion.declareHeightChange(shell);
    fixture.setNaturalScrollHeight(1700, { notify: false });
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    change.end();
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    // A quick second press still finds the control where it was.
    expect(fixture.viewport.scrollTop).toBe(1325);

    await new Promise<void>((resolve) => setTimeout(resolve, 520));
    await flushRetainedExtentReclaim();
    const visited = new Set<number>();
    for (let frame = 0; frame < 60 && fixture.viewport.scrollTop > 1025; frame += 1) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      visited.add(fixture.viewport.scrollTop);
    }
    expect(fixture.viewport.scrollTop).toBe(1025);
    // A glide passes through intermediate offsets instead of landing in one write.
    expect([...visited].some((offset) => offset > 1025 && offset < 1325)).toBe(true);
    expect(extentHoldPx(fixture.viewport)).toBe(0);
    unbindScrollportMotion(fixture.host);
  });

  it("ends when the reader scrolls or the application navigates", () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 1325 });
    const pinned = vi.fn(() => true);
    fixture.motion.setTailPin(pinned);
    const { shell, press } = shellAt(fixture, () => 1500);
    pointerClick(press);
    fixture.motion.declareHeightChange(shell).end();
    fixture.setNaturalScrollHeight(1700, { notify: false });
    frames.flush(1, 16);
    expect(fixture.viewport.scrollTop).toBe(1325);

    fixture.motion.commit(0, "jump");
    expect(fixture.viewport.scrollTop).toBe(0);
    unbindScrollportMotion(fixture.host);
  });
});

describe("ScrollportMotion direct input past the tail", () => {
  /** A reader left at 1325 when content below them contracted; the painted tail is 1025. */
  function heldPastTail() {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 1325, initialScrollHeight: 2000 });
    const stop = bindScrollportNativeInput(fixture.motion);
    fixture.motion.noteNativeInput("wheel");
    fixture.setNaturalScrollHeight(1700);
    expect(fixture.viewport.scrollTop).toBe(1325);
    expect(extentHoldPx(fixture.viewport)).toBeGreaterThan(0);
    /** Runs queued reclaim microtasks and the frames they schedule. */
    const settleFrames = async () => {
      for (let pass = 0; pass < 4; pass += 1) {
        await Promise.resolve();
        frames.flush(20, performance.now());
      }
    };
    return { fixture, stop, settleFrames };
  }

  it("refuses a wheel that would carry the reader further past the newest content", () => {
    const { fixture, stop } = heldPastTail();
    const down = wheel({ deltaY: 40 });
    fixture.viewport.dispatchEvent(down);
    expect(down.defaultPrevented).toBe(true);
    // Reading back toward the content is always free.
    const up = wheel({ deltaY: -40 });
    fixture.viewport.dispatchEvent(up);
    expect(up.defaultPrevented).toBe(false);
    stop();
    unbindScrollportMotion(fixture.host);
  });

  it("never grows range past the tail from the reader's own motion", () => {
    const { fixture, stop } = heldPastTail();
    const held = extentHoldPx(fixture.viewport);
    for (const offset of [1285, 1245, 1285, 1325]) {
      fixture.viewport.scrollTop = offset;
      fixture.motion.noteNativeInput("wheel");
      fixture.viewport.dispatchEvent(new Event("scroll"));
      expect(extentHoldPx(fixture.viewport), `at ${offset}`).toBeLessThanOrEqual(held);
    }
    stop();
    unbindScrollportMotion(fixture.host);
  });

  it("keeps held range through the gesture, then releases it so the tail is a hard end again", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    try {
      const { fixture, stop, settleFrames } = heldPastTail();
      const held = extentHoldPx(fixture.viewport);
      fixture.viewport.scrollTop = 900;
      fixture.motion.noteNativeInput("wheel");
      fixture.viewport.dispatchEvent(new Event("scroll"));
      await settleFrames();
      // Back on content, but the scrolling thread may still be applying deltas against this range.
      expect(extentHoldPx(fixture.viewport)).toBe(held);

      vi.advanceTimersByTime(700);
      await settleFrames();
      expect(extentHoldPx(fixture.viewport)).toBe(0);
      // With no held range, the native scroller itself ends at the newest content.
      expect(fixture.viewport.scrollHeight - 675).toBe(1025);
      const down = wheel({ deltaY: 40 });
      fixture.viewport.dispatchEvent(down);
      expect(down.defaultPrevented).toBe(false);
      stop();
      unbindScrollportMotion(fixture.host);
    } finally { vi.useRealTimers(); }
  });

  it("never contracts its own range under a live gesture, whatever content and offset do", async () => {
    // Range that grows and contracts at its end under WebKit's scrolling thread strands painting
    // and hit testing apart, so this scrollport's spacer only grows until input settles.
    let seed = 0x5eed;
    const random = () => (seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31;
    for (let run = 0; run < 20; run += 1) {
      const { fixture, stop, settleFrames } = heldPastTail();
      let held = extentHoldPx(fixture.viewport);
      for (let step = 0; step < 40; step += 1) {
        // Virtual rows near the end mount at estimates and then measure, so natural height flaps.
        fixture.setNaturalScrollHeight(1600 + Math.round(random() * 200));
        const max = fixture.viewport.scrollHeight - 675;
        fixture.viewport.scrollTop = Math.max(0, Math.min(max, fixture.viewport.scrollTop + Math.round((random() - 0.5) * 160)));
        fixture.motion.noteNativeInput("wheel");
        fixture.viewport.dispatchEvent(new Event("scroll"));
        fixture.motion.notifyLayoutMutated();
        await settleFrames();
        const now = extentHoldPx(fixture.viewport);
        expect(now, `run ${run} step ${step}`).toBeGreaterThanOrEqual(held);
        held = now;
      }
      stop();
      unbindScrollportMotion(fixture.host);
    }
  });

  it("leaves a wheel an inner scrollport claimed to that scrollport", () => {
    const { fixture, stop } = heldPastTail();
    const inner = createScrollportFixture({ initialScrollTop: 0, initialScrollHeight: 2000 });
    fixture.viewport.firstElementChild?.appendChild(inner.host);
    const stopInner = bindScrollportNativeInput(inner.motion);
    const down = wheel({ deltaY: 40 });
    inner.viewport.dispatchEvent(down);
    expect(down.defaultPrevented).toBe(false);
    stopInner();
    stop();
    unbindScrollportMotion(inner.host);
    unbindScrollportMotion(fixture.host);
  });

  it("never pulls an upward wheel back toward range it held", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 1325, initialScrollHeight: 2000 });
    fixture.motion.noteNativeInput("wheel");
    // Content contracts under the reader, and the clamp restores the held offset.
    fixture.setNaturalScrollHeight(1700);
    expect(fixture.viewport.scrollTop).toBe(1325);

    for (let step = 1; step <= 6; step += 1) {
      // The scrolling thread moves before the wheel and scroll events reach the page.
      const moved = 1325 - step * 40;
      fixture.viewport.scrollTop = moved;
      fixture.motion.noteNativeInput("wheel");
      fixture.viewport.dispatchEvent(new Event("scroll"));
      expect(fixture.viewport.scrollTop, `step ${step}`).toBe(moved);
    }
    unbindScrollportMotion(fixture.host);
  });
});

describe("ScrollportMotion under a live native stream", () => {
  /** A write while WebKit's scrolling thread applies a stream splits painting from hit testing. */
  it("writes no offset until the stream goes quiet, whatever layout and claims do", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    const frames = stubAnimationFrames();
    try {
      let seed = 0xd15c;
      const random = () => (seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31;
      for (let run = 0; run < 40; run += 1) {
        const fixture = createScrollportFixture({ initialScrollTop: 1325, initialScrollHeight: 2000 });
        const { motion, viewport } = fixture;
        const stop = bindScrollportNativeInput(motion);
        let pinned = random() < 0.5;
        motion.setTailPin(() => pinned);

        let top = viewport.scrollTop;
        let threadMoving = false;
        let lastInputAt = -Infinity;
        const violations: string[] = [];
        let step = 0;
        Object.defineProperty(viewport, "scrollTop", {
          configurable: true,
          get: () => top,
          set: (value: number) => {
            if (!threadMoving && performance.now() - lastInputAt < 150) {
              violations.push(`run ${run} step ${step}: wrote ${Math.round(value)} over ${Math.round(top)}`);
            }
            top = value;
          },
        });
        /** The scrolling thread moves first; the page hears the wheel and scroll afterwards. */
        const threadScroll = (delta: number) => {
          const max = Math.max(0, viewport.scrollHeight - 675);
          threadMoving = true;
          top = Math.max(0, Math.min(max, top + delta));
          threadMoving = false;
          lastInputAt = performance.now();
          motion.noteNativeInput(random() < 0.8 ? "wheel" : "touch");
          viewport.dispatchEvent(new Event("scroll"));
        };

        for (step = 0; step < 60; step += 1) {
          const roll = random();
          if (roll < 0.35) threadScroll(Math.round((random() - 0.3) * 120));
          else if (roll < 0.55) {
            // Rows at the end mount at estimates and measure; content streams in. A contraction's
            // clamp is the browser's own move.
            threadMoving = true;
            fixture.setNaturalScrollHeight(1500 + Math.round(random() * 800));
            threadMoving = false;
            motion.notifyLayoutMutated();
          } else if (roll < 0.65) motion.commit(viewport.scrollHeight, "repin_tail");
          else if (roll < 0.72) motion.reconcileTailBound();
          else if (roll < 0.77) motion.releaseTailRange();
          else if (roll < 0.82) pinned = !pinned;
          frames.flush(3, performance.now());
          vi.advanceTimersByTime(random() < 0.1 ? 700 : Math.round(random() * 40));
          frames.flush(3, performance.now());
        }
        expect(violations).toEqual([]);
        stop();
        unbindScrollportMotion(fixture.host);
      }
    } finally { vi.useRealTimers(); }
  });
});

describe("ScrollportMotion native input stopping at the range end", () => {
  /** Records every offset written to the viewport. */
  function recordOffsetWrites(viewport: HTMLElement): number[] {
    let top = viewport.scrollTop;
    const writes: number[] = [];
    Object.defineProperty(viewport, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (value: number) => { top = value; writes.push(value); },
    });
    return writes;
  }

  function wheelStream(fixture: ReturnType<typeof createScrollportFixture>, offsets: number[]) {
    for (const offset of offsets) {
      // The scrolling thread moves first; the page hears the wheel and scroll afterwards.
      fixture.viewport.scrollTop = offset;
      fixture.motion.noteNativeInput("wheel");
      fixture.viewport.dispatchEvent(new Event("scroll"));
      vi.advanceTimersByTime(16);
    }
  }

  it("re-seats the scrolling thread with one write away and back once the stream stops", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    stubAnimationFrames();
    try {
      const fixture = createScrollportFixture({ initialScrollTop: 1000, initialScrollHeight: 2000 });
      const writes = recordOffsetWrites(fixture.viewport);
      wheelStream(fixture, [1100, 1200, 1300, 1325, 1325, 1325]);
      writes.length = 0;
      // A stream still delivering events is the scrolling thread's; nothing writes under it.
      vi.advanceTimersByTime(100);
      expect(writes).toEqual([]);
      vi.advanceTimersByTime(60);
      // A same-value write is ignored, so only a move away and back makes WebKit re-seat it.
      expect(writes).toEqual([1324, 1325]);
      expect(fixture.viewport.scrollTop).toBe(1325);
      vi.advanceTimersByTime(1_000);
      expect(writes).toEqual([1324, 1325]);
    } finally { vi.useRealTimers(); }
  });

  it("writes nothing when the stream stops short of the range end", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    stubAnimationFrames();
    try {
      const fixture = createScrollportFixture({ initialScrollTop: 1000, initialScrollHeight: 2000 });
      const writes = recordOffsetWrites(fixture.viewport);
      wheelStream(fixture, [1100, 1200, 1325, 1250]);
      writes.length = 0;
      vi.advanceTimersByTime(1_000);
      expect(writes).toEqual([]);
    } finally { vi.useRealTimers(); }
  });

  it.each([
    { offsets: [1100, 1200, 1250], reseat: [1249, 1250] },
    { offsets: [600, 200, 0], reseat: [1, 0] },
  ])("re-seats the scrolling thread once, on the first pointer movement after a stream ($offsets)", ({ offsets, reseat }) => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    stubAnimationFrames();
    try {
      const fixture = createScrollportFixture({ initialScrollTop: 1000, initialScrollHeight: 2000 });
      const writes = recordOffsetWrites(fixture.viewport);
      wheelStream(fixture, offsets);
      vi.advanceTimersByTime(1_000);
      writes.length = 0;

      fixture.host.dispatchEvent(new Event("pointermove"));
      // The reseat waits for the frame's measure phase.
      expect(writes).toEqual([]);
      flushScrollportFrameForTests(fixture.viewport);
      expect(writes).toEqual(reseat);

      fixture.host.dispatchEvent(new Event("pointermove"));
      flushScrollportFrameForTests(fixture.viewport);
      expect(writes).toEqual(reseat);
    } finally { vi.useRealTimers(); }
  });

  it("writes nothing on a pointer movement once a new stream drives the offset", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    stubAnimationFrames();
    try {
      const fixture = createScrollportFixture({ initialScrollTop: 1000, initialScrollHeight: 2000 });
      const writes = recordOffsetWrites(fixture.viewport);
      wheelStream(fixture, [1100, 1200]);
      vi.advanceTimersByTime(1_000);
      // A stream that starts between the movement and its frame takes the offset back.
      fixture.host.dispatchEvent(new Event("pointermove"));
      wheelStream(fixture, [1150]);
      writes.length = 0;
      flushScrollportFrameForTests(fixture.viewport);
      // Movement during the stream finds nothing armed.
      fixture.host.dispatchEvent(new Event("pointermove"));
      flushScrollportFrameForTests(fixture.viewport);
      expect(writes).toEqual([]);
    } finally { vi.useRealTimers(); }
  });
});
