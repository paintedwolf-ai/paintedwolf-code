import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import { measureSync, perfMark } from "../chat/stream/den-main-thread-perf.ts";

/**
 * Shared wall clock for time copy. Ticks each second while an elapsed readout
 * subscribes, otherwise on minute boundaries; stops with no subscribers.
 */
export type NowResolution = "second" | "minute";

const SECOND_MS = 1000;
const MINUTE_MS = 60_000;

const [nowSecondMs, setNowSecondMs] = createSignal(Date.now());
const [nowMinuteMs, setNowMinuteMs] = createSignal(Date.now());
let lastMinute = Math.floor(Date.now() / MINUTE_MS);

const subscribers: Record<NowResolution, number> = { second: 0, minute: 0 };
let running: NowResolution | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;

function sample(): void {
  const now = Date.now();
  const currentMinute = Math.floor(now / MINUTE_MS);
  measureSync("clock.setNow", () => {
    if (subscribers.second > 0) setNowSecondMs(now);
    if (running === "minute" || currentMinute !== lastMinute) {
      lastMinute = currentMinute;
      setNowMinuteMs(now);
    }
  });
}

function scheduleMinute(): void {
  const now = Date.now();
  timer = setTimeout(() => {
    sample();
    scheduleMinute();
  }, MINUTE_MS - (now % MINUTE_MS) + 50);
}

// A sleeping machine or a hidden window misses ticks; resample when it returns.
function resample(): void {
  if (document.visibilityState === "visible") sample();
}

function clear(): void {
  if (running === "second") clearInterval(timer);
  else if (running === "minute") clearTimeout(timer);
  timer = undefined;
}

function reschedule(): void {
  const next: NowResolution | null =
    subscribers.second > 0 ? "second" : subscribers.minute > 0 ? "minute" : null;
  if (next === running) return;
  const wasIdle = running === null;
  clear();
  running = next;
  if (next === null) {
    perfMark("now-clock-stop");
    if (typeof document !== "undefined") {
      document.removeEventListener("visibilitychange", resample);
    }
    return;
  }
  if (wasIdle) {
    perfMark("now-clock-start");
    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", resample);
    }
  }
  const now = Date.now();
  lastMinute = Math.floor(now / MINUTE_MS);
  if (subscribers.second > 0) setNowSecondMs(now);
  setNowMinuteMs(now);
  if (next === "second") timer = setInterval(sample, SECOND_MS);
  else scheduleMinute();
}

// Hot reload disposes the shared timer.
import.meta.hot?.dispose(() => {
  subscribers.second = 0;
  subscribers.minute = 0;
  reschedule();
});

/** The shared clock at `resolution`, running while `enabled` holds for this caller. */
export function useNow(
  resolution: NowResolution,
  enabled: () => boolean = () => true,
): () => number {
  let subscribed = false;
  const sync = (next: boolean) => {
    if (next === subscribed) return;
    subscribed = next;
    subscribers[resolution] = Math.max(0, subscribers[resolution] + (next ? 1 : -1));
    reschedule();
  };
  sync(untrack(enabled));
  createEffect(() => sync(enabled()));
  onCleanup(() => sync(false));
  return resolution === "second" ? nowSecondMs : nowMinuteMs;
}
