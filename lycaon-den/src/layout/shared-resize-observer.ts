import { batch } from "solid-js";
import { measureSync } from "../chat/stream/den-main-thread-perf.ts";

/** Observed content-box size in CSS pixels. */
export type SharedContentBox = {
  width: number;
  height: number;
  borderWidth?: number;
  borderHeight?: number;
};

export type SharedResizeListener = (box: SharedContentBox) => void;

const listeners = new Map<Element, Set<SharedResizeListener>>();
let lastBox = new WeakMap<Element, SharedContentBox>();
let observer: ResizeObserver | undefined;
let deliveryToken: object | undefined;
const pendingDeliveries = new Map<Element, SharedContentBox>();

function boxFromEntry(entry: ResizeObserverEntry): SharedContentBox {
  const border = entry.borderBoxSize?.[0];
  return {
    width: entry.contentRect.width,
    height: entry.contentRect.height,
    ...(border ? { borderWidth: border.inlineSize, borderHeight: border.blockSize } : {}),
  };
}

function deliver(el: Element, box: SharedContentBox): void {
  const set = listeners.get(el);
  if (!set) return;
  // A listener may unsubscribe itself or a sibling while reacting.
  for (const listener of [...set]) listener(box);
}

function flushDeliveries(): void {
  deliveryToken = undefined;
  const deliveries = [...pendingDeliveries];
  pendingDeliveries.clear();
  measureSync("resize.deliver", () => batch(() => {
    for (const [el, box] of deliveries) deliver(el, box);
  }));
}

function scheduleDelivery(el: Element, box: SharedContentBox): void {
  pendingDeliveries.set(el, box);
  if (deliveryToken) return;
  const token = {};
  deliveryToken = token;
  queueMicrotask(() => {
    if (deliveryToken === token) flushDeliveries();
  });
}

function ensureObserver(): ResizeObserver | undefined {
  if (typeof ResizeObserver === "undefined") return undefined;
  if (observer) return observer;
  observer = new ResizeObserver((entries) => {
    for (const entry of entries) {
      const box = boxFromEntry(entry);
      const prior = lastBox.get(entry.target);
      if (prior && prior.width === box.width && prior.height === box.height &&
        prior.borderWidth === box.borderWidth && prior.borderHeight === box.borderHeight) continue;
      lastBox.set(entry.target, box);
      scheduleDelivery(entry.target, box);
    }
  });
  return observer;
}

/** Publish observed sizes before paint so live reflow cannot use stale geometry. */
export function observeSharedContentBox(
  el: Element,
  listener: SharedResizeListener,
): () => void {
  let set = listeners.get(el);
  if (!set) {
    set = new Set();
    listeners.set(el, set);
    ensureObserver()?.observe(el);
  }
  set.add(listener);
  const existing = lastBox.get(el);
  if (existing && !pendingDeliveries.has(el)) listener(existing);

  return () => {
    const current = listeners.get(el);
    if (!current) return;
    current.delete(listener);
    if (current.size > 0) return;
    listeners.delete(el);
    pendingDeliveries.delete(el);
    lastBox.delete(el);
    observer?.unobserve(el);
  };
}

/** Reset multiplex state between tests. */
export function resetSharedResizeObserverForTests(): void {
  observer?.disconnect();
  observer = undefined;
  deliveryToken = undefined;
  pendingDeliveries.clear();
  lastBox = new WeakMap();
  listeners.clear();
}
