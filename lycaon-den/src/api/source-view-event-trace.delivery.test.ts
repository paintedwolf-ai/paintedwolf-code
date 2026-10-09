// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { subscribeEvents } from "./events.ts";

const { phases } = vi.hoisted(() => ({ phases: [] as string[] }));
vi.mock("./source-view-event-trace.ts", () => ({
  traceSourceViewDelivery: (_event: unknown, phase: string) => phases.push(phase),
}));
vi.mock("../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "trace-client" }));
afterEach(() => { vi.unstubAllGlobals(); phases.length = 0; });

it.each(["handler", "invalidation", "success"])("records completion only after successful %s dispatch", async failure => {
  let frame: FrameRequestCallback | undefined;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { frame = callback; return 1; });
  vi.stubGlobal("cancelAnimationFrame", () => { frame = undefined; });
  const apply = (phase: string) => {
    phases.push(phase);
    if (failure === phase) throw new Error("Fixture apply failure");
  };
  const errors = vi.fn();
  const subscription = subscribeEvents({ baseUrl: "http://fixture", apiToken: "fixture" }, "project", {
    source_view: () => apply("handler"),
  }, {
    onInvalidate: () => apply("invalidation"), onError: errors,
    connect: async function* (_connection, _project, signal) {
      yield { comment: "connected" };
      yield { data: JSON.stringify({
        v: 1, event_id: "event", cursor: "cursor", topic: "source_view",
        published_at: new Date().toISOString(), scope: { kind: "project", project_id: "project" },
        data: { view_id: "view", kind: "tree", intent_revision: "intent", projection_revision: "projection", invalidated: false, terminal: true },
      }) };
      await new Promise<void>(resolve => {
        if (signal.aborted) resolve();
        else signal.addEventListener("abort", () => resolve(), { once: true });
      });
    },
  });
  try {
    await subscription.ready;
    await vi.waitFor(() => expect(frame).toBeDefined());
    frame!(0);
    if (failure === "success") {
      expect(phases).toEqual(["received", "applying", "handler", "invalidation", "applied"]);
      expect(errors).not.toHaveBeenCalled();
    } else {
      expect(phases).not.toContain("applied");
      expect(errors).toHaveBeenCalledOnce();
    }
  } finally { await subscription.close(); }
});
