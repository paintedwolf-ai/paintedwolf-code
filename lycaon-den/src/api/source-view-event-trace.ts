import type { EventEnvelope } from "./types.ts";
import { isPerfCaptureEnabled } from "../chat/stream/den-main-thread-perf.ts";
import { postDenPerfEvents } from "../chat/stream/den-perf-capture.ts";
import { denScrollDebugLog, isStreamScrollDebugEnabled } from "../chat/stream/den-scroll-debug.ts";

const received = new WeakMap<EventEnvelope, number>();

/** Local queue time is monotonic; published age includes host/browser clock skew. */
export function traceSourceViewDelivery(envelope: EventEnvelope, phase: "received" | "applied"): void {
  if (envelope.topic !== "source_view" || !isPerfCaptureEnabled()) return;
  const now = performance.now();
  const receivedAt = phase === "received" ? now : received.get(envelope);
  if (phase === "received") received.set(envelope, now);
  else received.delete(envelope);
  const published = Date.parse(envelope.published_at);
  const detail = {
    event_id: envelope.event_id, view_id: envelope.data.view_id,
    published_at: envelope.published_at, perf_ms: now, epoch_ms: Date.now(),
    published_age_ms: Number.isFinite(published) ? Date.now() - published : undefined,
    queue_ms: receivedAt === undefined ? undefined : now - receivedAt,
    visibility: typeof document === "undefined" ? "unavailable" : document.visibilityState,
  };
  if (isStreamScrollDebugEnabled()) denScrollDebugLog("perf", `source-view-${phase}`, detail);
  postDenPerfEvents([{ channel: "perf", event: `source-view-${phase}`, detail }]);
}

export function traceSourceViewState(viewId: string, state: string, phase: "invalidated" | "installed"): void {
  if (!isPerfCaptureEnabled()) return;
  const detail = { view_id: viewId, state, perf_ms: performance.now(), epoch_ms: Date.now() };
  if (isStreamScrollDebugEnabled()) denScrollDebugLog("perf", `source-view-${phase}`, detail);
  postDenPerfEvents([{ channel: "perf", event: `source-view-${phase}`, detail }]);
}
