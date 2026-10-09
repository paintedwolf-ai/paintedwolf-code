import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { EventEnvelope } from "./types.ts";
import { traceSourceViewDelivery, traceSourceViewState } from "./source-view-event-trace.ts";
const { enabled, post } = vi.hoisted(() => ({ enabled: vi.fn(() => true), post: vi.fn() }));
vi.mock("../chat/stream/den-main-thread-perf.ts", () => ({ isPerfCaptureEnabled: enabled }));
vi.mock("../chat/stream/den-perf-capture.ts", () => ({ postDenPerfEvents: post }));
vi.mock("../chat/stream/den-scroll-debug.ts", () => ({ isStreamScrollDebugEnabled: () => false }));
const envelope = () => ({ event_id: "event", topic: "source_view", published_at: new Date().toISOString(), cursor: "cursor", data: { view_id: "view", kind: "comparison", intent_revision: "intent", projection_revision: "projection", terminal: true, invalidated: false } }) as EventEnvelope;
beforeEach(() => { post.mockClear(); enabled.mockReturnValue(true); });
afterEach(() => vi.restoreAllMocks());
describe("source view delivery trace", () => {
  it("separates browser frame delay from host publication age without body text", () => {
    vi.spyOn(performance, "now").mockReturnValueOnce(10).mockReturnValueOnce(25).mockReturnValueOnce(32);
    const event = envelope();
    traceSourceViewDelivery(event, "received");
    traceSourceViewDelivery(event, "applying");
    traceSourceViewDelivery(event, "applied");
    const record = post.mock.calls[2]![0][0];
    expect(record.event).toBe("source-view-applied");
    expect(record.detail.queue_ms).toBe(15);
    expect(record.detail.apply_ms).toBe(7);
    expect(record.detail).toMatchObject({ event_id: "event", view_id: "view", published_at: event.published_at });
    expect(record.detail).toMatchObject({ cursor: "cursor", kind: "comparison", intent_revision: "intent", projection_revision: "projection", terminal: true, invalidated: false });
    expect(record.detail).not.toHaveProperty("data");
  });
  it("records the actual installed revision instead of inferring it from the last event", () => {
    traceSourceViewDelivery(envelope(), "received");
    traceSourceViewState({ id: "view", state: "ready", intent_revision: "new-intent", projection_revision: "new-projection" }, "installed");
    expect(post.mock.calls[1]![0][0]).toMatchObject({ event: "source-view-installed", detail: {
      view_id: "view", state: "ready", intent_revision: "new-intent", projection_revision: "new-projection",
    } });
  });
  it("does no capture work when debug is disabled", () => {
    enabled.mockReturnValue(false);
    traceSourceViewDelivery(envelope(), "received");
    traceSourceViewState({ id: "view", state: "ready", intent_revision: "intent", projection_revision: "projection" }, "installed");
    expect(post).not.toHaveBeenCalled();
  });
});
