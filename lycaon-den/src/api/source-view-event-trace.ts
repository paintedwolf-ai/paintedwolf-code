import type { EventEnvelope, SourceView } from "./types.ts";
import { isPerfCaptureEnabled } from "../chat/stream/den-main-thread-perf.ts";
import { postDenPerfEvents } from "../chat/stream/den-perf-capture.ts";
import { denScrollDebugLog, isStreamScrollDebugEnabled } from "../chat/stream/den-scroll-debug.ts";

const timings = new WeakMap<EventEnvelope, { received: number; applying?: number }>();

/** Local queue time is monotonic; published age includes host/browser clock skew. */
export function traceSourceViewDelivery(envelope: EventEnvelope, phase: "received" | "applying" | "applied"): void {
  if (envelope.topic !== "source_view" || !isPerfCaptureEnabled()) return;
  const now = performance.now();
  if (phase === "received") timings.set(envelope, { received: now });
  const timing = timings.get(envelope);
  if (phase === "applying" && timing) timing.applying = now;
  if (phase === "applied") timings.delete(envelope);
  const published = Date.parse(envelope.published_at);
  const detail = {
    event_id: envelope.event_id, cursor: envelope.cursor, view_id: envelope.data.view_id,
    kind: envelope.data.kind, intent_revision: envelope.data.intent_revision,
    projection_revision: envelope.data.projection_revision,
    terminal: envelope.data.terminal, invalidated: envelope.data.invalidated,
    published_at: envelope.published_at, perf_ms: now, epoch_ms: Date.now(),
    published_age_ms: Number.isFinite(published) ? Date.now() - published : undefined,
    queue_ms: timing?.applying === undefined ? undefined : timing.applying - timing.received,
    apply_ms: phase !== "applied" || timing?.applying === undefined ? undefined : now - timing.applying,
    visibility: typeof document === "undefined" ? "unavailable" : document.visibilityState,
  };
  if (isStreamScrollDebugEnabled()) denScrollDebugLog("perf", `source-view-${phase}`, detail);
  postDenPerfEvents([{ channel: "perf", event: `source-view-${phase}`, detail }]);
}

export function traceSourceViewState(
  view: Pick<SourceView, "id" | "state" | "intent_revision" | "projection_revision">,
  phase: "invalidated" | "installed",
): void {
  if (!isPerfCaptureEnabled()) return;
  const detail = { view_id: view.id, state: view.state,
    intent_revision: view.intent_revision, projection_revision: view.projection_revision,
    perf_ms: performance.now(), epoch_ms: Date.now() };
  if (isStreamScrollDebugEnabled()) denScrollDebugLog("perf", `source-view-${phase}`, detail);
  postDenPerfEvents([{ channel: "perf", event: `source-view-${phase}`, detail }]);
}
