// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { reconnectDelayMs, subscribeEvents } from "./events.ts";

vi.mock("../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "delivery-client" }));
let hidden = false;
let frame: FrameRequestCallback | undefined;
beforeEach(() => {
  hidden = false;
  frame = undefined;
  vi.useFakeTimers();
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { frame = callback; return 1; });
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });

const connection = { baseUrl: "http://fixture", apiToken: "fixture" };
const event = (id: string) => ({ data: JSON.stringify({
  v: 1, event_id: id, cursor: id, topic: "source_view", published_at: new Date().toISOString(),
  scope: { kind: "project", project_id: "project" },
  data: { view_id: id, kind: "tree", intent_revision: "intent", projection_revision: "projection", invalidated: false, terminal: true },
}) });
const aborted = (signal: AbortSignal) => new Promise<void>(resolve => {
  if (signal.aborted) resolve();
  else signal.addEventListener("abort", () => resolve(), { once: true });
});

it("applies a pending envelope on hide and close does not apply it twice", async () => {
  const applied = vi.fn();
  const sub = subscribeEvents(connection, "project", { source_view: applied }, {
    connect: async function* (_connection, _project, signal) {
      yield { comment: "connected" };
      yield event("one");
      await aborted(signal);
    },
  });
  try {
    await sub.ready;
    await vi.waitFor(() => expect(frame).toBeDefined(), { interval: 1 });
    const staleFrame = frame!;
    hidden = true;
    document.dispatchEvent(new Event("visibilitychange"));
    await Promise.resolve();
    expect(applied).toHaveBeenCalledOnce();
    await sub.close();
    staleFrame(0);
    await vi.advanceTimersByTimeAsync(100);
    expect(applied).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  } finally { await sub.close(); }
});

it("close drains pending work and cancels the hidden promotion", async () => {
  const applied = vi.fn();
  const sub = subscribeEvents(connection, "project", { source_view: applied }, {
    connect: async function* (_connection, _project, signal) {
      yield { comment: "connected" };
      yield event("one");
      await aborted(signal);
    },
  });
  await sub.ready;
  await vi.waitFor(() => expect(frame).toBeDefined(), { interval: 1 });
  hidden = true;
  document.dispatchEvent(new Event("visibilitychange"));
  await sub.close();
  frame!(0);
  expect(applied).toHaveBeenCalledOnce();
  expect(vi.getTimerCount()).toBe(0);
});

it("replays a failed hidden delivery from the applied prefix without duplicating success", async () => {
  hidden = true;
  const applied: string[] = [];
  const cursors: string[] = [];
  let fail = true;
  const errors = vi.fn();
  const sub = subscribeEvents(connection, "project", {
    source_view: data => {
      if (data.view_id === "two" && fail) { fail = false; throw new Error("Fixture apply failure"); }
      applied.push(data.view_id);
    },
  }, {
    onError: errors,
    connect: async function* (_connection, _project, signal, after) {
      cursors.push(after ?? "");
      yield { comment: "connected" };
      yield event("one");
      yield event("two");
      await aborted(signal);
    },
  });
  try {
    await sub.ready;
    await vi.waitFor(() => expect(errors).toHaveBeenCalledOnce(), { interval: 1 });
    expect(applied).toEqual(["one"]);
    await vi.advanceTimersByTimeAsync(reconnectDelayMs(1));
    await vi.waitFor(() => expect(applied).toEqual(["one", "two"]), { interval: 1 });
    expect(cursors).toEqual(["", "one"]);
    expect(frame).toBeUndefined();
  } finally { await sub.close(); }
  expect(vi.getTimerCount()).toBe(0);
});
