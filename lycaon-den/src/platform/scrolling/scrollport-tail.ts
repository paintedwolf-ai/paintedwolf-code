import { cancelScrollportFrame, scheduleScrollportFrame } from "./scrollport-frame.ts";
import { SCROLL_EPSILON_PX, TAIL_BOUND_CONFIRM_FRAMES, devicePixelTolerance, type ScrollportCommitSource, type ScrollportTailPolicy } from "./scrollport-motion-types.ts";

import type { ScrollportRevealOptions } from "./scrollport-reveal.ts";
type TailPorts = {
  invalidateGeometry(): void;
  measureGeometry<T>(run: () => T): T;
  tailOffsetY(): number;
  resizeRetainedExtent(): void;
  extentHeld(): number;
  isDirectInputActive(): boolean;
  isThumbGestureActive(): boolean;
  nativeStreamLive(): boolean;
  inputDeadline(): number;
  pressActive(): boolean;
  commit(offset: number, source: ScrollportCommitSource): void;
  revealOffset(offset: number, options?: ScrollportRevealOptions): Promise<boolean>;
};

export class ScrollportTail {
  private tailBound: { attempts: number; sampled: number } | undefined;
  private tailBoundTimer: ReturnType<typeof setTimeout> | undefined;
  private deferredTailBoundAttempts = 0;
  tailRangeReleased = false;
  glidingToTail = false;
  private tailPin: (() => boolean) | null = null;
  tailOffsetResolver: ((naturalTailOffset: number) => number | null) | undefined;
  private disposed = false;
  constructor(readonly viewport: HTMLElement, private readonly ports: TailPorts) {}

  setTailPin(pin: (() => boolean) | null): void {
    this.tailPin = pin;
  }

  applyTailPolicy(policy: ScrollportTailPolicy | undefined): void {
    this.setTailOffsetResolver(policy?.resolveTail);
    this.setTailPin(policy?.pinned ?? null);
  }

  isTailPinned(): boolean {
    return this.tailPin?.() ?? false;
  }

  private gestureControlsOffset(): boolean {
    return this.ports.isThumbGestureActive() || this.ports.nativeStreamLive();
  }

  pinControlsOffset(): boolean {
    return (
      this.isTailPinned() &&
      !this.gestureControlsOffset() &&
      !this.ports.pressActive() &&
      !this.glidingToTail
    );
  }

  tailCanRecedeSilently(): boolean {
    return (
      this.tailOffsetResolver !== undefined ||
      this.ports.extentHeld() > 0 ||
      this.ports.isDirectInputActive()
    );
  }

  reconcilePinnedTail(): void {
    if (!this.pinControlsOffset()) return;
    const tail = this.ports.tailOffsetY();
    const tolerance = devicePixelTolerance(this.viewport.ownerDocument.defaultView);
    if (Math.abs(this.viewport.scrollTop - tail) <= tolerance) return;
    this.ports.commit(tail, "repin_tail");
  }

  setTailOffsetResolver(
    resolver: ((naturalTailOffset: number) => number | null) | undefined,
  ): void {
    this.tailOffsetResolver = resolver;
    this.ports.invalidateGeometry();
    // Boundary changes require a fresh tail measurement before offset correction.
    this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
  }

  releaseTailRange(): void {
    this.tailRangeReleased = true;
    this.reconcileTailBound();
  }

  reconcileTailBound(): void {
    this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
  }

  scheduleTailBoundReconcile(attempts: number): void {
    if (this.disposed || attempts <= 0) return;
    if (this.tailBound) {
      this.tailBound.attempts = Math.max(this.tailBound.attempts, attempts);
      return;
    }
    // Claims can expire without another layout notification.
    if (this.ports.isDirectInputActive()) {
      this.deferTailBoundReconcile(attempts);
      return;
    }
    const overrun = this.ports.measureGeometry(() => this.tailOverrunTail());
    if (overrun !== null) this.awaitTailBoundConfirmation(attempts, overrun);
  }

  private awaitTailBoundConfirmation(attempts: number, sampled: number): void {
    this.tailBound = { attempts, sampled };
    scheduleScrollportFrame(this.viewport, "measure", this.confirmTailBound);
  }

  private confirmTailBound = (): void => {
    const pending = this.tailBound;
    this.tailBound = undefined;
    if (!pending || this.disposed) return;
    if (this.ports.isDirectInputActive()) {
      this.deferTailBoundReconcile(Math.max(pending.attempts, TAIL_BOUND_CONFIRM_FRAMES));
      return;
    }
    const overrun = this.ports.measureGeometry(() => this.tailOverrunTail());
    if (overrun === null) return;
    if (Math.abs(overrun - pending.sampled) > SCROLL_EPSILON_PX && pending.attempts > 1) {
      // Changing geometry needs another confirmation; this sample is the next baseline.
      this.awaitTailBoundConfirmation(pending.attempts - 1, overrun);
      return;
    }
    // Unheld overrun, such as a native scroll into published runway, never shows blank space.
    if (this.ports.extentHeld() === 0) {
      this.ports.commit(overrun, "tail_bound");
      return;
    }
    // Range this scrollport held closes smoothly under the reader.
    this.glidingToTail = true;
    void this.ports.revealOffset(overrun, { motion: "settle" }).catch(() => false).then((arrived) => {
      this.glidingToTail = false;
      if (this.disposed) return;
      // A press that interrupted the glide keeps the content under it; other interruptions retry after claims settle.
      if (!arrived) {
        if (!this.ports.pressActive()) {
          this.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
        }
        return;
      }
      this.tailRangeReleased = true;
      this.ports.measureGeometry(() => {
        this.ports.resizeRetainedExtent();
        this.reconcilePinnedTail();
      });
    });
  };

  private deferTailBoundReconcile(attempts: number): void {
    this.deferredTailBoundAttempts = Math.max(
      this.deferredTailBoundAttempts,
      attempts,
      TAIL_BOUND_CONFIRM_FRAMES,
    );
    if (this.tailBoundTimer !== undefined) return;
    const remaining = Math.max(
      16,
      Math.ceil(this.ports.inputDeadline() - performance.now()),
    );
    this.tailBoundTimer = setTimeout(() => {
      this.tailBoundTimer = undefined;
      if (this.disposed) return;
      if (this.ports.isDirectInputActive()) {
        this.deferTailBoundReconcile(this.deferredTailBoundAttempts);
        return;
      }
      const nextAttempts = this.deferredTailBoundAttempts;
      this.deferredTailBoundAttempts = 0;
      // Range a gesture kept open settles to what the reader occupies once its claim ends.
      this.ports.resizeRetainedExtent();
      // Geometry retries begin after the input claim expires.
      this.scheduleTailBoundReconcile(nextAttempts);
    }, remaining);
  }

  private cancelTailBoundReconcile(): void {
    this.tailBound = undefined;
    this.deferredTailBoundAttempts = 0;
    cancelScrollportFrame(this.viewport, this.confirmTailBound);
    if (this.tailBoundTimer !== undefined) {
      clearTimeout(this.tailBoundTimer);
      this.tailBoundTimer = undefined;
    }
  }

  private tailOverrunTail(): number | null {
    // A press anchor holds the overrun, and its settle glide is already returning it.
    if (this.disposed || this.ports.isDirectInputActive() || this.ports.pressActive() || this.glidingToTail) return null;
    const tail = this.ports.tailOffsetY();
    if (this.viewport.scrollTop <= tail + SCROLL_EPSILON_PX) return null;
    return tail;
  }

  dispose(): void {
    this.disposed = true;
    this.tailPin = null;
    this.cancelTailBoundReconcile();
  }
}
