import { flushScrollDebugBatch, sendScrollDebugBatch } from "../../api/scroll-debug.ts";

let scrollDebugLevel: number | undefined;
const debugSinkPayloads: string[] = [];
let debugSinkTimer: ReturnType<typeof setTimeout> | undefined;
let debugSinkRequest: AbortController | undefined;
let lifecycleBound = false;
let queuedBytes = 0;

export const DEN_SCROLL_DEBUG_SINK_FLUSH_MS = 500;
const MAX_DEBUG_SINK_PAYLOADS = 256;
// Three bytes per code unit bounds UTF-8 JSON and leaves beacon headroom.
const MAX_DEBUG_SINK_BYTES = 24 * 1024;
let droppedDebugSinkPayloads = 0;

function resolveScrollDebugLevel(): number {
  try {
    const ls =
      typeof localStorage !== "undefined"
        ? localStorage.getItem("den:scroll-debug")
        : null;
    if (ls === "0") return 0;
    if (ls === "1") return 1;
    if (ls === "2") return 2;
  } catch {
    // Storage is optional.
  }
  const env =
    typeof import.meta !== "undefined"
      ? import.meta.env?.VITE_DEN_SCROLL_DEBUG
      : undefined;
  if (env === "2") return 2;
  return env === "1" ? 1 : 0;
}

/** Current scroll-debug verbosity: 0 off, 1 events, 2 per-write traces. */
function streamScrollDebugLevel(): number {
  if (scrollDebugLevel === undefined) {
    scrollDebugLevel = resolveScrollDebugLevel();
  }
  return scrollDebugLevel;
}

/** Whether scroll/reveal debug logging is active. */
export function isStreamScrollDebugEnabled(): boolean {
  return streamScrollDebugLevel() > 0;
}

/** Level 2 records individual scrollTop writes. */
export function isStreamScrollTraceVerbose(): boolean {
  return streamScrollDebugLevel() >= 2;
}

/** Reset cached debug state and dispose pending transport. */
export function resetStreamScrollDebugForTests(): void {
  scrollDebugLevel = undefined;
  debugSinkPayloads.length = 0;
  queuedBytes = 0;
  droppedDebugSinkPayloads = 0;
  if (debugSinkTimer !== undefined) clearTimeout(debugSinkTimer);
  debugSinkTimer = undefined;
  debugSinkRequest?.abort();
  debugSinkRequest = undefined;
  if (lifecycleBound) {
    window.removeEventListener("pagehide", flushLifecycle);
    document.removeEventListener("visibilitychange", flushHidden);
    lifecycleBound = false;
  }
}

type DebugDetail = Record<string, string | number | boolean | undefined>;

function takeBatch(): { body: string; records: number } {
  const payloads = debugSinkPayloads.splice(0);
  const records = payloads.length + droppedDebugSinkPayloads;
  queuedBytes = 0;
  if (droppedDebugSinkPayloads > 0) {
    payloads.unshift(`${JSON.stringify({
      ts: new Date().toISOString(), channel: "perf", event: "debug-sink-overflow",
      dropped: droppedDebugSinkPayloads,
    })}\n`);
    droppedDebugSinkPayloads = 0;
  }
  return { body: payloads.join(""), records };
}

function flushLifecycle(): void {
  if (debugSinkTimer !== undefined) clearTimeout(debugSinkTimer);
  debugSinkTimer = undefined;
  if (!debugSinkPayloads.length && !droppedDebugSinkPayloads) return;
  const { body, records } = takeBatch();
  try {
    if (!flushScrollDebugBatch(body)) {
      droppedDebugSinkPayloads += records;
    }
  } catch {
    droppedDebugSinkPayloads += records;
  }
}

function flushHidden(): void {
  if (document.visibilityState === "hidden") flushLifecycle();
}

function flushDebugSink(): void {
  debugSinkTimer = undefined;
  if (debugSinkRequest || (!debugSinkPayloads.length && !droppedDebugSinkPayloads)) return;
  const { body, records } = takeBatch();
  const controller = new AbortController();
  debugSinkRequest = controller;
  const timeout = setTimeout(() => controller.abort(), 5000);
  // One outstanding request bounds transport work even when the sink is slow.
  void sendScrollDebugBatch(body, controller.signal).then((ok) => {
    if (!ok && debugSinkRequest === controller) droppedDebugSinkPayloads += records;
  }).catch(() => {
    if (debugSinkRequest === controller) droppedDebugSinkPayloads += records;
  }).finally(() => {
    clearTimeout(timeout);
    if (debugSinkRequest !== controller) return;
    debugSinkRequest = undefined;
    if (debugSinkPayloads.length) scheduleDebugSinkFlush();
  });
}

function scheduleDebugSinkFlush(): void {
  if (debugSinkTimer !== undefined || debugSinkRequest) return;
  debugSinkTimer = setTimeout(flushDebugSink, DEN_SCROLL_DEBUG_SINK_FLUSH_MS);
}

function enqueueDebugSink(payload: object): void {
  if (!lifecycleBound) {
    window.addEventListener("pagehide", flushLifecycle);
    document.addEventListener("visibilitychange", flushHidden);
    lifecycleBound = true;
  }
  const line = `${JSON.stringify(payload)}\n`;
  const bytes = line.length * 3;
  if (debugSinkPayloads.length < MAX_DEBUG_SINK_PAYLOADS && queuedBytes + bytes <= MAX_DEBUG_SINK_BYTES) {
    debugSinkPayloads.push(line);
    queuedBytes += bytes;
  } else {
    droppedDebugSinkPayloads++;
  }
  scheduleDebugSinkFlush();
}

import.meta.hot?.dispose(resetStreamScrollDebugForTests);

/** Level 1 captures batches; level 2 also prints interactive console traces. */
export function denScrollDebugLog(
  channel: "scroll" | "reveal" | "perf",
  event: string,
  detail: DebugDetail = {},
): void {
  if (!isStreamScrollDebugEnabled()) return;

  const payload = {
    ts: new Date().toISOString(),
    channel,
    event,
    ...detail,
  };

  const sinkEnabled = import.meta.env.VITE_DEN_SCROLL_DEBUG === "1" ||
    import.meta.env.VITE_DEN_SCROLL_DEBUG === "2";
  if (!sinkEnabled || isStreamScrollTraceVerbose()) {
    const compact = Object.entries(detail)
      .filter(([, v]) => v !== undefined)
      .map(([k, v]) => `${k}=${String(v)}`)
      .join(" ");
    console.info(compact ? `[den:${channel}] ${event} ${compact}` : `[den:${channel}] ${event}`);
  }
  if (sinkEnabled) enqueueDebugSink(payload);
}
