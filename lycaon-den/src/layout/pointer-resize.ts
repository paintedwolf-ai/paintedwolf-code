import type { ResizeSession } from "./resize-session.ts";

type PointerResizeOptions = {
  handle: HTMLElement;
  pointerId: number;
  startPosition: number;
  positionOf: (event: PointerEvent) => number;
  valueAt: (position: number) => number;
  session: ResizeSession;
  rootClass?: string;
};

/** Manages pointer-resize teardown across every interruption path. */
export function startPointerResize(options: PointerResizeOptions): () => void {
  let finished = false;
  let lastPosition: number | null = null;
  let previewFrame: number | undefined;
  const publish = () => {
    previewFrame = undefined;
    if (!finished && lastPosition !== null) {
      options.session.preview(options.valueAt(lastPosition));
    }
  };

  const finish = (outcome: "commit" | "cancel", position?: number) => {
    if (finished) return;
    finished = true;
    if (previewFrame !== undefined) cancelAnimationFrame(previewFrame);
    previewFrame = undefined;
    if (options.rootClass) {
      document.documentElement.classList.remove(options.rootClass);
    }
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
    window.removeEventListener("pointercancel", onCancel);
    window.removeEventListener("blur", onInterrupt);
    document.removeEventListener("visibilitychange", onVisibility);
    options.handle.removeEventListener("lostpointercapture", onLostCapture);
    try {
      options.handle.releasePointerCapture(options.pointerId);
    } catch {
      /* Capture is already released. */
    }
    if (outcome === "cancel") {
      options.session.cancel();
      return;
    }
    if (
      position !== undefined &&
      (lastPosition !== null ||
        Math.abs(position - options.startPosition) >= 0.5)
    ) {
      options.session.preview(options.valueAt(position));
    }
    options.session.commit();
  };

  const onMove = (event: PointerEvent) => {
    if (event.pointerId !== options.pointerId) return;
    lastPosition = options.positionOf(event);
    previewFrame ??= requestAnimationFrame(publish);
  };
  const onUp = (event: PointerEvent) => {
    if (event.pointerId !== options.pointerId) return;
    finish("commit", options.positionOf(event));
  };
  const onCancel = (event: PointerEvent) => {
    if (event.pointerId !== options.pointerId) return;
    finish("cancel");
  };
  const onInterrupt = () => finish("cancel");
  const onVisibility = () => {
    if (document.visibilityState === "hidden") finish("cancel");
  };
  const onLostCapture = (event: PointerEvent) => {
    if (
      event.pointerId !== undefined &&
      event.pointerId !== options.pointerId
    ) {
      return;
    }
    finish("cancel");
  };

  if (options.rootClass) {
    document.documentElement.classList.add(options.rootClass);
  }
  try {
    options.handle.setPointerCapture(options.pointerId);
  } catch {
    /* Window listeners still track the drag. */
  }

  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", onUp);
  window.addEventListener("pointercancel", onCancel);
  window.addEventListener("blur", onInterrupt);
  document.addEventListener("visibilitychange", onVisibility);
  options.handle.addEventListener("lostpointercapture", onLostCapture);

  return () => finish("cancel");
}
