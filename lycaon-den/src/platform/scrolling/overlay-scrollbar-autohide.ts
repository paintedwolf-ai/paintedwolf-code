import type { Elements } from "overlayscrollbars";
import {
  type ScrollportMotion,
} from "./scrollport-motion.ts";
import { DEN_SCROLLPORT_INPUT_EVENT } from "./scrollport-motion-types.ts";

/** Scrollbars carry this class while faded. */
export const DEN_SCROLLBAR_IDLE_CLASS = "den-scrollbar-idle";
/** Idle window after the last direct input before a scrollbar fades. */
export const SCROLLBAR_IDLE_DELAY_MS = 700;

const HOVER_POINTER_TYPES = new Set(["mouse", "pen"]);
/** Keys the browser scrolls a focused scrollport with. */
const SCROLL_KEYS = new Set([
  "ArrowUp",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "PageUp",
  "PageDown",
  "Home",
  "End",
  " ",
]);

type AutoHideElements = Pick<
  Elements,
  "host" | "scrollbarHorizontal" | "scrollbarVertical"
>;

function isEditableTarget(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLElement &&
    (target.isContentEditable || target.matches("input, textarea, select"))
  );
}

/** Direct input reveals the scrollbar; automatic scrolling leaves it faded. */
export function bindOverlayScrollbarAutoHide(
  elements: AutoHideElements,
  motion: ScrollportMotion,
): () => void {
  const { host } = elements;
  const bars = [
    elements.scrollbarVertical.scrollbar,
    elements.scrollbarHorizontal.scrollbar,
  ];
  let hideTimer: ReturnType<typeof setTimeout> | undefined;
  let idleAt = 0;
  let pointerOverBar = false;

  const setIdle = (idle: boolean) => {
    for (const bar of bars) bar.classList.toggle(DEN_SCROLLBAR_IDLE_CLASS, idle);
  };
  const clearHideTimer = () => {
    if (hideTimer === undefined) return;
    clearTimeout(hideTimer);
    hideTimer = undefined;
  };
  // One timer waits for the deadline; pointer traffic only moves the deadline.
  const armHide = (delay = SCROLLBAR_IDLE_DELAY_MS) => {
    idleAt = performance.now() + delay;
    if (hideTimer === undefined) hideTimer = setTimeout(onIdleDeadline, delay);
  };
  const onIdleDeadline = () => {
    hideTimer = undefined;
    const remaining = idleAt - performance.now();
    if (remaining > 0) {
      hideTimer = setTimeout(onIdleDeadline, remaining);
      return;
    }
    // A gesture still running keeps its bar until it settles.
    if (pointerOverBar || motion.input.isDirectInputActive()) {
      armHide();
      return;
    }
    setIdle(true);
  };
  const reveal = () => {
    setIdle(false);
    armHide();
  };

  let lastPointer: { x: number; y: number } | undefined;
  const onPointerMove = (event: PointerEvent) => {
    if (!HOVER_POINTER_TYPES.has(event.pointerType)) return;
    // Content scrolling under a resting pointer raises moves at the same position.
    const moved =
      lastPointer === undefined ||
      lastPointer.x !== event.screenX ||
      lastPointer.y !== event.screenY;
    lastPointer = { x: event.screenX, y: event.screenY };
    if (moved) reveal();
  };
  const onKeyDown = (event: KeyboardEvent) => {
    if (SCROLL_KEYS.has(event.key) && !isEditableTarget(event.target)) reveal();
  };
  const onBarEnter = () => {
    pointerOverBar = true;
    reveal();
  };
  const onBarLeave = () => {
    pointerOverBar = false;
    armHide();
  };

  setIdle(true);
  host.addEventListener("pointermove", onPointerMove, { passive: true });
  host.addEventListener(DEN_SCROLLPORT_INPUT_EVENT, reveal);
  host.addEventListener("keydown", onKeyDown);
  for (const bar of bars) {
    bar.addEventListener("pointerenter", onBarEnter);
    bar.addEventListener("pointerleave", onBarLeave);
  }

  return () => {
    clearHideTimer();
    host.removeEventListener("pointermove", onPointerMove);
    host.removeEventListener(DEN_SCROLLPORT_INPUT_EVENT, reveal);
    host.removeEventListener("keydown", onKeyDown);
    for (const bar of bars) {
      bar.removeEventListener("pointerenter", onBarEnter);
      bar.removeEventListener("pointerleave", onBarLeave);
      bar.classList.remove(DEN_SCROLLBAR_IDLE_CLASS);
    }
  };
}
