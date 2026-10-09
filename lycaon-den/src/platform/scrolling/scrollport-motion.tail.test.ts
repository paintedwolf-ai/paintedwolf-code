// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  setScrollportTailPolicy,
  unbindScrollportMotion,
} from "./scrollport-motion.ts";
import {
  bindScrollportMotion,
  createScrollportFixture,
  extentHoldPx,
  stubAnimationFrames,
  unbindTrackedScrollportMotion,
} from "./scrollport-motion-test-harness.ts";

afterEach(() => {
  unbindTrackedScrollportMotion();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ScrollportMotion live tail", () => {
  it("resolves the tail from content, not the held-open extent", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion, viewport } = fixture;

    motion.input.noteNativeInput("wheel");
    // Active input retains the current viewport offset.
    fixture.setNaturalScrollHeight(1_805);
    motion.notifyLayoutMutated();

    expect(extentHoldPx(viewport)).toBeGreaterThan(0);
    // Raw geometry now overstates the bottom by the hold's height.
    expect(viewport.scrollHeight - viewport.clientHeight).toBe(1_400);
    expect(motion.extent.tailOffsetY()).toBe(1_205);

    unbindScrollportMotion(fixture.host);
  });

  it("never parks a tail commit in synthetic extent", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion, viewport } = fixture;

    fixture.setNaturalScrollHeight(1_805);
    motion.notifyLayoutMutated();

    // Tail commits exclude retained blank extent.
    motion.commit(viewport.scrollHeight - viewport.clientHeight, "jump");
    expect(viewport.scrollTop).toBe(1_205);

    motion.commit(9_999, "repin_tail");
    expect(viewport.scrollTop).toBe(1_205);

    unbindScrollportMotion(fixture.host);
  });

  it("pulls a pinned viewport onto the tail before the frame is laid out", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion, viewport } = fixture;
    motion.tail.setTailPin(() => true);

    // Streaming contracts the transcript with no layout transaction open.
    fixture.setNaturalScrollHeight(1_700);
    motion.notifyLayoutMutated();

    // Contraction reconciles before paint.
    expect(viewport.scrollTop).toBe(1_100);

    unbindScrollportMotion(fixture.host);
  });

  it("pulls a pinned viewport onto a grown tail before paint", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion, viewport } = fixture;
    motion.tail.setTailPin(() => true);

    fixture.setNaturalScrollHeight(2_600);
    motion.notifyLayoutMutated();

    expect(viewport.scrollTop).toBe(2_000);

    unbindScrollportMotion(fixture.host);
  });

  it("leaves an unpinned viewport where the reader put it", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion, viewport } = fixture;
    motion.tail.setTailPin(null);

    fixture.setNaturalScrollHeight(1_700);
    motion.notifyLayoutMutated();

    expect(viewport.scrollTop).toBe(1_400);

    unbindScrollportMotion(fixture.host);
  });

  it("reads the pin at each reconcile", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion, viewport } = fixture;
    let pinned = false;
    motion.tail.setTailPin(() => pinned);

    fixture.setNaturalScrollHeight(2_600);
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(1_400);

    pinned = true;
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(2_000);

    unbindScrollportMotion(fixture.host);
  });

  it("never repins under a live wheel stream, and lands the pin once it goes quiet", () => {
    // A write while WebKit's scrolling thread applies the stream strands painting from hit testing.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    stubAnimationFrames();
    try {
      const fixture = createScrollportFixture({
        clientHeight: 600,
        initialScrollHeight: 2_000,
        initialScrollTop: 1_400,
      });
      const { motion, viewport } = fixture;
      motion.tail.setTailPin(() => true);

      motion.input.noteNativeInput("wheel");
      fixture.setNaturalScrollHeight(2_600);
      motion.notifyLayoutMutated();
      motion.commit(2_000, "repin_tail");
      expect(viewport.scrollTop).toBe(1_400);

      vi.advanceTimersByTime(100);
      motion.input.noteNativeInput("wheel");
      vi.advanceTimersByTime(149);
      expect(viewport.scrollTop).toBe(1_400);

      vi.advanceTimersByTime(1);
      expect(viewport.scrollTop).toBe(2_000);
      unbindScrollportMotion(fixture.host);
    } finally { vi.useRealTimers(); }
  });

  it("leaves a pinned offset to a touch or thumb gesture until it ends", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion, viewport } = fixture;
    motion.tail.setTailPin(() => true);

    motion.input.noteNativeInput("touch");
    fixture.setNaturalScrollHeight(2_600);
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(1_400);

    motion.input.interruptDirectInput();
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(2_000);

    motion.input.beginThumbGesture();
    fixture.setNaturalScrollHeight(3_000);
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(2_000);

    motion.input.endThumbGesture();
    motion.notifyLayoutMutated();
    expect(viewport.scrollTop).toBe(2_400);

    unbindScrollportMotion(fixture.host);
  });

  it("keeps a host's tail policy when the host rebinds", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    setScrollportTailPolicy(fixture.host, {
      resolveTail: () => 900,
      pinned: () => true,
    });
    expect(fixture.motion.extent.tailOffsetY()).toBe(900);

    const content = document.createElement("div");
    fixture.viewport.append(content);
    const rebound = bindScrollportMotion(fixture.host, fixture.viewport, content);

    expect(rebound).not.toBe(fixture.motion);
    expect(rebound.extent.tailOffsetY()).toBe(900);
    rebound.notifyLayoutMutated();
    expect(fixture.viewport.scrollTop).toBe(900);
    setScrollportTailPolicy(fixture.host, null);
    expect(rebound.extent.tailOffsetY()).toBe(1_400);
  });

  it("applies a tail policy set before the host binds", () => {
    stubAnimationFrames();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.append(viewport);
    let top = 1_000;
    Object.defineProperties(viewport, {
      clientHeight: { value: 600 },
      clientWidth: { value: 400 },
      scrollWidth: { value: 400 },
      scrollHeight: { value: 2_000 },
      scrollTop: {
        get: () => top,
        set: (value: number) => {
          top = value;
        },
      },
    });
    setScrollportTailPolicy(host, { pinned: () => true });

    const motion = bindScrollportMotion(host, viewport, viewport);
    motion.notifyLayoutMutated();

    expect(viewport.scrollTop).toBe(1_400);
    setScrollportTailPolicy(host, null);
  });
});

describe("ScrollportMotion tail pin versus declared layout", () => {
  it("coalesces reactive notifications and reads the latest layout before paint", async () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({ clientHeight: 600, initialScrollHeight: 2000, initialScrollTop: 1400 });
    fixture.motion.tail.setTailPin(() => true);
    fixture.resetScrollHeightReads();
    const requests = vi.mocked(requestAnimationFrame);
    const before = requests.mock.calls.length;
    for (let i = 0; i < 10; i++) {
      fixture.setNaturalScrollHeight(2200 + i * 10);
      fixture.motion.scheduleLayoutReconcile();
    }
    expect(requests.mock.calls.length - before).toBe(1);
    expect(fixture.scrollHeightReads()).toBe(0);
    await Promise.resolve();
    fixture.setNaturalScrollHeight(2400);
    fixture.motion.scheduleLayoutReconcile();
    expect(fixture.scrollHeightReads()).toBe(0);
    expect(requests.mock.calls.length - before).toBe(1);
    frames.flush(1, 16);
    expect(fixture.viewport.scrollTop).toBe(1800);
    expect(fixture.scrollHeightReads()).toBe(1);
    unbindScrollportMotion(fixture.host);
  });

  it("lets a resize observation consume pending reconciliation without a second measurement", () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture({ initialScrollTop: 0 });
    fixture.motion.tail.setTailPin(() => true);
    fixture.motion.scheduleLayoutReconcile();
    fixture.motion.notifyLayoutMutated();
    fixture.resetScrollHeightReads();
    frames.flush(10, 16);
    expect(fixture.scrollHeightReads()).toBe(0);
  });

  it("cancels queued geometry work when the scrollport detaches", () => {
    const frames = stubAnimationFrames();
    const fixture = createScrollportFixture();
    fixture.motion.tail.setTailPin(() => true);
    fixture.motion.scheduleLayoutReconcile();
    unbindScrollportMotion(fixture.host);
    fixture.resetScrollHeightReads();
    frames.flush(10, 16);
    expect(fixture.scrollHeightReads()).toBe(0);
  });

  it("shares width and height within an operation and invalidates after writes", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ clientHeight: 300, initialScrollHeight: 1000, initialScrollTop: 0 });
    const clientHeight = vi.fn(() => 300);
    Object.defineProperty(fixture.viewport, "clientHeight", { get: clientHeight, configurable: true });
    fixture.resetScrollHeightReads();
    fixture.motion.extent.measureGeometry(() => {
      expect(fixture.motion.extent.maxOffsetY()).toBe(700);
      expect(fixture.motion.extent.maxOffsetY()).toBe(700);
    });
    expect(clientHeight).toHaveBeenCalledTimes(1);
    expect(fixture.scrollHeightReads()).toBe(1);
    fixture.setNaturalScrollHeight(1300);
    expect(fixture.motion.extent.maxOffsetY()).toBe(1000);
    expect(clientHeight).toHaveBeenCalledTimes(2);
    unbindScrollportMotion(fixture.host);
  });

  it("resolves the painted transcript tail once per operation", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({ clientHeight: 300, initialScrollHeight: 1000, initialScrollTop: 0 });
    const resolve = vi.fn(() => 650);
    fixture.motion.tail.setTailOffsetResolver(resolve);
    resolve.mockClear();
    fixture.motion.extent.measureGeometry(() => {
      expect(fixture.motion.extent.tailOffsetY()).toBe(650);
      expect(fixture.motion.extent.maxOffsetY()).toBe(650);
      expect(fixture.motion.extent.inputMaxOffsetY()).toBe(650);
    });
    expect(resolve).toHaveBeenCalledTimes(1);
    resolve.mockReturnValue(600);
    expect(fixture.motion.extent.tailOffsetY()).toBe(600);
    expect(resolve).toHaveBeenCalledTimes(2);
    unbindScrollportMotion(fixture.host);
  });

  it("takes one layout flush per layout notification", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion } = fixture;
    motion.tail.setTailPin(() => true);
    // Nested geometry reads share one measure pass.
    fixture.resetScrollHeightReads();
    motion.notifyLayoutMutated();

    expect(fixture.scrollHeightReads()).toBe(1);

    unbindScrollportMotion(fixture.host);
  });

  it("re-reads the natural height between operations", () => {
    stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 300,
      initialScrollHeight: 1_000,
      initialScrollTop: 400,
    });
    const { motion, viewport } = fixture;

    // Each operation receives fresh geometry.
    motion.input.beginThumbGesture();
    fixture.setNaturalScrollHeight(700);
    motion.commit(500, "thumb_drag");

    expect(viewport.scrollTop).toBe(400);

    unbindScrollportMotion(fixture.host);
  });

  it("schedules one reclaim frame however many callers ask", () => {
    const { flush } = stubAnimationFrames();
    const fixture = createScrollportFixture({
      clientHeight: 600,
      initialScrollHeight: 2_000,
      initialScrollTop: 1_400,
    });
    const { motion } = fixture;
    const requestFrame = vi.mocked(requestAnimationFrame);

    for (let i = 0; i < 10; i += 1) motion.input.interruptDirectInput();
    const scheduledBefore = requestFrame.mock.calls.length;
    return Promise.resolve().then(() => {
      expect(requestFrame.mock.calls.length - scheduledBefore).toBe(1);
      flush(1, 0);
      unbindScrollportMotion(fixture.host);
    });
  });
});


describe("ScrollportMotion quick reveal", () => {
  it("eases into and out of a 220 ms reveal", () => {
    const frames = stubAnimationFrames();
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 0 });
    const complete = vi.fn();
    void motion.revealOffset(900, { complete });
    frames.flush(1, 0);
    for (let time = 20; time <= 80; time += 20) frames.flush(1, time);
    expect(viewport.scrollTop).toBeGreaterThan(0);
    expect(viewport.scrollTop).toBeLessThan(900 * 80 / 220);
    expect(complete).not.toHaveBeenCalled();
    for (let time = 100; time <= 220; time += 20) frames.flush(1, time);
    expect(viewport.scrollTop).toBe(900);
    expect(complete).toHaveBeenCalledOnce();
  });

  it("does not skip the animation when file presentation delays a frame", () => {
    const frames = stubAnimationFrames();
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 0 });
    void motion.revealOffset(900);
    frames.flush(1, performance.now() + 500);
    expect(viewport.scrollTop).toBe(0);
    frames.flush(1, performance.now() + 1000);
    expect(viewport.scrollTop).toBeGreaterThan(0);
    expect(viewport.scrollTop).toBeLessThan(90);
  });

  it("uses the expanded tree range when its rows reach layout before the first frame", () => {
    const frames = stubAnimationFrames();
    const { motion, viewport, setNaturalScrollHeight } = createScrollportFixture({ initialScrollTop: 0, initialScrollHeight: 800 });
    void motion.revealOffset(900);
    setNaturalScrollHeight(2000);
    frames.flush(1, 0);
    for (let time = 20; time <= 220; time += 20) frames.flush(1, time);
    expect(viewport.scrollTop).toBe(900);
  });

  it("replaces an older reveal and stops for native input", () => {
    const frames = stubAnimationFrames();
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 0 });
    const abandoned = vi.fn();
    void motion.revealOffset(900, { complete: abandoned });
    void motion.revealOffset(400);
    frames.flush(1, 0);
    frames.flush(1, 20);
    const interrupted = viewport.scrollTop;
    motion.input.noteNativeInput("wheel");
    frames.flush(10, 200);
    expect(viewport.scrollTop).toBe(interrupted);
    expect(abandoned).not.toHaveBeenCalled();
  });

  it("does not resume an old animation when a commit retargets the reveal", () => {
    const frames = stubAnimationFrames();
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 0 });
    const abandoned = vi.fn();
    let retargeted = false;
    motion.subscribeCommits(() => {
      if (retargeted || viewport.scrollTop === 0) return;
      retargeted = true;
      void motion.revealOffset(400);
    });
    void motion.revealOffset(900, { complete: abandoned });
    for (let time = 0; time <= 500; time += 20) frames.flush(1, time);
    expect(viewport.scrollTop).toBe(400);
    expect(abandoned).not.toHaveBeenCalled();
  });

  it("cancels an older reveal when the new target is already visible", () => {
    const frames = stubAnimationFrames();
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 0 });
    void motion.revealOffset(900);
    void motion.revealOffset(0);
    frames.flush(10, 500);
    expect(viewport.scrollTop).toBe(0);
  });

  it("skips animation for reduced motion", () => {
    stubAnimationFrames();
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    const { motion, viewport } = createScrollportFixture({ initialScrollTop: 0 });
    const complete = vi.fn();
    void motion.revealOffset(900, { complete });
    expect(viewport.scrollTop).toBe(900);
    expect(complete).toHaveBeenCalledOnce();
  });
});

it("exposes captured and committed offsets without another layout read", () => {
  const { viewport, motion } = createScrollportFixture({ initialScrollTop: 800 });
  expect(motion.offsetY()).toBe(800);
  viewport.scrollTop = 900.25;
  viewport.dispatchEvent(new Event("scroll"));
  expect(motion.offsetY()).toBe(900.25);
  motion.commit(945.125, "reveal");
  expect(motion.offsetY()).toBe(945.125);
  let reads = 0;
  let top = viewport.scrollTop;
  Object.defineProperty(viewport, "scrollTop", {
    configurable: true,
    get: () => { reads++; return top; },
    set: (value: number) => { top = value; },
  });
  for (let n = 0; n < 100; n++) expect(motion.offsetY()).toBe(945.125);
  expect(reads).toBe(0);
});

it("reports a thumb gesture only while the thumb is held", () => {
  const el = document.createElement("div");
  const motion = bindScrollportMotion(el, el, el);
  expect(motion.input.isThumbGestureActive()).toBe(false);
  motion.input.beginThumbGesture();
  expect(motion.input.isThumbGestureActive()).toBe(true);
  motion.input.endThumbGesture();
  expect(motion.input.isThumbGestureActive()).toBe(false);
});
