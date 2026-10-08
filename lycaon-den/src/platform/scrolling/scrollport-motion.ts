import { animateScrollportReveal, type ScrollportRevealOptions } from "./scrollport-reveal.ts";
import { prefersReducedMotion } from "../interaction/reduced-motion.ts";
import { observeScrollportOffset } from "./scrollport-offset.ts";
import { ScrollportPressAnchor, type DeclaredHeightChange } from "./scrollport-press-anchor.ts";
import {
  denScrollDebugLog,
  isStreamScrollTraceVerbose,
} from "../../chat/stream/den-scroll-debug.ts";

import { ScrollportInput } from "./scrollport-input.ts";
import { ScrollportExtent } from "./scrollport-extent.ts";
import { ScrollportTail } from "./scrollport-tail.ts";
import { SCROLL_EPSILON_PX, TAIL_BOUND_CONFIRM_FRAMES, type CommitHandler, type ScrollportCommitSource, type ScrollportTailPolicy } from "./scrollport-motion-types.ts";
export { SCROLL_EPSILON_PX, DEN_SCROLLPORT_INPUT_EVENT, SCROLLPORT_EXTENT_HOLD_ATTR, scrollportClientHeight } from "./scrollport-motion-types.ts";
export type { ScrollportCommitSource, ScrollportCommitPosition, ScrollportTailPolicy } from "./scrollport-motion-types.ts";
const ANCHOR_ENDING_SOURCES: ReadonlySet<ScrollportCommitSource> = new Set(["thumb_drag", "track_click", "restore_anchor", "reveal", "jump"]);
const motionByHost = new WeakMap<HTMLElement, ScrollportMotion>();
const motionByViewport = new WeakMap<HTMLElement, ScrollportMotion>();
const tailPolicyByHost = new WeakMap<HTMLElement, ScrollportTailPolicy>();

class ScrollportMotion {
  readonly viewport: HTMLElement;
  readonly host: HTMLElement;
  readonly content: HTMLElement;
  readonly input: ScrollportInput;
  readonly extent: ScrollportExtent;
  readonly tail: ScrollportTail;
  private layoutReconcileFrame: number | undefined;
  private lastObservedOffsetY = 0;
  private contentShiftRemainder = 0;
  private readonly commitHandlers = new Map<CommitHandler, object>();
  private disposed = false;
  private reveal: ReturnType<typeof animateScrollportReveal> | undefined;
  private readonly stopOffsetObservation: () => void;
  private readonly pressAnchor: ScrollportPressAnchor;

  constructor(host: HTMLElement, viewport: HTMLElement, content: HTMLElement) {
    this.host = host;
    this.viewport = viewport;
    this.content = content;
    this.extent = new ScrollportExtent(host, viewport, content, {
      isDirectInputActive: () => this.input.isDirectInputActive(),
      isThumbGestureActive: () => this.input.isThumbGestureActive(),
      isTailPinned: () => this.tail.isTailPinned(),
      pressActive: () => this.pressAnchor.active,
      lastObservedOffset: () => this.lastObservedOffsetY,
      resolveTail: (natural) => this.tail.tailOffsetResolver?.(natural),
      scheduleTailBoundReconcile: (attempts) => this.tail.scheduleTailBoundReconcile(attempts),
      claimsInput: (event) => claimedInputEvents.get(event) === this,
    });
    this.tail = new ScrollportTail(host, viewport, {
      invalidateGeometry: () => this.extent.invalidateGeometry(),
      measureGeometry: (run) => this.extent.measureGeometry(run),
      tailOffsetY: () => this.extent.tailOffsetY(),
      resizeRetainedExtent: () => this.extent.resizeRetainedExtent(),
      extentHeld: () => this.extent.extentHoldAppliedPx,
      isDirectInputActive: () => this.input.isDirectInputActive(),
      isThumbGestureActive: () => this.input.isThumbGestureActive(),
      nativeStreamLive: () => this.input.nativeStreamLive(),
      inputDeadline: () => this.input.inputDeadline(),
      pressActive: () => this.pressAnchor.active,
      commit: (offset, source) => this.commit(offset, source),
      revealOffset: (offset, options) => this.revealOffset(offset, options),
    });
    this.input = new ScrollportInput(host, viewport, {
      invalidateGeometry: () => this.extent.invalidateGeometry(),
      measureGeometry: (run) => this.extent.measureGeometry(run),
      nativeMaxOffsetY: () => this.extent.nativeMaxOffsetY(),
      reconcilePinnedTail: () => this.tail.reconcilePinnedTail(),
      resizeRetainedExtent: () => this.extent.resizeRetainedExtent(),
      scheduleRetainedExtentReclaim: () => this.extent.scheduleRetainedExtentReclaim(),
      resetApplicationClaim: () => {
        this.pressAnchor.end();
        this.contentShiftRemainder = 0;
        this.tail.tailRangeReleased = false;
        this.cancelReveal();
      },
    });

    // A rebound scrollport inherits the offset it is already showing.
    this.lastObservedOffsetY = viewport.scrollTop;
    this.host.addEventListener("pointerdown", this.cancelReveal);
    this.host.addEventListener("keydown", this.cancelReveal);
    this.stopOffsetObservation = observeScrollportOffset(viewport, this.noteObservedOffset);
    this.pressAnchor = new ScrollportPressAnchor(host, viewport, {
      carry: (delta) => this.carryAnchoredContent(delta),
      holdsRange: () => this.viewport.scrollTop > this.extent.tailOffsetY() + SCROLL_EPSILON_PX,
      released: () => this.extent.scheduleRetainedExtentReclaim(),
    });
  }

  declareHeightChange(shell: HTMLElement, opts?: { contraction?: number }): DeclaredHeightChange {
    const change = this.pressAnchor.declare(shell);
    const contraction = opts?.contraction ?? 0;
    if (!this.pressAnchor.active || contraction <= SCROLL_EPSILON_PX || !this.extent.canRetainExtent()) return change;
    const reservation = { floor: 0 };
    this.extent.measureGeometry(() => {
      reservation.floor = Math.max(0, this.extent.contentHeight() - contraction);
      this.extent.reservedContractions.add(reservation);
      this.extent.publishRetainedExtent();
    });
    return {
      end: () => {
        const reserved = this.extent.reservedContractions.delete(reservation);
        change.end();
        // The held offset keeps whatever range it still needs; the rest settles.
        if (reserved) this.extent.scheduleRetainedExtentReclaim();
      },
    };
  }

  private carryAnchoredContent(delta: number): void {
    this.extent.measureGeometry(() => {
      this.commitShift(delta, this.viewport.scrollTop, "content_shift");
      this.lastObservedOffsetY = this.viewport.scrollTop;
    });
  }

  private noteObservedOffset = (offset: number): void => {
    const previous = this.lastObservedOffsetY;
    this.lastObservedOffsetY = offset;
    if (offset >= previous - SCROLL_EPSILON_PX) return;
    this.restoreClampedOffset(previous);
  };

  offsetY(): number {
    return this.lastObservedOffsetY;
  }

  private restoreClampedOffset(previous: number): void {
    if (!this.extent.canRetainExtent()) return;
    if (!this.pressAnchor.active && (this.tail.isTailPinned() || this.tail.tailRangeReleased)) return;
    // Thumb dragging sets an absolute offset.
    if (this.input.isThumbGestureActive()) return;
    this.extent.measureGeometry(() => {
      const publishedMax = Math.max(
        0,
        this.extent.geometry("scrollHeight") - this.extent.geometry("clientHeight"),
      );
      if (previous <= publishedMax + SCROLL_EPSILON_PX) return;
      if (
        Math.abs(this.viewport.scrollTop - publishedMax) > SCROLL_EPSILON_PX
      ) {
        return;
      }
      this.commit(previous, "layout_compensation");
      this.tail.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
    });
  }

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

  commitShift(
    delta: number,
    fromOffset: number,
    source: ScrollportCommitSource,
  ): number {
    return this.extent.measureGeometry(() => {
      const lostOffset = fromOffset - this.viewport.scrollTop;
      // A measured offset beyond the native range proves a clamp, even for a one-pixel shrink.
      const reclaimed = lostOffset > 0 &&
        (fromOffset > this.extent.nativeMaxOffsetY() ||
          (!this.input.isDirectInputActive() && lostOffset > SCROLL_EPSILON_PX));
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
    else if (source === "repin_tail" && (this.pressAnchor.active || this.tail.glidingToTail || this.input.nativeStreamLive())) {
      // A native stream drives the offset; the pin lands once it goes quiet.
      return this.viewport.scrollTop;
    }
    return this.extent.measureGeometry(() => {
      if (source === "thumb_drag" && opts?.measuredMaxOffset === undefined) {
        this.extent.publishRetainedExtent();
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
        this.extent.publishLayoutCompensationRange(wanted);
      }
      const measuredMax = opts?.measuredMaxOffset;
      const maxX = axis === "y"
        ? beforeX
        : axis === "x" && measuredMax !== undefined
          ? measuredMax
          : this.extent.maxOffsetX();
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
          clientHeight: this.extent.geometry("clientHeight"),
          scrollHeight: this.viewport.scrollHeight,
          extentHold: this.extent.extentHoldAppliedPx,
          thumbDrag: this.input.isThumbGestureActive() ? 1 : 0,
          directInput: this.input.isDirectInputActive() ? 1 : 0,
        });
      }

      if (axis !== "x") {
        if (source !== "content_shift" && source !== "layout_compensation") this.contentShiftRemainder = 0;
        if (source === "tail_bound") this.tail.tailRangeReleased = true;
        else if (source !== "layout_compensation") this.tail.tailRangeReleased = false;
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
      if (this.extent.extentHoldAppliedPx > 0) {
        this.extent.measureGeometry(() => this.extent.publishRetainedExtent());
      }
      const position = { top: landedY, left: this.viewport.scrollLeft };
      for (const [handler, subscription] of [...this.commitHandlers]) {
        if (this.commitHandlers.get(handler) === subscription) handler(source, position);
      }
      // Editor paint can change measured content after an offset commit.
      if (this.commitHandlers.size > 0) this.extent.invalidateGeometry();
      return landedY;
    });
  }

  revealOffset(offset: number, options: ScrollportRevealOptions = {}): Promise<boolean> {
    this.cancelReveal();
    if (this.disposed) return Promise.resolve(false);
    const coordinates = options.coordinates ?? {
      read: () => this.viewport.scrollTop,
      publish: (offset: number) => this.commit(offset, "reveal"),
    };
    const reveal = animateScrollportReveal({
      ...options, coordinates,
      target: () => options.coordinates ? offset : Math.min(this.extent.maxOffsetY(), offset),
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

  shiftContent(deltaY: number, fromOffset: number): number {
    if (!Number.isFinite(deltaY) || !Number.isFinite(fromOffset)) return 0;
    if (this.input.isThumbGestureActive()) {
      denScrollDebugLog("scroll", "content-shift", {
        from: Math.round(this.viewport.scrollTop),
        delta: Math.round(deltaY),
        applied: 0,
        thumbDrag: 1,
      });
      return 0;
    }
    return this.extent.measureGeometry(() => {
      const before = this.viewport.scrollTop;
      if (this.tail.pinControlsOffset()) {
        // The tail pin absorbs content shifts.
        this.extent.invalidateGeometry();
        this.tail.reconcilePinnedTail();
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
      this.extent.scheduleRetainedExtentReclaim();
      return applied;
    });
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

  cancelApplicationMotion(): void {
    this.pressAnchor.end();
    this.cancelReveal();
    if (isStreamScrollTraceVerbose()) {
      denScrollDebugLog("scroll", "application-motion-cancelled", {
        from: Math.round(this.viewport.scrollTop),
      });
    }
    this.input.cancelApplicationInput();
  }

  dispose(): void {
    this.cancelReveal();
    this.host.removeEventListener("pointerdown", this.cancelReveal);
    this.host.removeEventListener("keydown", this.cancelReveal);
    this.disposed = true;
    this.input.dispose();
    this.cancelLayoutReconcile();
    this.tail.dispose();
    this.commitHandlers.clear();
    this.stopOffsetObservation();
    this.pressAnchor.dispose();
    this.extent.dispose();
  }


  scheduleLayoutReconcile(): void {
    this.extent.invalidateGeometry();
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
    this.extent.invalidateGeometry();
    this.extent.measureGeometry(() => {
      this.extent.refreshRetainedExtent();
      this.pressAnchor.noteLayout();
      this.tail.reconcilePinnedTail();
      // Releases a held offset once geometry stops moving.
      if (this.tail.tailCanRecedeSilently()) {
        this.tail.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
      }
    });
  }

  private commitClampMaxY(
    source: ScrollportCommitSource,
    extraCeiling: number,
  ): number {
    if (source === "thumb_drag" || source === "track_click") {
      return this.extent.inputMaxOffsetY();
    }
    // Application tail movement excludes retained blank extent.
    if (
      source === "repin_tail" ||
      source === "jump" ||
      source === "tail_bound"
    ) {
      return this.extent.tailOffsetY();
    }
    return Math.max(this.extent.maxOffsetY(), extraCeiling);
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
  motion.tail.applyTailPolicy(tailPolicyByHost.get(host));
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
  motionByHost.get(host)?.tail.applyTailPolicy(policy ?? undefined);
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
    motion.input.noteNativeInput(event.type === "touchmove" ? "touch" : "wheel");
  };
  motion.host.addEventListener("wheel", onInput, { passive: true });
  motion.host.addEventListener("touchmove", onInput, { passive: true });
  return () => {
    motion.host.removeEventListener("wheel", onInput);
    motion.host.removeEventListener("touchmove", onInput);
    motion.input.interruptDirectInput();
  };
}
