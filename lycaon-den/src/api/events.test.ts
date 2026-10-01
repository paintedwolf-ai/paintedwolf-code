// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";

vi.mock("../platform/connection/client-identity.ts", () => ({
  clientIdentity: () => "client-1",
  prepareClientIdentity: async () => "client-1",
}));
import {
  SSE_DISCONNECTED_AFTER_ATTEMPTS,
  isHeartbeatComment,
  reconnectDelayMs,
  reconnectStatus,
  subscribeEvents,
} from "./events.ts";
import { isBackendReachable } from "../platform/connection/sidecar-status.ts";
import {
  buildEventsUrl,
  parseEventEnvelope,
  parseSSEBuffer,
} from "./events-sse.ts";
import { createAppStore } from "../store/app-state.ts";
import { stubClient } from "../test/client-fixture.ts";
import { invalidateApprovalGrantsCache, isApprovalGrantsCacheLoaded, loadApprovalGrantsCache } from "../settings/security/approval-grants-cache.ts";
import type { EventEnvelope } from "./types.ts";
import { LycaonApiError } from "./http.ts";

function eventEnvelope(value: Record<string, unknown>): string {
  return JSON.stringify({
    event_id: "event-1",
    cursor: "cursor-1",
    ...value,
  });
}

describe("reconnectDelayMs", () => {
  it("caps exponential backoff at 60s", () => {
    expect(reconnectDelayMs(0)).toBe(1000);
    expect(reconnectDelayMs(6)).toBe(60_000);
    expect(reconnectDelayMs(10)).toBe(60_000);
  });
});

describe("buildEventsUrl", () => {
  const connection = { baseUrl: "http://127.0.0.1:8787", apiToken: "tok" };

  it("omits project_id for the device-wide stream", () => {
    const url = new URL(buildEventsUrl(connection, ""));
    expect(url.origin).toBe("http://127.0.0.1:8787");
    expect(url.pathname).toBe("/v1/events");
    expect(url.searchParams.get("project_id")).toBeNull();
    expect(url.searchParams.get("client_id")).toBeTruthy();
  });

  // The host ends this client's editing leases when its last stream ends.
  it("names the client holding the stream", () => {
    const url = new URL(buildEventsUrl(connection, "project-1"));
    expect(url.origin).toBe("http://127.0.0.1:8787");
    expect(url.pathname).toBe("/v1/events");
    expect(url.searchParams.get("project_id")).toBe("project-1");
    expect(url.searchParams.get("client_id")).toBeTruthy();
  });
});

describe("reconnectStatus", () => {
  it("reports connecting only on a first attempt that never established", () => {
    expect(reconnectStatus(0, false)).toBe("connecting");
    expect(reconnectStatus(0, true)).toBe("reconnecting");
  });

  it("stays reachable while a short outage could still be a blip", () => {
    for (let attempt = 1; attempt < SSE_DISCONNECTED_AFTER_ATTEMPTS; attempt++) {
      const status = reconnectStatus(attempt, true);
      expect(status).toBe("reconnecting");
      expect(isBackendReachable(status)).toBe(true);
    }
  });

  it("reports the sidecar gone once retries stop looking transient", () => {
    // Disconnected status enables engine restart.
    const status = reconnectStatus(SSE_DISCONNECTED_AFTER_ATTEMPTS, true);
    expect(status).toBe("disconnected");
    expect(isBackendReachable(status)).toBe(false);
  });

  it("does not climb back out of disconnected on later attempts", () => {
    expect(reconnectStatus(SSE_DISCONNECTED_AFTER_ATTEMPTS + 25, false)).toBe(
      "disconnected",
    );
  });
});

describe("isHeartbeatComment", () => {
  it("detects ping comments", () => {
    expect(isHeartbeatComment(": ping")).toBe(true);
  });
});

describe("parseSSEBuffer", () => {
  it("parses data and comment events", () => {
    const { messages, rest } = parseSSEBuffer(
      ": connected\n\ndata: {\"v\":1,\"topic\":\"session\",\"published_at\":\"t\",\"data\":{\"id\":\"s1\",\"status\":\"idle\"}}\n\n",
    );
    expect(rest).toBe("");
    expect(messages).toHaveLength(2);
    expect(messages[0]?.comment).toBe("connected");
    expect(messages[1]?.data).toContain("session");
  });
});

describe("parseEventEnvelope", () => {
  const valid = {
    v: 1,
    event_id: "event-1",
    cursor: "cursor-1",
    topic: "session",
    published_at: "2026-01-01T00:00:00Z",
    scope: { kind: "session", project_id: "project-1", session_id: "session-1" },
    data: { id: "session-1", project_id: "project-1", action: "updated", status: "idle" },
  } satisfies EventEnvelope;

  it("accepts a complete envelope", () => {
    expect(parseEventEnvelope(JSON.stringify(valid))).toEqual(valid);
  });

  it.each([
    ["invalid JSON", "{not-json"],
    ["unknown topic", JSON.stringify({ ...valid, topic: "unknown" })],
    ["missing event id", JSON.stringify({ ...valid, event_id: undefined })],
    ["null payload", JSON.stringify({ ...valid, data: null })],
    ["invalid scope", JSON.stringify({ ...valid, scope: { kind: "session" } })],
    ["unknown field", JSON.stringify({ ...valid, extra: true })],
  ])("rejects %s", (_name, raw) => {
    expect(parseEventEnvelope(raw)).toBeNull();
  });
});

describe("subscribeEvents", () => {
  it("awaits transport cleanup when a native window closes", async () => {
    let finishCleanup!: () => void;
    const cleanup = new Promise<void>((resolve) => { finishCleanup = resolve; });
    const released = vi.fn();
    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      { connect: async function* (_connection, _project, signal) {
        try {
          yield { comment: "connected" };
          await new Promise<void>((resolve) => {
            if (signal.aborted) resolve();
            else signal.addEventListener("abort", () => resolve(), { once: true });
          });
        } finally {
          await cleanup;
          released();
        }
      } },
    );
    await sub.ready;
    let closed = false;
    const closing = Promise.resolve(sub.close()).then(() => { closed = true; });
    await Promise.resolve();
    expect(closed).toBe(false);
    expect(released).not.toHaveBeenCalled();
    finishCleanup();
    await closing;
    expect(released).toHaveBeenCalledOnce();
  });

  it("resolves ready when the stream connects", async () => {
    let open!: () => void;
    const gate = new Promise<void>((resolve) => {
      open = resolve;
    });
    async function* delayedConnection() {
      await gate;
      yield { comment: "connected" };
      await new Promise(() => {});
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      { connect: () => delayedConnection() },
    );
    let resolved = false;
    void sub.ready.then(() => {
      resolved = true;
    });

    await Promise.resolve();
    expect(resolved).toBe(false);
    open();
    await sub.ready;
    expect(resolved).toBe(true);
    void sub.close();
  });

  it("dispatches topic handlers and invalidates store slices", async () => {
    const appStore = createAppStore();
    const sessionHandler = vi.fn();
    const invalidations: string[][] = [];
    const scopes: unknown[] = [];

    async function* mockConnect() {
      yield { comment: "connected" };
      yield {
        data: eventEnvelope({
          v: 1,
          topic: "session",
          published_at: "2025-01-01T00:00:00Z",
          scope: { kind: "session", project_id: "proj-1", session_id: "sess-1" },
          data: {
            id: "sess-1",
            project_id: "proj-1",
            action: "updated",
            status: "busy",
          },
        }),
      };
      await new Promise(() => {
        /* keep stream open */
      });
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      { session: sessionHandler },
      {
        storeActions: appStore.actions,
        onInvalidate: (keys, scope) => {
          invalidations.push([...keys]);
          scopes.push(scope);
        },
        connect: () => mockConnect(),
      },
    );

    await vi.waitFor(() => {
      expect(sessionHandler).toHaveBeenCalled();
    });

    expect(invalidations[0]).toEqual(["session"]);
    expect(scopes[0]).toEqual({
      kind: "session",
      project_id: "proj-1",
      session_id: "sess-1",
    });
    expect(sessionHandler).toHaveBeenCalledWith(
      expect.objectContaining({ id: "sess-1" }),
      scopes[0],
    );
    expect(appStore.state.sidecarStatus).toBe("connected");
    void sub.close();
  });

  // Device-scoped open events reach every project stream.
  it("dispatches cli_open arriving on a stream filtered to another project", async () => {
    const cliOpenHandler = vi.fn();

    async function* mockConnect() {
      yield { comment: "connected" };
      yield {
        data: eventEnvelope({
          v: 1,
          topic: "cli_open",
          published_at: "2025-01-01T00:00:00Z",
          scope: { kind: "device" },
          data: {
            action: "open",
            project_id: "proj-other",
            path: "/Users/x/dev/other",
          },
        }),
      };
      await new Promise(() => {
        /* keep stream open */
      });
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      { cli_open: cliOpenHandler },
      { connect: () => mockConnect() },
    );

    await vi.waitFor(() => {
      expect(cliOpenHandler).toHaveBeenCalledWith(
        {
          action: "open",
          project_id: "proj-other",
          path: "/Users/x/dev/other",
        },
        { kind: "device" },
      );
    });
    void sub.close();
  });

  it("dispatches deleted sessions without merging them into foreground state", async () => {
    const appStore = createAppStore();
    const sessionHandler = vi.fn();
    const mergeSession = vi.spyOn(appStore.actions, "mergeSession");

    async function* mockConnect() {
      yield { comment: "connected" };
      yield {
        data: eventEnvelope({
          v: 1,
          topic: "session",
          published_at: "2025-01-01T00:00:00Z",
          scope: { kind: "session", project_id: "proj-1", session_id: "sess-1" },
          data: {
            id: "sess-1",
            project_id: "proj-1",
            action: "deleted",
            status: "idle",
          },
        }),
      };
      await new Promise(() => {
        /* keep stream open */
      });
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      { session: sessionHandler },
      {
        appStore,
        storeActions: appStore.actions,
        connect: () => mockConnect(),
      },
    );

    await vi.waitFor(() => expect(sessionHandler).toHaveBeenCalledOnce());
    expect(mergeSession).not.toHaveBeenCalled();
    void sub.close();
  });

  it("keeps initial-open synchronization when bootstrap replaces an unopened stream", async () => {
    const opened = vi.fn();
    const cursors: (string | undefined)[] = [];
    async function* connect(_connection: unknown, _project: string, signal: AbortSignal, cursor?: string) {
      cursors.push(cursor);
      if (cursors.length > 1) yield { comment: "connected" };
      await new Promise<void>(resolve => {
        if (signal.aborted) resolve();
        else signal.addEventListener("abort", () => resolve(), { once: true });
      });
    }
    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" }, "proj-1", {},
      { connect, onOpen: opened },
    );
    try {
      sub.resumeAfter("bootstrap-cursor");
      await sub.ready;
      expect(cursors).toEqual(["", "bootstrap-cursor"]);
      expect(opened.mock.calls).toEqual([["initial"]]);
      sub.resumeAfter("later-cursor");
      await vi.waitFor(() => expect(opened.mock.calls).toEqual([["initial"], ["resume"]]));
    } finally { await sub.close(); }
  });

  it("flushes coalesced message events before resumeAfter restarts the stream", async () => {
    const rafQueue: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      rafQueue.push(cb);
      return rafQueue.length;
    });
    vi.stubGlobal("cancelAnimationFrame", (handle: number) => {
      rafQueue[handle - 1] = () => undefined;
    });

    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "proj-1",
      posture: "build",
      status: "idle",
      workspace_path: "/tmp",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    appStore.actions.installTranscriptBaseline("sess-1", [], 0);

    let connectGeneration = 0;
    let releaseMessage!: () => void;
    const messageGate = new Promise<void>((resolve) => {
      releaseMessage = resolve;
    });
    let messageScheduled = false;
    const opened = vi.fn();

    async function* mockConnect(
      _connection: unknown,
      _projectId: string,
      signal: AbortSignal,
      after: string,
    ) {
      const generation = ++connectGeneration;
      yield { comment: "connected" };
      if (generation === 1 && !after) {
        await messageGate;
        yield {
          data: eventEnvelope({
            v: 1,
            topic: "message",
            published_at: "2025-01-01T00:00:00Z",
            cursor: "c1",
			scope: { kind: "session", project_id: "proj-1", session_id: "sess-1" },
            data: {
              session_id: "sess-1",
              op: "append",
              seq: 1,
              message: {
                id: "msg-1",
                role: "user",
                content: "hello",
                origin: "user",
                authority: "user",
                trust_tier: "trusted",
                visibility: "transcript",
                seq: 1,
                ord: 1,
                created_at: "2025-01-01T00:00:00Z",
              },
            },
          }),
        };
        messageScheduled = true;
        await new Promise<void>((resolve) => {
          signal.addEventListener("abort", () => resolve(), { once: true });
        });
        return;
      }
      await new Promise(() => {
        /* keep second stream open */
      });
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        appStore,
        storeActions: appStore.actions,
        connect: mockConnect as never,
        onOpen: opened,
      },
    );

    await vi.waitFor(() => {
      expect(appStore.state.sidecarStatus).toBe("connected");
    });
    expect(opened).toHaveBeenLastCalledWith("initial");
    opened.mockClear();
    releaseMessage();
    await vi.waitFor(() => {
      expect(messageScheduled).toBe(true);
      expect(rafQueue.length).toBeGreaterThan(0);
    });
    expect(appStore.state.messages).toHaveLength(0);
    sub.resumeAfter("c1");
    expect(appStore.state.sidecarStatus).toBe("connected");

    await vi.waitFor(() => {
      expect(connectGeneration).toBe(2);
      expect(opened).toHaveBeenCalledWith("resume");
    });
    expect(appStore.state.messages.map((m) => m.id)).toEqual(["msg-1"]);
    void sub.close();
    vi.unstubAllGlobals();
  });

  it("keeps an established sidecar connected during the initial stream open", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });

    async function* mockConnect() {
      await gate;
      yield { comment: "connected" };
      await new Promise(() => {
        /* keep stream open */
      });
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        appStore,
        storeActions: appStore.actions,
        connect: () => mockConnect(),
      },
    );

    await new Promise<void>((resolve) => queueMicrotask(resolve));
    expect(appStore.state.sidecarStatus).toBe("connected");
    release();

    await vi.waitFor(() => {
      expect(appStore.state.sidecarStatus).toBe("connected");
    });
    void sub.close();
  });

  it("wakeReconnect skips pending reconnect backoff", async () => {
    vi.useFakeTimers();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    let connectPass = 0;
    const opened = vi.fn();

    async function* mockConnect() {
      connectPass += 1;
      if (connectPass === 1) {
        throw new Error("SSE stream ended");
      }
      yield { comment: "connected" };
      await new Promise(() => {
        /* keep stream open */
      });
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        appStore,
        storeActions: appStore.actions,
        connect: () => mockConnect(),
        onOpen: opened,
      },
    );

    await vi.advanceTimersByTimeAsync(0);
    expect(appStore.state.sidecarStatus).toBe("reconnecting");

    sub.wakeReconnect();
    await vi.advanceTimersByTimeAsync(0);

    await vi.waitFor(() => {
      expect(appStore.state.sidecarStatus).toBe("connected");
    });
    expect(connectPass).toBeGreaterThanOrEqual(2);
    expect(opened).toHaveBeenCalledWith("reconnect");

    void sub.close();
    vi.useRealTimers();
  });

  it("commits a cursor only after its event is applied", async () => {
    vi.useFakeTimers();
    const after: string[] = [];
    let pass = 0;
    const handler = vi.fn().mockImplementationOnce(() => {
      throw new Error("apply failed");
    });

    async function* mockConnect(cursor = "") {
      after.push(cursor);
      pass += 1;
      yield { comment: "connected" };
      if (pass <= 2) {
        yield {
          data: eventEnvelope({
            v: 1,
            topic: "session",
            cursor: "cursor-1",
            published_at: "2025-01-01T00:00:00Z",
            scope: { kind: "session", project_id: "proj-1", session_id: "sess-1" },
            data: {
              id: "sess-1",
              project_id: "proj-1",
              action: "updated",
              status: "busy",
            },
          }),
        };
      }
      await new Promise(() => {});
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      { session: handler },
      { connect: (_connection, _projectId, _signal, cursor) => mockConnect(cursor) },
    );

    await vi.advanceTimersByTimeAsync(2_000);
    await vi.waitFor(() => expect(after).toHaveLength(2));
    expect(after).toEqual(["", ""]);
    expect(handler).toHaveBeenCalledTimes(2);
    void sub.close();
    vi.useRealTimers();
  });

  it.each(["coalesced", "sorted", "retried"] as const)(
    "replays a failed middle event after a fast reconnect with %s messages",
    async (mode) => {
      vi.useFakeTimers();
      const scope = { kind: "session", project_id: "proj-1", session_id: "sess-1" };
      const message = (id: string, seq: number, cursor: string, eventId: string) => ({
        v: 1, published_at: "2025-01-01T00:00:00Z", scope, topic: "message", cursor, event_id: eventId,
        data: { session_id: "sess-1", op: "patch", message: { id, seq, role: "assistant", content: cursor, created_at: "t" } },
      });
      const events = [
        message("m", mode === "sorted" ? 20 : 1, "1", "one"),
        { v: 1, published_at: "2025-01-01T00:00:00Z", scope, topic: "settings", cursor: "2", event_id: "two", data: { area: "appearance" } },
        message(mode === "sorted" ? "other" : "m", mode === "sorted" ? 10 : 1, "3", mode === "retried" ? "one" : "three"),
      ];
      const after: string[] = [];
      const handler = vi.fn();
      const settings = vi.fn().mockImplementationOnce(() => { throw new Error("apply failed"); });
      const reconcile = vi.fn();
      async function* connect(_connection: unknown, _project: string, _signal: AbortSignal, cursor = "") {
        after.push(cursor);
        yield { comment: "connected" };
        for (const event of events) {
          if (Number(event.cursor) > Number(cursor || "0")) yield { data: JSON.stringify(event) };
        }
        await new Promise(() => {});
      }
      const sub = subscribeEvents(
        { baseUrl: "http://127.0.0.1:1", apiToken: "tok" }, "proj-1",
        { message: handler, settings },
        { connect, onReconcile: reconcile },
      );
      await vi.advanceTimersByTimeAsync(2_100);
      expect(after).toEqual(["", mode === "sorted" ? "" : "1"]);
      expect(settings).toHaveBeenCalledTimes(2);
      expect(handler).toHaveBeenCalledTimes(mode === "sorted" ? 2 : 1);
      expect(reconcile).not.toHaveBeenCalled();
      sub.resumeAfter("3");
      await vi.advanceTimersByTimeAsync(50);
      expect(after[after.length - 1]).toBe("3");
      void sub.close();
      vi.useRealTimers();
    },
  );

  it("periodically reconciles authoritative state while disconnected", async () => {
    vi.useFakeTimers();
    const refetch = vi.fn(async () => undefined);
    async function* disconnected() {
      await new Promise(() => {});
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        connect: () => disconnected(),
        fallbackRefetchMs: 100,
        onReconcile: refetch,
      },
    );

    await vi.advanceTimersByTimeAsync(350);
    expect(refetch).toHaveBeenCalledTimes(3);
    expect(refetch).toHaveBeenCalledWith("disconnected");
    void sub.close();
    vi.useRealTimers();
  });

  it("keeps reconciliation single-flight while disconnected", async () => {
    vi.useFakeTimers();
    let finish!: () => void;
    const gate = new Promise<void>((resolve) => {
      finish = resolve;
    });
    const reconcile = vi.fn(() => gate);
    async function* disconnected() {
      await new Promise(() => {});
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        connect: () => disconnected(),
        fallbackRefetchMs: 100,
        onReconcile: reconcile,
      },
    );

    await vi.advanceTimersByTimeAsync(500);
    expect(reconcile).toHaveBeenCalledOnce();
    finish();
    await vi.advanceTimersByTimeAsync(100);
    expect(reconcile).toHaveBeenCalledTimes(2);
    void sub.close();
    vi.useRealTimers();
  });

  it("does not rearm fallback after a stream connects during a refetch", async () => {
    vi.useFakeTimers();
    let finishRefetch!: () => void;
    const refetchGate = new Promise<void>((resolve) => {
      finishRefetch = resolve;
    });
    let connectStream!: () => void;
    const connectGate = new Promise<void>((resolve) => {
      connectStream = resolve;
    });
    const refetch = vi.fn(() => refetchGate);
    const opened = vi.fn();
    async function* delayedConnection() {
      await connectGate;
      yield { comment: "connected" };
      await new Promise(() => {});
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        connect: () => delayedConnection(),
        fallbackRefetchMs: 100,
        onReconcile: refetch,
        onOpen: opened,
      },
    );

    await vi.advanceTimersByTimeAsync(100);
    expect(refetch).toHaveBeenCalledTimes(1);
    connectStream();
    await vi.advanceTimersByTimeAsync(0);
    expect(opened).not.toHaveBeenCalled();
    finishRefetch();
    await vi.advanceTimersByTimeAsync(500);
    expect(refetch).toHaveBeenCalledTimes(1);
    expect(opened).toHaveBeenCalledOnce();
    expect(opened).toHaveBeenCalledWith("initial");
    void sub.close();
    vi.useRealTimers();
  });

  it("deduplicates retried event_ids but still advances the replay cursor", async () => {
    vi.useFakeTimers();
    const after: string[] = [];
    const handler = vi.fn();
    let pass = 0;

    const sessionEvent = (cursor: string) =>
      JSON.stringify({
        v: 1,
        topic: "session",
        event_id: "e5a0c1c4-7c34-4f5a-9d0e-000000000001",
        cursor,
        published_at: "2025-01-01T00:00:00Z",
        scope: { kind: "session", project_id: "proj-1", session_id: "sess-1" },
        data: {
          id: "sess-1",
          project_id: "proj-1",
          action: "updated",
          status: "busy",
        },
      });

    async function* mockConnect(cursor = "") {
      after.push(cursor);
      pass += 1;
      yield { comment: "connected" };
      if (pass === 1) {
        yield { data: sessionEvent("cursor-1") };
        // Server retry: same event_id, later replay boundary.
        yield { data: sessionEvent("cursor-2") };
        return;
      }
      await new Promise(() => {});
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      { session: handler },
      { connect: (_connection, _projectId, _signal, cursor) => mockConnect(cursor) },
    );

    await vi.advanceTimersByTimeAsync(2_000);
    await vi.waitFor(() => expect(after).toHaveLength(2));
    // The retried delivery was applied once, but its boundary still moved.
    expect(handler).toHaveBeenCalledTimes(1);
    expect(after).toEqual(["", "cursor-2"]);
    void sub.close();
    vi.useRealTimers();
  });

  it("rebuilds state before resuming from an expired cursor", async () => {
    const after: string[] = [];
    const recover = vi.fn(async () => undefined);
    let pass = 0;

    async function* mockConnect(cursor = "") {
      after.push(cursor);
      pass += 1;
      if (pass === 1) {
        throw new LycaonApiError(
          "event replay is no longer available",
          409,
          "event_replay_unavailable",
          { details: { event_cursor: "recovery-boundary" } },
        );
      }
      yield { comment: "connected" };
      await new Promise(() => {});
    }

    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        connect: (_connection, _projectId, _signal, cursor) => mockConnect(cursor),
        onReconcile: recover,
      },
    );

    await vi.waitFor(() => expect(after).toHaveLength(2));
    expect(recover).toHaveBeenCalledOnce();
    expect(recover).toHaveBeenCalledWith("replay_unavailable");
    expect(after).toEqual(["", "recovery-boundary"]);
    void sub.close();
  });
});

describe("subscribeEvents topic dispatch", () => {
  async function driveTopic(
    topic: string,
    data: Record<string, unknown>,
  ): Promise<string[][]> {
    const invalidations: string[][] = [];
    const appStore = createAppStore();
    async function* mockConnect() {
      yield { comment: "connected" };
      yield {
        data: eventEnvelope({
          v: 1,
          topic,
          published_at: "2025-01-01T00:00:00Z",
		  scope: { kind: "project", project_id: "proj-1" },
          data,
        }),
      };
      await new Promise(() => {});
    }
    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        storeActions: appStore.actions,
        onInvalidate: (keys) => invalidations.push([...keys]),
        connect: () => mockConnect(),
      },
    );
    await vi.waitFor(() => expect(invalidations.length).toBeGreaterThan(0));
    void sub.close();
    return invalidations;
  }

  it("refreshes cached permissions after a subsequent tool approval", async () => {
    invalidateApprovalGrantsCache();
    const listApprovalGrants = vi.fn().mockResolvedValue({ grants: [], quiets: [] });
    const client = stubClient({ listApprovalGrants });
    await loadApprovalGrantsCache(client);
    expect(isApprovalGrantsCacheLoaded()).toBe(true);
    await driveTopic("checkpoint", {
      checkpoint_id: "approval-2", session_id: "s1", kind: "tool_approval",
      status: "approved", issued_at: "2026-09-19T00:00:00Z",
    });
    expect(isApprovalGrantsCacheLoaded()).toBe(false);
    await loadApprovalGrantsCache(client);
    expect(listApprovalGrants).toHaveBeenCalledTimes(2);
    invalidateApprovalGrantsCache();
  });

  it("delegation event refreshes the board and the run's topology legs", async () => {
    const got = await driveTopic("delegation", {
      delegation_id: "d1",
      status: "active",
      leg_id: "l1",
      phase: "worker",
    });
    expect(got[0]).toEqual(["board", "workflows"]);
  });

  it("workflow event fires workflows + session invalidation", async () => {
    const got = await driveTopic("workflow", { session_id: "s1" });
    expect(got[0]).toEqual(["workflows", "session"]);
  });

  it("grounding event fires session invalidation", async () => {
    const got = await driveTopic("grounding", { session_id: "s1" });
    expect(got[0]).toEqual(["session"]);
  });

  it("cost event fires only cost invalidation", async () => {
    const got = await driveTopic("cost", { task_id: "t1" });
    expect(got[0]).toEqual(["cost"]);
  });

  it.each(["providers", "model_policy"])("%s event fires its own invalidation", async (topic) => {
    const got = await driveTopic(topic, {});
    expect(got[0]).toEqual([topic]);
  });

  it("settings event dispatches per facet with its area", async () => {
    const areas: string[] = [];
    const invalidations: string[][] = [];
    const appStore = createAppStore();
    async function* mockConnect() {
      yield { comment: "connected" };
      yield {
        data: eventEnvelope({
          v: 1,
          topic: "settings",
          published_at: "2025-01-01T00:00:00Z",
          scope: { kind: "project", project_id: "proj-1" },
          data: { area: "extensions", scope: "project", action: "updated" },
        }),
      };
      await new Promise(() => {});
    }
    const sub = subscribeEvents(
      { baseUrl: "http://127.0.0.1:1", apiToken: "tok" },
      "proj-1",
      {},
      {
        storeActions: appStore.actions,
        onInvalidate: (keys) => invalidations.push([...keys]),
        onSettingsEvent: (event) => areas.push(event.area),
        connect: () => mockConnect(),
      },
    );
    await vi.waitFor(() => expect(areas.length).toBeGreaterThan(0));
    void sub.close();
    expect(areas).toEqual(["extensions"]);
    expect(invalidations[0]).toEqual([]);
  });

  it("worker event fires no invalidation (cache gate)", async () => {
    const got = await driveTopic("worker", {
      worker_id: "t1",
      status: "running",
      parent_session_id: "s1",
    });
    expect(got[0]).toEqual([]);
  });

  it("board event fires no invalidation (SSE coalescer applies snapshot)", async () => {
    const got = await driveTopic("board", {
      snapshot: {
        summary: "No workers.",
        repo: { languages: [], file_count: 0, generated_at: "t" },
        cost: null,
        pack_content_hash: "h",
        detail_level: "compact",
        board: "",
        board_chars: 0,
        truncated: false,
        now: "t",
        now_line: "Now: t",
      },
    });
    expect(got[0]).toEqual([]);
  });
});
