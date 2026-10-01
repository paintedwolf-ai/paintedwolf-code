import { cancelScrollportFrame, scheduleScrollportFrame } from "../../../platform/scrolling/scrollport-frame.ts";

type Binding = { key: string; generation: unknown };
export type RowSize = { index: number; size: number };

/** One observer and read-before-write queue for a transcript's mounted rows. */
export function createTranscriptRowMeasurements(opts: {
  viewport: () => HTMLElement | null;
  identity: (element: Element) => { key: string; index: number } | null;
  generation: () => unknown;
  read: (element: Element, entry?: ResizeObserverEntry) => number;
  publish: (sizes: readonly RowSize[]) => void;
}) {
  const bindings = new Map<Element, Binding>();
  const dirty = new Set<Element>();
  let scheduledViewport: HTMLElement | null = null;
  let fallbackFrame: number | undefined;
  let disposed = false;
  let observer: ResizeObserver | undefined;
  const cancel = () => {
    if (scheduledViewport) cancelScrollportFrame(scheduledViewport, flush);
    scheduledViewport = null;
    if (fallbackFrame !== undefined) cancelAnimationFrame(fallbackFrame);
    fallbackFrame = undefined;
  };
  const forget = (element: Element) => {
    observer?.unobserve(element);
    bindings.delete(element);
    dirty.delete(element);
  };
  const register = (element: Element) => {
    const identity = opts.identity(element);
    if (!identity) return;
    if (!bindings.has(element)) {
      if (!observer && typeof ResizeObserver !== "undefined") observer = new ResizeObserver(onResize);
      observer?.observe(element, { box: "border-box" });
    }
    bindings.set(element, { key: identity.key, generation: opts.generation() });
  };
  const measure = (elements: Iterable<Element>, entries?: Map<Element, ResizeObserverEntry>) => {
    if (disposed) return;
    const sizes: RowSize[] = [];
    for (const element of elements) {
      const identity = opts.identity(element);
      if (!identity) { forget(element); continue; }
      const size = opts.read(element, entries?.get(element));
      if (Number.isFinite(size) && size > 0) sizes.push({ index: identity.index, size });
      register(element);
      dirty.delete(element);
    }
    if (dirty.size === 0) cancel();
    opts.publish(sizes);
  };
  function flush() {
    cancel();
    const elements = [...dirty].filter((element) => element.isConnected);
    for (const element of dirty) if (!element.isConnected) forget(element);
    measure(elements);
  }
  const queue = (element: Element) => {
    if (disposed || !opts.identity(element)) return;
    register(element);
    dirty.add(element);
    if (scheduledViewport || fallbackFrame !== undefined) return;
    scheduledViewport = opts.viewport();
    if (scheduledViewport) scheduleScrollportFrame(scheduledViewport, "measure", flush);
    else fallbackFrame = requestAnimationFrame(flush);
  };
  function onResize(entries: ResizeObserverEntry[]) {
    const current = new Map<Element, ResizeObserverEntry>();
    for (const entry of entries) {
      const element = entry.target;
      const binding = bindings.get(element);
      const identity = opts.identity(element);
      if (!element.isConnected || !binding || !identity) { forget(element); continue; }
      if (binding.key !== identity.key || binding.generation !== opts.generation()) {
        queue(element);
        continue;
      }
      current.set(element, entry);
    }
    // ResizeObserver already has this frame's boxes; no extra animation frame.
    measure(current.keys(), current);
  }
  return {
    measure,
    queue,
    forget,
    /** Keys of the rows an observed element currently renders. */
    mountedKeys: () => {
      const keys = new Set<string>();
      for (const [element, binding] of bindings) if (element.isConnected) keys.add(binding.key);
      return keys;
    },
    remeasure: () => { for (const element of bindings.keys()) if (element.isConnected) queue(element); },
    sweep: () => { for (const element of bindings.keys()) if (!element.isConnected) forget(element); },
    dispose: () => {
      disposed = true;
      cancel();
      observer?.disconnect();
      bindings.clear();
      dirty.clear();
    },
  };
}
