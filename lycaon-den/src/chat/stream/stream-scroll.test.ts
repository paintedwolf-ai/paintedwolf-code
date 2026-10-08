// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

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

import {
  applyStreamTabPanelInset,
  clearStreamTabPanelInset,
  commitStreamTail,
  glideStreamToTail,
  isStreamNearBottom,
  readStreamTabPanelInset,
  setStreamTailPin,
  shiftStreamContent,
  streamScrollEdges,
  streamTabPanelOverlayFits,
  streamTailOffset,
  streamTranscriptEl,
  suppressStreamScrollEngagement,
  suppressStreamScrollFade,
  syncStreamScrollFade,
  syncStreamScrollbarLayout,
  watchStreamGeometry,
  watchStreamReaderIntent,
  watchStreamTabPanelRetract,
} from "./stream-scroll.ts";
import { isStreamSpringScrolling } from "./stream-scroll-spring.ts";
import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";
import { mockScrollerMotion } from "../../test/scroll-mock.ts";
import {
  bindScrollportMotion,
  scrollportMotionForHost,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";
import {
  beginShellLayoutBusy,
  endShellLayoutBusy,
  resetShellLayoutBusyForTests,
} from "../../shell/shell-layout-busy.ts";

function mockScrollContainer(height: number, scrollTop: number, clientHeight: number) {
  const el = document.createElement("div");
  let top = scrollTop;
  Object.defineProperty(el, "scrollHeight", { get: () => height, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: clientHeight, configurable: true });
  Object.defineProperty(el, "scrollTop", {
    get: () => top,
    set: (value: number) => {
      top = value;
    },
    configurable: true,
  });
  mockScrollerMotion(el);
  // Tests assert on the absolute path while the scroller still moves.
  vi.spyOn(el, "scrollTo");
  bindScrollportMotion(el, el, el);
  return el;
}

/** A stream whose published range carries whatever top runway it is given. */
function insetFixture(opts: { contentHeight: number; clientHeight: number }) {
  let contentHeight = opts.contentHeight;
  const body = document.createElement("div");
  body.className = "den-chat-stream-body";
  const host = document.createElement("div");
  const insetPx = () => Number.parseFloat(body.style.paddingTop) || 0;
  Object.defineProperty(host, "scrollHeight", {
    get: () => contentHeight + insetPx(),
    configurable: true,
  });
  Object.defineProperty(host, "clientHeight", {
    value: opts.clientHeight,
    configurable: true,
  });
  Object.defineProperty(host, "scrollTop", {
    value: 0,
    writable: true,
    configurable: true,
  });
  mockScrollerMotion(host);
  vi.spyOn(host, "scrollTo");

  host.append(body);

  unbindScrollportMotion(host);
  bindScrollportMotion(host, host, body);
  return {
    host,
    body,
    setContentHeight(value: number) {
      contentHeight = value;
    },
  };
}

/** A transcript whose reader follows the latest message until told otherwise. */
function readerFixture(opts: {
  height: number;
  scrollTop: number;
  clientHeight: number;
  following?: boolean;
}) {
  const el = mockScrollContainer(opts.height, opts.scrollTop, opts.clientHeight);
  document.body.append(el);
  let following = opts.following ?? true;
  const handlers = {
    following: () => following,
    stopFollowing: vi.fn(() => {
      following = false;
    }),
    resumeFollowing: vi.fn(() => {
      following = true;
    }),
    onReaderInput: vi.fn(),
  };
  const stop = watchStreamReaderIntent(el, handlers);
  return {
    el,
    handlers,
    stop,
    scrollTo(top: number) {
      el.scrollTop = top;
      el.dispatchEvent(new Event("scroll"));
      flushScrollportFrameForTests(el);
    },
    wheel(deltaY: number) {
      el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY }));
    },
  };
}

function key(key: string, target: EventTarget = document.body, init: KeyboardEventInit = {}) {
  target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
}

/** A painted transcript: rows, the after-runway, and the end sentinel. */
function paintedStream(opts: {
  scrollHeight: number;
  scrollTop: number;
  endBottom: (scrollTop: number) => number;
  paddingBottom?: string;
}) {
  const stream = document.createElement("div");
  stream.className = "den-chat-stream";
  const body = document.createElement("div");
  body.className = "den-chat-stream-body";
  if (opts.paddingBottom) body.style.paddingBottom = opts.paddingBottom;
  const inner = document.createElement("div");
  inner.className = "den-chat-stream-inner";
  const after = document.createElement("div");
  after.dataset.transcriptRunway = "after";
  const transcriptEnd = document.createElement("div");
  transcriptEnd.dataset.transcriptEnd = "";
  inner.append(after, transcriptEnd);
  body.append(inner);
  stream.append(body);
  document.body.append(stream);
  let top = opts.scrollTop;
  Object.defineProperties(stream, {
    clientHeight: { value: 300 },
    scrollHeight: { value: opts.scrollHeight },
    scrollTop: {
      get: () => top,
      set: (value: number) => {
        top = value;
      },
    },
  });
  mockScrollerMotion(stream);
  vi.spyOn(stream, "getBoundingClientRect").mockReturnValue({ top: 0, bottom: 300 } as DOMRect);
  vi.spyOn(body, "getBoundingClientRect").mockImplementation(() => ({ top: -top } as DOMRect));
  vi.spyOn(transcriptEnd, "getBoundingClientRect").mockImplementation(() => {
    const bottom = opts.endBottom(top);
    return { top: bottom, bottom } as DOMRect;
  });
  bindScrollportMotion(stream, stream, stream);
  return stream;
}

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.getSelection()?.removeAllRanges();
  document.body.replaceChildren();
});

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

describe("the reader's own scrolling", () => {
  it.each(["PageDown", "Tab"])("a disclosure click supersedes deferred %s scrolling", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 300, clientHeight: 100 });
    key(input, f.el);
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.el.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    document.dispatchEvent(new MouseEvent("pointerup", { bubbles: true }));
    f.handlers.stopFollowing();
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(false);
    f.stop();
  });

  it.each(["keyboard", "wheel"])("a newer upward %s intent supersedes a deferred downward scroll", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 300, clientHeight: 100 });
    key("PageDown", f.el);
    f.el.scrollTop = 400;
    if (input === "keyboard") key("PageUp", f.el);
    else f.wheel(-40);
    expect(f.handlers.following()).toBe(false);
    // Native scroll events can arrive after the direction changes.
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(false);
    f.stop();
  });

  it("stops following when the reader wheels up a scrollable transcript", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.wheel(-40);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    f.stop();
  });

  it("ignores an upward wheel on a transcript that cannot scroll", () => {
    const f = readerFixture({ height: 80, scrollTop: 0, clientHeight: 100 });
    f.wheel(-40);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("never stops following on a downward wheel", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.wheel(40);
    f.scrollTo(400);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    f.stop();
  });

  it("reads upward motion without reader input as layout, not reading", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("returns a pinned tail that a layout clamp moved without reader input", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    setStreamTailPin(f.el, f.handlers.following);
    // WebKit clamps while laying out a newly inserted size container, then restores the range.
    f.scrollTo(246);
    expect(f.el.scrollTop).toBe(400);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    setStreamTailPin(f.el, null);
    f.stop();
  });

  it("leaves a layout clamp in place for a reader who is not following", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    setStreamTailPin(f.el, f.handlers.following);
    f.scrollTo(246);
    expect(f.el.scrollTop).toBe(246);
    setStreamTailPin(f.el, null);
    f.stop();
  });

  it("keeps following through a sideways swipe over wide content", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    f.el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaX: 60, deltaY: -4 }));
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("reads a layout move after Tab elsewhere as layout", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const composer = document.createElement("textarea");
    document.body.append(composer);
    key("Tab", composer);
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("reads a focus reveal after Tab into the transcript as reading", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const chip = document.createElement("button");
    f.el.append(chip);
    key("Tab");
    chip.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("leaves a key a transcript control handled to that control", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const menu = document.createElement("div");
    menu.tabIndex = 0;
    f.el.append(menu);
    menu.addEventListener("keydown", (event) => event.preventDefault());
    key("ArrowUp", menu);
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("stops following for an upward scroll after a scroll key, but not one typed in a field", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    const input = document.createElement("input");
    document.body.append(input);
    key("PageUp", input);
    f.scrollTo(800);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();

    key("PageUp");
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.scrollTo(700);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it.each(["Home", "PageUp"])("%s pauses follow when new content outgrows a cached empty range", (input) => {
    const f = readerFixture({ height: 80, scrollTop: 0, clientHeight: 100 });
    streamScrollEdges(f.el);
    Object.defineProperty(f.el, "scrollHeight", { value: 500, configurable: true });
    f.el.scrollTop = 400;
    key(input, f.el);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("stops following for an upward drag and forgets the pointer on release", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.scrollTo(850);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();

    f.handlers.resumeFollowing();
    document.dispatchEvent(new MouseEvent("pointerup", { bubbles: true }));
    f.scrollTo(800);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("resumes following when the reader scrolls back down to the tail", () => {
    const f = readerFixture({ height: 500, scrollTop: 200, clientHeight: 100, following: false });
    f.wheel(40);
    f.scrollTo(300);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();

    f.wheel(40);
    f.scrollTo(330);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("does not resume following for an application move to the tail", () => {
    const f = readerFixture({ height: 500, scrollTop: 200, clientHeight: 100, following: false });
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("stops following when the reader drags the scrollbar thumb up", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    const motion = scrollportMotionForHost(f.el)!;
    motion.input.beginThumbGesture();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    f.scrollTo(700);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    motion.input.endThumbGesture();
    f.stop();
  });

  it("resumes following when the scrollbar thumb returns to the tail", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 500, clientHeight: 100, following: false });
    const motion = scrollportMotionForHost(f.el)!;
    motion.input.beginThumbGesture();
    f.scrollTo(890);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    motion.input.endThumbGesture();
    f.stop();
  });

  it("reads a track click's landing, observed after the click, as reading", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 500, clientHeight: 100, following: false });
    const motion = scrollportMotionForHost(f.el)!;
    motion.input.beginThumbGesture();
    motion.input.endThumbGesture();
    f.scrollTo(900);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("leaves wheel input on the scrollport frame to the wheel's own direction", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 500, clientHeight: 100, following: false });
    scrollportMotionForHost(f.el)!.input.noteNativeInput("wheel");
    f.scrollTo(900);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it.each(["wheel", "End", "PageDown", "ArrowDown"])("resumes on %s at the tail without a scroll event", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    if (input === "wheel") f.wheel(40);
    else key(input, f.el);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("resumes on downward input before the conversation has overflow", () => {
    const f = readerFixture({ height: 80, scrollTop: 0, clientHeight: 100, following: false });
    f.wheel(40);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("resumes on End before new content can move the destination away", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100, following: false });
    key("PageDown", f.el);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    key("End", f.el);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it.each([true, false])("claims End without native scrolling when following=%s", (following) => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100, following });
    const event = new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true });
    f.el.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.handlers.stopFollowing();
    f.scrollTo(900);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    expect(f.handlers.following()).toBe(false);
    f.stop();
  });

  it("leaves following alone when scrolling an expanded tool output", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const output = mockScrollContainer(600, 200, 100);
    f.el.append(output);
    output.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: -40 }));
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();

    f.handlers.stopFollowing();
    output.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: 40 }));
    key("End", output);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    expect(f.handlers.onReaderInput).not.toHaveBeenCalled();
    f.stop();
  });

  it("does not read a content shift above the reader as the reader leaving", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 600, clientHeight: 100 });
    key("PageUp");
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.handlers.stopFollowing.mockClear();
    shiftStreamContent(f.el, -200, f.el.scrollTop);
    expect(f.el.scrollTop).toBe(400);
    f.el.dispatchEvent(new Event("scroll"));
    flushScrollportFrameForTests(f.el);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("stops following when the reader selects transcript text", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    const text = document.createElement("p");
    text.textContent = "Selectable transcript text";
    f.el.append(text);

    const caret = document.createRange();
    caret.setStart(text.firstChild!, 3);
    caret.collapse(true);
    document.getSelection()?.addRange(caret);
    document.dispatchEvent(new Event("selectionchange"));
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();

    document.getSelection()?.removeAllRanges();
    const range = document.createRange();
    range.selectNodeContents(text);
    document.getSelection()?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();

    // Existing selection survives the jump without stopping subsequent arrivals.
    f.handlers.resumeFollowing();
    document.dispatchEvent(new Event("selectionchange"));
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it.each(["wheel", "pointer", "keyboard"])("accepts %s input during attachment suppression", (input) => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    suppressStreamScrollEngagement(f.el, 1_000);
    if (input === "wheel") f.wheel(-40);
    if (input === "pointer") f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    if (input === "keyboard") key("PageUp");
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    expect(f.handlers.onReaderInput).toHaveBeenCalledOnce();
    if (input === "wheel") f.wheel(40);
    if (input === "keyboard") key("End");
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("does not turn attachment geometry changes into reader input", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100 });
    suppressStreamScrollEngagement(f.el, 1_000);
    f.scrollTo(300);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.onReaderInput).not.toHaveBeenCalled();
    f.stop();
  });

  it("keeps a jump glide alive when the reader clicks a card control", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100 });
    const button = document.createElement("button");
    f.el.append(button);
    void glideStreamToTail(f.el);
    button.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    button.click();
    document.dispatchEvent(new MouseEvent("pointerup", { bubbles: true }));
    expect(isStreamSpringScrolling(f.el)).toBe(true);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    f.wheel(-40);
    expect(isStreamSpringScrolling(f.el)).toBe(false);
    f.stop();
  });

  it("cancels a jump glide on upward wheel", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 100, clientHeight: 100, following: false });
    void glideStreamToTail(f.el);
    expect(isStreamSpringScrolling(f.el)).toBe(true);

    f.wheel(-40);

    expect(isStreamSpringScrolling(f.el)).toBe(false);
    f.stop();
  });

  it("does not release following or treat scroll drop as user input during unstable shell layout", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    beginShellLayoutBusy();
    try {
      f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
      f.scrollTo(0);
      expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
      expect(f.handlers.following()).toBe(true);
    } finally {
      endShellLayoutBusy();
      resetShellLayoutBusyForTests();
      f.stop();
    }
  });

  it("does not release following or treat scroll drop as user input when stream height collapses to 0", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 0 });
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.scrollTo(0);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(true);
    f.stop();
  });

  it("clicking interactive controls does not register reader input or release following on layout shift", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    const tab = document.createElement("div");
    tab.className = "tabs__tab";
    f.el.append(tab);
    tab.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    f.scrollTo(0);
    expect(f.handlers.stopFollowing).not.toHaveBeenCalled();
    expect(f.handlers.following()).toBe(true);
    f.stop();
  });

  function pointer(type: string, clientY: number, pointerType: string): MouseEvent {
    const event = new MouseEvent(type, { bubbles: true, clientY });
    Object.defineProperty(event, "pointerType", { value: pointerType });
    return event;
  }

  it("a mouse drag-selection toward the tail never resumes following", () => {
    const f = readerFixture({ height: 500, scrollTop: 380, clientHeight: 100, following: false });
    f.el.dispatchEvent(pointer("pointerdown", 100, "mouse"));
    document.dispatchEvent(pointer("pointermove", 160, "mouse"));
    f.scrollTo(400);
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    document.dispatchEvent(pointer("pointerup", 160, "mouse"));
    f.stop();
  });

  it("a press that has made a selection never resumes following", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    const text = document.createElement("p");
    text.textContent = "Selectable transcript text";
    f.el.append(text);
    f.el.dispatchEvent(pointer("pointerdown", 100, "touch"));
    const range = document.createRange();
    range.selectNodeContents(text);
    document.getSelection()?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    document.dispatchEvent(pointer("pointermove", 160, "touch"));
    expect(f.handlers.resumeFollowing).not.toHaveBeenCalled();
    f.stop();
  });

  it("a touch drag toward the tail still resumes following", () => {
    const f = readerFixture({ height: 500, scrollTop: 400, clientHeight: 100, following: false });
    f.el.dispatchEvent(pointer("pointerdown", 100, "touch"));
    document.dispatchEvent(pointer("pointermove", 160, "touch"));
    expect(f.handlers.resumeFollowing).toHaveBeenCalledOnce();
    f.stop();
  });

  it("vertical pointer drag beyond threshold registers reader input and releases following", () => {
    const f = readerFixture({ height: 1_000, scrollTop: 900, clientHeight: 100 });
    f.el.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, clientY: 200 }));
    document.dispatchEvent(new MouseEvent("pointermove", { bubbles: true, clientY: 190 }));
    f.scrollTo(850);
    expect(f.handlers.stopFollowing).toHaveBeenCalledOnce();
    f.stop();
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
