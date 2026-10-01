import { describe, expect, it } from "vitest";
import { createAppStore } from "../store/app-state.ts";
import { applyMessageEvent } from "../chat/transcript/projection/message-events.ts";
import type { EventEnvelope, EventEnvelopeBase, Message, MessageEvent } from "./types.ts";
import {
  createEventFrameQueue,
  type EventFrameScheduler,
  type EventReceipt,
} from "./event-frame-queue.ts";

function sessionStore() {
  const appStore = createAppStore();
  appStore.actions.setCurrentSession({
    id: "s1",
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "00000000-0000-4000-8000-000000000001",
    workspace_path: "/tmp/p",
    posture: "build",
    status: "idle",
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  });
  return appStore;
}

function manualScheduler(): EventFrameScheduler & { run: () => void } {
  let cb: (() => void) | undefined;
  return {
    request(fn) {
      cb = fn;
      return 1;
    },
    cancel() {
      cb = undefined;
    },
    run() {
      const fn = cb;
      cb = undefined;
      fn?.();
    },
  };
}

const SCOPE = {
  kind: "session",
  project_id: "00000000-0000-4000-8000-000000000001",
  session_id: "s1",
} as const;

let envelopeSeq = 0;

/** Wrap a topic payload in the envelope shell the stream delivers. */
function envelope<T extends EventEnvelope["topic"]>(
  topic: T,
  data: unknown,
): EventEnvelope {
  envelopeSeq += 1;
  return ({
    v: 1,
    event_id: `evt-${envelopeSeq}`,
    cursor: `c${envelopeSeq}`,
    published_at: "2025-01-01T00:00:00Z",
    topic,
    scope: SCOPE,
    data,
  } satisfies EventEnvelopeBase) as EventEnvelope;
}

function row(id: string, content: string, seq?: number): Message {
  return {
    id,
    role: "assistant",
    origin: "model",
    authority: "none",
    trust_tier: "trusted",
    content,
    created_at: "t",
    ...(seq === undefined ? {} : { seq }),
  } as Message;
}

function messageEnvelope(
  message: Message,
  op: MessageEvent["op"] = "patch",
): EventEnvelope {
  return envelope("message", { session_id: "s1", op, message });
}

/** Apply message envelopes into the store; record every topic for ordering. */
function storeApplier(appStore: ReturnType<typeof sessionStore>) {
  const seen: string[] = [];
  return {
    seen,
    apply(env: EventEnvelope) {
      seen.push(env.topic);
      if (env.topic === "message") applyMessageEvent(appStore, env.data);
    },
  };
}

describe("createEventFrameQueue", () => {
  it("checkpoints arrival order when a coalesced message crosses a failed event", () => {
    const scheduler = manualScheduler();
    const first = messageEnvelope(row("m", "first", 1));
    const middle = envelope("settings", {});
    const last = messageEnvelope(row("m", "last", 1));
    const checkpoints: string[] = [];
    const applied: string[] = [];
    const abandoned: string[] = [];
    const rendered: string[] = [];
    const queue = createEventFrameQueue((env) => {
      if (env === middle) throw new Error("settings failed");
      rendered.push(env.event_id);
    }, {
      scheduler,
      onApplied: (events) => applied.push(...events.map((e) => e.event_id)),
      onCheckpoint: (cursor) => checkpoints.push(cursor),
      onApplyError: (_, events) => abandoned.push(...events.map((e) => e.event_id)),
    });
    for (const env of [first, middle, last]) queue.enqueue(env);
    scheduler.run();
    expect(rendered).toEqual([last.event_id]);
    expect(applied).toEqual([first.event_id, last.event_id]);
    expect(checkpoints).toEqual([first.cursor]);
    expect(abandoned).toEqual([middle.event_id]);
  });

  it("does not checkpoint a later message sorted ahead of an unapplied prefix", () => {
    const scheduler = manualScheduler();
    const first = messageEnvelope(row("late", "late", 20));
    const middle = envelope("settings", {});
    const last = messageEnvelope(row("early", "early", 10));
    const checkpoints: string[] = [];
    const abandoned: string[] = [];
    const queue = createEventFrameQueue((env) => {
      if (env === middle) throw new Error("settings failed");
    }, {
      scheduler,
      onCheckpoint: (cursor) => checkpoints.push(cursor),
      onApplyError: (_, events) => abandoned.push(...events.map((e) => e.event_id)),
    });
    for (const env of [first, middle, last]) queue.enqueue(env);
    scheduler.run();
    expect(checkpoints).toEqual([]);
    expect(abandoned).toEqual([first.event_id, middle.event_id]);
  });

  it("does not let applied retries advance past buffered failures", () => {
    const scheduler = manualScheduler();
    const checkpoints: string[] = [];
    const queue = createEventFrameQueue(() => {
      throw new Error("failed");
    }, {
      scheduler,
      onCheckpoint: (cursor) => checkpoints.push(cursor),
    });
    queue.enqueue(envelope("settings", {}));
    queue.enqueue(envelope("activity", {}), true);
    scheduler.run();
    expect(checkpoints).toEqual([]);
  });

  it("retains retry receipt order across an intervening failure", () => {
    const scheduler = manualScheduler();
    const checkpoints: string[] = [];
    const first = envelope("activity", {});
    const middle = envelope("settings", {});
    const queue = createEventFrameQueue((env) => {
      if (env === middle) throw new Error("failed");
    }, { scheduler, onCheckpoint: (cursor) => checkpoints.push(cursor) });
    queue.enqueue(first);
    queue.enqueue(middle);
    queue.enqueue({ ...first, cursor: "later-retry" });
    scheduler.run();
    expect(checkpoints).toEqual([first.cursor]);
  });

  it("defers application until the frame drain", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(messageEnvelope(row("a1", "He")));
    expect(appStore.state.messages).toHaveLength(0);
    expect(queue.pending()).toBe(1);

    sched.run();
    expect(appStore.state.messages).toHaveLength(1);
    expect(appStore.state.messages[0]?.content).toBe("He");
  });

  it("collapses a burst of deltas on one row to the latest content", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(messageEnvelope(row("a1", "H")));
    queue.enqueue(messageEnvelope(row("a1", "He")));
    queue.enqueue(messageEnvelope(row("a1", "Hello")));
    expect(queue.pending()).toBe(1);
    sched.run();

    expect(appStore.state.messages).toHaveLength(1);
    expect(appStore.state.messages[0]?.content).toBe("Hello");
    expect(applier.seen).toEqual(["message"]);
  });

  it("applies distinct rows in transcript seq order", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(
      messageEnvelope(
        { ...row("pu2", "", 20), role: "system", authority: "system", origin: "host" } as Message,
        "append",
      ),
    );
    queue.enqueue(
      messageEnvelope(
        { ...row("u1", "Hi", 10), role: "user", authority: "user", origin: "user" } as Message,
        "append",
      ),
    );
    sched.run();

    // Out-of-seq arrival must not let the watermark swallow the older row.
    expect(appStore.state.messages.map((m) => m.id)).toEqual(["u1", "pu2"]);
  });

  it("applies distinct rows without seq in first-seen order", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(
      messageEnvelope(
        { ...row("u1", "Hi"), role: "user", authority: "user", origin: "user" } as Message,
        "append",
      ),
    );
    queue.enqueue(messageEnvelope(row("a1", "Hello")));
    sched.run();

    expect(appStore.state.messages.map((m) => m.id)).toEqual(["u1", "a1"]);
  });

  it("keeps a run envelope ahead of the row that names it", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(envelope("workflow", { workflow_run_id: "run-1" }));
    queue.enqueue(messageEnvelope(row("a1", "first", 1), "append"));
    queue.enqueue(envelope("checkpoint", { session_id: "s1" }));
    queue.enqueue(messageEnvelope(row("a2", "second", 2), "append"));
    sched.run();

    expect(applier.seen).toEqual([
      "workflow",
      "message",
      "checkpoint",
      "message",
    ]);
  });

  it("reorders messages by seq without moving another topic", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(messageEnvelope(row("b", "b", 20), "append"));
    queue.enqueue(envelope("activity", { session_id: "s1" }));
    queue.enqueue(messageEnvelope(row("a", "a", 10), "append"));
    sched.run();

    // The activity envelope holds slot 1; the two rows swap around it.
    expect(applier.seen).toEqual(["message", "activity", "message"]);
    expect(appStore.state.messages.map((m) => m.id)).toEqual(["a", "b"]);
  });

  it("flush applies buffered envelopes synchronously", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(messageEnvelope(row("a1", "Hello")));
    queue.flush();
    expect(appStore.state.messages[0]?.content).toBe("Hello");

    // A later frame callback is a no-op once flushed.
    sched.run();
    expect(appStore.state.messages).toHaveLength(1);
  });

  it("same-frame events collapse to the newest full-row snapshot", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(messageEnvelope(row("a1", "answer")));
    sched.run();

    // A stale snapshot then the committed row arrive in one frame: the newer
    // snapshot wins wholesale, fields absent from it are gone.
    queue.enqueue(
      messageEnvelope({
        ...row("a1", "answer", 2),
        tool_calls: [{ id: "call_1", name: "update_progress", args: {} }],
      } as Message),
    );
    queue.enqueue(messageEnvelope(row("a1", "answer final", 3)));
    sched.run();

    const msg = appStore.state.messages.find((m) => m.id === "a1");
    expect(msg?.content).toBe("answer final");
    expect(msg?.tool_calls).toBeUndefined();
  });

  it("a stale same-frame snapshot never regresses a newer one", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(messageEnvelope(row("a1", "newer", 5)));
    queue.enqueue(messageEnvelope(row("a1", "stale", 4)));
    sched.run();

    expect(appStore.state.messages.find((m) => m.id === "a1")?.content).toBe(
      "newer",
    );
  });

  it("cancel drops buffered envelopes without applying them", () => {
    const appStore = sessionStore();
    const sched = manualScheduler();
    const applier = storeApplier(appStore);
    const queue = createEventFrameQueue(applier.apply, { scheduler: sched });

    queue.enqueue(messageEnvelope(row("a1", "Hello")));
    queue.cancel();
    expect(queue.pending()).toBe(0);
    sched.run();
    expect(appStore.state.messages).toHaveLength(0);
  });

  it("drops a redelivery of an envelope already buffered", () => {
    const sched = manualScheduler();
    const seen: string[] = [];
    const queue = createEventFrameQueue((env) => seen.push(env.event_id), {
      scheduler: sched,
    });

    const env = envelope("activity", {});
    queue.enqueue(env);
    queue.enqueue(env);
    expect(queue.pending()).toBe(1);
    sched.run();
    expect(seen).toEqual([env.event_id]);
  });

  it("abandons the rest of a frame when one apply throws", () => {
    const sched = manualScheduler();
    const seen: string[] = [];
    let failure: { err: unknown; abandoned: readonly EventReceipt[] } | undefined;
    const boom = envelope("cost", {});
    const trailing = envelope("scan", {});
    const queue = createEventFrameQueue(
      (env) => {
        if (env.event_id === boom.event_id) throw new Error("apply failed");
        seen.push(env.topic);
      },
      {
        scheduler: sched,
        onApplyError: (err, abandoned) => {
          failure = { err, abandoned };
        },
      },
    );

    queue.enqueue(envelope("activity", {}));
    queue.enqueue(boom);
    queue.enqueue(trailing);
    sched.run();

    // The envelope before the throw applied; the throw and its tail did not.
    expect(seen).toEqual(["activity"]);
    expect((failure?.err as Error).message).toBe("apply failed");
    expect(failure?.abandoned.map((e) => e.event_id)).toEqual([
      boom.event_id,
      trailing.event_id,
    ]);
  });

  it("an envelope enqueued during a drain lands in the next frame", () => {
    const sched = manualScheduler();
    const seen: string[] = [];
    let reentered = false;
    const queue = createEventFrameQueue(
      (env) => {
        seen.push(env.topic);
        if (!reentered) {
          reentered = true;
          queue.enqueue(envelope("cost", {}));
        }
      },
      { scheduler: sched },
    );

    queue.enqueue(envelope("activity", {}));
    sched.run();
    expect(seen).toEqual(["activity"]);

    sched.run();
    expect(seen).toEqual(["activity", "cost"]);
  });
});
