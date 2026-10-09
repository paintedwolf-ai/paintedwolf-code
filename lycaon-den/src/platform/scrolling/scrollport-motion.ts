import { cancelScrollportFrame, scheduleScrollportFrame } from "./scrollport-frame.ts";
import { animateScrollportReveal, type ScrollportRevealOptions } from "./scrollport-reveal.ts";
import { prefersReducedMotion } from "../interaction/reduced-motion.ts";
import { observeScrollportOffset } from "./scrollport-offset.ts";
import { ScrollportPressAnchor, type DeclaredHeightChange } from "./scrollport-press-anchor.ts";
import {
  denScrollDebugLog,
  isStreamScrollTraceVerbose,
} from "../../chat/stream/den-scroll-debug.ts";

/** Grace period for layout changes following native input. */
const DIRECT_INPUT_SETTLE_MS = 600;
/** Wheel and touch streams deliver an event every frame, so this much quiet means the scrolling thread stopped. */
const NATIVE_INPUT_QUIET_MS = 150;
/** Scroll geometry tolerance. */
export const SCROLL_EPSILON_PX = 1;

/** Fractional viewport heights keep the tail aligned during resizing. */
export function scrollportClientHeight(viewport: HTMLElement): number {
  const rounded = viewport.clientHeight;
  const box = viewport.getBoundingClientRect().height;
  if (!(box > 0)) return rounded;
  // Borders and native scrollbars round away identically from both reads.
  const chrome = Math.max(0, viewport.offsetHeight - rounded);
  return Math.max(0, box - chrome);
}

/** Native offsets can differ by half a device pixel. */
function devicePixelTolerance(view: Window | null): number {
  const ratio = view?.devicePixelRatio;
  return 0.5 / (ratio && ratio > 0 ? ratio : 1);
}

/** Maximum frames to wait for stable transcript geometry. */
const TAIL_BOUND_CONFIRM_FRAMES = 30;

/** Fired when direct scrollport input begins. */
export const DEN_SCROLLPORT_INPUT_EVENT = "den:scrollport-input";

/** Marks the spacer that holds range past the painted tail. */
export const SCROLLPORT_EXTENT_HOLD_ATTR = "data-scrollport-extent-hold";

export type ScrollportCommitSource =
  | "thumb_drag"
  | "track_click"
  | "repin_tail"
  | "restore_anchor"
  | "layout_compensation"
  /** Carries the offset with content that moved above it. */
  | "content_shift"
  | "reveal"
  | "tab_inset"
  | "jump"
  /** Clamps an unclaimed offset to the painted content end. */
  | "tail_bound";

export type ScrollportCommitPosition = { top: number; left: number };
type CommitHandler = (source: ScrollportCommitSource, position: ScrollportCommitPosition) => void;

const ANCHOR_ENDING_SOURCES: ReadonlySet<ScrollportCommitSource> = new Set([
  "thumb_drag",
  "track_click",
  "restore_anchor",
  "reveal",
  "jump",
]);

const motionByHost = new WeakMap<HTMLElement, ScrollportMotion>();
const motionByViewport = new WeakMap<HTMLElement, ScrollportMotion>();

/** Tail policy retained across scrollport bindings. */
export type ScrollportTailPolicy = {
  /** A stricter painted-content boundary than the scroll range. */
  resolveTail?: (naturalTailOffset: number) => number | null;
  /** Keeps the offset on the tail while it holds. */
  pinned?: () => boolean;
};

const tailPolicyByHost = new WeakMap<HTMLElement, ScrollportTailPolicy>();

class ScrollportMotion {
  readonly viewport: HTMLElement;
  readonly host: HTMLElement;
  readonly content: HTMLElement;

  private thumbGestureActive = false;
  private directInputActiveUntil = 0;
  private inputSettledTimer: ReturnType<typeof setTimeout> | undefined;
  private inputSettledDeadline = 0;
  private readonly inputSettledHandlers = new Set<() => void>();
  private extentHoldEl: HTMLElement | null = null;
  private extentHoldAppliedPx = 0;
  /** Spacer contribution to scrollHeight, including layout gaps. */
  private extentHoldContributionPx = 0;
  /** The bound content absorbs a spacer, so this binding cannot retain range. */
  private extentHoldUnsupported = false;
  /** Geometry is valid only until the next layout write or outer pass ends. */
  private geometryCache: Partial<
    Record<"scrollHeight" | "scrollWidth" | "clientHeight" | "clientWidth" | "exactClientHeight", number>
  > = {};
  private layoutReconcileFrame: number | undefined;
  private tailOffsetCache: number | undefined;
  private measureDepth = 0;
  private extentReclaimRequest: object | undefined;
  /** A pending tail confirmation: frames left, and the overrun the last sample found. */
  private tailBound: { attempts: number; sampled: number } | undefined;
  private tailBoundTimer: ReturnType<typeof setTimeout> | undefined;
  /** Geometry snapshot for contraction detection. */
  private lastNaturalHeight = 0;
  private lastObservedOffsetY = 0;
  /** Once released to the tail, further contraction may move the native maximum. */
  private tailRangeReleased = false;
  /** Subpixel content movement not yet represented by the native offset. */
  private contentShiftRemainder = 0;
  /** Touch input suspends tail pinning until it settles. */
  private touchInputActiveUntil = 0;
  private tailPin: (() => boolean) | null = null;
  private tailOffsetResolver:
    | ((naturalTailOffset: number) => number | null)
    | undefined;
  private readonly commitHandlers = new Map<
    CommitHandler, object
  >();
  private readonly extentHandlers = new Set<() => void>();
  private disposed = false;
  private reveal: ReturnType<typeof animateScrollportReveal> | undefined;
  private readonly stopOffsetObservation: () => void;
  private readonly pressAnchor: ScrollportPressAnchor;
  /** The tail pin waits while a glide returns retained range. */
  private glidingToTail = false;
  /**
   * Natural heights that pressed collapses will contract to, reserved when each is declared so
   * the range already covers the held offset whenever its layout lands.
   */
  private readonly reservedContractions = new Set<{ floor: number }>();
  private nativeInputQuietTimer: ReturnType<typeof setTimeout> | undefined;
  private nativeInputQuietAt = 0;
  private pointerReseatArmed = false;

  constructor(host: HTMLElement, viewport: HTMLElement, content: HTMLElement) {
    this.host = host;
    this.viewport = viewport;
    this.content = content;
    // A rebound scrollport inherits the offset it is already showing.
    this.lastObservedOffsetY = viewport.scrollTop;
    this.host.addEventListener("pointerdown", this.cancelReveal);
    this.host.addEventListener("keydown", this.cancelReveal);
    this.stopOffsetObservation = observeScrollportOffset(viewport, this.noteObservedOffset);
    this.pressAnchor = new ScrollportPressAnchor(host, viewport, {
      carry: (delta) => this.carryAnchoredContent(delta),
      holdsRange: () => this.viewport.scrollTop > this.tailOffsetY() + SCROLL_EPSILON_PX,
      released: () => this.scheduleRetainedExtentReclaim(),
    });
  }

  /**
   * Declares a height change; one started by a pointer press keeps the pressed control in place.
   * A collapse's contraction is reserved before its first frame, because WebKit can land it after
   * frame callbacks and paint the clamped offset.
   */
  declareHeightChange(shell: HTMLElement, opts?: { contraction?: number }): DeclaredHeightChange {
    const change = this.pressAnchor.declare(shell);
    const contraction = opts?.contraction ?? 0;
    if (!this.pressAnchor.active || contraction <= SCROLL_EPSILON_PX || !this.canRetainExtent()) return change;
    const reservation = { floor: 0 };
    this.measureGeometry(() => {
      reservation.floor = Math.max(0, this.contentHeight() - contraction);
      this.reservedContractions.add(reservation);
      this.publishRetainedExtent();
    });
    return {
      end: () => {
        const reserved = this.reservedContractions.delete(reservation);
        change.end();
        // The held offset keeps whatever range it still needs; the rest settles.
        if (reserved) this.scheduleRetainedExtentReclaim();
      },
    };
  }

  private carryAnchoredContent(delta: number): void {
    this.measureGeometry(() => {
      this.commitShift(delta, this.viewport.scrollTop, "content_shift");
      this.lastObservedOffsetY = this.viewport.scrollTop;
    });
  }

  /** Last offset the scrollport actually reported, clamps included. */
  private noteObservedOffset = (offset: number): void => {
    const previous = this.lastObservedOffsetY;
    this.lastObservedOffsetY = offset;
    if (offset >= previous - SCROLL_EPSILON_PX) return;
    this.restoreClampedOffset(previous);
  };

  /** Latest captured or committed offset, independent of deferred virtual rendering. */
  offsetY(): number {
    return this.lastObservedOffsetY;
  }

  /** Native clamps retain their prior offset until geometry settles. */
  private restoreClampedOffset(previous: number): void {
    if (!this.canRetainExtent()) return;
    if (!this.pressAnchor.active && (this.isTailPinned() || this.tailRangeReleased)) return;
    // Thumb dragging sets an absolute offset.
    if (this.thumbGestureActive) return;
    this.measureGeometry(() => {
      const publishedMax = Math.max(
        0,
        this.geometry("scrollHeight") - this.geometry("clientHeight"),
      );
      if (previous <= publishedMax + SCROLL_EPSILON_PX) return;
      if (
        Math.abs(this.viewport.scrollTop - publishedMax) > SCROLL_EPSILON_PX
      ) {
        return;
      }
      this.commit(previous, "layout_compensation");
      this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
    });
  }

  /**
   * Sole application writer for this scrollport: applies a destination at once. Motion is
   * `revealOffset`, because a write during a native smooth scroll leaves WebKit painting one
   * offset while hit testing another.
   */
  commit(
    nextOffset: number,
    source: ScrollportCommitSource,
    opts?: {
      axis?: "x" | "y" | "both";
      /** Known range avoids another layout read. */
      measuredMaxOffset?: number;
    },
  ): void {
    this.write({ offset: nextOffset }, source, opts);
  }

  /** Adjusts offset for content moved by delta above viewport, preserving off-thread travel. */
  commitShift(
    delta: number,
    fromOffset: number,
    source: ScrollportCommitSource,
  ): number {
    return this.measureGeometry(() => {
      const lostOffset = fromOffset - this.viewport.scrollTop;
      // A measured offset beyond the native range proves a clamp, even for a one-pixel shrink.
      const reclaimed = lostOffset > 0 &&
        (fromOffset > this.nativeMaxOffsetY() ||
          (!this.isDirectInputActive() && lostOffset > SCROLL_EPSILON_PX));
      if (reclaimed) {
        return this.write({ offset: Math.max(0, fromOffset + delta) }, source) - fromOffset;
      }
      const before = this.viewport.scrollTop;
      const moved = this.write({ delta }, source) - before;
      // Quantized offsets land a fraction either side of the request; that fraction is this
      // correction's to carry. A whole pixel beyond it is travel the scrolling thread made.
      if (Math.abs(moved - delta) < 1) return moved;
      return delta >= 0 ? Math.min(delta, Math.max(0, moved)) : Math.max(delta, Math.min(0, moved));
    });
  }

  private write(
    request: { offset: number } | { delta: number },
    source: ScrollportCommitSource,
    opts?: {
      axis?: "x" | "y" | "both";
      /** Known range avoids another layout read. */
      measuredMaxOffset?: number;
    },
  ): number {
    // Deliberate moves end a press anchor; the tail pin waits for it and its settle glide.
    if (ANCHOR_ENDING_SOURCES.has(source)) this.pressAnchor.end();
    else if (source === "repin_tail" && (this.pressAnchor.active || this.glidingToTail || this.nativeStreamLive())) {
      // A native stream drives the offset; the pin lands once it goes quiet.
      return this.viewport.scrollTop;
    }
    return this.measureGeometry(() => {
      if (source === "thumb_drag" && opts?.measuredMaxOffset === undefined) {
        this.publishRetainedExtent();
      }

      // A relative write moves one axis and lands wherever the scroller already was.
      const relative = "delta" in request;
      const axis = relative ? "y" : opts?.axis ?? "y";
      const beforeX = this.viewport.scrollLeft;
      const beforeY = this.viewport.scrollTop;
      const wanted = relative ? beforeY + request.delta : request.offset;
      const holdsTarget =
        source === "layout_compensation" || source === "content_shift";
      if (holdsTarget && axis !== "x") {
        this.publishLayoutCompensationRange(wanted);
      }
      const measuredMax = opts?.measuredMaxOffset;
      const maxX = axis === "y"
        ? beforeX
        : axis === "x" && measuredMax !== undefined
          ? measuredMax
          : this.maxOffsetX();
      // A caller's measured range already includes any retained extent, so the tail needs no read.
      const maxY = axis === "x"
        ? beforeY
        : axis === "y" && measuredMax !== undefined
          ? measuredMax
          : this.commitClampMaxY(source, holdsTarget ? wanted : 0);
      const nextX =
        axis === "y" ? beforeX : Math.max(0, Math.min(maxX, wanted));
      const nextY =
        axis === "x" ? beforeY : Math.max(0, Math.min(maxY, wanted));

      if (isStreamScrollTraceVerbose()) {
        denScrollDebugLog("scroll", "commit", {
          source,
          from: Math.round(beforeY),
          to: Math.round(nextY),
          delta: relative ? Math.round(request.delta) : undefined,
          maxTop: maxY,
          clientHeight: this.geometry("clientHeight"),
          scrollHeight: this.viewport.scrollHeight,
          extentHold: this.extentHoldAppliedPx,
          thumbDrag: this.thumbGestureActive ? 1 : 0,
          directInput: this.isDirectInputActive() ? 1 : 0,
        });
      }

      if (axis !== "x") {
        if (source !== "content_shift" && source !== "layout_compensation") this.contentShiftRemainder = 0;
        if (source === "tail_bound") this.tailRangeReleased = true;
        else if (source !== "layout_compensation") this.tailRangeReleased = false;
      }
      if (relative ? request.delta === 0 : beforeX === nextX && beforeY === nextY) return beforeY;

      if (relative) {
        // The scroller clamps a relative move itself, against the position it holds.
        this.viewport.scrollBy(0, request.delta);
      } else if (axis === "both") {
        this.viewport.scrollLeft = nextX;
        this.viewport.scrollTop = nextY;
      } else if (axis === "x") {
        this.viewport.scrollLeft = nextX;
      } else {
        this.viewport.scrollTop = nextY;
      }
      // Synchronize scroller geometry after programmatic offset changes.
      void this.viewport.offsetHeight;
      // Native layout can clamp or reject an absolute write without emitting a scroll event.
      const landedY = this.viewport.scrollTop;
      // Restore point for native clamps during contraction.
      if (axis !== "x") this.lastObservedOffsetY = landedY;
      // Recompute retained extent after every offset change.
      if (this.extentHoldAppliedPx > 0) {
        this.measureGeometry(() => this.publishRetainedExtent());
      }
      const position = { top: landedY, left: this.viewport.scrollLeft };
      for (const [handler, subscription] of [...this.commitHandlers]) {
        if (this.commitHandlers.get(handler) === subscription) handler(source, position);
      }
      // Editor paint can change measured content after an offset commit.
      if (this.commitHandlers.size > 0) this.invalidateGeometry();
      return landedY;
    });
  }

  /** Reader input cancels motion and any pending viewport preparation. */
  revealOffset(offset: number, options: ScrollportRevealOptions = {}): Promise<boolean> {
    this.cancelReveal();
    if (this.disposed) return Promise.resolve(false);
    const coordinates = options.coordinates ?? {
      read: () => this.viewport.scrollTop,
      publish: (offset: number) => this.commit(offset, "reveal"),
    };
    const reveal = animateScrollportReveal({
      ...options, coordinates,
      target: () => options.coordinates ? offset : Math.min(this.maxOffsetY(), offset),
      reducedMotion: prefersReducedMotion(this.viewport.ownerDocument.defaultView),
    });
    this.reveal = reveal;
    reveal.begin();
    return reveal.finished.finally(() => {
      if (this.reveal === reveal) this.reveal = undefined;
    });
  }

  cancelReveal = (): void => {
    this.reveal?.cancel();
    this.reveal = undefined;
  };

  revealElement(
    element: Element,
    opts?: { block?: ScrollLogicalPosition; glide?: boolean },
  ): void {
    const viewportRect = this.viewport.getBoundingClientRect();
    const elementRect = element.getBoundingClientRect();
    const block = opts?.block ?? "nearest";
    let delta = 0;
    if (block === "center") {
      delta =
        elementRect.top -
        viewportRect.top -
        (viewportRect.height - elementRect.height) / 2;
    } else if (block === "end") {
      delta = elementRect.bottom - viewportRect.bottom;
    } else if (block === "start") {
      delta = elementRect.top - viewportRect.top;
    } else if (elementRect.top < viewportRect.top) {
      delta = elementRect.top - viewportRect.top;
    } else if (elementRect.bottom > viewportRect.bottom) {
      delta = elementRect.bottom - viewportRect.bottom;
    }
    if (Math.abs(delta) < SCROLL_EPSILON_PX) return;
    this.cancelApplicationMotion();
    if (opts?.glide) void this.revealOffset(this.viewport.scrollTop + delta);
    else this.commit(this.viewport.scrollTop + delta, "reveal");
  }

  /**
   * Carries the offset with content that moved above the reader, except during an
   * absolute-position thumb drag. `fromOffset` is the offset the caller measured against;
   * the write itself is relative, so in-flight scrolling keeps its travel.
   */
  shiftContent(deltaY: number, fromOffset: number): number {
    if (!Number.isFinite(deltaY) || !Number.isFinite(fromOffset)) return 0;
    if (this.thumbGestureActive) {
      denScrollDebugLog("scroll", "content-shift", {
        from: Math.round(this.viewport.scrollTop),
        delta: Math.round(deltaY),
        applied: 0,
        thumbDrag: 1,
      });
      return 0;
    }
    return this.measureGeometry(() => {
      const before = this.viewport.scrollTop;
      if (this.pinControlsOffset()) {
        // The tail pin absorbs content shifts.
        this.invalidateGeometry();
        this.reconcilePinnedTail();
        return this.viewport.scrollTop - before;
      }
      const requested = deltaY + this.contentShiftRemainder;
      const applied = this.commitShift(requested, fromOffset, "content_shift");
      this.lastObservedOffsetY = this.viewport.scrollTop;
      const remainder = requested - applied;
      // Boundary clamps discard excess fractional movement.
      this.contentShiftRemainder = Math.abs(remainder) < 1 ? remainder : 0;
      denScrollDebugLog("scroll", "content-shift", {
        from: Math.round(before),
        delta: Math.round(deltaY),
        applied: Math.round(applied),
      });
      this.scheduleRetainedExtentReclaim();
      return applied;
    });
  }

  /** Preserves the native scroll offset until input settles. */
  noteNativeInput(kind: "wheel" | "touch"): void {
    // The stream drives the offset again; its settle re-arms the reseat.
    this.disarmPointerReseat();
    this.noteDirectInputActivity();
    if (kind === "touch") {
      this.touchInputActiveUntil = performance.now() + DIRECT_INPUT_SETTLE_MS;
    }
    this.dispatchDirectInput();
    // Reclaim resizes any open hold; an unheld scrollport needs no geometry for an input event.
    this.scheduleRetainedExtentReclaim();
    this.nativeInputQuietAt = performance.now() + NATIVE_INPUT_QUIET_MS;
    // A pending timer re-arms itself until the stream goes quiet.
    if (this.nativeInputQuietTimer === undefined) {
      this.nativeInputQuietTimer = setTimeout(this.settleNativeStream, NATIVE_INPUT_QUIET_MS);
    }
  }

  /** Whether WebKit's scrolling thread may still be applying a native wheel or touch stream. */
  private nativeStreamLive(): boolean {
    const now = performance.now();
    return now < this.nativeInputQuietAt || now < this.touchInputActiveUntil;
  }

  /** Tail writes wait for native input to settle before correcting contracted offsets. */
  private readonly settleNativeStream = (): void => {
    this.nativeInputQuietTimer = undefined;
    if (this.disposed || this.thumbGestureActive) return;
    const remaining = Math.max(this.nativeInputQuietAt, this.touchInputActiveUntil) - performance.now();
    if (remaining > 0) {
      this.nativeInputQuietTimer = setTimeout(this.settleNativeStream, Math.ceil(remaining));
      return;
    }
    this.invalidateGeometry();
    this.measureGeometry(() => {
      this.reconcilePinnedTail();
      const offset = this.viewport.scrollTop;
      if (offset < 1 || offset < this.nativeMaxOffsetY() - SCROLL_EPSILON_PX) return;
      this.reseatScrollingThread("range-end-reseat");
    });
    this.armPointerReseat();
  };

  /**
   * Writes the offset one pixel away and back, which re-seats WebKit's scrolling thread on the main
   * thread's offset; WebKit ignores a same-value write. Both writes land before anything paints.
   */
  private reseatScrollingThread(event: "range-end-reseat" | "pointer-reseat"): void {
    const offset = this.viewport.scrollTop;
    const away = offset >= 1 ? offset - 1 : offset + 1;
    if (away > this.nativeMaxOffsetY()) return;
    this.viewport.scrollTop = away;
    this.viewport.scrollTop = offset;
    if (isStreamScrollTraceVerbose()) {
      denScrollDebugLog("scroll", event, { offset: Math.round(offset) });
    }
  }

  /**
   * Painting and hit testing can still part after a stream, so the first pointer movement after
   * one re-seats the scrolling thread. A press is hit tested before its handlers run.
   */
  private armPointerReseat(): void {
    if (this.pointerReseatArmed) return;
    this.pointerReseatArmed = true;
    this.host.addEventListener("pointermove", this.onPointerReseat, { passive: true });
  }

  private disarmPointerReseat(): void {
    if (!this.pointerReseatArmed) return;
    this.pointerReseatArmed = false;
    this.host.removeEventListener("pointermove", this.onPointerReseat);
    cancelScrollportFrame(this.viewport, this.pointerReseat);
  }

  private readonly onPointerReseat = (): void => {
    this.host.removeEventListener("pointermove", this.onPointerReseat);
    // The frame's measure phase shares the layout its other reads already pay for.
    scheduleScrollportFrame(this.viewport, "measure", this.pointerReseat);
  };

  private readonly pointerReseat = (): void => {
    this.pointerReseatArmed = false;
    if (this.disposed || this.thumbGestureActive || this.nativeStreamLive()) return;
    this.measureGeometry(() => this.reseatScrollingThread("pointer-reseat"));
  };

  beginThumbGesture(): void {
    this.thumbGestureActive = true;
    this.noteDirectInputActivity();
    // A gesture cannot reclaim, so an open hold is resized as the drag begins.
    this.resizeRetainedExtent();
    this.dispatchDirectInput();
  }

  subscribeCommits(
    handler: CommitHandler,
  ): () => void {
    const subscription = this.commitHandlers.get(handler) ?? {};
    this.commitHandlers.set(handler, subscription);
    return () => {
      if (this.commitHandlers.get(handler) === subscription) this.commitHandlers.delete(handler);
    };
  }

  /** The retained extent changed, so the scroll range and content disagree. */
  subscribeExtent(handler: () => void): () => void {
    this.extentHandlers.add(handler);
    return () => {
      this.extentHandlers.delete(handler);
    };
  }

  /** Reports the end of the direct-input grace period. */
  subscribeInputSettled(handler: () => void): () => void {
    this.inputSettledHandlers.add(handler);
    if (this.isDirectInputActive()) this.scheduleInputSettled();
    return () => {
      this.inputSettledHandlers.delete(handler);
      if (!this.inputSettledHandlers.size) this.cancelInputSettled();
    };
  }

  private scheduleInputSettled(): void {
    if (this.disposed || !this.inputSettledHandlers.size || this.thumbGestureActive) {
      this.cancelInputSettled();
      return;
    }
    const deadline = Math.max(performance.now(), this.directInputActiveUntil);
    // A pending timer that fires no later than the deadline re-arms itself while input stays active.
    if (this.inputSettledTimer !== undefined) {
      if (this.inputSettledDeadline <= deadline) return;
      clearTimeout(this.inputSettledTimer);
    }
    this.inputSettledDeadline = deadline;
    this.inputSettledTimer = setTimeout(() => {
      this.inputSettledTimer = undefined;
      if (this.isDirectInputActive()) { this.scheduleInputSettled(); return; }
      for (const handler of this.inputSettledHandlers) handler();
    }, Math.max(0, Math.ceil(deadline - performance.now())));
  }

  private cancelInputSettled(): void {
    if (this.inputSettledTimer === undefined) return;
    clearTimeout(this.inputSettledTimer);
    this.inputSettledTimer = undefined;
  }

  endThumbGesture(): void {
    this.thumbGestureActive = false;
    this.noteDirectInputActivity();
    this.dispatchDirectInput();
    this.scheduleRetainedExtentReclaim();
  }

  /** Ends the direct-input claim before its settle window closes. */
  interruptDirectInput(): void {
    if (isStreamScrollTraceVerbose()) {
      denScrollDebugLog("scroll", "direct-input-interrupted", {
        from: Math.round(this.viewport.scrollTop),
      });
    }
    this.directInputActiveUntil = 0;
    this.touchInputActiveUntil = 0;
    this.nativeInputQuietAt = 0;
    this.scheduleInputSettled();
    this.scheduleRetainedExtentReclaim();
  }

  cancelApplicationMotion(): void {
    this.pressAnchor.end();
    this.cancelReveal();
    if (isStreamScrollTraceVerbose()) {
      denScrollDebugLog("scroll", "application-motion-cancelled", {
        from: Math.round(this.viewport.scrollTop),
      });
    }
    this.thumbGestureActive = false;
    this.directInputActiveUntil = 0;
    this.touchInputActiveUntil = 0;
    this.nativeInputQuietAt = 0;
    this.scheduleInputSettled();
    // Preserve the current offset until natural content reaches it.
    this.scheduleRetainedExtentReclaim();
  }

  dispose(): void {
    this.cancelReveal();
    this.host.removeEventListener("pointerdown", this.cancelReveal);
    this.host.removeEventListener("keydown", this.cancelReveal);
    this.host.removeEventListener("wheel", this.refuseInputPastTail);
    if (this.nativeInputQuietTimer !== undefined) clearTimeout(this.nativeInputQuietTimer);
    this.disarmPointerReseat();
    this.disposed = true;
    this.cancelInputSettled();
    this.inputSettledHandlers.clear();
    this.cancelLayoutReconcile();
    this.tailPin = null;
    this.touchInputActiveUntil = 0;
    this.nativeInputQuietAt = 0;
    this.commitHandlers.clear();
    this.extentHandlers.clear();
    this.cancelRetainedExtentReclaim();
    this.cancelTailBoundReconcile();
    this.thumbGestureActive = false;
    this.directInputActiveUntil = 0;
    this.stopOffsetObservation();
    this.pressAnchor.dispose();
    this.clearExtentHoldSpacer();
  }

  /** A thumb drag commits every offset from script, so no scroll thread runs ahead of rendering. */
  isThumbGestureActive(): boolean {
    return this.thumbGestureActive;
  }

  /** Keep direct input active while its delayed geometry settles. */
  isDirectInputActive(): boolean {
    return (
      this.thumbGestureActive || performance.now() < this.directInputActiveUntil
    );
  }

  /** Thumb gestures and live wheel or touch streams suspend the pin. */
  setTailPin(pin: (() => boolean) | null): void {
    this.tailPin = pin;
  }

  applyTailPolicy(policy: ScrollportTailPolicy | undefined): void {
    this.setTailOffsetResolver(policy?.resolveTail);
    this.setTailPin(policy?.pinned ?? null);
  }

  private isTailPinned(): boolean {
    return this.tailPin?.() ?? false;
  }

  private gestureControlsOffset(): boolean {
    return this.thumbGestureActive || this.nativeStreamLive();
  }

  /** Gestures, press anchors, and their settle glide suspend the tail pin. */
  private pinControlsOffset(): boolean {
    return (
      this.isTailPinned() &&
      !this.gestureControlsOffset() &&
      !this.pressAnchor.active &&
      !this.glidingToTail
    );
  }

  /** Reactive geometry changes share one reconciliation before the next paint. */
  scheduleLayoutReconcile(): void {
    this.invalidateGeometry();
    if (this.disposed || this.layoutReconcileFrame !== undefined) return;
    this.layoutReconcileFrame = requestAnimationFrame(() => {
      this.layoutReconcileFrame = undefined;
      if (!this.disposed) this.notifyLayoutMutated();
    });
  }

  private cancelLayoutReconcile(): void {
    if (this.layoutReconcileFrame === undefined) return;
    cancelAnimationFrame(this.layoutReconcileFrame);
    this.layoutReconcileFrame = undefined;
  }

  notifyLayoutMutated(): void {
    // A resize observation already supplies the settled layout for this frame.
    this.cancelLayoutReconcile();
    this.invalidateGeometry();
    this.measureGeometry(() => {
      this.refreshRetainedExtent();
      this.pressAnchor.noteLayout();
      this.reconcilePinnedTail();
      // Releases a held offset once geometry stops moving.
      if (this.tailCanRecedeSilently()) {
        this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
      }
    });
  }

  /** Retained extent or a stricter boundary can leave the offset past the tail. */
  private tailCanRecedeSilently(): boolean {
    return (
      this.tailOffsetResolver !== undefined ||
      this.extentHoldAppliedPx > 0 ||
      this.isDirectInputActive()
    );
  }

  /** Pins the tail before paint within native offset precision. */
  private reconcilePinnedTail(): void {
    if (!this.pinControlsOffset()) return;
    const tail = this.tailOffsetY();
    const tolerance = devicePixelTolerance(this.viewport.ownerDocument.defaultView);
    if (Math.abs(this.viewport.scrollTop - tail) <= tolerance) return;
    this.commit(tail, "repin_tail");
  }

  /** Whether a live claim can extend the scroll range beyond content. */
  private hasRetentionClaim(): boolean {
    return (
      this.isTailPinned() ||
      this.pressAnchor.active ||
      this.reservedContractions.size > 0 ||
      this.extentHoldAppliedPx > 0 ||
      this.isDirectInputActive()
    );
  }

  /** Content that absorbs the spacer rules out retained range. */
  private canRetainExtent(): boolean {
    return !this.extentHoldUnsupported;
  }

  private hostLabel(): string {
    return (
      this.host.dataset.testid ??
      `${this.host.tagName.toLowerCase()}.${this.host.classList[0] ?? ""}`
    );
  }

  /** Measures changed layout, so a claim may open a hold before the frame paints. */
  private refreshRetainedExtent(): void {
    // Unclaimed range requires no layout measurement.
    if (!this.canRetainExtent() || !this.hasRetentionClaim()) return;
    this.measureGeometry(() => {
      this.noteLayoutContraction(this.contentHeight());
      this.publishRetainedExtent();
    });
  }

  /** Only existing extent holds need measurement after input. */
  private resizeRetainedExtent(): void {
    if (this.extentHoldAppliedPx === 0) return;
    this.refreshRetainedExtent();
  }

  /** Records contractions from the caller's geometry sample. */
  private noteLayoutContraction(natural: number): void {
    const previous = this.lastNaturalHeight;
    this.lastNaturalHeight = natural;
    if (previous <= 0 || natural >= previous) return;
    const offset = Math.max(this.lastObservedOffsetY, this.viewport.scrollTop);
    const viewportHeight = this.geometry("clientHeight");
    if (offset + viewportHeight <= natural + SCROLL_EPSILON_PX) return;
    denScrollDebugLog("scroll", "layout-contraction", {
      host: this.hostLabel(),
      from: Math.round(previous),
      to: Math.round(natural),
      lost: Math.round(previous - natural),
      offset: Math.round(offset),
      clientHeight: viewportHeight,
      hold: Math.round(this.extentHoldAppliedPx),
      tailPinned: this.isTailPinned(),
      directInput: this.isDirectInputActive(),
    });
  }

  private dispatchDirectInput(): void {
    this.host.dispatchEvent(
      new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT, { bubbles: false }),
    );
  }

  private noteDirectInputActivity(now = performance.now()): void {
    // Direct input resets the anchor and its fractional compensation.
    this.pressAnchor.end();
    this.contentShiftRemainder = 0;
    this.tailRangeReleased = false;
    this.cancelReveal();
    this.directInputActiveUntil = Math.max(
      this.directInputActiveUntil,
      now + DIRECT_INPUT_SETTLE_MS,
    );
    this.scheduleInputSettled();
  }

  /** Highest representable offset, including retained extent. */
  maxOffsetY(): number {
    return this.measureGeometry(() => {
      return Math.max(this.tailOffsetY(), this.viewport.scrollTop);
    });
  }

  /** Highest offset that still shows real content. */
  tailOffsetY(): number {
    if (this.measureDepth > 0 && this.tailOffsetCache !== undefined) return this.tailOffsetCache;
    const naturalTail = Math.max(
      0,
      this.contentHeight() - this.exactClientHeight(),
    );
    const resolved = this.tailOffsetResolver?.(naturalTail);
    const tail = resolved === null || resolved === undefined || !Number.isFinite(resolved)
      ? naturalTail : Math.max(0, Math.min(naturalTail, resolved));
    if (this.measureDepth > 0) this.tailOffsetCache = tail;
    return tail;
  }

  /** Supply a stricter painted-content boundary for a specialized scrollport. */
  setTailOffsetResolver(
    resolver: ((naturalTailOffset: number) => number | null) | undefined,
  ): void {
    this.tailOffsetResolver = resolver;
    this.invalidateGeometry();
    // Boundary changes require a fresh tail measurement before offset correction.
    this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
  }

  /** Scrollable height of real content, extent hold excluded. */
  contentHeight(): number {
    return Math.max(0, this.geometry("scrollHeight") - this.extentHoldContributionPx);
  }

  /** Where input may rest: the painted tail, or an offset a hold still keeps. */
  inputMaxOffsetY(): number {
    return Math.max(this.viewport.scrollTop, this.tailOffsetY());
  }

  /** Highest offset the scroller itself can hold, retained extent spacer included. */
  private nativeMaxOffsetY(): number {
    return Math.max(0, this.geometry("scrollHeight") - this.exactClientHeight());
  }

  maxOffsetX(): number {
    return Math.max(0, this.geometry("scrollWidth") - this.geometry("clientWidth"));
  }

  private publishLayoutCompensationRange(offset: number): void {
    if (!Number.isFinite(offset)) return;
    this.measureGeometry(() => this.publishRetainedExtent(offset));
  }


  private geometry(key: "scrollHeight" | "scrollWidth" | "clientHeight" | "clientWidth"): number {
    if (this.measureDepth === 0) return this.viewport[key];
    return this.geometryCache[key] ??= this.viewport[key];
  }

  private exactClientHeight(): number {
    if (this.measureDepth === 0) return scrollportClientHeight(this.viewport);
    return this.geometryCache.exactClientHeight ??= scrollportClientHeight(this.viewport);
  }

  private invalidateGeometry(): void {
    this.geometryCache = {};
    this.tailOffsetCache = undefined;
  }

  /** Nested measure passes share geometry until the outermost pass ends. */
  measureGeometry<T>(run: () => T): T {
    this.measureDepth += 1;
    try {
      return run();
    } finally {
      this.measureDepth -= 1;
      if (this.measureDepth === 0) this.invalidateGeometry();
    }
  }

  /** Publishes enough extent for current and pending offsets. */
  private publishRetainedExtent(extraCeiling = 0): void {
    if (!this.canRetainExtent()) {
      this.clearExtentHoldSpacer();
      return;
    }
    // A reserved contraction counts as landed, so its layout cannot clamp whenever it arrives.
    let natural = this.contentHeight();
    for (const reservation of this.reservedContractions) natural = Math.min(natural, reservation.floor);
    const naturalMax = Math.max(
      0,
      natural - this.geometry("clientHeight"),
    );
    // Range past the tail is only ever an offset the reader occupies, or one a pending write targets.
    const occupied = Math.max(this.viewport.scrollTop, extraCeiling);
    let wanted = Math.max(0, Math.ceil(occupied - naturalMax));
    // Held range does not shrink during direct input to prevent paint and hit-test offset divergence.
    if (this.isDirectInputActive()) wanted = Math.max(wanted, Math.round(this.extentHoldContributionPx));
    if (wanted <= 0) {
      this.clearExtentHoldSpacer();
      return;
    }
    if (this.extentHoldAppliedPx > 0 && Math.abs(this.extentHoldContributionPx - wanted) <= SCROLL_EPSILON_PX) return;
    // Range grows as max(0, B + h - clientHeight), so one write that adds range solves h exactly;
    // one that adds none sits in fill slack and retries a viewport taller.
    const clientHeight = this.geometry("clientHeight");
    let offsetPx = this.extentHoldAppliedPx > 0
      ? this.extentHoldAppliedPx - this.extentHoldContributionPx
      : 0;
    let contributed = 0;
    let height = 0;
    for (let attempt = 0; attempt < 3; attempt += 1) {
      height = Math.max(1, Math.ceil(wanted + offsetPx));
      contributed = this.writeExtentHold(height);
      // A growing viewport cannot retain blank space without changing layout.
      if (Math.abs(this.viewport.clientHeight - clientHeight) > SCROLL_EPSILON_PX) {
        this.extentHoldUnsupported = true;
        this.clearExtentHoldSpacer();
        return;
      }
      if (Math.abs(contributed - wanted) <= SCROLL_EPSILON_PX) break;
      if (contributed > SCROLL_EPSILON_PX) offsetPx = height - contributed;
      else if (attempt === 0) offsetPx += clientHeight;
      else break;
    }
    if (Math.abs(contributed - wanted) > SCROLL_EPSILON_PX && contributed < wanted) {
      this.extentHoldUnsupported = true;
      denScrollDebugLog("scroll", "extent-hold-unsupported", {
        host: this.hostLabel(),
        hold: height,
        required: wanted,
        added: contributed,
      });
      this.clearExtentHoldSpacer();
      return;
    }
    this.notifyExtentChanged();
  }

  /** Sizes the spacer and returns the scroll range it measurably adds. */
  private writeExtentHold(height: number): number {
    const natural = this.viewport.scrollHeight - this.extentHoldContributionPx;
    let hold = this.extentHoldEl;
    if (!hold) {
      hold = document.createElement("div");
      hold.setAttribute(SCROLLPORT_EXTENT_HOLD_ATTR, "");
      hold.setAttribute("aria-hidden", "true");
      hold.style.pointerEvents = "none";
      hold.style.flexShrink = "0";
      hold.style.width = "0";
      this.content.appendChild(hold);
      this.extentHoldEl = hold;
      // Blocking wheel listeners cost scroll latency, so one exists only while range is held.
      this.host.addEventListener("wheel", this.refuseInputPastTail, { passive: false });
    }
    hold.style.height = `${height}px`;
    this.extentHoldAppliedPx = height;
    this.invalidateGeometry();
    this.extentHoldContributionPx = Math.max(0, this.viewport.scrollHeight - natural);
    return this.extentHoldContributionPx;
  }

  private clearExtentHoldSpacer(): void {
    if (!this.extentHoldEl && this.extentHoldAppliedPx === 0) return;
    this.host.removeEventListener("wheel", this.refuseInputPastTail);
    this.extentHoldEl?.remove();
    this.extentHoldEl = null;
    this.extentHoldAppliedPx = 0;
    this.extentHoldContributionPx = 0;
    this.invalidateGeometry();
    this.notifyExtentChanged();
  }

  /**
   * Blocking travel past held content avoids writes that desync WebKit painting and hit testing.
   */
  private readonly refuseInputPastTail = (event: WheelEvent): void => {
    if (event.ctrlKey || event.deltaY <= 0 || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
    // An inner scrollport that claimed the wheel scrolls itself.
    if (claimedInputEvents.get(event) !== this) return;
    if (this.viewport.scrollTop >= this.tailOffsetY() - SCROLL_EPSILON_PX) event.preventDefault();
  };

  private notifyExtentChanged(): void {
    for (const handler of [...this.extentHandlers]) handler();
  }

  private readonly reclaimExtent = (): void => {
    this.extentReclaimRequest = undefined;
    if (this.disposed || this.thumbGestureActive) return;
    this.resizeRetainedExtent();
    this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
  };

  /** Reclaim before scroll observers and virtual rows publish their next frame. */
  private scheduleRetainedExtentReclaim(): void {
    if (this.disposed || this.thumbGestureActive) return;
    this.resizeRetainedExtent();
    if (this.extentReclaimRequest) return;
    const request = this.extentReclaimRequest = {};
    queueMicrotask(() => {
      if (this.disposed || this.extentReclaimRequest !== request) return;
      scheduleScrollportFrame(this.viewport, "measure", this.reclaimExtent);
    });
  }

  private cancelRetainedExtentReclaim(): void {
    this.extentReclaimRequest = undefined;
    cancelScrollportFrame(this.viewport, this.reclaimExtent);
  }

  /** An explicit content collapse may move the native tail until new input. */
  releaseTailRange(): void {
    this.tailRangeReleased = true;
    this.reconcileTailBound();
  }

  /** Returns an unclaimed overrun after the painted tail stabilizes. */
  reconcileTailBound(): void {
    this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
  }

  /**
   * Returns an unclaimed overrun once the painted tail holds still across a frame. Arming samples
   * the tail with the caller's geometry; each confirmation samples it once, in the scrollport's
   * measure phase, so the read shares the layout the frame's other measurements already pay for.
   */
  private scheduleTailBoundReconcile(attempts: number): void {
    if (this.disposed || this.tailBound || attempts <= 0) return;
    // Claims can expire without another layout notification.
    if (this.isDirectInputActive()) {
      this.deferTailBoundReconcile(attempts);
      return;
    }
    const overrun = this.measureGeometry(() => this.tailOverrunTail());
    if (overrun !== null) this.awaitTailBoundConfirmation(attempts, overrun);
  }

  private awaitTailBoundConfirmation(attempts: number, sampled: number): void {
    this.tailBound = { attempts, sampled };
    scheduleScrollportFrame(this.viewport, "measure", this.confirmTailBound);
  }

  private readonly confirmTailBound = (): void => {
    const pending = this.tailBound;
    this.tailBound = undefined;
    if (!pending || this.disposed) return;
    if (this.isDirectInputActive()) {
      this.deferTailBoundReconcile(pending.attempts - 1);
      return;
    }
    const overrun = this.measureGeometry(() => this.tailOverrunTail());
    if (overrun === null) return;
    if (Math.abs(overrun - pending.sampled) > SCROLL_EPSILON_PX) {
      // Changing geometry needs another confirmation; this sample is the next baseline.
      if (pending.attempts > 1) this.awaitTailBoundConfirmation(pending.attempts - 1, overrun);
      return;
    }
    // Unheld overrun, such as a native scroll into published runway, never shows blank space.
    if (this.extentHoldAppliedPx === 0) {
      this.commit(overrun, "tail_bound");
      return;
    }
    // Range this scrollport held closes smoothly under the reader.
    this.glidingToTail = true;
    void this.revealOffset(overrun, { motion: "settle" }).catch(() => false).then((arrived) => {
      this.glidingToTail = false;
      // A press that interrupted the glide keeps the content under it.
      if (!arrived || this.disposed) return;
      this.tailRangeReleased = true;
      this.measureGeometry(() => {
        this.resizeRetainedExtent();
        this.reconcilePinnedTail();
      });
    });
  };

  /** Schedules reconciliation for claim expiry. */
  private deferTailBoundReconcile(attempts: number): void {
    if (this.tailBoundTimer !== undefined) return;
    const remaining = Math.max(
      16,
      Math.ceil(this.directInputActiveUntil - performance.now()),
    );
    this.tailBoundTimer = setTimeout(() => {
      this.tailBoundTimer = undefined;
      if (this.disposed) return;
      // Range a gesture kept open settles to what the reader occupies once its claim ends.
      if (!this.isDirectInputActive()) this.resizeRetainedExtent();
      // Geometry retries begin after the input claim expires.
      this.scheduleTailBoundReconcile(
        this.isDirectInputActive() ? attempts : TAIL_BOUND_CONFIRM_FRAMES,
      );
    }, remaining);
  }

  private cancelTailBoundReconcile(): void {
    this.tailBound = undefined;
    cancelScrollportFrame(this.viewport, this.confirmTailBound);
    if (this.tailBoundTimer !== undefined) {
      clearTimeout(this.tailBoundTimer);
      this.tailBoundTimer = undefined;
    }
  }

  /** Returns the unclaimed painted tail below the current offset. */
  private tailOverrunTail(): number | null {
    // A press anchor holds the overrun, and its settle glide is already returning it.
    if (this.disposed || this.isDirectInputActive() || this.pressAnchor.active || this.glidingToTail) return null;
    const tail = this.tailOffsetY();
    if (this.viewport.scrollTop <= tail + SCROLL_EPSILON_PX) return null;
    return tail;
  }

  private commitClampMaxY(
    source: ScrollportCommitSource,
    extraCeiling: number,
  ): number {
    if (source === "thumb_drag" || source === "track_click") {
      return this.inputMaxOffsetY();
    }
    // Application tail movement excludes retained blank extent.
    if (
      source === "repin_tail" ||
      source === "jump" ||
      source === "tail_bound"
    ) {
      return this.tailOffsetY();
    }
    return Math.max(this.maxOffsetY(), extraCeiling);
  }
}

export type { ScrollportMotion };

export function bindScrollportMotion(
  host: HTMLElement,
  viewport: HTMLElement,
  content: HTMLElement,
): ScrollportMotion {
  const existing = motionByHost.get(host);
  if (existing && existing.viewport === viewport && existing.content === content) {
    return existing;
  }
  const viewportMotion = motionByViewport.get(viewport);
  if (viewportMotion && viewportMotion.host !== host) {
    throw new Error("A scroll viewport can belong to only one host.");
  }
  if (existing) {
    existing.dispose();
    motionByViewport.delete(existing.viewport);
  }
  const motion = new ScrollportMotion(host, viewport, content);
  motion.applyTailPolicy(tailPolicyByHost.get(host));
  motionByHost.set(host, motion);
  motionByViewport.set(viewport, motion);
  return motion;
}

/** Tail policy persists across scrollport bindings. */
export function setScrollportTailPolicy(
  host: HTMLElement,
  policy: ScrollportTailPolicy | null,
): void {
  if (policy) tailPolicyByHost.set(host, policy);
  else tailPolicyByHost.delete(host);
  motionByHost.get(host)?.applyTailPolicy(policy ?? undefined);
}

export function unbindScrollportMotion(host: HTMLElement): void {
  const motion = motionByHost.get(host);
  if (!motion) return;
  motion.dispose();
  motionByViewport.delete(motion.viewport);
  motionByHost.delete(host);
}

export function scrollportMotionForHost(
  host: HTMLElement,
): ScrollportMotion | undefined {
  return motionByHost.get(host);
}

export function scrollportMotionForViewport(
  viewport: HTMLElement,
): ScrollportMotion | undefined {
  return motionByViewport.get(viewport);
}

export function scrollportMotionContaining(
  element: Element,
): ScrollportMotion | undefined {
  let current: Element | null = element;
  while (current) {
    if (current instanceof HTMLElement) {
      const motion = motionByHost.get(current);
      if (motion) return motion;
    }
    current = current.parentElement;
  }
  return undefined;
}

/** Reveals an element through its scrollport's application writer. */
export function revealElementInScrollport(
  element: Element,
  opts?: { block?: ScrollLogicalPosition },
): void {
  const block = opts?.block ?? "nearest";
  const motion = scrollportMotionContaining(element);
  if (motion) {
    motion.revealElement(element, { block });
    return;
  }
  element.scrollIntoView({ block, inline: "nearest" });
}

export function requireScrollportMotionForViewport(
  viewport: HTMLElement,
): ScrollportMotion {
  const motion = motionByViewport.get(viewport);
  if (!motion) {
    throw new Error("Scrollport motion is not attached to this viewport.");
  }
  return motion;
}

/** Events already claimed by the innermost scrollport they passed through. */
const claimedInputEvents = new WeakMap<Event, ScrollportMotion>();

/** Unscrollable axes pass input to the parent when CSS allows it. */
export function wheelPassesThroughScrollport(
  viewport: HTMLElement,
  event: WheelEvent,
): boolean {
  const horizontal = Math.abs(event.deltaX) > Math.abs(event.deltaY);
  const style = getComputedStyle(viewport);
  const chaining = horizontal ? style.overscrollBehaviorX : style.overscrollBehaviorY;
  if (chaining !== "auto") return false;
  const overflow = horizontal ? style.overflowX : style.overflowY;
  if (overflow === "hidden" || overflow === "clip") return true;
  return horizontal
    ? viewport.scrollWidth <= viewport.clientWidth
    : viewport.scrollHeight <= viewport.clientHeight;
}

/** The innermost scrollport claims native input before layout reconciliation. */
export function bindScrollportNativeInput(motion: ScrollportMotion): () => void {
  const onInput = (event: Event) => {
    // Pinch input changes zoom.
    if (event instanceof WheelEvent && event.ctrlKey) return;
    if (event instanceof WheelEvent && wheelPassesThroughScrollport(motion.viewport, event)) return;
    if (claimedInputEvents.has(event)) return;
    claimedInputEvents.set(event, motion);
    motion.noteNativeInput(event.type === "touchmove" ? "touch" : "wheel");
  };
  motion.host.addEventListener("wheel", onInput, { passive: true });
  motion.host.addEventListener("touchmove", onInput, { passive: true });
  return () => {
    motion.host.removeEventListener("wheel", onInput);
    motion.host.removeEventListener("touchmove", onInput);
    motion.interruptDirectInput();
  };
}
