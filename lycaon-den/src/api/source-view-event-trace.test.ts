import { beforeEach, describe, expect, it, vi } from "vitest";
import type { EventEnvelope } from "./types.ts";
import { traceSourceViewDelivery, traceSourceViewState } from "./source-view-event-trace.ts";
const { enabled, post } = vi.hoisted(() => ({ enabled: vi.fn(() => true), post: vi.fn() }));
vi.mock("../chat/stream/den-main-thread-perf.ts", () => ({ isPerfCaptureEnabled: enabled }));
vi.mock("../chat/stream/den-perf-capture.ts", () => ({ postDenPerfEvents: post }));
vi.mock("../chat/stream/den-scroll-debug.ts", () => ({ isStreamScrollDebugEnabled: () => false }));
const envelope = () => ({ event_id: "event", topic: "source_view", published_at: new Date().toISOString(), data: { view_id: "view" } }) as EventEnvelope;
beforeEach(() => { post.mockClear(); enabled.mockReturnValue(true); });
describe("source view delivery trace", () => {
  it("separates browser frame delay from host publication age without body text", () => {
    const event = envelope();
    traceSourceViewDelivery(event, "received");
    traceSourceViewDelivery(event, "applied");
    const record = post.mock.calls[1]![0][0];
    expect(record.event).toBe("source-view-applied");
    expect(record.detail.queue_ms).toBeGreaterThanOrEqual(0);
    expect(record.detail).toMatchObject({ event_id: "event", view_id: "view", published_at: event.published_at });
    expect(record.detail).not.toHaveProperty("data");
  });
  it("does no capture work when debug is disabled", () => {
    enabled.mockReturnValue(false);
    traceSourceViewDelivery(envelope(), "received");
    traceSourceViewState("view", "ready", "installed");
    expect(post).not.toHaveBeenCalled();
  });
});
