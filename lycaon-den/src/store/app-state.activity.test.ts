import {
  describe,
  expect,
  it,
} from "vitest";

import {
  createAppStore,
  INITIAL_APP_STATE,
} from "./app-state.ts";

describe("createAppStore", () => {

  it("setLLMCallStatus tracks llm turn activity chrome", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });
    // provider is kept: the composer names who a live call is waiting on.
    expect(store.state.sessionActivity["sess-1"]?.llmTurn).toEqual({
      callId: "c1",
      status: "active",
      provider: "mock",
      retry: undefined,
      guarded: false,
      provisionalHidden: false,
    });
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "ok",
      tokens: {},
    });
    expect(store.state.sessionActivity["sess-1"]?.llmTurn?.status).toBe("done");
    store.actions.clearLlmTurn("sess-1");
    expect(store.state.sessionActivity["sess-1"]?.llmTurn).toBeUndefined();
  });

  it("setLLMCallStatus marks guarded turns from coordinator_loop", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
      coordinator_loop: { host_turn: true, surface: "implement_synthesis", guarded: true },
    });
    expect(store.state.sessionActivity["sess-1"]?.llmTurn?.guarded).toBe(true);
  });

  it("mergeSession idle with title clears activity chrome", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setLLMCallStatus({
      status: "active",
      session_id: "sess-1",
      call_id: "c1",
      provider: "openai",
      model: "gpt-4o",
      tokens: { prompt: 1, completion: 0 },
      coordinator_loop: { host_turn: false, iteration: 1, max_iterations: 8 },
    });
    store.actions.mergeSession({
      id: "sess-1",
      status: "idle",
      title: "Chess game",
    });
    expect(store.state.currentSession?.status).toBe("idle");
    expect(store.state.currentSession?.title).toBe("Chess game");
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
    expect(store.state.coordinatorLoopProgress).toBeUndefined();
  });

  it("a submitted prompt stays in flight until an event after its admission applies", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const held = () => store.state.sessionActivity["sess-1"]?.promptSubmissions;
    store.actions.holdPromptSubmission("sess-1", "submission-1");
    store.actions.holdPromptSubmission("sess-1", "submission-1");
    expect(held()).toEqual([{ id: "submission-1" }]);

    // Before admission answers, no event reflects the prompt.
    store.actions.releasePromptSubmissionsThrough("sess-1", 100);
    store.actions.mergeSession({ id: "sess-1", status: "idle" });
    expect(held()).toEqual([{ id: "submission-1" }]);

    store.actions.admitPromptSubmission("sess-1", "submission-1", 10, 4);
    expect(held()).toEqual([{ id: "submission-1", revision: 10 }]);
    // Revision 10 was minted, not published; events up to it predate admission.
    store.actions.releasePromptSubmissionsThrough("sess-1", 10);
    expect(held()).toEqual([{ id: "submission-1", revision: 10 }]);
    store.actions.releasePromptSubmissionsThrough("sess-1", 11);
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
  });

  it("admission ends a hold at once when a later event already applied", () => {
    const store = createAppStore();
    store.actions.holdPromptSubmission("sess-1", "passed");
    store.actions.admitPromptSubmission("sess-1", "passed", 10, 11);
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();

    // A host that publishes no session events has nothing later to wait for.
    store.actions.holdPromptSubmission("sess-1", "unpublished");
    store.actions.admitPromptSubmission("sess-1", "unpublished", 0, undefined);
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();

    // Admission for a prompt this client no longer holds changes nothing.
    store.actions.admitPromptSubmission("sess-1", "released", 10, undefined);
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
  });

  it("a prompt request releases only the submission it held", () => {
    const store = createAppStore();
    store.actions.holdPromptSubmission("sess-1", "submission-1");
    store.actions.holdPromptSubmission("sess-1", "submission-2");
    store.actions.admitPromptSubmission("sess-1", "submission-2", 5, undefined);
    store.actions.releasePromptSubmission("sess-1", "submission-1");
    expect(store.state.sessionActivity["sess-1"]?.promptSubmissions).toEqual([{ id: "submission-2", revision: 5 }]);
    store.actions.releasePromptSubmission("sess-1", "submission-2");
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
  });

  it("mergeSession carries the host's prompt_pending, reading an omitted value as false", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.mergeSession({ id: "sess-1", status: "idle", prompt_pending: true });
    expect(store.state.currentSession?.prompt_pending).toBe(true);
    store.actions.mergeSession({ id: "sess-1", status: "busy" });
    expect(store.state.currentSession?.prompt_pending).toBe(false);
  });

  it("setLLMCallStatus tracks the turn on the event's own session", () => {
    const store = createAppStore();
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });
    expect(store.state.sessionActivity["sess-1"]?.llmTurn?.status).toBe(
      "active",
    );
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "ok",
      tokens: {},
    });
    expect(store.state.sessionActivity["sess-1"]?.llmTurn?.status).toBe("done");
  });

  it("setLLMCallStatus from a background session leaves foreground chrome alone", () => {
    const store = createAppStore({
      ...INITIAL_APP_STATE,
      currentSession: {
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        status: "idle",
        posture: "build",
        workspace_path: "/p",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
    });
    // Background turns leave the foreground loop and context rings unchanged.
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-2",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: { prompt: 42 },
      coordinator_loop: { host_turn: true, iteration: 3, max_iterations: 30 },
    });

    expect(store.state.sessionActivity["sess-2"]?.llmTurn?.status).toBe("active");
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
    expect(store.state.coordinatorLoopProgress).toBeUndefined();
    expect(store.state.contextUsage).toBeUndefined();
  });

  it("setLLMCallStatus ok keeps coordinator loop progress until session idle", () => {
    const store = createAppStore({
      ...INITIAL_APP_STATE,
      currentSession: {
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        status: "busy",
        posture: "build",
        workspace_path: "/p",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
    });
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
      coordinator_loop: {
        host_turn: true,
        iteration: 3,
        max_iterations: 30,
        surface: "implement_overlay_promote",
      },
    });
    store.actions.setLLMCallStatus({
      call_id: "c1",
      session_id: "sess-1",
      provider: "mock",
      model: "mock",
      status: "ok",
      tokens: {},
      coordinator_loop: {
        host_turn: true,
        iteration: 3,
        max_iterations: 30,
        surface: "implement_overlay_promote",
      },
    });
    expect(store.state.coordinatorLoopProgress?.surface).toBe(
      "implement_overlay_promote",
    );
    store.actions.mergeSession({ id: "sess-1", status: "idle" });
    expect(store.state.coordinatorLoopProgress).toBeUndefined();
  });

  it("setActivity keeps active leases until their matching terminal edge", () => {
    const store = createAppStore();
    const active = {
      activity_id: "activity-1",
      session_id: "sess-1",
      kind: "running_tool" as const,
      status: "active" as const,
      started_at: "2026-01-01T00:00:00Z",
      tool_name: "verify",
      tool_call_id: "call-1",
    };
    store.actions.setActivity(active);
    expect(
      store.state.sessionActivity["sess-1"]?.activities?.["activity-1"],
    ).toEqual(active);

    store.actions.setActivity({ ...active, status: "done" });
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
  });

  it("mergeSession idle clears llm and turn activity flags", () => {
    const store = createAppStore({
      ...INITIAL_APP_STATE,
      currentSession: {
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        status: "busy",
        posture: "build",
        workspace_path: "/p",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
      coordinatorLoopProgress: {
        host_turn: true,
        iteration: 15,
        max_iterations: 15,
        surface: "implement_overlay_promote",
      },
      sessionActivity: { "sess-1": { llmTurn: { status: "active" } } },
    });
    store.actions.mergeSession({ id: "sess-1", status: "idle" });
    expect(store.state.currentSession?.status).toBe("idle");
    expect(store.state.coordinatorLoopProgress).toBeUndefined();
    expect(store.state.sessionActivity["sess-1"]).toBeUndefined();
  });

  it("mergeSession idle preserves the stop-flight latch until HTTP settles", () => {
    const store = createAppStore({
      ...INITIAL_APP_STATE,
      currentSession: {
        id: "sess-1",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "00000000-0000-4000-8000-000000000001",
        status: "busy",
        posture: "build",
        workspace_path: "/p",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      },
      sessionActivity: {
        "sess-1": {
          stopping: true,
          llmTurn: { status: "active" },
        },
      },
    });
    store.actions.mergeSession({ id: "sess-1", status: "idle" });
    expect(store.state.sessionActivity["sess-1"]).toEqual({ stopping: true });
  });
});
