/** Marks a scroller while its content moves; nothing outside the scroller changes. */
export const DEN_SCROLLING_ATTR = "data-den-scrolling";

type ScrollActivityPhase = "start" | "settle";
type ScrollActivityListener = (
  phase: ScrollActivityPhase,
  target: HTMLElement,
) => void;

const SCROLL_SETTLE_MS = 120;

let targetListeners = new WeakMap<HTMLElement, Set<ScrollActivityListener>>();
const anyListeners = new Set<ScrollActivityListener>();
const activeTargets = new Set<HTMLElement>();
const lastActivity = new WeakMap<HTMLElement, number>();

let stopInstalled: (() => void) | undefined;
let settleTimer: ReturnType<typeof setTimeout> | undefined;
/** Where the pointer last was in the viewport; scrolling under it moves content, not the pointer. */
let pointerAt: { x: number; y: number } | undefined;

function now(): number {
  return performance.now();
}

function scrollTarget(event: Event): HTMLElement | null {
  if (event.target instanceof HTMLElement) return event.target;
  if (event.target instanceof Document) {
    return event.target.scrollingElement as HTMLElement | null;
  }
  return null;
}

function notify(target: HTMLElement, phase: ScrollActivityPhase): void {
  for (const listener of targetListeners.get(target) ?? []) {
    listener(phase, target);
  }
  for (const listener of anyListeners) listener(phase, target);
}

function begin(target: HTMLElement): void {
  if (!activeTargets.has(target)) {
    activeTargets.add(target);
    target.setAttribute(DEN_SCROLLING_ATTR, "");
    notify(target, "start");
  }
  lastActivity.set(target, now());
  scheduleSettleCheck();
}

function settle(target: HTMLElement): void {
  if (!activeTargets.delete(target)) return;
  target.removeAttribute(DEN_SCROLLING_ATTR);
  notify(target, "settle");
  if (activeTargets.size === 0 && settleTimer !== undefined) {
    clearTimeout(settleTimer);
    settleTimer = undefined;
  }
}

function scheduleSettleCheck(delay = SCROLL_SETTLE_MS): void {
  if (settleTimer !== undefined) return;
  settleTimer = setTimeout(checkForSettledTargets, delay);
}

function checkForSettledTargets(): void {
  settleTimer = undefined;
  const timestamp = now();
  let nextCheckMs = SCROLL_SETTLE_MS;
  let pending = false;
  for (const target of activeTargets) {
    const elapsed = timestamp - (lastActivity.get(target) ?? timestamp);
    if (elapsed >= SCROLL_SETTLE_MS) settle(target);
    else {
      pending = true;
      nextCheckMs = Math.min(nextCheckMs, SCROLL_SETTLE_MS - elapsed);
    }
  }
  if (pending) scheduleSettleCheck(nextCheckMs);
}

/** Installs the shared scroll lifecycle and resting-pointer listeners. */
export function setupScrollActivity(): () => void {
  if (stopInstalled) return stopInstalled;
  const onScroll = (event: Event) => {
    const target = scrollTarget(event);
    if (target) begin(target);
  };
  const onScrollEnd = (event: Event) => {
    const target = scrollTarget(event);
    if (target) settle(target);
  };
  const onPointerMove = (event: PointerEvent) => {
    pointerAt = { x: event.clientX, y: event.clientY };
  };
  const onPointerOut = (event: PointerEvent) => {
    if (event.relatedTarget === null) pointerAt = undefined;
  };
  window.addEventListener("scroll", onScroll, { capture: true, passive: true });
  window.addEventListener("scrollend", onScrollEnd, {
    capture: true,
    passive: true,
  });
  window.addEventListener("pointermove", onPointerMove, { capture: true, passive: true });
  window.addEventListener("pointerout", onPointerOut, { capture: true, passive: true });
  stopInstalled = () => {
    window.removeEventListener("scroll", onScroll, true);
    window.removeEventListener("scrollend", onScrollEnd, true);
    window.removeEventListener("pointermove", onPointerMove, true);
    window.removeEventListener("pointerout", onPointerOut, true);
    if (settleTimer !== undefined) clearTimeout(settleTimer);
    settleTimer = undefined;
    for (const target of activeTargets) target.removeAttribute(DEN_SCROLLING_ATTR);
    activeTargets.clear();
    pointerAt = undefined;
    stopInstalled = undefined;
  };
  return stopInstalled;
}

export function subscribeScrollActivity(
  target: HTMLElement,
  listener: ScrollActivityListener,
): () => void {
  let listeners = targetListeners.get(target);
  if (!listeners) {
    listeners = new Set();
    targetListeners.set(target, listeners);
  }
  listeners.add(listener);
  return () => {
    listeners?.delete(listener);
    if (listeners?.size === 0) targetListeners.delete(target);
  };
}

export function subscribeAnyScrollActivity(
  listener: ScrollActivityListener,
): () => void {
  anyListeners.add(listener);
  return () => anyListeners.delete(listener);
}

/** Whether content is moving in this scroller; pointer events over it then come from the content, not the pointer. */
export function isScrollActive(target: HTMLElement): boolean {
  return activeTargets.has(target);
}

/** The element under the resting pointer, for re-evaluating hover once scrolling settles. */
export function elementUnderPointer(): Element | null {
  if (!pointerAt) return null;
  return document.elementFromPoint(pointerAt.x, pointerAt.y);
}

export function resetScrollActivityForTests(): void {
  stopInstalled?.();
  targetListeners = new WeakMap();
  anyListeners.clear();
}
