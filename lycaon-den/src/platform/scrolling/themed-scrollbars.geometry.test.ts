// @vitest-environment jsdom
import {
  construction,
  instance,
  makeFrame,
  giveVerticalBar,
} from "./themed-scrollbars-test-harness.ts";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import { OverlayScrollbars } from "overlayscrollbars";
import { DEN_SCROLL_DIRECTION_ATTR, attachThemedViewportScrollbar, resetScrollMeasureQuietForTests, setupThemedScrollbars, syncThemedScrollbar, updateThemedViewportScrollbar } from "./themed-scrollbars.ts";
import { scrollbarChrome } from "./scrollbar-chrome.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "./scrollport-motion-types.ts";
import { bindOverlayScrollbarAutoHide } from "./overlay-scrollbar-autohide.ts";
import { bindOverlayScrollbarInput } from "./overlay-scrollbar-input.ts";
import { resetSharedResizeObserverForTests } from "../../layout/shared-resize-observer.ts";

describe("themed scrollbars", () => {
  let stop: (() => void) | undefined;

  beforeEach(() => {
    vi.mocked(OverlayScrollbars).mockClear();
    vi.mocked(bindOverlayScrollbarInput).mockClear();
    vi.mocked(bindOverlayScrollbarAutoHide).mockClear();
    resetScrollMeasureQuietForTests();
  });

  afterEach(() => {
    stop?.();
    stop = undefined;
    document.body.replaceChildren();
    vi.useRealTimers();
  });

  it.each([
    { axis: "y" as const, overflow: { x: "hidden", y: "scroll" } },
    { axis: "x" as const, overflow: { x: "scroll", y: "hidden" } },
    { axis: "both" as const, overflow: { x: "scroll", y: "scroll" } },
  ])("honors a viewport's $axis scrolling policy", ({ axis, overflow }) => {
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.append(viewport);
    document.body.append(host);
    const detach = attachThemedViewportScrollbar(host, viewport, { axis });
    expect(construction(host)?.options.overflow).toEqual(overflow);
    detach();
  });

  it("watches only structure, and never probes a frame whose content churns", async () => {
    vi.useFakeTimers();
    let deliver: MutationCallback | undefined;
    const observe = vi.fn();
    vi.stubGlobal("MutationObserver", class {
      constructor(callback: MutationCallback) { deliver = callback; }
      observe(...args: unknown[]) { observe(...args); }
      disconnect() {}
    });
    try {
      const root = document.createElement("div");
      root.id = "root";
      const { frame, viewport, content } = makeFrame();
      root.append(frame);
      document.body.append(root);
      stop = setupThemedScrollbars();
      await vi.runAllTimersAsync();
      expect(observe.mock.calls[0]?.[1]).not.toHaveProperty("characterData");
      const scrollbar = instance(frame);
      const reads = vi.fn(() => 400);
      Object.defineProperties(viewport, {
        clientWidth: { get: reads }, clientHeight: { get: reads },
        scrollWidth: { get: reads }, scrollHeight: { get: reads },
      });
      scrollbar.update.mockClear();
      const row = document.createElement("div");
      content.append(row);
      deliver!([{ target: content, type: "childList", addedNodes: [row], removedNodes: [] } as unknown as MutationRecord], {} as MutationObserver);
      await vi.runAllTimersAsync();
      expect(reads).not.toHaveBeenCalled();
      expect(scrollbar.update).not.toHaveBeenCalled();
      expect(construction(frame)?.options.update).toMatchObject({ ignoreMutation: expect.any(Function) });
      const ignore = (construction(frame)?.options.update as { ignoreMutation: () => boolean }).ignoreMutation;
      expect(ignore()).toBe(true);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("reads no viewport geometry for a logical host whose chrome sits beside the viewport", () => {
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    const vertical = document.createElement("div");
    const horizontal = document.createElement("div");
    host.append(viewport, vertical, horizontal);
    document.body.append(host);
    // The attach reads the chrome elements, so the mocked instance exists first.
    const scrollbar = OverlayScrollbars({ target: host }, {}) as unknown as ReturnType<typeof instance>;
    (scrollbar as { elements: () => unknown }).elements = () => ({
      scrollOffsetElement: viewport,
      scrollbarVertical: { scrollbar: vertical },
      scrollbarHorizontal: { scrollbar: horizontal },
    });
    attachThemedViewportScrollbar(host, viewport, {
      vertical: {
        read: () => ({ offset: 260, viewport: 300, extent: 2_600 }),
        scrollTo: () => {},
      },
    });
    scrollbar.state.mockReturnValue({ overflowStyle: { x: "hidden", y: "scroll" }, hasOverflow: { x: false, y: true } });
    const reads = vi.fn(() => 0);
    Object.defineProperties(viewport, {
      clientWidth: { get: reads }, clientHeight: { get: reads }, scrollWidth: { get: reads },
      scrollHeight: { get: reads }, scrollLeft: { get: reads }, scrollTop: { get: reads },
    });
    expect(scrollbarChrome.read(host)).toEqual({
      verticalPercent: "0.1154",
      verticalPosition: String(260 / 2_300),
      horizontalPercent: "1",
      // The logical range places the handle too; no timeline runs in this document.
      handles: { x: "0", y: "0.113" },
    });
    expect(reads).not.toHaveBeenCalled();
  });

  it("re-arms one direct-input recheck timer through an input stream", () => {
    vi.useFakeTimers();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.append(viewport);
    document.body.append(host);
    const detach = attachThemedViewportScrollbar(host, viewport);
    for (let n = 0; n < 12; n += 1) {
      host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
      vi.advanceTimersByTime(16);
    }
    expect(vi.getTimerCount()).toBe(1);
    detach();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("places scroll-driven editor chrome inside the host's own measure pass", () => {
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.append(viewport);
    document.body.append(host);
    const schedule = vi.fn();
    attachThemedViewportScrollbar(host, viewport, { schedule });
    const bar = giveVerticalBar(host, viewport);
    Object.defineProperties(viewport, {
      clientHeight: { configurable: true, value: 200 },
      scrollHeight: { configurable: true, value: 800 },
      clientWidth: { configurable: true, value: 400 },
      scrollWidth: { configurable: true, value: 400 },
      scrollLeft: { configurable: true, value: 0 },
      scrollTop: { configurable: true, value: 300 },
    });
    const binding = vi.mocked(bindOverlayScrollbarInput).mock.results.at(-1)?.value as { refreshGeometry: ReturnType<typeof vi.fn> };

    viewport.dispatchEvent(new Event("scroll"));

    expect(schedule).toHaveBeenCalledOnce();
    const [read, write] = schedule.mock.calls[0] as [() => unknown, (geometry: unknown) => void];
    write(read());
    expect(bar.style.getPropertyValue("--os-viewport-percent")).toBe("0.25");
    expect(bar.style.getPropertyValue("--os-scroll-percent")).toBe("0.5");
    // The host's write phase re-applies no input.
    expect(bar.style.transform).toBe("");
    expect(binding.refreshGeometry).not.toHaveBeenCalled();
  });

  it("attaches a frame now without probing overflow and does no work when resynced", () => {
    const { frame } = makeFrame();
    document.body.appendChild(frame);

    syncThemedScrollbar(frame);
    syncThemedScrollbar(frame);

    expect(
      vi.mocked(OverlayScrollbars).mock.calls.filter((call) => call.length > 1),
    ).toHaveLength(1);
    expect(frame.getAttribute(DEN_SCROLL_DIRECTION_ATTR)).toBe("ltr");
    expect(instance(frame).update).not.toHaveBeenCalled();
    expect(bindOverlayScrollbarInput).toHaveBeenCalledTimes(1);
  });

  it("skips a forced update when the signed geometry has not moved", async () => {
    const { frame } = makeFrame();
    document.body.appendChild(frame);
    syncThemedScrollbar(frame);

    updateThemedViewportScrollbar(frame, "400x660:400x1250");
    await new Promise((resolve) => requestAnimationFrame(resolve));
    expect(instance(frame).update).toHaveBeenCalledTimes(1);

    updateThemedViewportScrollbar(frame, "400x660:400x1250");
    await new Promise((resolve) => requestAnimationFrame(resolve));
    expect(instance(frame).update).toHaveBeenCalledTimes(1);

    updateThemedViewportScrollbar(frame, "400x660:400x1310");
    await new Promise((resolve) => requestAnimationFrame(resolve));
    expect(instance(frame).update).toHaveBeenCalledTimes(2);
  });

  it("coalesces resize updates to one animation frame", () => {
    const { frame } = makeFrame();
    document.body.appendChild(frame);
    syncThemedScrollbar(frame);
    const frameOptions = construction(frame)?.options as {
      update?: { debounce?: { resize?: unknown } };
    };
    expect(frameOptions.update?.debounce?.resize).toBe(0);

    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    document.body.appendChild(host);
    attachThemedViewportScrollbar(host, viewport);
    const namedOptions = construction(host)?.options as { update?: { debounce?: { resize?: unknown } } };
    expect(namedOptions.update?.debounce?.resize).toBe(0);
  });

  it("refreshes, coalesces, and deduplicates viewport geometry updates", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    document.body.appendChild(host);
    attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);

    updateThemedViewportScrollbar(host, "100:200:300");
    updateThemedViewportScrollbar(host, "100:200:300");
    await vi.runAllTimersAsync();
    expect(scrollbar.update).toHaveBeenCalledTimes(1);
    expect(scrollbar.update).toHaveBeenCalledWith();

    updateThemedViewportScrollbar(host, "100:200:301");
    await vi.runAllTimersAsync();
    expect(scrollbar.update).toHaveBeenCalledTimes(2);
  });

  describe("extent observation", () => {
    let deliver: ((entries: ResizeObserverEntry[]) => void) | undefined;
    let observed: Element[] = [];

    beforeEach(() => {
      observed = [];
      vi.stubGlobal("ResizeObserver", class {
        constructor(callback: (entries: ResizeObserverEntry[]) => void) { deliver = callback; }
        observe(target: Element) { observed.push(target); }
        unobserve(target: Element) { observed = observed.filter((entry) => entry !== target); }
        disconnect() { observed = []; }
      });
      resetSharedResizeObserverForTests();
    });

    afterEach(() => {
      resetSharedResizeObserverForTests();
      vi.unstubAllGlobals();
    });

    const entry = (target: Element, blockSize: number, inlineSize = 264) =>
      ({ target, borderBoxSize: [{ blockSize, inlineSize }], contentRect: { width: inlineSize, height: blockSize } }) as unknown as ResizeObserverEntry;

    it("publishes geometry when the viewport or its extent element resizes", async () => {
      vi.useFakeTimers();
      const { frame, viewport, content } = makeFrame();
      document.body.append(frame);
      const detach = attachThemedViewportScrollbar(frame, viewport, { axis: "y", extent: content });
      expect(observed).toEqual([viewport, content]);
      const scrollbar = instance(frame);

      deliver!([entry(viewport, 640), entry(content, 912)]);
      await vi.runAllTimersAsync();
      expect(scrollbar.update).toHaveBeenCalledTimes(1);

      // A sub-pixel change is the same geometry.
      deliver!([entry(content, 912.2)]);
      await vi.runAllTimersAsync();
      expect(scrollbar.update).toHaveBeenCalledTimes(1);

      // Rows leaving shrink the extent to the viewport.
      deliver!([entry(content, 640)]);
      await vi.runAllTimersAsync();
      expect(scrollbar.update).toHaveBeenCalledTimes(2);

      detach();
      expect(observed).toEqual([]);
    });

    it("leaves a vertical scrollport's library alone through a width drag", async () => {
      vi.useFakeTimers();
      const { frame, viewport, content } = makeFrame();
      document.body.append(frame);
      attachThemedViewportScrollbar(frame, viewport, { axis: "y", extent: content });
      const scrollbar = instance(frame);
      deliver!([entry(viewport, 640, 264), entry(content, 912, 264)]);
      await vi.runAllTimersAsync();
      expect(scrollbar.update).toHaveBeenCalledTimes(1);

      for (const width of [270, 280, 290, 300]) {
        deliver!([entry(viewport, 640, width), entry(content, 912, width)]);
        await vi.runAllTimersAsync();
      }
      expect(scrollbar.update).toHaveBeenCalledTimes(1);
    });

    it("tracks a sideways extent's width", async () => {
      vi.useFakeTimers();
      const { frame, viewport, content } = makeFrame("x");
      document.body.append(frame);
      attachThemedViewportScrollbar(frame, viewport, { axis: "x", extent: content });
      const scrollbar = instance(frame);
      deliver!([entry(viewport, 40, 300), entry(content, 40, 900)]);
      await vi.runAllTimersAsync();
      deliver!([entry(content, 40, 1_200)]);
      await vi.runAllTimersAsync();
      expect(scrollbar.update).toHaveBeenCalledTimes(2);
    });

    it("observes each discovered frame's viewport and content", async () => {
      vi.useFakeTimers();
      const root = document.createElement("div");
      root.id = "root";
      const { frame, viewport, content } = makeFrame();
      root.append(frame);
      document.body.append(root);
      stop = setupThemedScrollbars();
      await vi.runAllTimersAsync();
      expect(observed).toEqual([viewport, content]);
      frame.remove();
      await vi.runAllTimersAsync();
      expect(observed).toEqual([]);
    });
  });

  it("replays a viewport update requested while its host was detached", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    root.appendChild(host);
    document.body.appendChild(root);
    stop = setupThemedScrollbars();
    attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);

    host.remove();
    updateThemedViewportScrollbar(host, "detached-change");
    await vi.runAllTimersAsync();
    expect(scrollbar.update).not.toHaveBeenCalled();

    root.appendChild(host);
    await vi.runAllTimersAsync();
    expect(scrollbar.update).toHaveBeenCalledOnce();
    expect(scrollbar.update).toHaveBeenCalledWith();
  });

  it("retains disconnected geometry after input settles until the host returns", async () => {
    vi.useFakeTimers();
    const root = document.createElement("div");
    root.id = "root";
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.append(viewport);
    root.append(host);
    document.body.append(root);
    stop = setupThemedScrollbars();
    const detach = attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);
    try {
      host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
      updateThemedViewportScrollbar(host, "before-detach");
      host.remove();
      await vi.runAllTimersAsync();
      expect(scrollbar.update).not.toHaveBeenCalled();
      updateThemedViewportScrollbar(host, "while-detached");
      root.append(host);
      await vi.runAllTimersAsync();
      expect(scrollbar.update).toHaveBeenCalledOnce();
    } finally {
      detach();
    }
  });

  it("cancels pending geometry and input timers when the reactive scope disposes", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.append(viewport);
    document.body.append(host);
    const detach = attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);
    updateThemedViewportScrollbar(host, "scheduled");
    host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
    updateThemedViewportScrollbar(host, "held");
    detach();
    await vi.runAllTimersAsync();
    expect(scrollbar.update).not.toHaveBeenCalled();
    expect(scrollbar.destroy).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });
});
