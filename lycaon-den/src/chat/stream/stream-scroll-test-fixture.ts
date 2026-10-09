// @vitest-environment jsdom
import { afterEach, vi } from "vitest";

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

import { watchStreamReaderIntent } from "./reader-intent/reader-intent.ts";

import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";
import { mockScrollerMotion } from "../../test/scroll-mock.ts";
import { bindScrollportMotion, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";

export function mockScrollContainer(height: number, scrollTop: number, clientHeight: number) {
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
export function insetFixture(opts: { contentHeight: number; clientHeight: number }) {
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
export function readerFixture(opts: {
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

export function key(key: string, target: EventTarget = document.body, init: KeyboardEventInit = {}) {
  target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
}

/** A painted transcript: rows, the after-runway, and the end sentinel. */
export function paintedStream(opts: {
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

export function installStreamScrollCleanup() { afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.getSelection()?.removeAllRanges();
  document.body.replaceChildren();
}); }
