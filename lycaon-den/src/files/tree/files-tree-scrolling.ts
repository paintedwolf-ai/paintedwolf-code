import { editorTreeVisibleLevelsPref } from "../../settings/editor/editor-prefs.ts";
import { FilesTreeViewport } from "./files-tree-viewport.ts";
import { type FilesTreeSession } from "./files-tree-paged-session.ts";
import { buildPagedTreeModel } from "./files-tree-paged-model.ts";
import { type TreePresentation } from "./files-tree-navigation.ts";
import { SegmentedScroll } from "../../ui/paged-view/geometry.ts";
import { observeScrollportOffset } from "../../platform/scrolling/scrollport-offset.ts";
import { batch, createEffect, createMemo, createSignal, onCleanup, onMount, untrack } from "solid-js";
import {
  DEN_SCROLLPORT_INPUT_EVENT,
  scrollportMotionForHost,
  scrollportMotionForViewport,
} from "../../platform/scrolling/scrollport-motion.ts";
import { attachThemedViewportScrollbar, updateThemedViewportScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { FILES_TREE_ROW_HEIGHT_PX } from "./project-files-tree-flat.ts";
import { type FixedVirtualWindow, fixedVirtualWindow, observeVirtualScrollOffset, windowCoversRows } from "./files-tree-virtual-scroll.ts";
import {
  EMPTY_STICKY_SLOTS,
  NO_STICKY_FOLD,
  retainStickySlots,
  stickyStackFromIndex,
  treeLayoutRange,
  type StickyFold,
  type StickySlot,
} from "./files-tree-sticky.ts";
import { setFilesTreeScrollTop } from "./files-tree-view-state.ts";
import { subscribeScrollActivity } from "../../platform/scrolling/scroll-activity.ts";
import type { Setter } from "solid-js";
import type { DirState } from "./files-tree-context.ts";

export type FilesTreeScrollRestoration = { activeReveal: string | null; pendingScroll: number | undefined };

type ScrollBinding = {
  reportTreeError: (error: unknown) => void;
  displayModel: () => ReturnType<typeof buildPagedTreeModel>;
  state: FilesTreeSession["state"];
  surfaceLive: () => boolean;
  sourceView: () => FilesTreeSession | undefined;
  presentedTree: () => { workspace: string; presentation: TreePresentation | undefined };
  setStickyHostEl: (element: HTMLElement | undefined) => void;
  revealViewport: () => unknown;
  navigatingViewport: () => boolean;
  cancelTreeReveal: () => void;
  cancelRebase: () => void;
  viewportReader: () => FilesTreeViewport | undefined;
  setGestureVersion: Setter<number>;
  viewStateKey: () => string;
  dirStates: () => Record<string, DirState>;
};

const VIRTUAL_BUFFER_VIEWPORTS = 1.5;
/** A thumb drag commits every offset from script, so its rows need no scroll-thread lead. */
const THUMB_DRAG_BUFFER_VIEWPORTS = 0.25;

export function createFilesTreeScrolling(options: {
  navElement: () => HTMLElement | undefined;
  scrollElement: () => HTMLElement | undefined;
  setScrollElement: (element: HTMLElement | undefined) => void;
  scrollEpoch: () => number;
  setScrollEpoch: Setter<number>;
  restoration: FilesTreeScrollRestoration;
}) {
  let scrollViewportHeight = 0;
  const [virtualScrollTop, setVirtualScrollTop] = createSignal(0);
  const [virtualViewportHeight, setVirtualViewportHeight] = createSignal(480);
  const geometry = new SegmentedScroll(FILES_TREE_ROW_HEIGHT_PX);
  const [geometryVersion, setGeometryVersion] = createSignal(0);
  const [navigatingViewport, setNavigatingViewport] = createSignal(false);
  const [paintOffset, setPaintOffset] = createSignal(0);
  const [scrollActive, setScrollActive] = createSignal(false);
  type ScrollbarTarget = { offset: number; origin: number; intent: string; abort: AbortController };
  const [scrollbarTarget, setScrollbarTarget] = createSignal<ScrollbarTarget>();
  /** Thumb movement shares one destination read in flight. */
  let scrollbarRead: ScrollbarTarget | undefined;
  const cancelScrollbarNavigation = () => {
    scrollbarRead?.abort.abort(); scrollbarRead = undefined;
    scrollbarTarget()?.abort.abort(); setScrollbarTarget(undefined); setPaintOffset(0);
  };
  onCleanup(cancelScrollbarNavigation);
  function resolveScrollEl(): HTMLElement | undefined {
    const scroller = options.navElement()?.closest<HTMLElement>(".den-files-tree-scroll");
    return scroller ?? options.navElement()?.parentElement ?? undefined;
  }

  /** Reads the captured scroll offset without forcing layout. */
  function currentScrollTop(el: HTMLElement): number {
    return scrollportMotionForViewport(el)?.offsetY() ?? el.scrollTop;
  }

  /** The resize-observed viewport height; the layout read only seeds it. */
  function currentViewportHeight(el: HTMLElement): number {
    return scrollViewportHeight || el.clientHeight;
  }

  function bind(deps: ScrollBinding) {
    const resolveStickyHost = (): HTMLElement | undefined =>
      options.navElement()
        ?.closest<HTMLElement>(".den-files-tree-scroll-frame")
        ?.querySelector<HTMLElement>("[data-files-tree-sticky-host]") ??
      undefined;

    const [virtualWindow, setVirtualWindow] =
      createSignal<FixedVirtualWindow | null>(null);
    // Pinned geometry and row indentation derive from the same displayed model and offset.
    const stickyStack = createMemo(() => stickyStackFromIndex(
      deps.displayModel(), virtualScrollTop(), virtualViewportHeight(), editorTreeVisibleLevelsPref(),
    ));
    const stickySlots = createMemo<readonly StickySlot[]>(previous => retainStickySlots(previous, stickyStack().slots), EMPTY_STICKY_SLOTS);
    const stickyTops = createMemo(() => stickyStack().tops, undefined, {
      equals: (a, b) => a.length === b.length && a.every((top, index) => top === b[index]),
    });
    const stickyHeight = () => stickyStack().height;
    const stickyFold = createMemo<StickyFold>(() => stickyStack().fold, NO_STICKY_FOLD, {
      equals: (a, b) => a.floor === b.floor && a.levels === b.levels,
    });
    const stickyKeys = createMemo(() => stickySlots().map(slot => slot.key), undefined, {
      equals: (a, b) => a.length === b.length && a.every((key, index) => key === b[index]),
    });
    createEffect(() => {
      options.scrollEpoch();
      deps.setStickyHostEl(resolveStickyHost());
    });

    // Thumb drags use a narrow row window until release.
    let thumbDrag = false;
    const updateVirtualWindow = () => {
      setVirtualWindow((previous) =>
        fixedVirtualWindow({
          count: deps.displayModel().length,
          scrollTop: untrack(virtualScrollTop),
          viewportHeight: virtualViewportHeight(),
          rowHeight: FILES_TREE_ROW_HEIGHT_PX,
          bufferViewports: thumbDrag ? THUMB_DRAG_BUFFER_VIEWPORTS : VIRTUAL_BUFFER_VIEWPORTS,
          previous: previous ?? undefined,
        }),
      );
    };

    /** Publishes destination rows and their layout before a programmatic scroll commit. */
    const layoutViewport = (offset: number) => batch(() => {
      const physical = geometry.reveal(Math.floor(offset / FILES_TREE_ROW_HEIGHT_PX),
        offset % FILES_TREE_ROW_HEIGHT_PX, deps.displayModel().length, virtualViewportHeight());
      setGeometryVersion(version => version + 1);
      setPaintOffset(0);
      setVirtualScrollTop(geometry.logicalOffset(physical));
      updateVirtualWindow();
      return physical;
    });

    const adoptScrollEl = () => {
      const next = resolveScrollEl();
      if (next === options.scrollElement()) return;
      options.setScrollElement(next);
      scrollViewportHeight = next?.clientHeight ?? 0;
      setVirtualScrollTop(options.restoration.pendingScroll ?? geometry.logicalOffset(next?.scrollTop ?? 0));
      setVirtualViewportHeight(
        Math.max(FILES_TREE_ROW_HEIGHT_PX, scrollViewportHeight || 480),
      );
      updateVirtualWindow();
      options.setScrollEpoch((n) => n + 1);
    };

    /** Pinned rows forward wheel input to their sibling scroller. */
    const forwardStickyWheel = (event: WheelEvent) => {
      // Pinch input changes zoom.
      if (event.ctrlKey) return;
      const el = options.scrollElement() ?? resolveScrollEl();
      if (!el) return;
      const scale =
        event.deltaMode === WheelEvent.DOM_DELTA_LINE
          ? FILES_TREE_ROW_HEIGHT_PX
          : event.deltaMode === WheelEvent.DOM_DELTA_PAGE
            ? el.clientHeight
            : 1;
      if (event.deltaY !== 0) el.scrollTop += event.deltaY * scale;
      if (event.deltaX !== 0) el.scrollLeft += event.deltaX * scale;
    };

    createEffect(() => {
      const current = deps.state();
      if (!deps.surfaceLive() || untrack(scrollbarTarget)?.intent !== `${current?.id}:${current?.intent_revision}`) cancelScrollbarNavigation();
    });

    const viewportCovered = (offset: number) => {
      const model = deps.displayModel();
      const start = Math.floor(offset / FILES_TREE_ROW_HEIGHT_PX);
      const end = Math.min(model.length, Math.ceil((offset + virtualViewportHeight()) / FILES_TREE_ROW_HEIGHT_PX));
      const context = treeLayoutRange(start, end, model.length);
      for (let index = context.start; index < context.end; index++) if (!model.rowAt(index)) return false;
      return true;
    };

    /** Every visible row at `offset` has a mounted element. */
    const viewportPrepared = (offset: number) => {
      const start = Math.floor(offset / FILES_TREE_ROW_HEIGHT_PX);
      const end = Math.min(deps.displayModel().length, Math.ceil((offset + virtualViewportHeight()) / FILES_TREE_ROW_HEIGHT_PX));
      return windowCoversRows(virtualWindow(), start, end) && viewportCovered(offset);
    };

    const seekViewport = (offset: number, source: "thumb_drag" | "track_click" | "jump", native = false) => {
      const viewport = options.scrollElement() ?? resolveScrollEl();
      const host = options.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
      if (!viewport || !host) return;

      if (scrollbarTarget()?.offset === offset) return;
      if (source === "thumb_drag") thumbDrag = true;
      const commit = (offset: number) => {
        const physical = batch(() => {
          setVirtualScrollTop(offset);
          cancelScrollbarNavigation();
          return layoutViewport(offset);
        });
        // Destination rows reach the DOM before the scroll offset changes.
        scrollportMotionForHost(host)?.commit(physical, source);
      };
      deps.cancelRebase();
      if (viewportCovered(offset)) { commit(offset); return; }
      const view = deps.sourceView();
      const current = deps.state();
      if (!view || !current) return;
      const intent = `${current.id}:${current.intent_revision}`;
      const target: ScrollbarTarget = { offset, origin: currentScrollTop(viewport), intent, abort: new AbortController() };
      const reading = scrollbarRead !== undefined;
      batch(() => {
        const previous = scrollbarTarget();
        // Thumb movement retargets the active read without restarting it.
        if (previous && previous !== scrollbarRead) previous.abort.abort();
        setScrollbarTarget(target); setPaintOffset(0); deps.viewportReader()?.clear();
        if (native) setPaintOffset(currentScrollTop(viewport) - geometry.physicalOffset(untrack(virtualScrollTop)));
      });
      const rowRange = (at: number) => {
        const model = deps.displayModel();
        const start = Math.floor(at / FILES_TREE_ROW_HEIGHT_PX);
        const end = Math.min(model.length, Math.ceil((at + virtualViewportHeight()) / FILES_TREE_ROW_HEIGHT_PX));
        return { start: model.sourceIndex(start), end: model.sourceIndex(end) };
      };
      // A completed read advances to the latest uncovered destination.
      const readDestination = (destination: ScrollbarTarget) => {
        scrollbarRead = destination;
        const { start, end } = rowRange(destination.offset);
        void view.range(start, end, destination.abort.signal).then(() => {
          if (scrollbarRead !== destination) return;
          scrollbarRead = undefined;
          const latest = scrollbarTarget();
          if (!latest || view !== deps.sourceView() || !deps.surfaceLive()) return;
          if (latest === destination || viewportCovered(latest.offset)) { commit(latest.offset); return; }
          readDestination(latest);
        }).catch(error => {
          if (scrollbarRead !== destination) return;
          scrollbarRead = undefined;
          if (scrollbarTarget()) commit(untrack(virtualScrollTop));
          deps.reportTreeError(error);
        });
      };
      if (reading) {
        // Host read-ahead demand follows the thumb while the current read finishes.
        const { start, end } = rowRange(offset);
        view.updateViewport(start, end);
        return;
      }
      readDestination(target);
    };

    onMount(() => {
      adoptScrollEl();
      const viewport = resolveScrollEl();
      const host = options.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
      if (!viewport || !host) return;
      const detach = attachThemedViewportScrollbar(host, viewport, {
        vertical: {
          read: () => ({
            offset: scrollbarTarget()?.offset ?? geometry.logicalOffset(currentScrollTop(viewport)),
            viewport: virtualViewportHeight(),
            extent: deps.displayModel().length * FILES_TREE_ROW_HEIGHT_PX,
          }),
          scrollTo: (offset, source) => seekViewport(offset, source),
        },
      });
      onCleanup(detach);
      // Thumb release refills the buffer and permits a pending presentation change.
      const motion = scrollportMotionForHost(host);
      if (motion) onCleanup(motion.input.subscribeInputSettled(() => deps.setGestureVersion(version => version + 1)));
      const onDirectInput = () => {
        if (deps.revealViewport() || deps.navigatingViewport()) deps.cancelTreeReveal();
        options.restoration.pendingScroll = undefined; options.restoration.activeReveal = null;
        deps.setGestureVersion(version => version + 1);
        if (!thumbDrag || motion?.input.isThumbGestureActive()) return;
        thumbDrag = false;
        updateVirtualWindow();
      };
      host.addEventListener(DEN_SCROLLPORT_INPUT_EVENT, onDirectInput);
      host.addEventListener("pointerdown", onDirectInput, true);
      host.addEventListener("keydown", onDirectInput, true);
      onCleanup(() => {
        host.removeEventListener(DEN_SCROLLPORT_INPUT_EVENT, onDirectInput);
        host.removeEventListener("pointerdown", onDirectInput, true);
        host.removeEventListener("keydown", onDirectInput, true);
      });
      queueMicrotask(tryApplyPendingScrollRestore);
    });

    createEffect(() => {
      const rowCount = deps.displayModel().length;
      const viewportHeight = virtualViewportHeight();
      const target = scrollbarTarget();
      void options.scrollEpoch();
      const host = options.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
      if (!host) return;
      updateThemedViewportScrollbar(host, `${rowCount}:${viewportHeight}:${geometryVersion()}:${target?.offset ?? ""}`);
    });

    const expandedDirsStillLoading = () => deps.state()?.state !== "ready";

    const tryApplyPendingScrollRestore = () => {
      const top = options.restoration.pendingScroll;
      if (top == null || deps.revealViewport()) return;
      const el = options.scrollElement() ?? resolveScrollEl();
      if (!el) return;
      if (expandedDirsStillLoading()) {
        return;
      }
      if (!deps.presentedTree().presentation?.frames.length && deps.state()?.extent.rows !== 0) return;
      const host = options.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
      const motion = host ? scrollportMotionForHost(host) : undefined;
      if (!motion) return;
      if (!viewportCovered(Math.min(top, Math.max(0, deps.displayModel().length * FILES_TREE_ROW_HEIGHT_PX - virtualViewportHeight())))) return;
      const physical = batch(() => {
        options.restoration.pendingScroll = undefined;
        return layoutViewport(top);
      });
      motion.commit(physical, "restore_anchor");
    };

    createEffect(() => {
      options.scrollEpoch();
      deps.displayModel().length;
      deps.dirStates();
      tryApplyPendingScrollRestore();
    });

    createEffect(() => {
      options.scrollEpoch();
      const projectId = deps.viewStateKey();
      const el = options.scrollElement();
      if (!el || !projectId) return;
      const stopPaint = observeScrollportOffset(el, offset => {
        if (options.restoration.pendingScroll != null) return;
        // Unprepared destinations retain the last painted rows.
        const logical = geometry.logicalOffset(offset);
        const prepared = viewportPrepared(logical);
        batch(() => {
          setPaintOffset(prepared ? 0 : offset - geometry.physicalOffset(untrack(virtualScrollTop)));
          // Native movement updates pinned geometry and indentation in the same event.
          if (prepared) {
            setVirtualScrollTop(logical);
          }
        });
      });
      const stop = untrack(() => observeVirtualScrollOffset(el, (offset, scrolling) => {
        if (options.restoration.pendingScroll != null) return;
        const target = scrollbarTarget();
        const logical = geometry.logicalOffset(offset);
        if (scrolling && !viewportCovered(logical) && !deps.revealViewport() && !deps.navigatingViewport()) {
          seekViewport(logical, "jump", true);
          return;
        }
        if (scrolling && target && offset !== target.origin) cancelScrollbarNavigation();
        const physical = geometry.recenter(offset, deps.displayModel().length, virtualViewportHeight());
        batch(() => {
          if (physical !== offset) setGeometryVersion(version => version + 1);
          setPaintOffset(0);
          setVirtualScrollTop(geometry.logicalOffset(physical));
          updateVirtualWindow();
        });
        if (physical !== offset) scrollportMotionForViewport(el)?.commit(physical, "restore_anchor");
      }));
      onCleanup(() => { stopPaint(); stop(); });
    });

    createEffect(() => {
      options.scrollEpoch();
      const projectId = deps.viewStateKey();
      const el = options.scrollElement();
      if (!el || !projectId) return;
      const stop = subscribeScrollActivity(el, (phase) => {
        setScrollActive(phase === "start");
        if (phase !== "settle") return;
        // A settled glide re-evaluates a pending presentation change.
        deps.setGestureVersion(version => version + 1);
        if (options.restoration.pendingScroll == null) {
          setFilesTreeScrollTop(projectId, geometry.logicalOffset(currentScrollTop(el)));
        }
      });
      onCleanup(stop);
    });

    createEffect(() => {
      options.scrollEpoch();
      const el = options.scrollElement();
      if (!el) return;
      const emit = (height: number) => {
        const nextHeight = Math.max(
          FILES_TREE_ROW_HEIGHT_PX,
          Math.round(height) || 480,
        );
        if (nextHeight === scrollViewportHeight) return;
        scrollViewportHeight = nextHeight;
        setVirtualViewportHeight(nextHeight);
        updateVirtualWindow();
      };
      // Seed before the first resize entry.
      emit(el.clientHeight);
      if (typeof ResizeObserver === "undefined") return;
      // Coalesce viewport height and row-window changes in one reactive update.
      const observer = new ResizeObserver((entries) => {
        const box = entries[entries.length - 1]?.contentRect;
        if (box) batch(() => emit(box.height));
      });
      observer.observe(el);
      onCleanup(() => observer.disconnect());
    });

    let geometryRows = 0;
    createEffect(() => {
      const rows = deps.displayModel().length;
      virtualViewportHeight();
      if (rows < geometryRows) untrack(() => {
        const physical = layoutViewport(untrack(virtualScrollTop));
        const host = options.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
        const motion = host ? scrollportMotionForHost(host) : undefined;
        motion?.cancelApplicationMotion();
        motion?.commit(physical, "tail_bound");
        motion?.tail.releaseTailRange();
      });
      geometryRows = rows;
      updateVirtualWindow();
    });

    return { virtualWindow, stickySlots, stickyTops, stickyHeight, stickyFold, stickyKeys, forwardStickyWheel, layoutViewport };
  }
  return { virtualScrollTop, setVirtualScrollTop, virtualViewportHeight, geometry,
    navigatingViewport, setNavigatingViewport, paintOffset, scrollActive, scrollbarTarget, geometryVersion,
    cancelScrollbarNavigation, resolveScrollEl, currentScrollTop, currentViewportHeight, bind };
}
