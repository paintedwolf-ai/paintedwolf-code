import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import type { Message, QueueDraft, Session } from "../../api/types.ts";
import type { PendingSend } from "./pending-sends.ts";
import {
  reconcilePendingOnBaseline,
  reconcilePendingOnQueueDraft,
  reconcilePendingOnUserRow,
  sweepPendingOnIdle,
} from "./pending-sends.ts";

const SESSION_ID = "sess-1";

function session(id = SESSION_ID): Session {
  return {
    id,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "00000000-0000-4000-8000-000000000001",
    workspace_path: "/tmp/p",
    posture: "build",
    status: "idle",
    created_at: "t",
    updated_at: "t",
  } as Session;
}

function userRow(id: string, overrides?: Partial<Message>): Message {
  return {
    id,
    role: "user",
    origin: "user",
    authority: "user",
    trust_tier: "trusted",
    content: "hi",
    created_at: "t",
    seq: 1,
    ord: 1,
    ...overrides,
  };
}

function entry(operationId: string, overrides?: Partial<PendingSend>): Omit<PendingSend, "createdAt"> {
  return {
    kind: "prompt",
    operationId,
    text: "hi",
    state: "sending",
    ...overrides,
  };
}

function draft(overrides?: Partial<QueueDraft>): QueueDraft {
  return {
    queue_items: [{ submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-1", text: "later", created_at: "t" }],
    hold: false,
    sending: false,
    revision: 1,
    ...overrides,
  };
}

const ids = (store: ReturnType<typeof createAppStore>) =>
  store.state.pendingSends[SESSION_ID]?.map((e) => e.operationId);

describe("pending sends", () => {
  it("captures a seat timestamp once, including queue reservations", () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    try {
      const store = createAppStore();
      store.actions.addPendingSend(SESSION_ID, entry("op-1"));
      now.mockReturnValue(2000);
      store.actions.patchPendingSend(SESSION_ID, "op-1", { state: "accepted" });
      expect(store.state.pendingSends[SESSION_ID]?.[0]?.createdAt).toBe(1000);
      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({ sending: true }));
      now.mockReturnValue(3000);
      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({ sending: true, revision: 2 }));
      expect(store.state.pendingSends[SESSION_ID]?.find((item) => item.operationId === "item-1")?.createdAt).toBe(2000);
    } finally {
      now.mockRestore();
    }
  });

  it("add, patch, remove — and a patch after remove never resurrects", () => {
    const store = createAppStore();
    store.actions.addPendingSend(SESSION_ID, entry("op-1"));
    store.actions.addPendingSend(SESSION_ID, entry("op-2"));
    expect(store.state.pendingSends[SESSION_ID]).toHaveLength(2);

    store.actions.patchPendingSend(SESSION_ID, "op-1", { state: "accepted" });
    expect(store.state.pendingSends[SESSION_ID]?.[0]?.state).toBe("accepted");

    store.actions.removePendingSends(SESSION_ID, ["op-1"]);
    expect(ids(store)).toEqual(["op-2"]);

    // The echo can precede the prompt response.
    store.actions.patchPendingSend(SESSION_ID, "op-1", { state: "accepted" });
    expect(ids(store)).toEqual(["op-2"]);

    store.actions.removePendingSends(SESSION_ID, ["op-2"]);
    expect(store.state.pendingSends[SESSION_ID]).toBeUndefined();
  });

  it("a matching user row drops exactly its entry", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session());
    store.actions.addPendingSend(SESSION_ID, entry("op-1"));
    store.actions.addPendingSend(SESSION_ID, entry("op-2"));

    reconcilePendingOnUserRow(store, SESSION_ID, userRow("unrelated"));
    expect(store.state.pendingSends[SESSION_ID]).toHaveLength(2);

    reconcilePendingOnUserRow(store, SESSION_ID, userRow("op-1"));
    expect(ids(store)).toEqual(["op-2"]);
  });

  describe("queue draft", () => {
    it.each(["prompt", "queued_prompt"] as const)("a pending %s hands off to the host queue", (kind) => {
      const store = createAppStore();
      store.actions.addPendingSend(SESSION_ID, entry("op-1"));
      store.actions.addPendingSend(SESSION_ID, entry("op-2", { kind }));

      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({
        queue_items: [{ submitted_by: "00000000-0000-4000-8000-000000000002", id: "op-2", text: "hi", created_at: "t" }],
        revision: 3,
      }));
      expect(ids(store)).toEqual(["op-1"]);

      // Queue state can precede the prompt response.
      store.actions.patchPendingSend(SESSION_ID, "op-2", { state: "accepted" });
      expect(ids(store)).toEqual(["op-1"]);
    });

    it("a reserved head takes a transcript seat as the continuation it will become", () => {
      const store = createAppStore();

      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({ sending: true }));
      expect(store.state.pendingSends[SESSION_ID]).toEqual([
        expect.objectContaining({
          kind: "queue_send",
          operationId: "item-1",
          text: "later",
          state: "accepted",
        }),
      ]);

      // Repeated snapshots preserve one seat.
      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({ sending: true, revision: 2 }));
      expect(store.state.pendingSends[SESSION_ID]).toHaveLength(1);
    });

    it("a seat whose item is queued behind another reserved head is released", () => {
      const store = createAppStore();
      store.actions.addPendingSend(SESSION_ID, entry("item-2", { kind: "queue_send", state: "accepted" }));

      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({
        sending: true,
        queue_items: [
          { submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-1", text: "later", created_at: "t" },
          { submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-2", text: "after", created_at: "t" },
        ],
      }));
      expect(ids(store)).toEqual(["item-1"]);
    });

    it("a linked head group is one row carrying the first item's id", () => {
      const store = createAppStore();

      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({
        sending: true,
        queue_items: [
          { submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-1", text: "first ", group_id: "g", created_at: "t" },
          { submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-2", text: "second", group_id: "g", created_at: "t" },
          { submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-3", text: "unrelated", created_at: "t" },
        ],
      }));
      expect(store.state.pendingSends[SESSION_ID]).toEqual([
        expect.objectContaining({ operationId: "item-1", text: "first\n\nsecond" }),
      ]);
    });

    it("cancel send releases the seat: the item is back in the queue, unreserved", () => {
      const store = createAppStore();
      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({ sending: true }));
      expect(ids(store)).toEqual(["item-1"]);

      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({ sending: false, revision: 2 }));
      expect(store.state.pendingSends[SESSION_ID]).toBeUndefined();
    });

    it("a taken reservation keeps its seat until the continuation echoes", () => {
      const store = createAppStore();
      store.actions.setCurrentSession(session());
      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({ sending: true }));

      // Queue release can precede the message echo.
      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({
        queue_items: [],
        sending: false,
        revision: 2,
      }));
      expect(ids(store)).toEqual(["item-1"]);

      reconcilePendingOnUserRow(
        store,
        SESSION_ID,
        userRow("item-1", { kind: "user_continuation" }),
      );
      expect(store.state.pendingSends[SESSION_ID]).toBeUndefined();
    });

    it("a reservation observed with the composer's own prompt still queued keeps both facts", () => {
      const store = createAppStore();
      // The composer prompt is queued behind the reserved head.
      store.actions.addPendingSend(SESSION_ID, entry("op-1", { state: "accepted" }));

      reconcilePendingOnQueueDraft(store, SESSION_ID, draft({
        sending: true,
        queue_items: [
          { submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-1", text: "later", created_at: "t" },
          { submitted_by: "00000000-0000-4000-8000-000000000002", id: "op-1", text: "hi", created_at: "t" },
        ],
      }));
      expect(store.state.pendingSends[SESSION_ID]).toEqual([
        expect.objectContaining({ kind: "queue_send", operationId: "item-1" }),
      ]);
    });
  });

  it("baseline install resolves entries whose rows arrived while offline", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session());
    store.actions.addPendingSend(SESSION_ID, entry("op-1", { state: "accepted" }));
    store.actions.installTranscriptBaseline(
      SESSION_ID,
      [userRow("op-1")],
      1,
    );

    reconcilePendingOnBaseline(store, SESSION_ID);
    expect(store.state.pendingSends[SESSION_ID]).toBeUndefined();
  });

  it("idle sweep reaps only accepted entries with no durable surface", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session());
    const epoch = store.state.sessionViewEpoch;
    store.actions.installTranscriptBaseline(SESSION_ID, [userRow("op-echoed")], 1);
    store.actions.setQueueDraft(SESSION_ID, epoch, draft({
      queue_items: [{ submitted_by: "00000000-0000-4000-8000-000000000002", id: "op-queued", text: "later", created_at: "t" }],
    }));
    store.actions.addPendingSend(SESSION_ID, entry("op-sending"));
    store.actions.addPendingSend(SESSION_ID, entry("op-echoed", { state: "accepted" }));
    store.actions.addPendingSend(SESSION_ID, entry("op-queued", { state: "accepted" }));
    store.actions.addPendingSend(SESSION_ID, entry("op-dead", { state: "accepted" }));
    store.actions.addPendingSend(
      SESSION_ID,
      entry("send-dead", { kind: "queue_send", state: "accepted" }),
    );

    sweepPendingOnIdle(store, SESSION_ID);
    expect(ids(store)).toEqual(["op-sending", "op-echoed", "op-queued"]);
  });

  it("idle sweep ignores background sessions", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("sess-other"));
    store.actions.addPendingSend(SESSION_ID, entry("op-1", { state: "accepted" }));

    sweepPendingOnIdle(store, SESSION_ID);
    expect(store.state.pendingSends[SESSION_ID]).toHaveLength(1);
  });

  it("session switches clear every pending entry", () => {
    const paths = [
      (s: ReturnType<typeof createAppStore>) =>
        s.actions.clearChatForSessionSwitch(),
      (s: ReturnType<typeof createAppStore>) =>
        s.actions.beginSessionResumeSwitch(),
      (s: ReturnType<typeof createAppStore>) =>
        s.actions.resetChatForSessionSwitch(),
    ];
    for (const clear of paths) {
      const store = createAppStore();
      store.actions.addPendingSend(SESSION_ID, entry("op-1"));
      clear(store);
      expect(store.state.pendingSends).toEqual({});
    }
  });
});
