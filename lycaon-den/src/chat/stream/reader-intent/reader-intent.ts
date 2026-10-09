import { isShellLayoutUnstable } from "../../../shell/shell-layout-busy.ts";
import { cancelStreamSpringScroll } from "../stream-scroll-spring.ts";
import { denScrollDebugLog, isStreamScrollTraceVerbose } from "../den-scroll-debug.ts";
import { scrollportMotionForHost, scrollportMotionForViewport, wheelPassesThroughScrollport } from "../../../platform/scrolling/scrollport-motion.ts";
import { DEN_SCROLLPORT_INPUT_EVENT, SCROLL_EPSILON_PX } from "../../../platform/scrolling/scrollport-motion-types.ts";
import { isPresented } from "../../../ui/presented.ts";
import { streamEngagement, isStreamScrollEngagementSuppressed } from "./reader-state.ts";
import { subscribeStreamScroll, streamTailOffset, reconcileStreamLayout, isStreamNearBottom, streamHasVerticalOverflow, measureStreamOverflow, STREAM_SCROLL_BOTTOM_THRESHOLD_PX } from "../stream-scroll.ts";
const STREAM_READER_INTENT_WINDOW_MS = 400;

const scrollVisualTimerByTarget = new WeakMap<
  HTMLElement,
  ReturnType<typeof setTimeout>
>();

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
