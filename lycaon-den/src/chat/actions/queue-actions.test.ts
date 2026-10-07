import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { QueueDraft, Session, SessionBootstrap } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import {
  cancelQueueSend,
  refreshQueue,
  removeQueueItems,
  sendQueue,
} from "./queue-actions.ts";

const session = (id: string): Session => ({
  id,
  owner_person_id: "00000000-0000-4000-8000-000000000002",
  project_id: "project-1",
  posture: "build",
  status: "idle",
  created_at: "t",
  activity_at: "t",
  updated_at: "t",
});

const draft = (revision: number, text = "queued"): QueueDraft => ({
  revision,
  hold: false,
  sending: false,
  queue_items: [{ submitted_by: "00000000-0000-4000-8000-000000000002", id: "item-1", text, created_at: "t" }],
});

describe("queue actions", () => {
  it("installs bootstrap queue revisions monotonically and reconciles only accepted snapshots", () => {
    const store = createAppStore();
    const bootstrap = (queue: QueueDraft): SessionBootstrap => ({
      event_cursor: "",
      session: session("a"),
      transcript: { messages: [], watermark: 0, turn_clocks: {}, turn_loads: {} },
      progress: { revision: 1, steps: [] },
      turn_clock: { session_id: "a", active_ms: 0, work_ms: 0, running: false },
      findings: { revision: 1, findings: [] },
      queue, coordinator: {}, workers: [], checkpoints: [], background_outputs: [], previews: [],
    });
    store.actions.installSessionBootstrap(bootstrap({ ...draft(2), sending: true }));
    expect(store.state.pendingSends.a).toHaveLength(1);
    store.actions.removePendingSends("a", ["item-1"]);
    store.actions.setQueueDraft("a", store.state.sessionViewEpoch, { ...draft(3), queue_items: [] });
    store.actions.installSessionBootstrap(bootstrap({ ...draft(2), sending: true }));
    expect(store.state.queueDraft?.revision).toBe(3);
    expect(store.state.pendingSends.a).toBeUndefined();
  });

  it("a bootstrap at the remembered revision re-seats the reserved head after a session switch", async () => {
    const store = createAppStore();
    const bootstrap = (queue: QueueDraft): SessionBootstrap => ({
      event_cursor: "",
      session: session("a"),
      transcript: { messages: [], watermark: 0, turn_clocks: {}, turn_loads: {} },
      progress: { revision: 1, steps: [] },
      turn_clock: { session_id: "a", active_ms: 0, work_ms: 0, running: false },
      findings: { revision: 1, findings: [] },
      queue, coordinator: {}, workers: [], checkpoints: [], background_outputs: [], previews: [],
    });
    store.actions.installSessionBootstrap(bootstrap(draft(1)));
    const sending = { ...draft(2), sending: true };
    const client = stubClient({ updateSessionQueue: vi.fn().mockResolvedValue(sending) });
    await sendQueue(store, client, "a");
    expect(store.state.pendingSends.a).toEqual([
      expect.objectContaining({ kind: "queue_send", operationId: "item-1" }),
    ]);

    // Switching away drops every pending send; the host still reports revision 2.
    store.actions.beginSessionResumeSwitch("a");
    expect(store.state.pendingSends).toEqual({});
    store.actions.installSessionBootstrap(bootstrap(sending));
    expect(store.state.queueDraft?.revision).toBe(2);
    expect(store.state.pendingSends.a).toEqual([
      expect.objectContaining({ kind: "queue_send", operationId: "item-1", state: "accepted" }),
    ]);

    // A seated head is not reseated by a repeat bootstrap.
    store.actions.installSessionBootstrap(bootstrap(sending));
    expect(store.state.pendingSends.a).toHaveLength(1);
  });

  it("queue admission hands an optimistic transcript bubble to the queue surface", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("session-1"));
    appStore.actions.addPendingSend("session-1", {
      kind: "prompt",
      operationId: "item-1",
      text: "queued",
      state: "accepted",
    });
    const client = stubClient({
      getSessionQueue: vi.fn().mockResolvedValue(draft(2)),
    });

    await refreshQueue(appStore, client, "session-1");

    // The queue popover controls queued prompts.
    expect(appStore.state.pendingSends["session-1"]).toBeUndefined();
    expect(appStore.state.queueDraft?.queue_items[0]?.id).toBe("item-1");
  });

  it("sends a compare-and-swap revision and refreshes after a conflict", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("session-1"));
    appStore.actions.setQueueDraft(
      "session-1",
      appStore.state.sessionViewEpoch,
      draft(4),
    );
    const client = stubClient({
      updateSessionQueue: vi
        .fn()
        .mockRejectedValue(
          new LycaonApiError("stale queue", 409, "queue_revision_conflict"),
        ),
      getSessionQueue: vi.fn().mockResolvedValue(draft(5, "authoritative")),
    });

    await expect(
      removeQueueItems(appStore, client, "session-1", ["item-1"]),
    ).rejects.toMatchObject({ code: "queue_revision_conflict" });

    expect(client.updateSessionQueue).toHaveBeenCalledWith("session-1", {
      op: "remove",
      item_ids: ["item-1"],
      expected_revision: 4,
    });
    expect(appStore.state.queueDraft).toEqual(draft(5, "authoritative"));
  });

  it("reserves the queue head for the running turn", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("session-1"));
    appStore.actions.setQueueDraft(
      "session-1",
      appStore.state.sessionViewEpoch,
      draft(7),
    );
    const sending = { ...draft(8), sending: true };
    const client = stubClient({
      updateSessionQueue: vi.fn().mockResolvedValue(sending),
    });

    await sendQueue(appStore, client, "session-1");

    expect(client.updateSessionQueue).toHaveBeenCalledWith("session-1", {
      op: "send",
      expected_revision: 7,
    });
    expect(appStore.state.queueDraft).toEqual(sending);
    // Host confirmation creates the reserved seat.
    expect(appStore.state.pendingSends["session-1"]).toEqual([
      expect.objectContaining({
        kind: "queue_send",
        operationId: "item-1",
        text: "queued",
      }),
    ]);
  });

  it("does not commit a late mutation response after A to B to A", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("session-a"));
    appStore.actions.setQueueDraft(
      "session-a",
      appStore.state.sessionViewEpoch,
      draft(1),
    );
    let resolveMutation!: (value: QueueDraft) => void;
    const response = new Promise<QueueDraft>((resolve) => {
      resolveMutation = resolve;
    });
    const client = stubClient({
      updateSessionQueue: vi.fn().mockReturnValue(response),
    });

    const pending = removeQueueItems(appStore, client, "session-a", ["item-1"]);
    appStore.actions.setCurrentSession(session("session-b"));
    appStore.actions.setCurrentSession(session("session-a"));
    resolveMutation({ ...draft(2, "stale"), sending: true });
    await pending;

    expect(appStore.state.queueDraft).toEqual(draft(1));
    expect(appStore.state.pendingSends["session-a"]).toBeUndefined();
  });

  it("rejects a stale refresh after a newer draft consumed the reserved head", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("a"));
    let resolve!: (value: QueueDraft) => void;
    const client = stubClient({ getSessionQueue: vi.fn(() => new Promise<QueueDraft>((done) => { resolve = done; })) });
    const pending = refreshQueue(appStore, client, "a");
    const consumed = { ...draft(3), queue_items: [] };
    expect(appStore.actions.setQueueDraft("a", appStore.state.sessionViewEpoch, consumed)).toBe(true);
    resolve({ ...draft(2), sending: true });
    expect(await pending).toBeUndefined();
    expect(appStore.state.queueDraft).toEqual(consumed);
    expect(appStore.state.pendingSends.a).toBeUndefined();
  });

  it("keeps independent session revisions across A to B to A and rejects old epochs", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("a"));
    const epochA = store.state.sessionViewEpoch;
    store.actions.setQueueDraft("a", epochA, draft(9));
    store.actions.setCurrentSession(session("b"));
    expect(store.state.queueDraft).toBeUndefined();
    expect(store.actions.setQueueDraft("b", store.state.sessionViewEpoch, draft(1))).toBe(true);
    store.actions.setCurrentSession(session("a"));
    expect(store.state.queueDraft?.revision).toBe(9);
    expect(store.actions.setQueueDraft("a", epochA, draft(10))).toBe(false);
    expect(store.actions.setQueueDraft("a", store.state.sessionViewEpoch, draft(8))).toBe(false);
    expect(store.actions.setQueueDraft("a", store.state.sessionViewEpoch, draft(9))).toBe(false);
    expect(store.actions.setQueueDraft("a", store.state.sessionViewEpoch, draft(10))).toBe(true);
  });

  it("does not refresh into a new view when an old mutation reports conflict", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("a"));
    let reject!: (err: unknown) => void;
    const client = stubClient({
      updateSessionQueue: vi.fn(() => new Promise<QueueDraft>((_, fail) => { reject = fail; })),
      getSessionQueue: vi.fn(),
    });
    const pending = sendQueue(store, client, "a");
    store.actions.setCurrentSession(session("b"));
    store.actions.setCurrentSession(session("a"));
    reject(new LycaonApiError("conflict", 409, "queue_revision_conflict"));
    await expect(pending).rejects.toMatchObject({ code: "queue_revision_conflict" });
    expect(client.getSessionQueue).not.toHaveBeenCalled();
    expect(store.state.pendingSends.a).toBeUndefined();
  });

  it("releases a reservation the turn has not taken yet", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("session-1"));
    const reserved = { ...draft(9), sending: true };
    const released = { ...draft(10), sending: false };
    const client = stubClient({
      getSessionQueue: vi.fn().mockResolvedValue(reserved),
      updateSessionQueue: vi.fn().mockResolvedValue(released),
    });
    await refreshQueue(appStore, client, "session-1");
    expect(appStore.state.pendingSends["session-1"]).toHaveLength(1);

    await cancelQueueSend(appStore, client, "session-1");

    expect(client.updateSessionQueue).toHaveBeenCalledWith("session-1", {
      op: "cancel_send",
      expected_revision: 9,
    });
    // Cancellation restores editing and removes the seat.
    expect(appStore.state.queueDraft?.sending).toBe(false);
    expect(appStore.state.queueDraft?.queue_items).toEqual(released.queue_items);
    expect(appStore.state.pendingSends["session-1"]).toBeUndefined();
  });
});
