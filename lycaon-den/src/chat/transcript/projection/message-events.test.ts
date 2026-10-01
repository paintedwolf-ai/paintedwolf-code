import { describe, expect, it, vi } from "vitest";
import type { Message } from "../../../api/types.ts";
import { createAppStore } from "../../../store/app-state.ts";
import { applyMessageEvent, completeChatHydrationAndReplay } from "./message-events.ts";

/** Seed the transcript baseline for the store's current session (watermark 0). */
function seedMessages(store: ReturnType<typeof createAppStore>, rows: Message[]) {
  const sid = store.state.currentSession?.id ?? "seed-session";
  store.actions.installTranscriptBaseline(sid, rows, 0);
}

function activeLlmTurn(store: ReturnType<typeof createAppStore>, sessionId: string) {
  store.actions.setCurrentSession({
    id: sessionId,
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
    call_id: "a1",
    session_id: sessionId,
    provider: "mock",
    model: "mock",
    status: "active",
    tokens: {},
  });
}

describe("applyMessageEvent", () => {
  it("appends parent session rows from SSE", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    seedMessages(store, [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t" },
    ]);

    const applied = applyMessageEvent(store, {
      session_id: "parent-1",
      op: "append",
      message: {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Your game is ready.",
        created_at: "t2",
      },
    });

    expect(applied).toBe(true);
    expect(store.state.messages).toHaveLength(2);
    expect(store.state.messages[1]?.content).toContain("ready");
  });

  it("resolves the pending-send bubble when its echo row lands", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    seedMessages(store, []);
    store.actions.addPendingSend("parent-1", {
      kind: "prompt",
      operationId: "op-1",
      text: "hi",
      state: "sending",
    });

    applyMessageEvent(store, {
      session_id: "parent-1",
      op: "append",
      message: {
        id: "a-unrelated",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "working…",
        created_at: "t",
      },
    });
    expect(store.state.pendingSends["parent-1"]).toHaveLength(1);

    applyMessageEvent(store, {
      session_id: "parent-1",
      op: "append",
      message: {
        id: "op-1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hi",
        created_at: "t",
      },
    });
    expect(store.state.pendingSends["parent-1"]).toBeUndefined();
    expect(store.state.messages.map((m) => m.id)).toEqual([
      "a-unrelated",
      "op-1",
    ]);
  });

  it("patches an existing row by id", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    seedMessages(store, [
      { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "Hel", created_at: "t" },
    ]);

    applyMessageEvent(store, {
      session_id: "parent-1",
      op: "patch",
      message: { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "Hello", created_at: "t" },
    });

    expect(store.state.messages[0]?.content).toBe("Hello");
  });

  it("skips no-op patches", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const row = { id: "a1", role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "Hello", created_at: "t" };
    seedMessages(store, [row]);

    const applied = applyMessageEvent(store, {
      session_id: "parent-1",
      op: "patch",
      message: { ...row },
    });

    expect(applied).toBe(false);
  });

  it("adopts patch events as full-row snapshots — absent fields clear", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "0a4c8296-291e-42e4-8daf-af798b03c5e0",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "spec",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const planRunId = "0ea26756-a763-4c2a-b933-fa6b64064a20";
    seedMessages(store, [
      {
        id: "940fad1a-398e-4fdd-8942-25f824bf4ff0",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: planRunId,
        created_at: "t",
        tool_calls: [{ id: "call_progress", name: "update_progress", args: {} }],
      },
    ]);

    // The replacement snapshot retains the run id and removes the unexecuted tool call.
    applyMessageEvent(store, {
      session_id: "0a4c8296-291e-42e4-8daf-af798b03c5e0",
      op: "patch",
      message: {
        id: "940fad1a-398e-4fdd-8942-25f824bf4ff0",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "final prose",
        workflow_run_id: planRunId,
        created_at: "t",
      },
    });

    expect(store.state.messages[0]?.workflow_run_id).toBe(planRunId);
    expect(store.state.messages[0]?.content).toBe("final prose");
    expect(store.state.messages[0]?.tool_calls).toBeUndefined();
  });

  it("clears llm turn chrome when authoritative assistant patch arrives after llm idle", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setLLMCallStatus({
      call_id: "a1",
      session_id: "parent-1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });
    store.actions.setLLMCallStatus({
      call_id: "a1",
      session_id: "parent-1",
      provider: "mock",
      model: "mock",
      status: "ok",
      tokens: {},
    });
    expect(store.state.sessionActivity["parent-1"]?.llmTurn?.status).toBe("done");

    applyMessageEvent(store, {
      session_id: "parent-1",
      op: "patch",
      message: { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "Hello", created_at: "t" },
    });

    expect(store.state.messages[0]?.content).toBe("Hello");
    expect(store.state.sessionActivity["parent-1"]?.llmTurn).toBeUndefined();
  });

  it("keeps llm turn chrome during content patches while llm is active", () => {
    const store = createAppStore();
    activeLlmTurn(store, "parent-1");

    applyMessageEvent(store, {
      session_id: "parent-1",
      op: "patch",
      message: { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "Hello", created_at: "t" },
    });

    expect(store.state.messages[0]?.content).toBe("Hello");
    expect(store.state.sessionActivity["parent-1"]?.llmTurn?.status).toBe("active");
  });

  it("clears llm turn chrome when authoritative assistant patch includes tool_calls", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setLLMCallStatus({
      call_id: "a1",
      session_id: "parent-1",
      provider: "mock",
      model: "mock",
      status: "active",
      tokens: {},
    });

    applyMessageEvent(store, {
      session_id: "parent-1",
      op: "patch",
      message: {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "x.go" } }],
        created_at: "t",
      },
    });

    expect(store.state.messages[0]?.tool_calls).toHaveLength(1);
    expect(store.state.sessionActivity["parent-1"]?.llmTurn).toBeUndefined();
  });

  it("ignores events for unrelated sessions", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    seedMessages(store, []);

    expect(
      applyMessageEvent(store, {
        session_id: "other",
        op: "append",
        message: { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "x", created_at: "t" },
      }),
    ).toBe(false);
    expect(store.state.messages).toHaveLength(0);
  });

  it("routes child session SSE via parent task tool result before worker row has child_session_id", async () => {
    vi.useFakeTimers();
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "job-1",
        parent_session_id: "parent-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);
    seedMessages(store, [
      {
        id: "tool-1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: JSON.stringify({
          job_id: "job-1",
          child_session_id: "child-1",
        }),
        tool_result: {
          content: "enqueued",
          tool: "task",
          dispatch: {
            worker_id: "job-1",
            child_session_id: "child-1",
          },
        },
        created_at: "t",
      },
    ]);

    expect(
      applyMessageEvent(store, {
        session_id: "child-1",
        op: "append",
        message: { id: "a1", worker_id: "job-1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "Early", created_at: "t" },
      }),
    ).toBe(true);

    await vi.advanceTimersByTimeAsync(100);
    expect(store.state.workerTranscripts["job-1"]?.rows?.[0]?.content).toBe("Early");
    vi.useRealTimers();
  });

  it("syncs worker roster when parent task tool result arrives over SSE", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });

    applyMessageEvent(store, {
      session_id: "parent-1",
      op: "append",
      message: {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content:
          '{"job_id":"job-1","child_session_id":"child-1","status":"enqueued"}',
        tool_result: {
          content:
            '{"job_id":"job-1","child_session_id":"child-1","status":"enqueued"}',
          tool: "task",
          dispatch: {
            worker_id: "job-1",
            child_session_id: "child-1",
          },
        },
        created_at: "t",
      },
    });

    expect(store.state.workers).toHaveLength(1);
    expect(store.state.workers[0]).toMatchObject({
      id: "job-1",
      parent_session_id: "parent-1",
      child_session_id: "child-1",
    });
  });

  it("queues child worker session patches for coalesced transcript cache", async () => {
    vi.useFakeTimers();
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "job-1",
        parent_session_id: "parent-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);

    expect(
      applyMessageEvent(store, {
        session_id: "child-1",
        op: "patch",
        message: { id: "a1", worker_id: "job-1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "Hi", created_at: "t" },
      }),
    ).toBe(true);
    expect(store.state.workerTranscripts["job-1"]?.rows).toBeUndefined();

    await vi.advanceTimersByTimeAsync(100);
    expect(store.state.workerTranscripts["job-1"]?.rows?.[0]?.content).toBe("Hi");
    vi.useRealTimers();
  });

  it("routes a resumed child-session row to its exact worker job", async () => {
    vi.useFakeTimers();
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "job-old",
        parent_session_id: "parent-1",
        child_session_id: "child-1",
        agent_type: "repo-researcher",
        status: "complete",
        created_at: "t1",
      },
      {
        id: "job-new",
        parent_session_id: "parent-1",
        child_session_id: "child-1",
        agent_type: "repo-researcher",
        status: "running",
        created_at: "t2",
      },
    ]);

    expect(
      applyMessageEvent(store, {
        session_id: "child-1",
        op: "append",
        message: {
          id: "a-new",
          worker_id: "job-new",
          role: "assistant",
          origin: "model" as const,
          authority: "none" as const,
          trust_tier: "trusted" as const,
          content: "continuation",
          created_at: "t2",
        },
      }),
    ).toBe(true);

    await vi.advanceTimersByTimeAsync(100);
    expect(store.state.workerTranscripts["job-old"]?.rows).toBeUndefined();
    expect(store.state.workerTranscripts["job-new"]?.rows?.[0]?.id).toBe("a-new");
    vi.useRealTimers();
  });

  it("buffers parent events under the hydration lock and replays them on completion", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "parent-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    // Lock the session as a resume hydrate would.
    store.actions.beginSessionResumeSwitch("parent-1");

    const applied = applyMessageEvent(store, {
      session_id: "parent-1",
      op: "append",
      seq: 3,
      message: { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "mid-hydrate row", created_at: "t", seq: 3 },
    });
    // Blocked, not dropped.
    expect(applied).toBe(false);
    expect(store.state.messages.find((m) => m.id === "a1")).toBeUndefined();

    // Baseline installs at watermark 1 (before the buffered row), then replay.
    store.actions.installTranscriptBaseline(
      "parent-1",
      [{ id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", created_at: "t", seq: 1 }],
      1,
    );
    completeChatHydrationAndReplay(store);

    expect(store.state.chatHydrationLock).toBeUndefined();
    expect(store.state.messages.find((m) => m.id === "a1")?.content).toBe(
      "mid-hydrate row",
    );
  });
});
