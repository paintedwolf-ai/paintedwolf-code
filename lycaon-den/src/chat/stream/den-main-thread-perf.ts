/** Main-thread stall instrumentation. */

import {
  denScrollDebugLog,
  isStreamScrollDebugEnabled,
} from "./den-scroll-debug.ts";
import { postDenPerfEvents } from "./den-perf-capture.ts";
import { fullDebugLoggingPref } from "../../settings/system/debug-prefs.ts";

const LONG_TASK_MS = 50;

const LAG_STALL_MS = 120;

const LAG_SAMPLE_INTERVAL_MS = 250;

const DEFAULT_SYNC_LOG_MS = 8;

const BREADCRUMB_MAX = 16;

const BREADCRUMB_TRAIL = 6;

let observerInstalled = false;
let performanceObserver: PerformanceObserver | null = null;
let lagSampleTimer: ReturnType<typeof setInterval> | null = null;

type Breadcrumb = { event: string; at: number; duration?: number };

const breadcrumbs: Breadcrumb[] = [];
const syncTotals = new Map<string, { count: number; total: number; max: number }>();
let totalsSince = 0;

function flushSyncTotals(now: number): void {
  if (now - totalsSince < 1000) return;
  const windowMs = now - totalsSince;
  totalsSince = now;
  const events = [...syncTotals].map(([label, totals]) => ({
    event: "sync-summary", channel: "perf",
    detail: {
      label, count: totals.count, total_ms: Math.round(totals.total * 100) / 100,
      max_ms: Math.round(totals.max * 100) / 100, window_ms: Math.round(windowMs),
    },
  }));
  if (isStreamScrollDebugEnabled()) {
    for (const event of events) denScrollDebugLog("perf", event.event, event.detail);
  }
  if (fullDebugLoggingPref()) postDenPerfEvents(events);
  syncTotals.clear();
}


function pushBreadcrumb(event: string, at: number, duration?: number): void {
  breadcrumbs.push({ event, at, duration });
  if (breadcrumbs.length > BREADCRUMB_MAX) breadcrumbs.shift();
}

function recentTrail(now: number): string {
  return breadcrumbs
    .filter((b) => now - b.at <= 5000)
    .slice(-BREADCRUMB_TRAIL)
    .map((b) => `${b.event}@-${Math.round(now - b.at)}${b.duration === undefined ? "" : `(${Math.round(b.duration)}ms)`}`)
    .join(" ");
}

function nowMs(): number {
  return typeof performance !== "undefined" &&
    typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

export function isPerfCaptureEnabled(): boolean {
  return isStreamScrollDebugEnabled() || fullDebugLoggingPref();
}

/** Reports when timer delay cannot measure main-thread load. */
function platformTimersThrottled(): boolean {
  if (typeof document === "undefined") return false;
  if (document.visibilityState !== "visible") return true;
  return typeof document.hasFocus === "function" && !document.hasFocus();
}

type PerfDetail = Record<string, string | number | boolean | undefined>;

function emitStall(event: string, detail: PerfDetail): void {
  if (isStreamScrollDebugEnabled()) {
    denScrollDebugLog("perf", event, detail);
  }
  if (fullDebugLoggingPref()) {
    postDenPerfEvents([{ event, channel: "perf", detail }]);
  }
}

export function installMainThreadPerfObserver(): void {
  if (observerInstalled) return;
  if (!isPerfCaptureEnabled()) return;
  observerInstalled = true;

  try {
    const PO = (globalThis as { PerformanceObserver?: typeof PerformanceObserver })
      .PerformanceObserver;
    if (typeof PO === "function") {
      performanceObserver = new PO((list) => {
        for (const entry of list.getEntries()) {
          if (entry.duration < LONG_TASK_MS) continue;
          emitStall("longtask", {
            dur_ms: Math.round(entry.duration),
            start_ms: Math.round(entry.startTime),
            name: entry.name,
            recent: recentTrail(nowMs()) || undefined,
          });
        }
      });
      performanceObserver.observe({
        entryTypes: ["longtask"],
      });
    }
  } catch {
    // Lag sampling covers unsupported observers.
  }

  totalsSince = nowMs();
  if (fullDebugLoggingPref()) {
    postDenPerfEvents([{
      event: "clock-sync", detail: { perf_ms: totalsSince, epoch_ms: Date.now() },
    }]);
  }
  let expected = totalsSince + LAG_SAMPLE_INTERVAL_MS;
  const sample = () => {
    const actual = nowMs();
    const lag = actual - expected;
    flushSyncTotals(actual);
    if (lag >= LAG_STALL_MS) {
      // Background timer throttling is not a main-thread stall.
      emitStall(platformTimersThrottled() ? "loop-throttled" : "loop-stall", {
        lag_ms: Math.round(lag),
        recent: recentTrail(actual) || undefined,
      });
    }
    expected = nowMs() + LAG_SAMPLE_INTERVAL_MS;
  };
  lagSampleTimer = setInterval(sample, LAG_SAMPLE_INTERVAL_MS);
}

/** Stop observers and timers installed by main-thread diagnostics. */
export function uninstallMainThreadPerfObserver(): void {
  performanceObserver?.disconnect();
  performanceObserver = null;
  if (lagSampleTimer !== null) clearInterval(lagSampleTimer);
  lagSampleTimer = null;
  observerInstalled = false;
  syncTotals.clear();
}

export function resetMainThreadPerfObserverForTests(): void {
  uninstallMainThreadPerfObserver();
  breadcrumbs.length = 0;
}

if (import.meta.hot) {
  import.meta.hot.dispose(uninstallMainThreadPerfObserver);
}

export function measureSync<T>(
  label: string,
  fn: () => T,
  detail: PerfDetail = {},
  thresholdMs: number = DEFAULT_SYNC_LOG_MS,
): T {
  if (!isPerfCaptureEnabled()) return fn();
  const start = nowMs();
  try {
    return fn();
  } finally {
    recordSyncDuration(label, nowMs() - start, detail, thresholdMs);
  }
}

/** Records synchronous work that another component timed. */
export function recordSyncDuration(
  label: string,
  durMs: number,
  detail: PerfDetail = {},
  thresholdMs: number = DEFAULT_SYNC_LOG_MS,
): void {
  if (!isPerfCaptureEnabled()) return;
  let totals = syncTotals.get(label);
  if (!totals && syncTotals.size < 128) {
    totals = { count: 0, total: 0, max: 0 };
    syncTotals.set(label, totals);
  }
  if (totals) {
    totals.count++;
    totals.total += durMs;
    totals.max = Math.max(totals.max, durMs);
  }
  if (durMs >= thresholdMs) {
    pushBreadcrumb(label, nowMs(), durMs);
    if (isStreamScrollDebugEnabled()) {
      denScrollDebugLog("perf", "sync", {
        label,
        dur_ms: Math.round(durMs * 100) / 100,
        ...detail,
      });
    }
  }
}

function emitTimelineMarker(event: string, detail: PerfDetail): void {
  const keys = Object.keys(detail);
  const label = keys.length
    ? `${event} ${keys
        .filter((k) => detail[k] !== undefined)
        .map((k) => `${k}=${String(detail[k])}`)
        .join(" ")}`
    : event;
  // Diagnostics remain isolated from measured work.
  try {
    const stamp = (console as { timeStamp?: (label?: string) => void }).timeStamp;
    if (typeof stamp === "function") {
      stamp.call(console, label);
    } else if (
      typeof performance !== "undefined" &&
      typeof performance.mark === "function"
    ) {
      performance.mark(label);
    }
  } catch {
    /* Diagnostic failures are inert. */
  }
}

export function perfMark(event: string, detail: PerfDetail = {}): void {
  emitTimelineMarker(event, detail);
  if (!isPerfCaptureEnabled()) return;
  pushBreadcrumb(event, nowMs());
  if (isStreamScrollDebugEnabled()) {
    denScrollDebugLog("perf", event, detail);
  }
}
