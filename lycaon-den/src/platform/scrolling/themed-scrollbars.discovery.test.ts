// @vitest-environment jsdom
import {
  instance,
  frameHtml,
  makeFrame,
  giveVerticalBar,
  type Frame,
} from "./themed-scrollbars-test-harness.ts";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import { attachThemedViewportScrollbar, beginScrollMeasureQuiet, resetScrollMeasureQuietForTests, setupThemedScrollbars, syncThemedScrollbar, updateThemedViewportScrollbar } from "./themed-scrollbars.ts";
import { wrapInScrollportFrame } from "./scrollport-frame-dom.ts";
import {
  scrollportMotionForHost,
} from "./scrollport-motion.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "./scrollport-motion-types.ts";

describe("discovered attach under a measurement hold", () => {
  let stop: (() => void) | undefined;

  afterEach(() => {
    stop?.();
    stop = undefined;
    resetScrollMeasureQuietForTests();
    document.body.innerHTML = "";
    vi.useRealTimers();
  });

  it("defers construction until the hold releases", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const scope = document.createElement("div");
    root.appendChild(scope);
    const release = beginScrollMeasureQuiet(scope);

    scope.innerHTML = frameHtml("x", "markdown-code-scroll");
    await vi.runAllTimersAsync();
    const codeFrame = scope.firstElementChild as HTMLElement;
    expect(instance(codeFrame)).toBeFalsy();

    release();
    await vi.runAllTimersAsync();
    expect(instance(codeFrame)).toBeTruthy();
  });

  it("drops a deferred frame that left the document", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const scope = document.createElement("div");
    root.appendChild(scope);
    const release = beginScrollMeasureQuiet(scope);
    scope.innerHTML = frameHtml("x", "markdown-code-scroll");
    await vi.runAllTimersAsync();
    const codeFrame = scope.firstElementChild as HTMLElement;
    codeFrame.remove();

    release();
    await vi.runAllTimersAsync();
    expect(instance(codeFrame)).toBeFalsy();
  });
});

describe("deferred frames attach on approach", () => {
  let stop: (() => void) | undefined;
  let observed: Array<{ target: Element; cb: IntersectionObserverCallback }> = [];

  beforeEach(() => {
    observed = [];
    class FakeIntersectionObserver {
      constructor(private readonly cb: IntersectionObserverCallback) {}
      observe(target: Element) {
        observed.push({ target, cb: this.cb });
      }
      unobserve(target: Element) {
        observed = observed.filter((row) => row.target !== target);
      }
      disconnect() {
        observed = [];
      }
    }
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
  });

  afterEach(() => {
    stop?.();
    stop = undefined;
    resetScrollMeasureQuietForTests();
    document.body.innerHTML = "";
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  function intersect(target: Element): void {
    const row = observed.find((entry) => entry.target === target);
    row?.cb(
      [{ target, isIntersecting: true } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    );
  }

  const deferredCodeFrame = () => {
    const holder = document.createElement("div");
    const pre = document.createElement("pre");
    pre.innerHTML = "<code>wide</code>";
    holder.append(pre);
    wrapInScrollportFrame(pre, { frameClass: "markdown-code-scroll", axis: "x", defer: true });
    return holder.innerHTML;
  };

  it("builds nothing for a code block until it comes into view", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    root.innerHTML = deferredCodeFrame();
    await vi.runAllTimersAsync();
    const codeFrame = root.firstElementChild as HTMLElement;
    expect(instance(codeFrame)).toBeFalsy();

    intersect(codeFrame);
    await vi.runAllTimersAsync();
    expect(instance(codeFrame)).toBeTruthy();
  });

  it("releases discovered instances and observations when stopped", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    root.innerHTML = `${frameHtml()}${deferredCodeFrame()}`;
    document.body.appendChild(root);
    stop = setupThemedScrollbars();
    await vi.runAllTimersAsync();

    const pane = root.firstElementChild as HTMLElement;
    const attached = instance(pane);
    expect(attached).toBeTruthy();
    expect(observed).toHaveLength(1);

    stop();
    stop = undefined;
    expect(attached.destroy).toHaveBeenCalledOnce();
    expect(observed).toHaveLength(0);
  });

  it("keeps frames without the defer mark eager", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    const { frame } = makeFrame("y", "den-search-results-scroll");
    root.appendChild(frame);
    await vi.runAllTimersAsync();

    expect(instance(frame)).toBeTruthy();
    expect(observed).toHaveLength(0);
  });

  it("stops watching a code block that leaves the document", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    document.body.appendChild(root);
    stop = setupThemedScrollbars();

    root.innerHTML = deferredCodeFrame();
    await vi.runAllTimersAsync();
    const codeFrame = root.firstElementChild as HTMLElement;
    expect(observed).toHaveLength(1);

    codeFrame.remove();
    await vi.runAllTimersAsync();
    expect(observed).toHaveLength(0);
  });

  describe("live scrollbar placement", () => {
    type Geometry = { clientHeight: number; scrollHeight: number; scrollTop?: number };

    function applyGeometry(element: HTMLElement, geometry: Geometry): void {
      let top = geometry.scrollTop ?? 0;
      Object.defineProperties(element, {
        clientHeight: { configurable: true, get: () => geometry.clientHeight },
        scrollHeight: { configurable: true, get: () => geometry.scrollHeight },
        clientWidth: { configurable: true, value: 400 },
        scrollWidth: { configurable: true, value: 400 },
        scrollLeft: { configurable: true, value: 0 },
        scrollTop: {
          configurable: true,
          get: () => top,
          set: (next: number) => {
            top = next;
          },
        },
      });
    }

    function attachedFrame(): Frame {
      const parts = makeFrame();
      document.body.append(parts.frame);
      syncThemedScrollbar(parts.frame);
      return parts;
    }

    it("places frame chrome from the live range once per scroll frame, never translating it", async () => {
      vi.useFakeTimers();
      const { frame, viewport } = attachedFrame();
      const geometry = { clientHeight: 200, scrollHeight: 800 };
      applyGeometry(viewport, geometry);
      const bar = giveVerticalBar(frame, viewport);

      viewport.scrollTop = 300;
      geometry.scrollHeight = 1_000;
      viewport.dispatchEvent(new Event("scroll"));
      viewport.scrollTop = 301;
      viewport.dispatchEvent(new Event("scroll"));
      await vi.advanceTimersByTimeAsync(20);

      expect(bar.parentElement).toBe(frame);
      expect(bar.style.transform).toBe("");
      expect(bar.style.getPropertyValue("--os-viewport-percent")).toBe("0.2");
      // The handle offset comes from the same frame read, never from the library's scroll listener.
      expect(bar.style.getPropertyValue("--os-scroll-percent")).toBe(String(Math.round((301 / 800) * 1e4) / 1e4));
    });

    it("sizes the thumb before the deferred library update when the range grows", () => {
      const host = document.createElement("div");
      const viewport = document.createElement("div");
      host.appendChild(viewport);
      document.body.appendChild(host);
      attachThemedViewportScrollbar(host, viewport);
      const geometry = { clientHeight: 300, scrollHeight: 1_200 };
      applyGeometry(viewport, geometry);
      const bar = giveVerticalBar(host, viewport);
      host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));

      geometry.scrollHeight = 2_400;
      updateThemedViewportScrollbar(host, "grown");

      expect(bar.style.getPropertyValue("--os-viewport-percent")).toBe("0.125");
      expect(instance(host).update).not.toHaveBeenCalled();
    });

    it("re-places chrome in the frame after the retained extent changes", async () => {
      vi.useFakeTimers();
      const { frame, viewport } = attachedFrame();
      const naturalHeight = 1_000;
      Object.defineProperties(viewport, {
        clientHeight: { configurable: true, value: 300 },
        clientWidth: { configurable: true, value: 400 },
        scrollWidth: { configurable: true, value: 400 },
        scrollLeft: { configurable: true, value: 0 },
        scrollTop: { configurable: true, writable: true, value: 650 },
        scrollHeight: {
          configurable: true,
          get: () => {
            const hold = viewport.querySelector<HTMLElement>("[data-scrollport-extent-hold]");
            return naturalHeight + (Number.parseFloat(hold?.style.height ?? "") || 0);
          },
        },
      });
      const bar = giveVerticalBar(frame, viewport);
      const motion = scrollportMotionForHost(frame);
      if (!motion) throw new Error("expected a scrollport motion");

      // A compensating commit holds 200px of range past the content.
      motion.commit(900, "layout_compensation");
      // Placement shares the frame's single measurement pass.
      await vi.advanceTimersByTimeAsync(20);

      expect(bar.style.getPropertyValue("--os-viewport-percent")).toBe("0.25");
    });

    it("leaves chrome alone while measurement is held", () => {
      const { frame, viewport } = attachedFrame();
      applyGeometry(viewport, { clientHeight: 200, scrollHeight: 800 });
      const bar = giveVerticalBar(frame, viewport);
      const release = beginScrollMeasureQuiet(frame);

      viewport.scrollTop = 300;
      viewport.dispatchEvent(new Event("scroll"));

      expect(bar.style.getPropertyValue("--os-scroll-percent")).toBe("");
      release();
    });
  });
});
