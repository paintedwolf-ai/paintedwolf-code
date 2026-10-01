import { vi } from "vitest";
import { mockScrollerMotion } from "../../test/scroll-mock.ts";
import {
  bindScrollportMotion as bindScrollportMotionImpl,
  unbindScrollportMotion,
} from "./scrollport-motion.ts";

const boundScrollportHosts = new Set<HTMLElement>();

/** Binds motion and remembers the host for `unbindTrackedScrollportMotion`. */
export function bindScrollportMotion(
  ...args: Parameters<typeof bindScrollportMotionImpl>
): ReturnType<typeof bindScrollportMotionImpl> {
  boundScrollportHosts.add(args[0]);
  return bindScrollportMotionImpl(...args);
}

/** Releases every host bound through this harness; call it from `afterEach`. */
export function unbindTrackedScrollportMotion(): void {
  for (const host of boundScrollportHosts) unbindScrollportMotion(host);
  boundScrollportHosts.clear();
}

export function stubAnimationFrames() {
  const frames: Array<{ id: number; cb: FrameRequestCallback }> = [];
  let frameId = 0;
  vi.stubGlobal(
    "requestAnimationFrame",
    vi.fn((callback: FrameRequestCallback) => {
      frameId += 1;
      frames.push({ id: frameId, cb: callback });
      return frameId;
    }),
  );
  vi.stubGlobal(
    "cancelAnimationFrame",
    vi.fn((id: number) => {
      const index = frames.findIndex((frame) => frame.id === id);
      if (index >= 0) frames.splice(index, 1);
    }),
  );
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  return {
    flush(count: number, timestamp: number) {
      for (let i = 0; i < count && frames.length > 0; i += 1) {
        frames.shift()?.cb(timestamp);
      }
    },
  };
}

export function createScrollportFixture(opts?: {
  clientHeight?: number;
  initialScrollTop?: number;
  initialScrollHeight?: number;
}) {
  const clientHeight = opts?.clientHeight ?? 675;
  let naturalScrollHeight = opts?.initialScrollHeight ?? 2000;
  let scrollHeightReads = 0;

  const host = document.createElement("div");
  host.className = "den-chat-stream";
  const inner = document.createElement("section");
  inner.className = "den-chat-stream-inner";
  const afterRunway = document.createElement("div");
  afterRunway.setAttribute("data-transcript-runway", "after");
  inner.appendChild(afterRunway);

  const viewport = document.createElement("div");
  viewport.appendChild(inner);
  host.appendChild(viewport);

  Object.defineProperties(viewport, {
    clientHeight: { value: clientHeight, configurable: true },
    clientWidth: { value: 400, configurable: true },
    scrollWidth: { value: 400, configurable: true },
    scrollTop: {
      value: opts?.initialScrollTop ?? 1200,
      writable: true,
      configurable: true,
    },
    scrollLeft: { value: 0, writable: true, configurable: true },
    scrollHeight: {
      get() {
        scrollHeightReads += 1;
        const hold = afterRunway.nextElementSibling;
        const holdPx =
          hold instanceof HTMLElement
            ? Number.parseFloat(hold.style.height) ||
              hold.offsetHeight ||
              0
            : 0;
        return naturalScrollHeight + holdPx;
      },
      configurable: true,
    },
  });
  mockScrollerMotion(viewport);
  Object.defineProperty(viewport.style, "scrollBehavior", {
    value: "auto",
    writable: true,
    configurable: true,
  });

  return {
    host,
    viewport,
    motion: bindScrollportMotion(host, viewport, inner),
    setNaturalScrollHeight(height: number, opts?: { notify?: boolean }) {
      naturalScrollHeight = height;
      // Match the browser's clamp on contraction, and the scroll it dispatches.
      const max = Math.max(0, naturalScrollHeight + extentHoldPx(viewport) - clientHeight);
      if (viewport.scrollTop <= max) return;
      viewport.scrollTop = max;
      if (opts?.notify !== false) viewport.dispatchEvent(new Event("scroll"));
    },
    /** Forced layout flushes taken through this viewport. */
    scrollHeightReads: () => scrollHeightReads,
    resetScrollHeightReads() {
      scrollHeightReads = 0;
    },
  };
}

/** Flushes settle, reclaim, and tail-confirmation frames, then any glide the viewport runs. */
export async function flushRetainedExtentReclaim(viewport?: HTMLElement): Promise<void> {
  await new Promise<void>((resolve) => queueMicrotask(resolve));
  for (let frame = 0; frame < 6; frame += 1) {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  }
  if (!viewport) return;
  let still = 0;
  for (let frame = 0; frame < 60 && still < 2; frame += 1) {
    const before = viewport.scrollTop;
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    still = viewport.scrollTop === before ? still + 1 : 0;
  }
}

export function extentHoldPx(viewport: HTMLElement): number {
  const runway = viewport.querySelector('[data-transcript-runway="after"]');
  const hold = runway?.nextElementSibling;
  if (!(hold instanceof HTMLElement)) return 0;
  return Number.parseFloat(hold.style.height || "0") || 0;
}

export function wheel(init: WheelEventInit): WheelEvent {
  return new WheelEvent("wheel", { bubbles: true, cancelable: true, ...init });
}
