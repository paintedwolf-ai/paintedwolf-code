// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../store/app-state.ts";
import { type AppStore } from "../store/app-state-model.ts";
import { createNoticeStore, registerNoticePublisher } from "../notices/notice-store.ts";
import { selectSessionNotices } from "../notices/notice-select.ts";
import { resetTurnOutcomesForTests, sessionIdleDisposition } from "../chat/recovery/turn-outcome.ts";
import { resetSessionEventRevisions, subscribeEvents, type EventSubscription } from "./events.ts";
import type { LLMCallEvent, SessionEvent, SessionHostError } from "./types.ts";
import { LycaonApiError } from "./http.ts";
import { refreshSessionSnapshot } from "./session-snapshot-refresh.ts";
import { stopChatActivity } from "../chat/session/session-lifecycle.ts";
import { isChatActivityLive } from "../chat/session/session-activity.ts";
import { appliedSessionRevision } from "./session-event-revisions.ts";
import { stubClient } from "../test/client-fixture.ts";
import type { Session } from "./types.ts";

const projectId = "22222222-2222-4222-8222-222222222222";
const sessionId = "11111111-1111-4111-8111-111111111111";
const subscriptions: EventSubscription[] = [];
const hostError: SessionHostError = {
  code: "provider_empty_completion", title: "No response", message: "The model returned no response.",
};

afterEach(() => {
  for (const subscription of subscriptions.splice(0)) void subscription.close();
  registerNoticePublisher(null);
  resetTurnOutcomesForTests();
});

function setup() {
  const store = createAppStore();
  store.actions.setCurrentSession({
    id: sessionId, owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: projectId, status: "busy", posture: "build",
    workspace_path: "/project", created_at: "t", activity_at: "t", updated_at: "t",
  });
  const notices = createNoticeStore();
  registerNoticePublisher(notices);
  return { store, notices };
}

async function deliver(
  store: AppStore,
  events: { revision: number; data: Partial<SessionEvent> }[],
  replayReset = false,
  flush = true,
  llm?: LLMCallEvent,
) {
  const handler = vi.fn();
  const invalidate = vi.fn();
  const reconcile = vi.fn(async () => undefined);
  let attempt = 0;
  let markEnqueued!: () => void;
  const enqueued = new Promise<void>((resolve) => { markEnqueued = resolve; });
  async function* connect() {
    if (replayReset && attempt++ === 0) {
      throw new LycaonApiError("Replay expired", 409, "event_replay_unavailable", {
        details: { event_cursor: "new-generation" },
      });
    }
    yield { comment: "connected" };
    for (const [index, event] of events.entries()) {
      yield { data: JSON.stringify({
        v: 1, event_id: `event-${event.revision}-${index}`, cursor: `cursor-${index}`,
        topic: "session", published_at: "2026-01-01T00:00:00Z", entity_revision: event.revision,
        scope: { kind: "session", project_id: projectId, session_id: sessionId },
        data: { id: sessionId, project_id: projectId, action: "updated", ...event.data },
      }) };
    }
    if (llm) {
      yield { data: JSON.stringify({
        v: 1, event_id: "llm-event", cursor: "llm-cursor", topic: "llm",
        published_at: "2026-01-01T00:00:00Z",
        scope: { kind: "session", project_id: projectId, session_id: sessionId },
        data: llm,
      }) };
    }
    markEnqueued();
    await new Promise<never>(() => {});
  }
  const subscription = subscribeEvents(
    { baseUrl: "http://127.0.0.1:8787", apiToken: "fixture" }, projectId,
    { session: handler },
    { appStore: store, storeActions: store.actions, onInvalidate: invalidate, onReconcile: reconcile, connect },
  );
  subscriptions.push(subscription);
  await enqueued;
  if (flush) await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  return { handler, invalidate, subscription, reconcile };
}

describe("session event revision ordering", () => {
  it("holds a sent prompt through stale events and ends the hold at the first event after admission", async () => {
    const { store } = setup();
    store.actions.holdPromptSubmission(sessionId, "sent");
    store.actions.admitPromptSubmission(sessionId, "sent", 5, appliedSessionRevision(store.actions, sessionId));

    // Published before admission, delivered after it: the session looks at rest.
    await deliver(store, [{ revision: 4, data: { status: "idle" } }]);
    expect(store.state.sessionActivity[sessionId]?.promptSubmissions).toEqual([{ id: "sent", revision: 5 }]);
    expect(isChatActivityLive(store, sessionId)).toBe(true);

    await deliver(store, [{ revision: 6, data: { status: "idle", prompt_pending: true } }]);
    expect(appliedSessionRevision(store.actions, sessionId)).toBe(6);
    expect(store.state.sessionActivity[sessionId]).toBeUndefined();
    // Session state carries the prompt from here.
    expect(isChatActivityLive(store, sessionId)).toBe(true);
  });

  it.each(["session and LLM", "LLM only"])("keeps newer %s events when an earlier abort response arrives", async (kind) => {
    const { store } = setup();
    const stale = { ...store.state.currentSession!, status: "idle" as const };
    let resolveAbort!: (session: Session) => void;
    const listWorkers = vi.fn().mockResolvedValue([]);
    const client = stubClient({
      abortSession: () => new Promise<Session>((resolve) => { resolveAbort = resolve; }),
      listWorkers,
    });
    const aborting = stopChatActivity(store, client, sessionId, projectId, "/project", []);
    await deliver(store, kind === "LLM only" ? [] : [{ revision: 2, data: { status: "busy", title: "New turn", current_turn: 2 } }], false, true, {
      call_id: "new-call", session_id: sessionId, provider: "mock", model: "mock", status: "active", tokens: {},
    });
    store.actions.holdPromptSubmission(sessionId, "accepted-prompt");
    resolveAbort(stale);
    await aborting;
    expect(store.state.currentSession?.status).toBe("busy");
    if (kind === "session and LLM") {
      expect(store.state.currentSession).toMatchObject({ title: "New turn", current_turn: 2 });
    }
    expect(listWorkers).toHaveBeenCalledOnce();
    expect(store.state.sessionActivity[sessionId]).toMatchObject({
      llmTurn: { callId: "new-call", status: "active" }, promptSubmissions: [{ id: "accepted-prompt" }],
    });
    expect(store.state.sessionActivity[sessionId]?.stopping).toBeUndefined();
  });

  it("keeps live activity when an idle GET response trails an LLM event", async () => {
    const { store } = setup();
    const stale = { ...store.state.currentSession!, status: "idle" as const };
    let resolveRead!: (session: Session) => void;
    const reading = refreshSessionSnapshot(store, stubClient({
      getSession: () => new Promise<Session>((resolve) => { resolveRead = resolve; }),
    }), sessionId);
    await deliver(store, [], false, true, {
      call_id: "new-call", session_id: sessionId, provider: "mock", model: "mock", status: "active", tokens: {},
    });
    resolveRead(stale);
    await reading;
    expect(store.state.currentSession?.status).toBe("busy");
    expect(store.state.sessionActivity[sessionId]?.llmTurn).toMatchObject({ callId: "new-call", status: "active" });
  });

  it("does not let an earlier HTTP read replace an accepted host event", async () => {
    const { store, notices } = setup();
    const stale = { ...store.state.currentSession! };
    let resolveRead!: (session: Session) => void;
    const response = new Promise<Session>((resolve) => { resolveRead = resolve; });
    const refresh = refreshSessionSnapshot(store, stubClient({ getSession: () => response }), sessionId);

    await deliver(store, [{ revision: 2, data: {
      status: "idle", title: "Current title", current_turn: 2,
      ui: { pending_workflow_start: null }, idle_disposition: "turn_error", host_error: hostError,
    } }]);
    resolveRead({ ...stale, status: "busy", title: "Old title", current_turn: 1, ui: {} });
    await refresh;

    expect(store.state.currentSession).toMatchObject({
      status: "idle", title: "Current title", current_turn: 2,
      ui: { pending_workflow_start: null },
    });
    expect(sessionIdleDisposition(sessionId)).toBe("turn_error");
    expect(selectSessionNotices(notices.index(), sessionId)).toHaveLength(1);
  });

  it("keeps an old subscription's close flush out of the new revision generation", async () => {
    const { store } = setup();
    const old = await deliver(store, [{ revision: 100, data: { status: "busy" } }], false, false);
    expect(old.handler).not.toHaveBeenCalled();

    resetSessionEventRevisions(store.actions);
    void old.subscription.close();
    expect(old.handler).toHaveBeenCalledOnce();
    const current = await deliver(store, [{ revision: 1, data: { status: "idle" } }]);

    expect(current.handler).toHaveBeenCalledOnce();
    expect(store.state.currentSession?.status).toBe("idle");
  });

  it.each(["replay boundary", "backend connection"] as const)(
    "accepts restarted revisions after a new %s",
    async (boundary) => {
      const { store } = setup();
      const first = await deliver(store, [{ revision: 100, data: { status: "busy" } }]);
      void first.subscription.close();
      if (boundary === "backend connection") resetSessionEventRevisions(store.actions);

      const second = await deliver(store, [{ revision: 1, data: { status: "idle" } }], boundary === "replay boundary");

      expect(store.state.currentSession?.status).toBe("idle");
      expect(second.handler).toHaveBeenCalledOnce();
      if (boundary === "replay boundary") {
        expect(second.reconcile).toHaveBeenCalledWith("replay_unavailable");
      }
    },
  );

  it("keeps a newer error snapshot when older busy and completion snapshots arrive", async () => {
    const { store, notices } = setup();
    const ui = { pending_workflow_start: null };
    const dispatched = await deliver(store, [
      { revision: 2, data: {
        status: "idle", title: "Current title", current_turn: 2, ui,
        idle_disposition: "turn_error", host_error: hostError,
      } },
      { revision: 1, data: { status: "busy", title: "Old title", current_turn: 1, ui: {} } },
      { revision: 0, data: { status: "idle", idle_disposition: "completed" } },
    ]);

    expect(store.state.currentSession).toMatchObject({ status: "idle", title: "Current title", current_turn: 2, ui });
    expect(sessionIdleDisposition(sessionId)).toBe("turn_error");
    expect(selectSessionNotices(notices.index(), sessionId)).toHaveLength(1);
    expect(dispatched.handler).toHaveBeenCalledOnce();
    expect(dispatched.invalidate).toHaveBeenCalledOnce();
  });

  it("shows an older error after resubscription without changing newer busy state", async () => {
    const { store, notices } = setup();
    const ui = { pending_workflow_start: null };
    const first = await deliver(store, [{ revision: 2, data: {
      status: "busy", title: "Current title", current_turn: 2, ui,
    } }]);
    void first.subscription.close();
    store.actions.holdPromptSubmission(sessionId, "accepted-prompt");
    store.actions.addPendingSend(sessionId, {
      kind: "prompt", operationId: "accepted-prompt", text: "Continue", state: "accepted",
    });

    const second = await deliver(store, [{ revision: 1, data: {
      status: "idle", title: "Old title", current_turn: 1, ui: {},
      idle_disposition: "turn_error", host_error: hostError,
    } }]);

    expect(selectSessionNotices(notices.index(), sessionId)).toHaveLength(1);
    expect(store.state.currentSession).toMatchObject({ status: "busy", title: "Current title", current_turn: 2, ui });
    expect(store.state.sessionActivity[sessionId]?.promptSubmissions).toEqual([{ id: "accepted-prompt" }]);
    expect(store.state.pendingSends[sessionId]).toHaveLength(1);
    expect(sessionIdleDisposition(sessionId)).toBeUndefined();
    expect(second.handler).not.toHaveBeenCalled();
    expect(second.invalidate).not.toHaveBeenCalled();
  });
});
