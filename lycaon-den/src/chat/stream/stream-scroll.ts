import {
  isShellLayoutUnstable,
  onShellLayoutSettled,
} from "../../shell/shell-layout-busy.ts";
import {
  cancelStreamSpringScroll,
  streamSpringScrollTo,
} from "./stream-scroll-spring.ts";
import {
  denScrollDebugLog,
  isStreamScrollTraceVerbose,
} from "./den-scroll-debug.ts";
import {
  requireScrollportMotionForViewport,
  scrollportMotionForHost,
  scrollportClientHeight,
  scrollportMotionForViewport,
  setScrollportTailPolicy,
  wheelPassesThroughScrollport,
  DEN_SCROLLPORT_INPUT_EVENT,
  SCROLL_EPSILON_PX,
  type ScrollportMotion,
  type ScrollportCommitSource,
} from "../../platform/scrolling/scrollport-motion.ts";
import { updateThemedViewportScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { cancelScrollportFrame, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { observeScrollportOffset } from "../../platform/scrolling/scrollport-offset.ts";
import { isPresented } from "../../ui/presented.ts";

const streamTailPins = new WeakMap<HTMLElement, () => boolean>();
const streamTailPolicies = new WeakMap<HTMLElement, ScrollportMotion>();

/** Tail extent and pin state survive scrollport replacement. */
function applyStreamTailPolicy(streamTarget: HTMLElement): void {
  const motion = scrollportMotionForViewport(streamTarget);
  if (!motion) return;
  streamTailPolicies.set(streamTarget, motion);
  const pinned = streamTailPins.get(streamTarget);
  setScrollportTailPolicy(motion.host, {
    resolveTail: (naturalTail) =>
      streamRenderedTranscriptTailOffset(streamTarget) ?? naturalTail,
    ...(pinned ? { pinned } : {}),
  });
}

function ensureStreamTailPolicy(streamTarget: HTMLElement): void {
  if (streamTailPolicies.get(streamTarget) !== scrollportMotionForViewport(streamTarget)) {
    applyStreamTailPolicy(streamTarget);
  }
}

function streamScrollMotion(streamTarget: HTMLElement): ScrollportMotion {
  const motion = requireScrollportMotionForViewport(streamTarget);
  ensureStreamTailPolicy(streamTarget);
  return motion;
}

/** Sole application writer for a named stream scrollport. */
export function commitStreamScroll(
  streamTarget: HTMLElement,
  scrollTop: number,
  source: ScrollportCommitSource,
  opts?: { glide?: boolean; cancelMotion?: boolean },
): void {
  // Deferred callbacks may outlive their scrollport.
  if (!scrollportMotionForViewport(streamTarget)) return;
  const motion = streamScrollMotion(streamTarget);
  if (opts?.cancelMotion !== false && source !== "thumb_drag") {
    motion.cancelApplicationMotion();
  }
  if (opts?.glide) void motion.revealOffset(scrollTop);
  else motion.commit(scrollTop, source);
}

/** Moves to the last offset that shows content. */
export function commitStreamTail(
  streamTarget: HTMLElement,
  source: "jump" | "repin_tail",
): void {
  if (!scrollportMotionForViewport(streamTarget)) return;
  cancelStreamSpringScroll(streamTarget);
  commitStreamScroll(streamTarget, streamTailOffset(streamTarget), source, {
    cancelMotion: source === "jump",
  });
}

/** Glides to the live tail as it moves. Resolves `true` on arrival. */
export function glideStreamToTail(streamTarget: HTMLElement): Promise<boolean> {
  if (!scrollportMotionForViewport(streamTarget)) return Promise.resolve(false);
  streamScrollMotion(streamTarget);
  return streamSpringScrollTo(streamTarget, () => streamTailOffset(streamTarget));
}

/** Content shifts carry the offset without ending input. */
export function shiftStreamContent(
  streamTarget: HTMLElement,
  deltaY: number,
  fromOffset: number,
): number {
  const motion = scrollportMotionForViewport(streamTarget);
  if (!motion) return 0;
  const applied = motion.shiftContent(deltaY, fromOffset);
  const state = streamEngagement(streamTarget);
  // Content shifts move the sampling baseline with the reader.
  if (applied !== 0 && state.lastScrollTop !== undefined) {
    state.lastScrollTop += applied;
  }
  return applied;
}

/** Resolves the tail pin and the tail boundary before the next paint. */
export function reconcileStreamLayout(streamTarget: HTMLElement): void {
  if (!scrollportMotionForViewport(streamTarget)) return;
  streamScrollMotion(streamTarget).notifyLayoutMutated();
}

/** Keeps the stream on its live tail while `pin` holds, however it is bound. */
export function setStreamTailPin(
  streamTarget: HTMLElement,
  pin: (() => boolean) | null,
): void {
  if (pin) streamTailPins.set(streamTarget, pin);
  else streamTailPins.delete(streamTarget);
  applyStreamTailPolicy(streamTarget);
}

/** Pixels from the bottom treated as the live tail. */
const STREAM_SCROLL_BOTTOM_THRESHOLD_PX = 80;

/** Pixels from the top before the top edge fade hides. */
const STREAM_SCROLL_TOP_THRESHOLD_PX = 4;

/** Attribution window for scroll events following reader input. */
const STREAM_READER_INTENT_WINDOW_MS = 400;

function coalesceStreamScrollFrame(
  streamTarget: HTMLElement,
  fn: () => void,
): void {
  scheduleScrollportFrame(streamTarget, "observe", fn);
}

function isStreamNearTop(streamTarget: HTMLElement): boolean {
  return streamTarget.scrollTop <= STREAM_SCROLL_TOP_THRESHOLD_PX;
}

function measureStreamOverflow(streamTarget: HTMLElement): boolean {
  return streamContentHeight(streamTarget) > streamTarget.clientHeight + 1;
}

type StreamEngagement = {
  suppressEngageUntil: number;
  lastScrollTop?: number;
  activePointer: boolean;
  /**
   * The active press selects rather than scrolls: a mouse or pen press on content, or
   * any press once it has made a selection. It may release following, never resume it.
   */
  selectionPress?: boolean;
  /** A scrollbar thumb drag is moving the stream. */
  scrollbarGesture?: boolean;
  /** Shared frame sample that avoids a layout read in the wheel handler. */
  cachedVerticalOverflow?: boolean;
  /** Last reader input able to scroll the stream up. */
  lastUpwardIntentAt?: number;
  /** Last reader input able to scroll the stream down. */
  lastDownwardIntentAt?: number;
  /** Scroll range at the last sample; a change moves the scrollbar thumb. */
  lastScrollHeight?: number;
};

const streamEngagementByTarget = new WeakMap<HTMLElement, StreamEngagement>();

function streamEngagement(streamTarget: HTMLElement): StreamEngagement {
  let state = streamEngagementByTarget.get(streamTarget);
  if (!state) {
    state = { suppressEngageUntil: 0, activePointer: false };
    streamEngagementByTarget.set(streamTarget, state);
  }
  return state;
}

/** Offset changes during this window are not read as reader motion. */
export function suppressStreamScrollEngagement(
  streamTarget: HTMLElement,
  durationMs: number,
): void {
  streamEngagement(streamTarget).suppressEngageUntil =
    performance.now() + durationMs;
}

function isStreamScrollEngagementSuppressed(streamTarget: HTMLElement): boolean {
  return (
    isShellLayoutUnstable() ||
    performance.now() < streamEngagement(streamTarget).suppressEngageUntil
  );
}

const streamFadeQuietUntil = new WeakMap<HTMLElement, number>();

function streamWrapForFade(streamTarget: HTMLElement): HTMLElement | null {
  const wrap = streamTarget.closest(".den-chat-stream-wrap");
  return wrap instanceof HTMLElement ? wrap : null;
}

/** Hides edge fades immediately during session attachment. */
export function suppressStreamScrollFade(
  streamTarget: HTMLElement | undefined,
  durationMs: number,
): void {
  if (!streamTarget) return;
  const wrap = streamWrapForFade(streamTarget);
  if (!wrap) return;
  const until = Date.now() + Math.max(0, durationMs);
  const prev = streamFadeQuietUntil.get(wrap) ?? 0;
  streamFadeQuietUntil.set(wrap, Math.max(prev, until));
  wrap.toggleAttribute("data-fade-quiet", true);
}

function releaseStreamScrollFadeQuiet(wrap: HTMLElement): void {
  const until = streamFadeQuietUntil.get(wrap) ?? 0;
  if (Date.now() < until) return;
  streamFadeQuietUntil.delete(wrap);
  if (wrap.hasAttribute("data-fade-quiet")) {
    wrap.toggleAttribute("data-fade-quiet", false);
  }
}

function isUserInputEvent(event: Event): boolean {
  return event.isTrusted || import.meta.env.VITEST;
}

function streamScrollKeyDirection(event: KeyboardEvent): "up" | "down" | null {
  switch (event.key) {
    case "ArrowUp":
    case "PageUp":
    case "Home":
      return "up";
    case "ArrowDown":
    case "PageDown":
    case "End":
      return "down";
    case " ":
      return event.shiftKey ? "up" : "down";
    default:
      return null;
  }
}

function isEditableEventTarget(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    (target instanceof HTMLElement && target.isContentEditable)
  );
}

/** CSS chaining determines whether nested wheel input reaches chat. */
function targetsNestedScrollport(event: Event, streamTarget: HTMLElement): boolean {
  for (const target of event.composedPath()) {
    if (target === streamTarget) return false;
    if (!(target instanceof HTMLElement)) continue;
    const motion = scrollportMotionForHost(target);
    if (!motion) continue;
    if (event instanceof WheelEvent && wheelPassesThroughScrollport(motion.viewport, event)) continue;
    return true;
  }
  return false;
}

function isStreamSelecting(streamTarget: HTMLElement): boolean {
  if (typeof document === "undefined") return false;
  const selection = window.getSelection();
  if (!selection?.rangeCount || selection.isCollapsed) return false;
  const range = selection.getRangeAt(0);
  return (
    streamTarget.contains(range.commonAncestorContainer) ||
    streamTarget.contains(range.startContainer) ||
    streamTarget.contains(range.endContainer)
  );
}

/** Returns the last offset that still shows content. */
export function streamTailOffset(streamTarget: HTMLElement): number {
  const motion = scrollportMotionForViewport(streamTarget);
  if (motion) {
    ensureStreamTailPolicy(streamTarget);
    return motion.extent.tailOffsetY();
  }
  const naturalTail = Math.max(0, streamTarget.scrollHeight - scrollportClientHeight(streamTarget));
  const renderedTail = streamRenderedTranscriptTailOffset(streamTarget);
  if (renderedTail === null) return naturalTail;
  return Math.max(0, Math.min(naturalTail, renderedTail));
}

/** Resolve the explicit content end, excluding any range published past it. */
function streamRenderedTranscriptTailOffset(streamTarget: HTMLElement): number | null {
  const inner = streamTarget.querySelector<HTMLElement>(
    ".den-chat-stream-inner",
  );
  if (!inner) return null;

  const transcriptEnd = inner.querySelector<HTMLElement>(
    "[data-transcript-end]",
  );
  if (!transcriptEnd) return null;

  const body = inner.closest<HTMLElement>(".den-chat-stream-body");
  if (!body) return null;
  // Both boxes belong to the same scrolled layout. WebKit can report a newer
  // scrollTop than its client rects; mixing them makes the tail chase itself.
  const contentTop = body.getBoundingClientRect().top;
  const contentEnd = transcriptEnd.getBoundingClientRect().bottom;
  if (!Number.isFinite(contentTop) || !Number.isFinite(contentEnd)) {
    return null;
  }
  const tailClearance = Number.parseFloat(getComputedStyle(body).paddingBottom) || 0;
  return Math.max(
    0,
    contentEnd -
      contentTop +
      tailClearance -
      scrollportClientHeight(streamTarget),
  );
}

/** Scrollable content height, synthetic extent excluded. */
function streamContentHeight(streamTarget: HTMLElement): number {
  const motion = scrollportMotionForViewport(streamTarget);
  if (motion) return motion.extent.contentHeight();
  return streamTarget.scrollHeight;
}

const scrollVisualTimerByTarget = new WeakMap<
  HTMLElement,
  ReturnType<typeof setTimeout>
>();

const streamScrollHandlersByTarget = new WeakMap<HTMLElement, Map<() => void, object>>();
const streamScrollBindingCleanupByTarget = new WeakMap<
  HTMLElement,
  () => void
>();
const streamScrollDeliveries = new WeakMap<HTMLElement, () => void>();

function dispatchStreamScroll(streamTarget: HTMLElement): void {
  let dispatch = streamScrollDeliveries.get(streamTarget);
  if (!dispatch) {
    dispatch = () => {
      const handlers = streamScrollHandlersByTarget.get(streamTarget);
      if (!handlers) return;
      const deliver = () => {
        for (const [handler, subscription] of [...handlers]) {
          if (handlers.get(handler) === subscription) handler();
        }
      };
      const motion = scrollportMotionForViewport(streamTarget);
      if (motion) motion.extent.measureGeometry(deliver);
      else deliver();
    };
    streamScrollDeliveries.set(streamTarget, dispatch);
  }
  coalesceStreamScrollFrame(streamTarget, dispatch);
}

/** One coalesced delivery per frame for every scroll observer of the stream. */
export function subscribeStreamScroll(
  streamTarget: HTMLElement,
  handler: () => void,
): () => void {
  let handlers = streamScrollHandlersByTarget.get(streamTarget);
  if (!handlers) {
    handlers = new Map();
    streamScrollHandlersByTarget.set(streamTarget, handlers);
    streamScrollBindingCleanupByTarget.set(
      streamTarget,
      observeScrollportOffset(streamTarget, () => dispatchStreamScroll(streamTarget)),
    );
  }
  const subscription = handlers.get(handler) ?? {};
  handlers.set(handler, subscription);
  return () => {
    if (handlers?.get(handler) !== subscription) return;
    handlers.delete(handler);
    if (handlers.size !== 0) return;
    streamScrollBindingCleanupByTarget.get(streamTarget)?.();
    streamScrollBindingCleanupByTarget.delete(streamTarget);
    streamScrollHandlersByTarget.delete(streamTarget);
    const delivery = streamScrollDeliveries.get(streamTarget);
    if (delivery) cancelScrollportFrame(streamTarget, delivery);
    streamScrollDeliveries.delete(streamTarget);
  };
}

function isRecent(at: number | undefined): boolean {
  return at !== undefined && performance.now() - at <= STREAM_READER_INTENT_WINDOW_MS;
}

export type StreamReaderIntentHandlers = {
  following: () => boolean;
  /** The reader scrolled up, or selected transcript text. */
  stopFollowing: () => void;
  /** The reader requested the live tail. */
  resumeFollowing: () => void;
  /** Reader input able to move the stream. */
  onReaderInput: () => void;
};

function syncStreamReaderScroll(
  streamTarget: HTMLElement,
  handlers: StreamReaderIntentHandlers,
): void {
  if (
    isShellLayoutUnstable() ||
    !isPresented(streamTarget) ||
    streamTarget.clientHeight <= 0 ||
    streamTarget.closest('[data-resident="idle"]') !== null
  ) {
    return;
  }
  const state = streamEngagement(streamTarget);
  const scrollTop = streamTarget.scrollTop;
  const lastScrollTop = state.lastScrollTop ?? scrollTop;
  state.lastScrollTop = scrollTop;
  const scrollHeight = streamTarget.scrollHeight;
  const lastScrollHeight = state.lastScrollHeight ?? scrollHeight;
  state.lastScrollHeight = scrollHeight;
  if (scrollHeight !== lastScrollHeight) {
    denScrollDebugLog("scroll", "range-change", {
      from: lastScrollHeight,
      to: scrollHeight,
      delta: scrollHeight - lastScrollHeight,
      offset: Math.round(scrollTop),
      moved: Math.round(scrollTop - lastScrollTop),
    });
  }
  if (isStreamScrollEngagementSuppressed(streamTarget)) return;

  const tail = streamTailOffset(streamTarget);
  // Native scroll samples can exceed the painted transcript.
  if (scrollTop > tail + SCROLL_EPSILON_PX) {
    scrollportMotionForViewport(streamTarget)?.tail.reconcileTailBound();
  }

  const pressing = state.activePointer || state.scrollbarGesture === true;
  if (scrollTop < lastScrollTop - 1) {
    // Upward motion without reader input is a layout clamp or an application move. A clamp can
    // land after the range is restored (WebKit's interleaved size-container pass), so the pin
    // reclaims the tail here.
    if (!pressing && !isRecent(state.lastUpwardIntentAt)) {
      if (handlers.following()) reconcileStreamLayout(streamTarget);
      return;
    }
    if (isStreamScrollTraceVerbose()) {
      denScrollDebugLog("scroll", "observed", {
        from: Math.round(lastScrollTop),
        to: Math.round(scrollTop),
        maxTop: tail,
        pointer: state.activePointer,
      });
    }
    // Scrolling suppresses entry fades.
    markStreamScrollingVisual(streamTarget);
    if (!handlers.following()) return;
    denScrollDebugLog("scroll", "tail-released", {
      from: Math.round(lastScrollTop),
      to: Math.round(scrollTop),
      maxTop: tail,
      pointer: state.activePointer,
      scrollbar: state.scrollbarGesture === true,
    });
    handlers.stopFollowing();
    return;
  }

  const readerMovedDown =
    scrollTop > lastScrollTop + 1 &&
    !state.selectionPress &&
    (pressing || isRecent(state.lastDownwardIntentAt));
  if (
    readerMovedDown &&
    !handlers.following() &&
    tail - scrollTop <= STREAM_SCROLL_BOTTOM_THRESHOLD_PX
  ) {
    handlers.resumeFollowing();
  }
}

function markStreamScrollingVisual(streamTarget: HTMLElement): void {
  const wrap = streamTarget.closest(".den-chat-stream-wrap");
  if (!(wrap instanceof HTMLElement)) return;
  wrap.classList.add("den-chat-stream-wrap--scrolling");
  const prev = scrollVisualTimerByTarget.get(streamTarget);
  if (prev) clearTimeout(prev);
  scrollVisualTimerByTarget.set(
    streamTarget,
    setTimeout(() => {
      scrollVisualTimerByTarget.delete(streamTarget);
      wrap.classList.remove("den-chat-stream-wrap--scrolling");
    }, 120),
  );
}

/** Reader input controls following; geometry changes preserve it. */
export function watchStreamReaderIntent(
  streamTarget: HTMLElement,
  handlers: StreamReaderIntentHandlers,
): () => void {
  const state = streamEngagement(streamTarget);
  state.lastScrollTop = streamTarget.scrollTop;
  let lastTabAt: number | undefined;
  streamTarget.style.overflowAnchor = "none";

  const unsubscribeScroll = subscribeStreamScroll(streamTarget, () =>
    syncStreamReaderScroll(streamTarget, handlers),
  );

  const resumeDownwardIntent = () => {
    // At the native maximum a downward gesture emits no scroll event.
    if (!handlers.following() && isStreamNearBottom(streamTarget)) {
      handlers.resumeFollowing();
      reconcileStreamLayout(streamTarget);
    }
  };

  const onWheel = (event: WheelEvent) => {
    if (!isUserInputEvent(event) || event.ctrlKey) return;
    if (targetsNestedScrollport(event, streamTarget)) return;
    // A sideways swipe over wide code or tables is not a reading gesture.
    if (Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
    if (event.deltaY !== 0) state.suppressEngageUntil = 0;
    if (event.deltaY > 0) {
      state.lastUpwardIntentAt = undefined;
      state.lastDownwardIntentAt = performance.now();
      handlers.onReaderInput();
      resumeDownwardIntent();
      return;
    }
    // Only scrollable input starts the intent window.
    if (event.deltaY === 0 || !streamHasVerticalOverflow(streamTarget)) return;
    state.lastDownwardIntentAt = undefined;
    state.lastUpwardIntentAt = performance.now();
    cancelStreamSpringScroll(streamTarget);
    handlers.onReaderInput();
    if (handlers.following()) handlers.stopFollowing();
  };

  const isInteractiveTarget = (target: EventTarget | null): boolean => {
    return (
      target instanceof Element &&
      Boolean(
        target.closest(
          "button, a, input, textarea, [role='button'], .tabs__tab, .den-split-divider, summary, [data-disclosure-key]",
        ),
      )
    );
  };

  let pointerStartY: number | undefined;

  const onPointerDown = (event: Event) => {
    if (!isUserInputEvent(event) || isShellLayoutUnstable()) return;
    if (isInteractiveTarget(event.target)) return;
    state.suppressEngageUntil = 0;
    state.lastUpwardIntentAt = undefined;
    state.lastDownwardIntentAt = undefined;
    state.activePointer = true;
    const pointerType = "pointerType" in event ? (event as PointerEvent).pointerType : undefined;
    state.selectionPress = pointerType === "mouse" || pointerType === "pen";
    pointerStartY = "clientY" in event ? (event as MouseEvent).clientY : undefined;
    lastTabAt = undefined;
    // Pointer presses keep following until scrolling or selection begins.
    handlers.onReaderInput();
  };

  const onPointerMove = (event: Event) => {
    if (!state.activePointer || pointerStartY === undefined) return;
    const clientY = "clientY" in event ? (event as MouseEvent).clientY : undefined;
    if (clientY === undefined) return;
    const deltaY = clientY - pointerStartY;
    if (deltaY < -4) {
      state.lastDownwardIntentAt = undefined;
      state.lastUpwardIntentAt = performance.now();
      cancelStreamSpringScroll(streamTarget);
      handlers.onReaderInput();
    } else if (deltaY > 4) {
      if (state.selectionPress || isStreamSelecting(streamTarget)) {
        state.selectionPress = true;
        handlers.onReaderInput();
        return;
      }
      state.lastUpwardIntentAt = undefined;
      state.lastDownwardIntentAt = performance.now();
      handlers.onReaderInput();
      resumeDownwardIntent();
    }
  };

  const onPointerUp = () => {
    state.activePointer = false;
    state.selectionPress = false;
    pointerStartY = undefined;
  };

  const onKeyDown = (event: KeyboardEvent) => {
    if (!isUserInputEvent(event) || event.defaultPrevented) return;
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    const now = performance.now();
    if (event.key === "Tab") {
      lastTabAt = now;
      return;
    }
    if (isEditableEventTarget(event.target)) return;
    if (targetsNestedScrollport(event, streamTarget)) return;
    // Space activates the focused control.
    if (
      event.key === " " &&
      event.target instanceof Element &&
      event.target.closest("button, summary, a[href], [role='button']")
    ) {
      return;
    }
    const direction = streamScrollKeyDirection(event);
    if (!direction) return;
    // Scroll keys move the stream only while focus is in it or on the page.
    const target = event.target;
    if (
      target instanceof Node &&
      target !== document.body &&
      !streamTarget.contains(target)
    ) {
      return;
    }
    state.suppressEngageUntil = 0;
    handlers.onReaderInput();
    if (event.key === "End") {
      event.preventDefault();
      state.lastUpwardIntentAt = undefined;
      state.lastDownwardIntentAt = undefined;
      handlers.resumeFollowing();
      reconcileStreamLayout(streamTarget);
      return;
    }
    if (direction === "down") {
      state.lastUpwardIntentAt = undefined;
      state.lastDownwardIntentAt = now;
      resumeDownwardIntent();
      return;
    }
    state.lastDownwardIntentAt = undefined;
    state.lastUpwardIntentAt = now;
    cancelStreamSpringScroll(streamTarget);
    // Scroll keys use current content height, bypassing the cached fade sample.
    if (handlers.following() && measureStreamOverflow(streamTarget)) {
      handlers.stopFollowing();
    }
  };

  const onFocusIn = () => {
    if (!isRecent(lastTabAt)) return;
    // Tab focus can scroll the transcript without a wheel event.
    const now = performance.now();
    state.suppressEngageUntil = 0;
    state.lastUpwardIntentAt = now;
    state.lastDownwardIntentAt = now;
  };

  // Thumb drags and track clicks reach the scrollport frame, never the stream's own listeners.
  const onScrollportInput = (event: Event) => {
    const motion = scrollportMotionForViewport(streamTarget);
    if (!motion || event.target !== motion.host) return;
    if (motion.input.isThumbGestureActive()) {
      if (state.scrollbarGesture) return;
      state.scrollbarGesture = true;
      state.suppressEngageUntil = 0;
      state.lastUpwardIntentAt = undefined;
      state.lastDownwardIntentAt = undefined;
      cancelStreamSpringScroll(streamTarget);
      handlers.onReaderInput();
      return;
    }
    if (!state.scrollbarGesture) return;
    state.scrollbarGesture = false;
    // The gesture's last offset is observed after it ends, in either direction.
    const now = performance.now();
    state.lastUpwardIntentAt = now;
    state.lastDownwardIntentAt = now;
  };

  // Only a new selection releases following.
  let selecting = isStreamSelecting(streamTarget);
  const onSelectionChange = () => {
    const wasSelecting = selecting;
    selecting = isStreamSelecting(streamTarget);
    if (selecting && state.activePointer) state.selectionPress = true;
    if (selecting && !wasSelecting) {
      handlers.onReaderInput();
      if (handlers.following()) handlers.stopFollowing();
    }
  };

  streamTarget.addEventListener("wheel", onWheel, { passive: true });
  streamTarget.addEventListener("pointerdown", onPointerDown, { capture: true });
  streamTarget.addEventListener("touchstart", onPointerDown, {
    passive: true,
    capture: true,
  });
  document.addEventListener("pointermove", onPointerMove, {
    passive: true,
    capture: true,
  });
  document.addEventListener("touchmove", onPointerMove, {
    passive: true,
    capture: true,
  });
  document.addEventListener("pointerup", onPointerUp, { capture: true });
  document.addEventListener("pointercancel", onPointerUp, { capture: true });
  // A drag or a lost window can end the press without a pointerup.
  document.addEventListener("dragstart", onPointerUp, { capture: true });
  window.addEventListener("blur", onPointerUp);
  // Bubble phase: a control that handles its own keys prevents the default first.
  document.addEventListener("keydown", onKeyDown);
  streamTarget.addEventListener("focusin", onFocusIn);
  document.addEventListener("selectionchange", onSelectionChange);
  // The input event does not bubble; capture sees it on the way to the frame.
  document.addEventListener(DEN_SCROLLPORT_INPUT_EVENT, onScrollportInput, { capture: true });

  return () => {
    const visualTimer = scrollVisualTimerByTarget.get(streamTarget);
    if (visualTimer) clearTimeout(visualTimer);
    scrollVisualTimerByTarget.delete(streamTarget);
    unsubscribeScroll();
    streamTarget.removeEventListener("wheel", onWheel);
    streamTarget.removeEventListener("pointerdown", onPointerDown, true);
    streamTarget.removeEventListener("touchstart", onPointerDown, true);
    document.removeEventListener("pointermove", onPointerMove, true);
    document.removeEventListener("touchmove", onPointerMove, true);
    document.removeEventListener("pointerup", onPointerUp, true);
    document.removeEventListener("pointercancel", onPointerUp, true);
    document.removeEventListener("dragstart", onPointerUp, true);
    window.removeEventListener("blur", onPointerUp);
    document.removeEventListener("keydown", onKeyDown);
    streamTarget.removeEventListener("focusin", onFocusIn);
    document.removeEventListener("selectionchange", onSelectionChange);
    document.removeEventListener(DEN_SCROLLPORT_INPUT_EVENT, onScrollportInput, true);
    state.activePointer = false;
    state.selectionPress = false;
    state.scrollbarGesture = false;
    pointerStartY = undefined;
  };
}

export type StreamScrollEdges = {
  fadeTop: boolean;
  fadeBottom: boolean;
};

/** The rows element, or the stream itself before rows mount. */
export function streamTranscriptEl(streamTarget: HTMLElement): HTMLElement {
  const body = streamTarget.querySelector(".den-chat-stream-body");
  if (body instanceof HTMLElement) return body;
  return streamTarget;
}

export function syncStreamScrollbarLayout(streamTarget: HTMLElement | undefined): void {
  if (!streamTarget) return;
  const motion = scrollportMotionForViewport(streamTarget);
  if (!motion) return;
  // Signed updates skip the forced measurement during per-frame reconciliation.
  const { clientWidth, clientHeight, scrollWidth, scrollHeight } = streamTarget;
  updateThemedViewportScrollbar(
    motion.host,
    `${clientWidth}x${clientHeight}:${scrollWidth}x${scrollHeight}`,
  );
}

const TAB_PANEL_INSET_ATTR = "data-tab-panel-inset";

type StreamTabPanelInsetState = {
  active: boolean;
  insetPx: number;
};

export function readStreamTabPanelInset(
  streamTarget: HTMLElement,
): StreamTabPanelInsetState {
  const body = streamTranscriptEl(streamTarget);
  const raw = body.getAttribute(TAB_PANEL_INSET_ATTR);
  const insetPx = raw ? Number.parseInt(raw, 10) : 0;
  return {
    active: Number.isFinite(insetPx) && insetPx > 0,
    insetPx: Number.isFinite(insetPx) ? insetPx : 0,
  };
}

function streamNaturalScrollMaxTop(streamTarget: HTMLElement): number {
  const { insetPx } = readStreamTabPanelInset(streamTarget);
  return Math.max(0, streamTarget.scrollHeight - insetPx - streamTarget.clientHeight);
}

/** Returns the missing scroll range needed to clear the panel. */
function requiredStreamTabPanelInset(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): number {
  if (panelHeightPx <= 0) return 0;
  return Math.max(
    0,
    Math.ceil(panelHeightPx - streamNaturalScrollMaxTop(streamTarget)),
  );
}

export function streamTabPanelOverlayFits(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): boolean {
  return requiredStreamTabPanelInset(streamTarget, panelHeightPx) === 0;
}

function streamIsPastFirstViewport(streamTarget: HTMLElement): boolean {
  return streamTarget.scrollTop >= streamTarget.clientHeight - STREAM_SCROLL_TOP_THRESHOLD_PX;
}

function writeStreamTabPanelInset(
  streamTarget: HTMLElement,
  insetPx: number,
): void {
  const body = streamTranscriptEl(streamTarget);
  if (insetPx <= 0) {
    body.style.paddingTop = "";
    body.removeAttribute(TAB_PANEL_INSET_ATTR);
  } else {
    body.style.paddingTop = `${insetPx}px`;
    body.setAttribute(TAB_PANEL_INSET_ATTR, String(insetPx));
  }
}

export function clearStreamTabPanelInset(
  streamTarget: HTMLElement,
  opts?: { resetScroll?: boolean },
): void {
  const { active, insetPx } = readStreamTabPanelInset(streamTarget);
  if (!active) return;
  const scrollTop = streamTarget.scrollTop;
  writeStreamTabPanelInset(streamTarget, 0);
  syncStreamScrollbarLayout(streamTarget);
  if (
    opts?.resetScroll !== false &&
    scrollTop > 0 &&
    Math.abs(scrollTop - insetPx) <= STREAM_SCROLL_TOP_THRESHOLD_PX
  ) {
    commitStreamScroll(streamTarget, 0, "tab_inset");
  }
}

/** Adds the missing scroll range below the panel. */
export function applyStreamTabPanelInset(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): void {
  const required = requiredStreamTabPanelInset(streamTarget, panelHeightPx);
  const previous = readStreamTabPanelInset(streamTarget);
  if (required <= 0) {
    if (previous.active) clearStreamTabPanelInset(streamTarget);
    return;
  }
  if (previous.insetPx === required) return;
  writeStreamTabPanelInset(streamTarget, required);
  syncStreamScrollbarLayout(streamTarget);
}

function reconcileStreamTabPanelInset(
  streamTarget: HTMLElement,
  panelHeightPx: number,
): void {
  const required = requiredStreamTabPanelInset(streamTarget, panelHeightPx);
  const { active, insetPx } = readStreamTabPanelInset(streamTarget);
  // Taking runway back moves content under a reader still inside it.
  if (required < insetPx && !streamIsPastFirstViewport(streamTarget)) return;
  if (required <= 0) {
    if (active) clearStreamTabPanelInset(streamTarget, { resetScroll: false });
    return;
  }
  applyStreamTabPanelInset(streamTarget, panelHeightPx);
}

export type StreamTabPanelRetractOptions = {
  tabOpen: () => boolean;
  retracted: () => boolean;
  manualOpen?: () => boolean;
  onRetractedChange: (retracted: boolean) => void;
};

/** Project panel retraction from the canonical scroll offset. */
export function watchStreamTabPanelRetract(
  streamTarget: HTMLElement,
  opts: StreamTabPanelRetractOptions,
): () => void {
  const sync = () => {
    const atTop = isStreamNearTop(streamTarget);
    const manualOpen = opts.manualOpen?.() ?? false;
    const next = opts.tabOpen() && !manualOpen && atTop;
    const retracted = opts.retracted();
    if (next === retracted) return;
    denScrollDebugLog("scroll", "tab-panel-retraction", {
      scrollTop: Math.round(streamTarget.scrollTop),
      clientHeight: streamTarget.clientHeight,
      retracted,
      next,
      manualOpen,
      overflowAnchor: streamTarget.style.overflowAnchor,
    });
    opts.onRetractedChange(next);
  };

  const unsubscribe = subscribeStreamScroll(streamTarget, sync);
  sync();

  return unsubscribe;
}

export type StreamTabPanelInsetWatchOptions = {
  tabOpen: () => boolean;
  retracted: () => boolean;
  panelHeightPx: () => number;
};

export type StreamGeometryWatchOptions = StreamTabPanelInsetWatchOptions & {
  /** Runs inside the resize observation, before paint. */
  onGeometryChanged: (originChanged: boolean) => void;
};

/** Removes the synthetic runway once transcript content supplies enough room. */
export function watchStreamTabPanelInset(
  streamTarget: HTMLElement,
  opts: StreamTabPanelInsetWatchOptions,
): () => void {
  const sync = () => {
    if (isStreamScrollEngagementSuppressed(streamTarget)) return;
    const wrap = streamTarget.closest(".den-chat-stream-wrap");
    if (wrap?.classList.contains("den-chat-stream-wrap--scrolling")) return;
    if (!opts.tabOpen() || opts.retracted()) return;
    const height = opts.panelHeightPx();
    if (height <= 0) return;
    const inset = readStreamTabPanelInset(streamTarget);
    if (!inset.active && streamTabPanelOverlayFits(streamTarget, height)) return;
    reconcileStreamTabPanelInset(streamTarget, height);
  };
  return subscribeStreamScroll(streamTarget, sync);
}

/** Geometry reports precede paint; tab runway reconciliation follows next frame. */
export function watchStreamGeometry(
  streamTarget: HTMLElement,
  opts: StreamGeometryWatchOptions,
): () => void {
  let frame: number | undefined;
  const scheduleInset = () => {
    if (frame !== undefined) return;
    frame = requestAnimationFrame(() => {
      frame = undefined;
      if (
        !isStreamScrollEngagementSuppressed(streamTarget) &&
        opts.tabOpen() &&
        !opts.retracted()
      ) {
        const height = opts.panelHeightPx();
        if (height > 0) reconcileStreamTabPanelInset(streamTarget, height);
      }
    });
  };
  const paddingTop = new Map<Element, number>();
  const observer = new ResizeObserver((entries) => {
    let originChanged = false;
    for (const entry of entries) {
      const top = entry.contentRect.y;
      if (paddingTop.get(entry.target) === top) continue;
      paddingTop.set(entry.target, top);
      originChanged = true;
    }
    opts.onGeometryChanged(originChanged);
    scheduleInset();
  });
  observer.observe(streamTarget);
  const body = streamTranscriptEl(streamTarget);
  // Body padding contributes to the tail even when its content box is unchanged.
  if (body !== streamTarget) observer.observe(body, { box: "border-box" });
  return () => {
    observer.disconnect();
    if (frame !== undefined) cancelAnimationFrame(frame);
  };
}

/** Uses the shared overflow sample when available to avoid another layout read. */
function streamHasVerticalOverflow(streamTarget: HTMLElement): boolean {
  const cached = streamEngagement(streamTarget).cachedVerticalOverflow;
  if (cached !== undefined) return cached;
  // Content overflow excludes held scroll ranges.
  return measureStreamOverflow(streamTarget);
}

export function streamScrollEdges(streamTarget: HTMLElement): StreamScrollEdges {
  const overflow = measureStreamOverflow(streamTarget);
  streamEngagement(streamTarget).cachedVerticalOverflow = overflow;
  return {
    // Top fade only when content can scroll under the header.
    fadeTop: overflow && !isStreamNearTop(streamTarget),
    // Bottom fade for the full overflow range; body padding clears the tail band.
    fadeBottom: overflow,
  };
}

export function syncStreamScrollFade(streamTarget: HTMLElement | undefined): void {
  if (!streamTarget) return;
  const wrap = streamWrapForFade(streamTarget);
  if (!wrap) return;
  const { fadeTop, fadeBottom } = streamScrollEdges(streamTarget);
  if (wrap.hasAttribute("data-fade-top") !== fadeTop) {
    wrap.toggleAttribute("data-fade-top", fadeTop);
  }
  if (wrap.hasAttribute("data-fade-bottom") !== fadeBottom) {
    wrap.toggleAttribute("data-fade-bottom", fadeBottom);
  }
  releaseStreamScrollFadeQuiet(wrap);
}

/** Coalesced {@link syncStreamScrollFade} — one geometry pass per frame per stream. */
export function scheduleSyncStreamScrollFade(
  streamTarget: HTMLElement | undefined,
): void {
  if (!streamTarget) return;
  coalesceStreamScrollFrame(streamTarget, () =>
    syncStreamScrollFade(streamTarget),
  );
}

export function watchStreamScrollFade(streamTarget: HTMLElement): () => void {
  // Suppress unstable geometry during viewport attachment.
  suppressStreamScrollFade(streamTarget, 320);

  const syncFade = () => {
    if (isShellLayoutUnstable()) return;
    syncStreamScrollFade(streamTarget);
  };

  // Coalesce scroll + resize into one layout pass per frame.
  const bumpFade = () => coalesceStreamScrollFrame(streamTarget, syncFade);

  const unsubscribe = subscribeStreamScroll(streamTarget, bumpFade);
  bumpFade();

  const ro = new ResizeObserver(bumpFade);
  ro.observe(streamTarget);

  const stopSettle = onShellLayoutSettled(() => {
    syncStreamScrollFade(streamTarget);
    syncStreamScrollbarLayout(streamTarget);
  });

  const releaseTimer = window.setTimeout(() => {
    const wrap = streamWrapForFade(streamTarget);
    if (wrap) releaseStreamScrollFadeQuiet(wrap);
    syncStreamScrollFade(streamTarget);
  }, 320);

  return () => {
    window.clearTimeout(releaseTimer);
    unsubscribe();
    ro.disconnect();
    stopSettle();
  };
}

export function isStreamNearBottom(
  streamTarget: HTMLElement,
  threshold = STREAM_SCROLL_BOTTOM_THRESHOLD_PX,
): boolean {
  return streamTailOffset(streamTarget) - streamTarget.scrollTop <= threshold;
}
