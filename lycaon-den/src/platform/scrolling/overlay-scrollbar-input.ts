import type { Elements } from "overlayscrollbars";
import type { ScrollportMotion } from "./scrollport-motion.ts";

export const DEN_SCROLLBAR_DRAGGING_ATTR = "data-den-scrollbar-dragging";

const HANDLE_INTERACTIVE_CLASS = "den-scrollbar-handle-interactive";
const TRACK_INTERACTIVE_CLASS = "den-scrollbar-track-interactive";

type Axis = "x" | "y";

export type ScrollbarAxisModel = {
  read(): { offset: number; viewport: number; extent: number };
  scrollTo(offset: number, source: "thumb_drag" | "track_click"): void;
};

export type ScrollbarInput = {
  dispose(): void;
  refreshGeometry(): boolean;
};

type ScrollbarElements = Pick<
  Elements,
  "host" | "scrollOffsetElement" | "scrollbarHorizontal" | "scrollbarVertical"
>;

function bindAxis(
  elements: ScrollbarElements,
  axis: Axis,
  motion: ScrollportMotion,
  model?: ScrollbarAxisModel,
): ScrollbarInput {
  const scrollbar =
    axis === "y" ? elements.scrollbarVertical : elements.scrollbarHorizontal;
  const { handle, track } = scrollbar;
  const viewport = elements.scrollOffsetElement;
  scrollbar.scrollbar.classList.add(
    HANDLE_INTERACTIVE_CLASS,
    TRACK_INTERACTIVE_CLASS,
  );

  let pointerId: number | undefined;
  let pointerStart = 0;
  let scrollStart = 0;
  let scrollRange = 0;
  let handleRange = 0;
  let pendingPointer = 0;
  let frame: number | undefined;
  let appliedRange = 0;

  const readPointer = (event: PointerEvent) =>
    axis === "y" ? event.clientY : event.clientX;
  const readGeometry = () => model?.read() ?? (axis === "y"
    ? { offset: viewport.scrollTop, viewport: viewport.clientHeight, extent: viewport.scrollHeight }
    : { offset: viewport.scrollLeft, viewport: viewport.clientWidth, extent: viewport.scrollWidth });
  const commit = (offset: number, source: "thumb_drag" | "track_click", range: number) => {
    if (model) model.scrollTo(offset, source);
    else motion.commit(offset, source, { axis, measuredMaxOffset: range });
  };

  const applyPosition = () => {
    if (pointerId === undefined || handleRange <= 0 || scrollRange <= 0) return;
    const pointerDelta = pendingPointer - pointerStart;
    const geometry = readGeometry();
    appliedRange = Math.max(0, geometry.extent - geometry.viewport);
    const fraction = Math.max(0, Math.min(1, scrollStart / scrollRange + pointerDelta / handleRange));
    commit(fraction * appliedRange, "thumb_drag", appliedRange);
  };
  const applyPending = () => { frame = undefined; applyPosition(); };

  const schedule = () => {
    if (frame !== undefined) return;
    if (typeof requestAnimationFrame === "function") {
      frame = requestAnimationFrame(applyPending);
    } else {
      applyPending();
    }
  };

  const finish = (event?: PointerEvent) => {
    if (pointerId === undefined) return;
    if (event && event.pointerId !== pointerId) return;
    if (frame !== undefined && typeof cancelAnimationFrame === "function") {
      cancelAnimationFrame(frame);
      frame = undefined;
      applyPending();
    }
    const captured = pointerId;
    pointerId = undefined;
    motion.input.endThumbGesture();
    elements.host.removeAttribute(DEN_SCROLLBAR_DRAGGING_ATTR);
    if (handle.hasPointerCapture?.(captured)) {
      handle.releasePointerCapture(captured);
    }
  };

  const onPointerDown = (event: PointerEvent) => {
    if (!event.isPrimary || event.button !== 0 || pointerId !== undefined) return;
    const geometry = readGeometry();
    const trackSize = axis === "y" ? track.clientHeight : track.clientWidth;
    const handleSize = axis === "y" ? handle.clientHeight : handle.clientWidth;
    scrollRange = Math.max(0, geometry.extent - geometry.viewport);
    appliedRange = scrollRange;
    handleRange = Math.max(0, trackSize - handleSize);
    if (scrollRange <= 0 || handleRange <= 0) return;

    event.preventDefault();
    event.stopPropagation();
    motion.input.beginThumbGesture();
    pointerId = event.pointerId;
    pointerStart = pendingPointer = readPointer(event);
    scrollStart = geometry.offset;
    elements.host.setAttribute(DEN_SCROLLBAR_DRAGGING_ATTR, axis);
    handle.setPointerCapture?.(pointerId);
  };

  const onPointerMove = (event: PointerEvent) => {
    if (event.pointerId !== pointerId) return;
    event.preventDefault();
    pendingPointer = readPointer(event);
    schedule();
  };

  const onTrackPointerDown = (event: PointerEvent) => {
    if (
      !event.isPrimary ||
      event.button !== 0 ||
      pointerId !== undefined ||
      (event.target instanceof Node && handle.contains(event.target))
    ) {
      return;
    }
    const geometry = readGeometry();
    const trackRect = track.getBoundingClientRect();
    const handleRect = handle.getBoundingClientRect();
    const trackSize = axis === "y" ? trackRect.height : trackRect.width;
    const handleSize = axis === "y" ? handleRect.height : handleRect.width;
    const range = Math.max(0, geometry.extent - geometry.viewport);
    const travel = Math.max(0, trackSize - handleSize);
    if (range <= 0 || travel <= 0) return;

    const pointer = readPointer(event);
    const trackStart = axis === "y" ? trackRect.top : trackRect.left;
    const handleStart = axis === "y" ? handleRect.top : handleRect.left;
    const handleCenter = handleStart - trackStart + handleSize / 2;
    const next = geometry.offset + ((pointer - trackStart - handleCenter) / travel) * range;
    event.preventDefault();
    event.stopPropagation();
    motion.input.beginThumbGesture();
    commit(next, "track_click", range);
    motion.input.endThumbGesture();
  };

  track.addEventListener("pointerdown", onTrackPointerDown);
  handle.addEventListener("pointerdown", onPointerDown);
  handle.addEventListener("pointermove", onPointerMove);
  handle.addEventListener("pointerup", finish);
  handle.addEventListener("pointercancel", finish);
  handle.addEventListener("lostpointercapture", finish);

  return {
    refreshGeometry() {
      if (pointerId === undefined) return false;
      const geometry = readGeometry();
      if (Math.max(0, geometry.extent - geometry.viewport) === appliedRange) return false;
      applyPosition();
      return true;
    },
    dispose() {
      finish();
      scrollbar.scrollbar.classList.remove(
        HANDLE_INTERACTIVE_CLASS,
        TRACK_INTERACTIVE_CLASS,
      );
      track.removeEventListener("pointerdown", onTrackPointerDown);
      handle.removeEventListener("pointerdown", onPointerDown);
      handle.removeEventListener("pointermove", onPointerMove);
      handle.removeEventListener("pointerup", finish);
      handle.removeEventListener("pointercancel", finish);
      handle.removeEventListener("lostpointercapture", finish);
    },
  };
}

export function bindOverlayScrollbarInput(
  elements: ScrollbarElements,
  motion: ScrollportMotion,
  vertical?: ScrollbarAxisModel,
): ScrollbarInput {
  const stopX = bindAxis(elements, "x", motion);
  const stopY = bindAxis(elements, "y", motion, vertical);
  return {
    dispose() { stopX.dispose(); stopY.dispose(); },
    refreshGeometry() {
      const x = stopX.refreshGeometry();
      const y = stopY.refreshGeometry();
      return x || y;
    },
  };
}
