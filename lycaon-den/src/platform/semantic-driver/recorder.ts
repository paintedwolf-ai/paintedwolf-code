/** In-page timeline recorder streaming layout shifts, tasks, and geometry to the host via CDP binding. */

import { describeElement } from "./core.ts";

export type RecordOptions = {
  binding: string;
  watch?: string[];
};

type RecordEvent = Record<string, unknown> & { kind: string; t: number };

type WatchSample = {
  present: boolean;
  x?: number;
  y?: number;
  width?: number;
  height?: number;
  scroll_top?: number;
  scroll_left?: number;
};

const MAX_WATCH = 8;
const MAX_SHIFT_SOURCES = 3;
const GEOMETRY_EPSILON_PX = 0.5;

type Recorder = { stop(): void };

declare global {
  interface Window {
    __lycaonRecorder?: Recorder;
  }
}

function wallTime(performanceTime: number): number {
  return performance.timeOrigin + performanceTime;
}

function round(n: number): number {
  return Math.round(n * 10) / 10;
}

function boxOf(r: DOMRectReadOnly): { x: number; y: number; width: number; height: number } {
  return { x: round(r.x), y: round(r.y), width: round(r.width), height: round(r.height) };
}

function sample(selector: string): WatchSample {
  let el: Element | null = null;
  try {
    el = document.querySelector(selector);
  } catch {
    el = null;
  }
  if (!el) return { present: false };
  const out: WatchSample = { present: true, ...boxOf(el.getBoundingClientRect()) };
  if (el.scrollHeight > el.clientHeight || el.scrollWidth > el.clientWidth) {
    out.scroll_top = round(el.scrollTop);
    out.scroll_left = round(el.scrollLeft);
  }
  return out;
}

function moved(a: WatchSample | undefined, b: WatchSample): boolean {
  if (!a || a.present !== b.present) return true;
  for (const key of ["x", "y", "width", "height", "scroll_top", "scroll_left"] as const) {
    if (Math.abs((a[key] ?? 0) - (b[key] ?? 0)) > GEOMETRY_EPSILON_PX) return true;
  }
  return false;
}

/** Starts streaming; a recording already running in this document is replaced. */
export function startRecording(opts: RecordOptions): { ok: boolean } {
  window.__lycaonRecorder?.stop();
  const deliver = (window as unknown as Record<string, unknown>)[opts.binding];
  if (typeof deliver !== "function") return { ok: false };
  const watch = (opts.watch ?? []).slice(0, MAX_WATCH);
  let queue: RecordEvent[] = [];
  const flush = () => {
    if (queue.length === 0) return;
    const batch = queue;
    queue = [];
    (deliver as (payload: string) => void)(JSON.stringify(batch));
  };
  const observers: PerformanceObserver[] = [];
  const observe = (type: string, onEntry: (entry: PerformanceEntry) => void) => {
    if (!PerformanceObserver.supportedEntryTypes?.includes(type)) return;
    const observer = new PerformanceObserver((list) => list.getEntries().forEach(onEntry));
    observer.observe({ type, buffered: false });
    observers.push(observer);
  };
  observe("layout-shift", (entry) => {
    const shift = entry as PerformanceEntry & {
      value: number;
      hadRecentInput: boolean;
      sources?: { node?: Node | null; previousRect: DOMRectReadOnly; currentRect: DOMRectReadOnly }[];
    };
    queue.push({
      kind: "layout_shift",
      t: wallTime(shift.startTime),
      value: Math.round(shift.value * 10_000) / 10_000,
      had_recent_input: shift.hadRecentInput || undefined,
      sources: (shift.sources ?? []).slice(0, MAX_SHIFT_SOURCES).map((s) => ({
        node: s.node instanceof Element ? describeElement(s.node) : undefined,
        from: boxOf(s.previousRect),
        to: boxOf(s.currentRect),
      })),
    });
  });
  observe("longtask", (entry) => {
    queue.push({ kind: "long_task", t: wallTime(entry.startTime), duration_ms: round(entry.duration) });
  });
  const last = new Map<string, WatchSample>();
  // The host samples text geometry for screening only when this epoch moves.
  let geometryEpoch = 0;
  let reportedEpoch = -1;
  let seenMutations = mutationCount();
  const geometryMoved = () => {
    geometryEpoch++;
  };
  document.addEventListener("scroll", geometryMoved, { capture: true, passive: true });
  window.addEventListener("resize", geometryMoved, { passive: true });
  let frame = 0;
  const tick = () => {
    const now = wallTime(performance.now());
    const mutations = mutationCount();
    if (mutations !== seenMutations) {
      seenMutations = mutations;
      geometryEpoch++;
    }
    if (geometryEpoch !== reportedEpoch) {
      reportedEpoch = geometryEpoch;
      queue.push({ kind: "geometry", t: now, epoch: geometryEpoch });
    }
    for (const selector of watch) {
      const next = sample(selector);
      if (moved(last.get(selector), next)) {
        last.set(selector, next);
        queue.push({ kind: "watch", t: now, selector, ...next });
      }
    }
    flush();
    frame = requestAnimationFrame(tick);
  };
  tick();
  window.__lycaonRecorder = {
    stop() {
      cancelAnimationFrame(frame);
      observers.forEach((o) => o.disconnect());
      document.removeEventListener("scroll", geometryMoved, { capture: true });
      window.removeEventListener("resize", geometryMoved);
      flush();
      if (window.__lycaonRecorder === this) window.__lycaonRecorder = undefined;
    },
  };
  return { ok: true };
}

/** The driver's idle hooks count every DOM mutation; the recorder reads that count. */
function mutationCount(): number {
  const count = (window as unknown as { __lycaonMutations?: unknown }).__lycaonMutations;
  return typeof count === "number" ? count : 0;
}

export function stopRecording(): { ok: boolean } {
  window.__lycaonRecorder?.stop();
  return { ok: true };
}
