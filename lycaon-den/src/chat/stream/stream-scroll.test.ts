// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";

// Tests opt into resize callbacks when needed.
vi.hoisted(() => {
  class MockResizeObserver {
    observe = vi.fn();
    unobserve = vi.fn();
    disconnect = vi.fn();
  }
  globalThis.ResizeObserver =
    MockResizeObserver as unknown as typeof ResizeObserver;
});

vi.mock("overlayscrollbars", () => ({ OverlayScrollbars: () => undefined }));

import { applyStreamTabPanelInset, clearStreamTabPanelInset, commitStreamTail, isStreamNearBottom, readStreamTabPanelInset, setStreamTailPin, shiftStreamContent, streamScrollEdges, streamTabPanelOverlayFits, streamTailOffset, streamTranscriptEl, suppressStreamScrollFade, syncStreamScrollFade, syncStreamScrollbarLayout, watchStreamGeometry, watchStreamTabPanelRetract } from "./stream-scroll.ts";

import { watchStreamReaderIntent } from "./reader-intent/reader-intent.ts";

import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";

import { bindScrollportMotion, scrollportMotionForHost, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";

import { installStreamScrollCleanup, mockScrollContainer, insetFixture, paintedStream } from "./stream-scroll-test-fixture.ts";
installStreamScrollCleanup();

describe("stream viewport bindings", () => {
  it("commits and shifts the viewport inside a stationary scrollbar frame", () => {
    const stream = paintedStream({ scrollHeight: 1800, scrollTop: 0, endBottom: (top) => 1200 - top });
    unbindScrollportMotion(stream);
    const frame = document.createElement("div");
    document.body.append(frame);
    frame.append(stream);
    bindScrollportMotion(frame, stream, stream);
    try {
      expect(streamTailOffset(stream)).toBe(900);
      commitStreamTail(stream, "jump");
      expect(stream.scrollTop).toBe(900);
      expect(shiftStreamContent(stream, -100, stream.scrollTop)).toBe(-100);
      expect(stream.scrollTop).toBe(800);
      expect(frame.scrollTop).toBe(0);
    } finally {
      unbindScrollportMotion(frame);
    }
  });

  it("reinstalls the tail policy when the same viewport binds to a new frame", () => {
    const stream = paintedStream({ scrollHeight: 1800, scrollTop: 0, endBottom: (top) => 1200 - top });
    setStreamTailPin(stream, () => true);
    expect(streamTailOffset(stream)).toBe(900);
    unbindScrollportMotion(stream);
    const frame = document.createElement("div");
    document.body.append(frame);
    frame.append(stream);
    const motion = bindScrollportMotion(frame, stream, stream);
    try {
      expect(streamTailOffset(stream)).toBe(900);
      motion.notifyLayoutMutated();
      expect(stream.scrollTop).toBe(900);
      expect(frame.scrollTop).toBe(0);
    } finally {
      unbindScrollportMotion(frame);
    }
  });
});

describe("stream tail", () => {
  it("keeps the tail fixed when native offsets advance before scrolled client rects", () => {
    const stream = paintedStream({
      scrollHeight: 1_500,
      scrollTop: 900,
      endBottom: () => 280,
      paddingBottom: "20px",
    });
    const body = stream.querySelector<HTMLElement>(".den-chat-stream-body")!;
    vi.mocked(body.getBoundingClientRect).mockReturnValue({ top: -900 } as DOMRect);
    setStreamTailPin(stream, () => true);

    for (const nativeOffset of [954, 846, 1_100, 900]) {
      stream.scrollTop = nativeOffset;
      expect(streamTailOffset(stream)).toBe(900);
      scrollportMotionForHost(stream)!.notifyLayoutMutated();
      expect(stream.scrollTop).toBe(900);
    }
  });

  it("counts the virtual runway before the end sentinel as content", () => {
    const stream = paintedStream({ scrollHeight: 1_300, scrollTop: 900, endBottom: () => 400 });
    expect(streamTailOffset(stream)).toBe(1_000);
  });

  it("clamps a commit past a stale published range to the painted end", () => {
    const stream = paintedStream({
      scrollHeight: 1_300,
      scrollTop: 100,
      endBottom: (top) => 400 - top,
      paddingBottom: "20px",
    });
    expect(streamTailOffset(stream)).toBe(120);

    scrollportMotionForHost(stream)!.commit(900, "thumb_drag");

    expect(stream.scrollTop).toBe(120);
  });

  it("commits the live tail", () => {
    const el = mockScrollContainer(500, 200, 100);
    commitStreamTail(el, "jump");
    expect(el.scrollTop).toBe(400);
    expect(el.scrollTo).not.toHaveBeenCalled();
  });

  it("isStreamNearBottom when within threshold", () => {
    const el = mockScrollContainer(500, 430, 100);
    expect(isStreamNearBottom(el, 80)).toBe(true);
    el.scrollTop = 200;
    expect(isStreamNearBottom(el, 80)).toBe(false);
  });

  it("a transcript that fits is at the tail", () => {
    const el = mockScrollContainer(80, 0, 100);
    expect(isStreamNearBottom(el)).toBe(true);
  });
});

describe("stream geometry", () => {
  it("reports geometry inside the resize observation and reconciles the tab runway a frame later", async () => {
    let notify: ((entries?: ResizeObserverEntry[]) => void) | undefined;
    const observed: Element[] = [];
    const boxes: (ResizeObserverBoxOptions | undefined)[] = [];
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: ResizeObserverCallback) {
        notify = (entries = []) => callback(entries, this as unknown as ResizeObserver);
      }
      observe(el: Element, options?: ResizeObserverOptions) {
        observed.push(el);
        boxes.push(options?.box);
      }
      unobserve() {}
      disconnect() {}
    });
    const host = mockScrollContainer(80, 0, 100);
    const body = document.createElement("div");
    body.className = "den-chat-stream-body";

    host.className = "den-chat-stream";
    host.append(body);

    unbindScrollportMotion(host);
    bindScrollportMotion(host, host, body);
    const onGeometryChanged = vi.fn();

    const stop = watchStreamGeometry(host, {
      tabOpen: () => true,
      retracted: () => false,
      panelHeightPx: () => 120,
      onGeometryChanged,
    });
    expect(observed).toEqual([host, body]);
    expect(boxes).toEqual([undefined, "border-box"]);

    notify?.();
    expect(onGeometryChanged).toHaveBeenCalledWith(false);
    const entry = (target: Element, y: number, height: number) => ({ target, contentRect: { y, height } }) as ResizeObserverEntry;
    notify?.([entry(host, 24, 100), entry(body, 0, 80)]);
    expect(onGeometryChanged).toHaveBeenLastCalledWith(true);
    notify?.([entry(body, 0, 140)]);
    expect(onGeometryChanged).toHaveBeenLastCalledWith(false);
    notify?.([entry(body, 120, 140)]);
    expect(onGeometryChanged).toHaveBeenLastCalledWith(true);
    expect(readStreamTabPanelInset(host).active).toBe(false);
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    expect(readStreamTabPanelInset(host)).toEqual({ active: true, insetPx: 120 });
    stop();
  });

  it("streamTranscriptEl prefers den-chat-stream-body over the stream itself", () => {
    const host = mockScrollContainer(500, 120, 100);
    const body = document.createElement("div");
    body.className = "den-chat-stream-body";

    host.className = "den-chat-stream";
    host.append(body);

    expect(streamTranscriptEl(host)).toBe(body);
    expect(streamTranscriptEl(document.createElement("div"))).toBeInstanceOf(
      HTMLDivElement,
    );
  });

  it("syncStreamScrollbarLayout is safe without a stream target", () => {
    const raf = vi.spyOn(globalThis, "requestAnimationFrame");
    expect(() => syncStreamScrollbarLayout(undefined)).not.toThrow();
    expect(raf).not.toHaveBeenCalled();
  });
});

describe("tab panel", () => {
  function microtaskFrames() {
    vi.useFakeTimers();
    vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
      queueMicrotask(() => cb(0));
      return 1;
    });
  }

  it("retracts exactly while an open tab is at the top", async () => {
    microtaskFrames();
    const host = mockScrollContainer(500, 0, 100);

    let retracted = false;
    const stop = watchStreamTabPanelRetract(host, {
      tabOpen: () => true,
      retracted: () => retracted,
      onRetractedChange: (next) => {
        retracted = next;
      },
    });

    host.dispatchEvent(new Event("scroll"));
    await vi.runAllTimersAsync();
    expect(retracted).toBe(true);

    host.scrollTop = 60;
    host.dispatchEvent(new Event("scroll"));
    await vi.runAllTimersAsync();
    expect(retracted).toBe(false);

    host.scrollTop = 0;
    host.dispatchEvent(new Event("scroll"));
    await vi.runAllTimersAsync();
    expect(retracted).toBe(true);

    stop();
  });

  it("expands as soon as the transcript is no longer at the top", async () => {
    microtaskFrames();
    const host = mockScrollContainer(120, 0, 100);
    let retracted = false;
    const stop = watchStreamTabPanelRetract(host, {
      tabOpen: () => true,
      retracted: () => retracted,
      onRetractedChange: (next) => {
        retracted = next;
      },
    });

    expect(retracted).toBe(true);

    host.scrollTop = 5;
    host.dispatchEvent(new Event("scroll"));
    await vi.runAllTimersAsync();
    expect(retracted).toBe(false);
    stop();
  });

  it("keeps a manual open expanded through layout scrolls until reader input", async () => {
    microtaskFrames();
    const host = mockScrollContainer(500, 0, 100);
    let manualOpen = true;
    let retracted = false;
    const stopReader = watchStreamReaderIntent(host, {
      following: () => false,
      stopFollowing: vi.fn(),
      resumeFollowing: vi.fn(),
      onReaderInput: () => { manualOpen = false; },
    });
    const stop = watchStreamTabPanelRetract(host, {
      tabOpen: () => true,
      retracted: () => retracted,
      manualOpen: () => manualOpen,
      onRetractedChange: (next) => { retracted = next; },
    });

    for (const top of [120, 0]) {
      host.scrollTop = top;
      host.dispatchEvent(new Event("scroll"));
      await vi.runAllTimersAsync();
      expect(manualOpen).toBe(true);
      expect(retracted).toBe(false);
    }

    host.dispatchEvent(new WheelEvent("wheel", { deltaY: 120 }));
    host.scrollTop = 120;
    host.dispatchEvent(new Event("scroll"));
    await vi.runAllTimersAsync();
    expect(manualOpen).toBe(false);
    expect(retracted).toBe(false);

    host.scrollTop = 0;
    host.dispatchEvent(new Event("scroll"));
    await vi.runAllTimersAsync();
    expect(retracted).toBe(true);
    stop();
    stopReader();
  });

  it("clears retraction when the tab closes", async () => {
    microtaskFrames();
    const host = mockScrollContainer(500, 0, 100);
    let retracted = false;
    let tabOpen = true;
    const stop = watchStreamTabPanelRetract(host, {
      tabOpen: () => tabOpen,
      retracted: () => retracted,
      onRetractedChange: (next) => {
        retracted = next;
      },
    });

    expect(retracted).toBe(true);

    tabOpen = false;
    host.dispatchEvent(new Event("scroll"));
    await vi.runAllTimersAsync();
    expect(retracted).toBe(false);
    stop();
  });

  it("projects tab retraction from native host scrolls and ignores nested scrolls", () => {
    const host = mockScrollContainer(500, 0, 100);
    const nested = mockScrollContainer(500, 0, 100);
    host.append(nested);
    let retracted = false;
    const stop = watchStreamTabPanelRetract(host, {
      tabOpen: () => true,
      retracted: () => retracted,
      onRetractedChange: (next) => { retracted = next; },
    });
    expect(retracted).toBe(true);
    host.scrollTop = 60;
    nested.dispatchEvent(new Event("scroll"));
    flushScrollportFrameForTests(host);
    expect(retracted).toBe(true);
    host.dispatchEvent(new Event("scroll"));
    flushScrollportFrameForTests(host);
    expect(retracted).toBe(false);
    stop();
  });

  it("adds top runway when an overlay panel opens over short content", () => {
    const host = mockScrollContainer(80, 0, 100);
    const body = document.createElement("div");
    body.className = "den-chat-stream-body";

    host.append(body);

    unbindScrollportMotion(host);
    bindScrollportMotion(host, host, body);

    applyStreamTabPanelInset(host, 120);

    expect(readStreamTabPanelInset(host)).toEqual({ active: true, insetPx: 120 });
    expect(body.style.paddingTop).toBe("120px");
    expect(host.scrollTo).not.toHaveBeenCalled();
  });

  it("adds the runway the transcript is short by, not a whole panel", () => {
    // The transcript clears all but 8px of the overlay by itself.
    const { host, body } = insetFixture({ contentHeight: 212, clientHeight: 100 });

    applyStreamTabPanelInset(host, 120);

    expect(readStreamTabPanelInset(host).insetPx).toBe(8);
    // The runway is measured without itself, so re-applying is a no-op.
    expect(host.scrollHeight).toBe(220);
    applyStreamTabPanelInset(host, 120);
    expect(readStreamTabPanelInset(host).insetPx).toBe(8);
    expect(body.style.paddingTop).toBe("8px");
  });

  it("does not flip a whole panel of range as the transcript crosses the threshold", () => {
    // Live re-measurement wobbles either side of the panel height.
    const fixture = insetFixture({ contentHeight: 221, clientHeight: 100 });
    const ranges: number[] = [];

    for (const contentHeight of [221, 219, 222, 218, 220, 221]) {
      fixture.setContentHeight(contentHeight);
      applyStreamTabPanelInset(fixture.host, 120);
      ranges.push(fixture.host.scrollHeight);
    }

    // Published range varies only with the measured height.
    expect(Math.max(...ranges) - Math.min(...ranges)).toBeLessThanOrEqual(4);
  });

  it("leaves a naturally scrollable transcript overlay-only", () => {
    const host = mockScrollContainer(500, 0, 100);
    const body = document.createElement("div");
    body.className = "den-chat-stream-body";

    host.append(body);

    unbindScrollportMotion(host);
    bindScrollportMotion(host, host, body);

    expect(streamTabPanelOverlayFits(host, 120)).toBe(true);
    applyStreamTabPanelInset(host, 120);

    expect(readStreamTabPanelInset(host).active).toBe(false);
    expect(host.scrollTo).not.toHaveBeenCalled();
  });

  it("clears the top runway when the overlay retracts", () => {
    const host = mockScrollContainer(200, 120, 100);
    const body = document.createElement("div");
    body.className = "den-chat-stream-body";
    body.style.paddingTop = "120px";
    body.setAttribute("data-tab-panel-inset", "120");

    host.append(body);

    unbindScrollportMotion(host);
    bindScrollportMotion(host, host, body);

    clearStreamTabPanelInset(host);

    expect(readStreamTabPanelInset(host).active).toBe(false);
    expect(host.scrollTop).toBe(0);
  });
});

describe("edge fades", () => {
  it("streamScrollEdges hides fades when no overflow", () => {
    const el = mockScrollContainer(80, 0, 100);
    expect(streamScrollEdges(el)).toEqual({ fadeTop: false, fadeBottom: false });
  });

  it("streamScrollEdges keeps the bottom soft-land on while overflowing", () => {
    const el = mockScrollContainer(500, 0, 100);
    expect(streamScrollEdges(el)).toEqual({ fadeTop: false, fadeBottom: true });
    el.scrollTop = 200;
    expect(streamScrollEdges(el)).toEqual({ fadeTop: true, fadeBottom: true });
    el.scrollTop = 400;
    expect(streamScrollEdges(el)).toEqual({ fadeTop: true, fadeBottom: true });
  });

  it("syncStreamScrollFade tracks the native host offset", () => {
    const host = mockScrollContainer(500, 200, 100);

    host.className = "den-chat-stream";
    const wrap = document.createElement("div");
    wrap.className = "den-chat-stream-wrap den-chat-stream-wrap--scrolling";
    wrap.append(host);

    syncStreamScrollFade(host);
    expect(wrap.hasAttribute("data-fade-top")).toBe(true);
    expect(wrap.hasAttribute("data-fade-bottom")).toBe(true);

    host.scrollTop = 0;
    syncStreamScrollFade(host);
    expect(wrap.hasAttribute("data-fade-top")).toBe(false);
    expect(wrap.hasAttribute("data-fade-bottom")).toBe(true);
  });

  it("suppressStreamScrollFade keeps data-fade-quiet until the quiet window elapses", () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const host = mockScrollContainer(500, 200, 100);

    host.className = "den-chat-stream";
    const wrap = document.createElement("div");
    wrap.className = "den-chat-stream-wrap";
    wrap.append(host);

    suppressStreamScrollFade(host, 400);
    expect(wrap.hasAttribute("data-fade-quiet")).toBe(true);
    syncStreamScrollFade(host);
    expect(wrap.hasAttribute("data-fade-quiet")).toBe(true);

    vi.setSystemTime(500);
    syncStreamScrollFade(host);
    expect(wrap.hasAttribute("data-fade-quiet")).toBe(false);
  });
});
