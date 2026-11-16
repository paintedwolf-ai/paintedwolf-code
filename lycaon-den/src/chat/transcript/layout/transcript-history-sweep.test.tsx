// @vitest-environment jsdom
import type { Elements } from "overlayscrollbars";
import { createMemo, createRoot } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { bindOverlayScrollbarInput } from "../../../platform/scrolling/overlay-scrollbar-input.ts";
import {
  bindScrollportMotion,
  bindScrollportNativeInput,
  unbindScrollportMotion,
} from "../../../platform/scrolling/scrollport-motion.ts";
import { createAppStore } from "../../../store/app-state.ts";
import { stubClient } from "../../../test/client-fixture.ts";
import { resetStreamScrollDebugForTests } from "../../stream/den-scroll-debug.ts";
import { transcriptReaderReach } from "../history/transcript-reader-reach.ts";
import {
  createTranscriptHistoryView,
  createTranscriptPaging,
} from "./transcript-load-more.ts";
import {
  SWEEP_EVICT_SESSION_LENGTH,
  SWEEP_FIT_SESSION_LENGTH,
  byteBoundedPageLength,
  createByteBoundedTranscriptSession,
} from "./transcript-scale-fixture.ts";
import { createTranscriptVirtualizer } from "./transcript-virtualizer.ts";
import {
  TRANSCRIPT_RESIDENT_ORD_BUDGET,
  TRANSCRIPT_RESIDENT_ORD_HARD_CAP,
  contiguousWindowRows,
  residentPageOrds,
} from "./transcript-window.ts";

function requestKeyFromQuery(query?: {
  beforeMessageId?: string;
  afterMessageId?: string;
  afterOrd?: number;
  from?: "oldest";
}): string {
  if (query?.beforeMessageId) return `older:${query.beforeMessageId}`;
  if (query?.afterMessageId) return `newer:${query.afterMessageId}`;
  if (typeof query?.afterOrd === "number") return `seek:${query.afterOrd}`;
  if (query?.from === "oldest") return "start";
  return "tail";
}

async function flushSweepTurn(): Promise<void> {
  for (let i = 0; i < 16; i += 1) {
    await new Promise<void>((resolve) => queueMicrotask(resolve));
  }
}

function createSweepHarness(sessionLength: number, sessionId: string) {
  const fixture = createByteBoundedTranscriptSession(sessionLength, 11);
  const appStore = createAppStore();
  appStore.actions.installTranscriptBaseline(
    sessionId,
    fixture.tail,
    sessionLength,
    {
      hasMoreBefore: sessionLength > fixture.tail.length,
      hasMoreAfter: false,
      bounds: { firstOrd: 1, lastOrd: sessionLength },
    },
  );

  const viewportHeight = 500;
  let scrollTop = 0;
  const scroller = document.createElement("div");
  scroller.className = "den-chat-stream";
  Object.defineProperty(scroller, "clientHeight", {
    value: viewportHeight,
    configurable: true,
  });
  Object.defineProperty(scroller, "scrollTop", {
    get: () => scrollTop,
    set: (next: number) => {
      scrollTop = Math.max(0, next);
    },
    configurable: true,
  });
  document.body.appendChild(scroller);

  const track = document.createElement("div");
  const handle = document.createElement("div");
  track.appendChild(handle);
  Object.defineProperty(track, "clientHeight", {
    value: viewportHeight,
    configurable: true,
  });
  Object.defineProperty(handle, "clientHeight", {
    value: 50,
    configurable: true,
  });
  const elements = {
    host: scroller,
    scrollOffsetElement: scroller,
    scrollbarVertical: {
      scrollbar: document.createElement("div"),
      track,
      handle,
    },
    scrollbarHorizontal: {
      scrollbar: document.createElement("div"),
      track: document.createElement("div"),
      handle: document.createElement("div"),
    },
  } as unknown as Elements;

  const requestKeys: string[] = [];
  const anchorDriftsPx: { driftPx: number; rowHeightPx: number }[] = [];
  let maxResidentPageOrds = 0;
  let tailGapEverSet = false;
  let reachOverlappedVictim = false;

  const root = createRoot((dispose) => {
    const motion = bindScrollportMotion(scroller, scroller, scroller);
    const stopNativeInput = bindScrollportNativeInput(motion);
    const presentedMessages = createMemo(() =>
      contiguousWindowRows(appStore.state.transcript),
    );
    const items = createMemo(() => fixture.projectItems(presentedMessages()));
    const historyView = createTranscriptHistoryView({
      enabled: () => true,
      messages: presentedMessages,
      items,
      appStore: () => appStore,
    });
    const virtualizerRef: { current?: ReturnType<typeof createTranscriptVirtualizer> } = {};
    const maxScrollOffset = (fallback: number) =>
      Math.max(
        fallback,
        (virtualizerRef.current?.getTotalSize() ?? fallback + viewportHeight) - viewportHeight,
      );
    const virtualizer = createTranscriptVirtualizer({
      items,
      scrollElement: () => scroller,
      revealOffset: (offset) => {
        const target = Math.max(0, offset);
        motion.commit(target, "reveal", {
          axis: "y",
          measuredMaxOffset: maxScrollOffset(target),
        });
      },
      shiftContent: (deltaY) => {
        const before = scrollTop;
        const target = Math.max(0, before + deltaY);
        motion.commit(target, "content_shift", {
          axis: "y",
          measuredMaxOffset: maxScrollOffset(target),
        });
        return scrollTop - before;
      },
      rowSizeHint: (item) => fixture.measuredRowHeight(item),
      restoredRowEstimate: (item) => fixture.measuredRowHeight(item),
      unloadedOrds: historyView.unloadedOrds,
    });
    virtualizerRef.current = virtualizer;

    Object.defineProperty(scroller, "scrollHeight", {
      get: () => virtualizer.getTotalSize(),
      configurable: true,
    });

    // Start at the live tail.
    const initialTail = Math.max(0, virtualizer.getTotalSize() - viewportHeight);
    motion.commit(initialTail, "jump", {
      axis: "y",
      measuredMaxOffset: initialTail,
    });
    virtualizer.scrollOffset = scrollTop;

    const baseLoadPage = appStore.actions.loadTranscriptPage;
    appStore.actions.loadTranscriptPage = (request, page) => {
      virtualizer.getVirtualItemForOffset(0);
      const beforeOffset = virtualizer.scrollOffset ?? scrollTop;
      const beforeVisible = virtualizer.measurementsCache.find(
        (m) => m.end > beforeOffset && m.start < beforeOffset + viewportHeight,
      );
      const targetKey = beforeVisible?.key;
      const beforeViewportY = beforeVisible
        ? beforeVisible.start - beforeOffset
        : undefined;
      const prevPages = Object.entries(appStore.state.transcript.pages);

      const applied = baseLoadPage(request, page);
      if (applied) {
        if (appStore.state.transcript.hasTailGap) tailGapEverSet = true;
        const pageOrds = residentPageOrds(appStore.state.transcript.pages);
        if (pageOrds > maxResidentPageOrds) maxResidentPageOrds = pageOrds;
        const reach = transcriptReaderReach(sessionId);
        const nextPages = appStore.state.transcript.pages;
        if (request.edge === "older" || request.edge === "newer") {
          for (const [key, prevPage] of prevPages) {
            if (key in nextPages || prevPage.length === 0) continue;
            const firstOrd = prevPage[0]?.ord ?? 0;
            const lastOrd = prevPage[prevPage.length - 1]?.ord ?? firstOrd;
            if (
              reach &&
              pageOrds <= TRANSCRIPT_RESIDENT_ORD_HARD_CAP &&
              firstOrd <= reach.lastOrd &&
              lastOrd >= reach.firstOrd
            ) {
              reachOverlappedVictim = true;
            }
          }
        }
        if (targetKey !== undefined && beforeViewportY !== undefined && request.edge !== "seek") {
          virtualizer.getVirtualItemForOffset(0);
          const afterOffset = virtualizer.scrollOffset ?? scrollTop;
          const afterItem = virtualizer.measurementsCache.find(
            (m) => m.key === targetKey,
          );
          if (afterItem) {
            const afterViewportY = afterItem.start - afterOffset;
            anchorDriftsPx.push({
              driftPx: Math.abs(afterViewportY - beforeViewportY),
              rowHeightPx: afterItem.size,
            });
          }
        }
      }
      return applied;
    };

    const scrollbarInput = bindOverlayScrollbarInput(elements, motion, {
      read: () => ({
        offset: scrollTop,
        viewport: viewportHeight,
        extent: virtualizer.getTotalSize(),
      }),
      scrollTo: (next, source) => {
        const target = Math.max(0, next);
        motion.commit(target, source, {
          axis: "y",
          measuredMaxOffset: Math.max(target, virtualizer.getTotalSize() - viewportHeight),
        });
        virtualizer.scrollOffset = scrollTop;
        scroller.dispatchEvent(new Event("scroll"));
      },
    });

    const listSessionMessages = vi.fn(
      async (
        _id: string,
        query?: {
          beforeMessageId?: string;
          afterMessageId?: string;
          afterOrd?: number;
          from?: "oldest";
        },
      ) => {
        requestKeys.push(requestKeyFromQuery(query));
        return fixture.fetchPage(query);
      },
    );

    const paging = createTranscriptPaging(scroller, {
      client: () => stubClient({ listSessionMessages }),
      appStore,
      sessionId: () => sessionId,
      geometry: () => historyView.geometry(virtualizer),
    });

    return {
      dispose: () => {
        paging.dispose();
        scrollbarInput.dispose();
        stopNativeInput();
        unbindScrollportMotion(scroller);
        dispose();
        scroller.remove();
      },
      virtualizer,
      paging,
      scrollbarInput,
      handle,
      wheelTo: (next: number, deltaY: number) => {
        scroller.dispatchEvent(new WheelEvent("wheel", { deltaY, bubbles: true }));
        const maxOffset = Math.max(0, virtualizer.getTotalSize() - viewportHeight);
        scrollTop = Math.max(0, Math.min(next, maxOffset));
        virtualizer.scrollOffset = scrollTop;
        scroller.dispatchEvent(new Event("scroll"));
      },
      setScrollTop: (next: number) => {
        const maxOffset = Math.max(0, virtualizer.getTotalSize() - viewportHeight);
        const target = Math.max(0, Math.min(next, maxOffset));
        motion.commit(target, "jump", {
          axis: "y",
          measuredMaxOffset: maxOffset,
        });
        virtualizer.scrollOffset = scrollTop;
        scroller.dispatchEvent(new Event("scroll"));
      },
      getScrollTop: () => scrollTop,
    };
  });

  return {
    appStore,
    fixture,
    requestKeys,
    anchorDriftsPx,
    stats: () => ({
      maxResidentPageOrds,
      tailGapEverSet,
      reachOverlappedVictim,
    }),
    ...root,
  };
}

describe("transcript history sweep invariants (TH05)", () => {
  afterEach(() => {
    localStorage.removeItem("den:scroll-debug");
    resetStreamScrollDebugForTests();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("generates seeded byte-bounded page lengths in [17, 53]", () => {
    for (let cursor = 1; cursor <= 500; cursor += 1) {
      const len = byteBoundedPageLength(11, cursor);
      expect(len).toBeGreaterThanOrEqual(17);
      expect(len).toBeLessThanOrEqual(53);
    }
  });

  it.each([
    { length: SWEEP_FIT_SESSION_LENGTH, fitsBudget: true },
    { length: SWEEP_EVICT_SESSION_LENGTH, fitsBudget: false },
  ])(
    "wheel sweep (tail -> start -> tail) at $length messages satisfies I6 and I7",
    async ({ length, fitsBudget }) => {
      vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
        queueMicrotask(() => cb(0));
        return 1;
      });
      const harness = createSweepHarness(length, `wheel-${length}`);
      try {
        await flushSweepTurn();
        harness.requestKeys.length = 0;

        // Sweep upward from the live tail to the start of history via native wheel events.
        let guard = 0;
        while (harness.appStore.state.transcript.hasMoreBefore && guard < 80) {
          guard += 1;
          const extent = harness.virtualizer.residentExtent();
          harness.wheelTo(Math.max(0, extent.startPx + 80), -120);
          await flushSweepTurn();
        }
        harness.wheelTo(0, -120);
        await flushSweepTurn();
        expect(harness.appStore.state.transcript.hasMoreBefore).toBe(false);
        const upKeys = harness.requestKeys.slice();
        expect(upKeys.length).toBeGreaterThan(0);
        // I6: no request key repeats within the upward sweep.
        expect(new Set(upKeys).size).toBe(upKeys.length);

        if (fitsBudget) {
          // At 305 messages, all older pages fit inside the 500-message budget and the tail never detaches.
          expect(harness.stats().tailGapEverSet).toBe(false);
          expect(harness.appStore.state.transcript.hasTailGap).toBe(false);
          expect(harness.appStore.state.messages[0]?.ord).toBe(1);
          expect(harness.appStore.state.messages.at(-1)?.ord).toBe(length);
        } else {
          expect(harness.appStore.state.transcript.hasTailGap).toBe(true);
          expect(harness.stats().maxResidentPageOrds).toBeLessThanOrEqual(
            TRANSCRIPT_RESIDENT_ORD_HARD_CAP,
          );
          expect(harness.stats().reachOverlappedVictim).toBe(false);
        }

        // Sweep downward from the start of history back to the live tail via native wheel events.
        harness.requestKeys.length = 0;
        guard = 0;
        while (harness.appStore.state.transcript.hasTailGap && guard < 80) {
          guard += 1;
          const extent = harness.virtualizer.residentExtent();
          harness.wheelTo(Math.max(0, extent.endPx - 400), 120);
          await flushSweepTurn();
        }
        expect(harness.appStore.state.transcript.hasTailGap).toBe(false);
        const downKeys = harness.requestKeys.slice();
        // I6: no request key repeats within the downward sweep.
        expect(new Set(downKeys).size).toBe(downKeys.length);
        expect(
          residentPageOrds(harness.appStore.state.transcript.pages),
        ).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_ORD_BUDGET);

        // I7: across every page landing, the first visible resident row kept its viewport offset within one row height.
        expect(harness.anchorDriftsPx.length).toBeGreaterThan(0);
        for (const { driftPx, rowHeightPx } of harness.anchorDriftsPx) {
          expect(driftPx).toBeLessThanOrEqual(rowHeightPx);
        }
      } finally {
        harness.dispose();
      }
    },
  );

  it.each([
    { length: SWEEP_FIT_SESSION_LENGTH, fitsBudget: true },
    { length: SWEEP_EVICT_SESSION_LENGTH, fitsBudget: false },
  ])(
    "thumb-drag sweep (bottom -> top -> bottom with held pauses) at $length messages satisfies I5, I6, and I7",
    async ({ length, fitsBudget }) => {
      vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
        queueMicrotask(() => cb(0));
        return 1;
      });
      const harness = createSweepHarness(length, `drag-${length}`);
      const pointer = (type: string, clientY: number) =>
        Object.assign(new Event(type, { bubbles: true, cancelable: true }), {
          isPrimary: true,
          button: 0,
          pointerId: 1,
          clientY,
        });
      try {
        await flushSweepTurn();
        harness.requestKeys.length = 0;

        // Grab thumb at the bottom (clientY = 450) and drag up to the top (clientY = 0) in steps,
        // holding the pointer stationary at each step while pages land and refreshGeometry() rebases.
        let clientY = 450;
        harness.handle.dispatchEvent(pointer("pointerdown", clientY));
        while (harness.appStore.state.transcript.hasMoreBefore && clientY > 0) {
          clientY = Math.max(0, clientY - 45);
          harness.handle.dispatchEvent(pointer("pointermove", clientY));
          const keysBeforePause = harness.requestKeys.length;
          await flushSweepTurn();
          if (harness.requestKeys.length > keysBeforePause) {
            // Extent changed while the thumb remained held still at clientY.
            const offsetBeforeRefresh = harness.getScrollTop();
            harness.scrollbarInput.refreshGeometry();
            expect(harness.getScrollTop()).toBe(offsetBeforeRefresh);
            await flushSweepTurn();
          }
        }
        // Finish reaching the top of history if any older pages remain.
        let guard = 0;
        while (harness.appStore.state.transcript.hasMoreBefore && guard < 60) {
          guard += 1;
          const extent = harness.virtualizer.residentExtent();
          harness.setScrollTop(Math.max(0, extent.startPx + 80));
          harness.scrollbarInput.refreshGeometry();
          await flushSweepTurn();
        }
        expect(harness.appStore.state.transcript.hasMoreBefore).toBe(false);
        const upKeys = harness.requestKeys.slice();
        expect(new Set(upKeys).size).toBe(upKeys.length);
        // No newer-edge request was triggered during the upward thumb drag.
        expect(upKeys.some((k) => k.startsWith("newer:"))).toBe(false);

        if (fitsBudget) {
          expect(harness.stats().tailGapEverSet).toBe(false);
        } else {
          // Jumping into the unloaded before/after runway beyond reach issues a seek.
          expect(upKeys.some((k) => k.startsWith("seek:"))).toBe(true);
          expect(harness.stats().reachOverlappedVictim).toBe(false);
        }

        // Now drag back down from top to bottom with held pauses.
        harness.requestKeys.length = 0;
        while (harness.appStore.state.transcript.hasTailGap && clientY < 450) {
          clientY = Math.min(450, clientY + 45);
          harness.handle.dispatchEvent(pointer("pointermove", clientY));
          await flushSweepTurn();
          harness.scrollbarInput.refreshGeometry();
          await flushSweepTurn();
        }
        guard = 0;
        while (harness.appStore.state.transcript.hasTailGap && guard < 60) {
          guard += 1;
          const extent = harness.virtualizer.residentExtent();
          harness.setScrollTop(Math.max(0, extent.endPx - 400));
          harness.scrollbarInput.refreshGeometry();
          await flushSweepTurn();
        }
        harness.handle.dispatchEvent(pointer("pointerup", clientY));

        expect(harness.appStore.state.transcript.hasTailGap).toBe(false);
        const downKeys = harness.requestKeys.slice();
        expect(new Set(downKeys).size).toBe(downKeys.length);
        expect(downKeys.some((k) => k.startsWith("older:"))).toBe(false);

        for (const { driftPx, rowHeightPx } of harness.anchorDriftsPx) {
          expect(driftPx).toBeLessThanOrEqual(rowHeightPx);
        }
      } finally {
        harness.dispose();
      }
    },
  );
});
