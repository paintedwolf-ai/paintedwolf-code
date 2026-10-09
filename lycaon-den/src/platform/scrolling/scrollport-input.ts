import { cancelScrollportFrame, scheduleScrollportFrame } from "./scrollport-frame.ts";
import { denScrollDebugLog, isStreamScrollTraceVerbose } from "../../chat/stream/den-scroll-debug.ts";
import { SCROLL_EPSILON_PX, DEN_SCROLLPORT_INPUT_EVENT } from "./scrollport-motion-types.ts";

type InputPorts = {
  invalidateGeometry(): void;
  measureGeometry<T>(run: () => T): T;
  nativeMaxOffsetY(): number;
  reconcilePinnedTail(): void;
  resizeRetainedExtent(): void;
  scheduleRetainedExtentReclaim(): void;
  resetApplicationClaim(): void;
};
const DIRECT_INPUT_SETTLE_MS = 600;
const NATIVE_INPUT_QUIET_MS = 150;

export class ScrollportInput {
  private thumbGestureActive = false;
  private directInputActiveUntil = 0;
  private inputSettledTimer: ReturnType<typeof setTimeout> | undefined;
  private inputSettledDeadline = 0;
  private readonly inputSettledHandlers = new Set<() => void>();
  private touchInputActiveUntil = 0;
  private nativeInputQuietTimer: ReturnType<typeof setTimeout> | undefined;
  private nativeInputQuietAt = 0;
  private pointerReseatArmed = false;
  private disposed = false;
  constructor(readonly host: HTMLElement, readonly viewport: HTMLElement, private readonly ports: InputPorts) {}

  noteNativeInput(kind: "wheel" | "touch"): void {
    // The stream drives the offset again; its settle re-arms the reseat.
    this.disarmPointerReseat();
    this.noteDirectInputActivity();
    if (kind === "touch") {
      this.touchInputActiveUntil = performance.now() + DIRECT_INPUT_SETTLE_MS;
    }
    this.dispatchDirectInput();
    // Reclaim resizes any open hold; an unheld scrollport needs no geometry for an input event.
    this.ports.scheduleRetainedExtentReclaim();
    this.nativeInputQuietAt = performance.now() + NATIVE_INPUT_QUIET_MS;
    // A pending timer re-arms itself until the stream goes quiet.
    if (this.nativeInputQuietTimer === undefined) {
      this.nativeInputQuietTimer = setTimeout(this.settleNativeStream, NATIVE_INPUT_QUIET_MS);
    }
  }

  nativeStreamLive(): boolean {
    const now = performance.now();
    return now < this.nativeInputQuietAt || now < this.touchInputActiveUntil;
  }

  private settleNativeStream = (): void => {
    this.nativeInputQuietTimer = undefined;
    if (this.disposed || this.thumbGestureActive) return;
    const remaining = Math.max(this.nativeInputQuietAt, this.touchInputActiveUntil) - performance.now();
    if (remaining > 0) {
      this.nativeInputQuietTimer = setTimeout(this.settleNativeStream, Math.ceil(remaining));
      return;
    }
    this.ports.invalidateGeometry();
    this.ports.measureGeometry(() => {
      this.ports.reconcilePinnedTail();
      const offset = this.viewport.scrollTop;
      if (offset < 1 || offset < this.ports.nativeMaxOffsetY() - SCROLL_EPSILON_PX) return;
      this.reseatScrollingThread("range-end-reseat");
    });
    this.armPointerReseat();
  };

  private reseatScrollingThread(event: "range-end-reseat" | "pointer-reseat"): void {
    const offset = this.viewport.scrollTop;
    const away = offset >= 1 ? offset - 1 : offset + 1;
    if (away > this.ports.nativeMaxOffsetY()) return;
    this.viewport.scrollTop = away;
    this.viewport.scrollTop = offset;
    if (isStreamScrollTraceVerbose()) {
      denScrollDebugLog("scroll", event, { offset: Math.round(offset) });
    }
  }

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

  private onPointerReseat = (): void => {
    this.host.removeEventListener("pointermove", this.onPointerReseat);
    // The frame's measure phase shares the layout its other reads already pay for.
    scheduleScrollportFrame(this.viewport, "measure", this.pointerReseat);
  };

  private pointerReseat = (): void => {
    this.pointerReseatArmed = false;
    if (this.disposed || this.thumbGestureActive || this.nativeStreamLive()) return;
    this.ports.measureGeometry(() => this.reseatScrollingThread("pointer-reseat"));
  };

  beginThumbGesture(): void {
    this.thumbGestureActive = true;
    this.noteDirectInputActivity();
    // A gesture cannot reclaim, so an open hold is resized as the drag begins.
    this.ports.resizeRetainedExtent();
    this.dispatchDirectInput();
  }

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
    this.ports.scheduleRetainedExtentReclaim();
  }

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
    this.ports.scheduleRetainedExtentReclaim();
  }

  isThumbGestureActive(): boolean {
    return this.thumbGestureActive;
  }

  isDirectInputActive(): boolean {
    return (
      this.thumbGestureActive || performance.now() < this.directInputActiveUntil
    );
  }

  private dispatchDirectInput(): void {
    this.host.dispatchEvent(
      new CustomEvent(DEN_SCROLLPORT_INPUT_EVENT, { bubbles: false }),
    );
  }

  private noteDirectInputActivity(now = performance.now()): void {
    // Direct input resets the anchor and its fractional compensation.
    this.ports.resetApplicationClaim();
    this.directInputActiveUntil = Math.max(
      this.directInputActiveUntil,
      now + DIRECT_INPUT_SETTLE_MS,
    );
    this.scheduleInputSettled();
  }

  inputDeadline(): number { return this.directInputActiveUntil; }
  cancelApplicationInput(): void {
    this.thumbGestureActive = false;
    this.directInputActiveUntil = 0;
    this.touchInputActiveUntil = 0;
    this.nativeInputQuietAt = 0;
    this.scheduleInputSettled();
    this.ports.scheduleRetainedExtentReclaim();
  }
  dispose(): void {
    if (this.nativeInputQuietTimer !== undefined) clearTimeout(this.nativeInputQuietTimer);
    this.disarmPointerReseat();
    this.disposed = true;
    this.cancelInputSettled();
    this.inputSettledHandlers.clear();
    this.thumbGestureActive = false;
    this.directInputActiveUntil = 0;
    this.touchInputActiveUntil = 0;
    this.nativeInputQuietAt = 0;
  }
}
