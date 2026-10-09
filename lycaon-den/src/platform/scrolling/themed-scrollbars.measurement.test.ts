// @vitest-environment jsdom
import {
  instance,
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
import { attachThemedViewportScrollbar, beginScrollMeasureQuiet, isScrollMeasureQuietForTests, resetScrollMeasureQuietForTests, setupThemedScrollbars, updateThemedViewportScrollbar } from "./themed-scrollbars.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "./scrollport-motion.ts";
import { bindOverlayScrollbarAutoHide } from "./overlay-scrollbar-autohide.ts";
import { bindOverlayScrollbarInput } from "./overlay-scrollbar-input.ts";

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

  it("sleeps attached instances during coordinated measurement quiet", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    document.body.appendChild(host);
    attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);

    const release = beginScrollMeasureQuiet(host);
    updateThemedViewportScrollbar(host, "changed");
    await vi.runAllTimersAsync();
    expect(scrollbar.sleep).toHaveBeenCalledWith(true);
    expect(scrollbar.update).not.toHaveBeenCalled();

    release();
    expect(scrollbar.sleep).not.toHaveBeenCalledWith(false);
    const holdAgain = beginScrollMeasureQuiet(host);
    holdAgain();
    await vi.runAllTimersAsync();
    expect(scrollbar.sleep.mock.calls).toEqual([[true], [false]]);
    // Waking already refreshes geometry.
    expect(scrollbar.update).not.toHaveBeenCalled();
  });

  for (const quiet of [false, true]) {
    it(`reconnects during direct input without draining held work (quiet=${quiet})`, async () => {
      vi.useFakeTimers();
      let deliver: MutationCallback | undefined;
      vi.stubGlobal("MutationObserver", class {
        constructor(callback: MutationCallback) { deliver = callback; }
        observe() {}
        disconnect() {}
      });
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
      const release = quiet ? beginScrollMeasureQuiet(host) : () => {};
      host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
      updateThemedViewportScrollbar(host, "during-input");
      host.remove();
      root.append(host);

      // The iteration cap turns a synchronous loop into a test failure.
      const nativeAdd = Set.prototype.add;
      let requeues = 0;
      const guard = vi.spyOn(Set.prototype, "add").mockImplementation(function(this: Set<unknown>, value) {
        if (value === host && ++requeues > 20) throw new Error("Scrollbar observer did not yield");
        return nativeAdd.call(this, value);
      });
      try {
        const record = { target: root, addedNodes: [host], removedNodes: [] } as unknown as MutationRecord;
        expect(() => deliver!([record], {} as MutationObserver)).not.toThrow();
        expect(scrollbar.update).not.toHaveBeenCalled();
        await vi.advanceTimersByTimeAsync(100);
        expect(scrollbar.update).not.toHaveBeenCalled();
        await vi.runAllTimersAsync();
        expect(scrollbar.update).toHaveBeenCalledTimes(quiet ? 0 : 1);
        release();
        await vi.runAllTimersAsync();
        expect(scrollbar.update).toHaveBeenCalledTimes(quiet ? 0 : 1);
        if (quiet) expect(scrollbar.sleep).toHaveBeenLastCalledWith(false);
        expect(instance(host)).toBe(scrollbar);
      } finally {
        guard.mockRestore();
        release();
        detach();
        stop();
        stop = undefined;
        vi.unstubAllGlobals();
      }
    });
  }

  it("holds an update when measurement quiet starts before its frame", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    document.body.appendChild(host);
    attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);

    updateThemedViewportScrollbar(host, "changed");
    const release = beginScrollMeasureQuiet(host);
    await vi.runAllTimersAsync();
    expect(scrollbar.update).not.toHaveBeenCalled();

    release();
    await vi.runAllTimersAsync();
    expect(scrollbar.sleep).toHaveBeenLastCalledWith(false);
    expect(scrollbar.update).not.toHaveBeenCalled();
  });

  it("defers geometry updates until direct input and quiet settle", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    document.body.appendChild(host);
    attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);
    const revealing = document.createElement("p");
    viewport.appendChild(revealing);

    const release = beginScrollMeasureQuiet(revealing);
    expect(scrollbar.sleep).toHaveBeenLastCalledWith(true);

    host.dispatchEvent(new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT));
    expect(scrollbar.sleep).toHaveBeenLastCalledWith(false);

    updateThemedViewportScrollbar(host, "grown");
    await vi.runAllTimersAsync();
    expect(scrollbar.update).not.toHaveBeenCalled();
    expect(scrollbar.sleep).toHaveBeenLastCalledWith(true);

    release();
    expect(scrollbar.sleep).toHaveBeenLastCalledWith(true);
    await vi.runAllTimersAsync();
    expect(scrollbar.sleep).toHaveBeenLastCalledWith(false);
    expect(scrollbar.update).not.toHaveBeenCalled();
  });

  it("leaves scrollers outside the scope measuring", async () => {
    vi.useFakeTimers();
    const mount = (): { host: HTMLElement; viewport: HTMLElement } => {
      const host = document.createElement("div");
      const viewport = document.createElement("div");
      host.appendChild(viewport);
      document.body.appendChild(host);
      attachThemedViewportScrollbar(host, viewport);
      return { host, viewport };
    };
    const inScope = mount();
    const elsewhere = mount();

    const release = beginScrollMeasureQuiet(inScope.host);
    updateThemedViewportScrollbar(inScope.host, "changed");
    updateThemedViewportScrollbar(elsewhere.host, "changed");
    await vi.runAllTimersAsync();
    expect(instance(inScope.host).sleep).toHaveBeenCalledWith(true);
    expect(instance(elsewhere.host).sleep).not.toHaveBeenCalled();
    expect(instance(inScope.host).update).not.toHaveBeenCalled();
    expect(instance(elsewhere.host).update).toHaveBeenCalledTimes(1);

    release();
    await vi.runAllTimersAsync();
    expect(instance(inScope.host).sleep).toHaveBeenLastCalledWith(false);
    expect(instance(inScope.host).update).not.toHaveBeenCalled();
  });

  it("quiets a scroller that attaches while a hold is open", async () => {
    vi.useFakeTimers();
    const scope = document.createElement("div");
    document.body.appendChild(scope);
    const release = beginScrollMeasureQuiet(scope);
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    scope.appendChild(host);
    attachThemedViewportScrollbar(host, viewport);
    expect(instance(host).sleep).toHaveBeenCalledWith(true);

    release();
    expect(instance(host).sleep).toHaveBeenLastCalledWith(true);
    await vi.runAllTimersAsync();
    expect(instance(host).sleep).toHaveBeenLastCalledWith(false);
  });

  it("sleeps and wakes a host once however many holds overlap", async () => {
    vi.useFakeTimers();
    const host = document.createElement("div");
    const viewport = document.createElement("div");
    host.appendChild(viewport);
    document.body.appendChild(host);
    attachThemedViewportScrollbar(host, viewport);
    const scrollbar = instance(host);

    const outer = beginScrollMeasureQuiet(host);
    const inner = beginScrollMeasureQuiet(host);
    expect(scrollbar.sleep).toHaveBeenCalledTimes(1);

    inner();
    expect(scrollbar.sleep).toHaveBeenCalledTimes(1);
    expect(isScrollMeasureQuietForTests()).toBe(true);

    outer();
    outer();
    expect(scrollbar.sleep).toHaveBeenCalledTimes(1);
    await vi.runAllTimersAsync();
    expect(scrollbar.sleep).toHaveBeenCalledTimes(2);
    expect(scrollbar.sleep).toHaveBeenLastCalledWith(false);
    expect(isScrollMeasureQuietForTests()).toBe(false);
    await vi.runAllTimersAsync();
  });
});
