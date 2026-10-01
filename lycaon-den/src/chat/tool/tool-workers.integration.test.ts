import { describe, expect, it } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { upsertPendingCheckpoint } from "../checkpoint/checkpoint-model.ts";
import { toolApprovalFixture } from "../checkpoint/approval-test-fixtures.ts";

describe("tool-workers integration", () => {
  it("checkpoint SSE pending then resolved updates store", () => {
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
    store.actions.setWorkers([
      {
        id: "job-1",
        parent_session_id: "sess-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);
    store.actions.mergeCheckpoint({
      id: "chk-1",
      session_id: "child-1",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t",
      tool_approval: toolApprovalFixture(),
    });
    expect(store.state.pendingCheckpoints).toHaveLength(1);
    expect(store.state.pendingCheckpoints[0]?.checkpointId).toBe("chk-1");
    expect(store.state.pendingCheckpoints[0]?.sessionId).toBe("child-1");

    store.actions.mergeCheckpoint({
      id: "chk-1",
      session_id: "child-1",
      kind: "tool_approval",
      status: "approved",
      issued_at: "t",
    });
    expect(store.state.pendingCheckpoints).toHaveLength(0);
  });

  it("drops worker child checkpoints for a different coordinator session", () => {
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
    store.actions.mergeCheckpoint({
      id: "chk-1",
      session_id: "child-other",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t",
      tool_approval: toolApprovalFixture(),
    });
    expect(store.state.pendingCheckpoints).toHaveLength(0);
  });

  it("content_apply checkpoint merges into store", () => {
    const store = createAppStore();
    store.actions.mergeCheckpoint({
      id: "chk-2",
      session_id: "sess-1",
      kind: "content_apply",
      status: "pending",
      issued_at: "t",
      content_apply: {
        tool: "write",
        path: "a.txt",
        after: "hello",
        hunks: [
          { id: "host-hunk", path: "a.txt", before: "", after: "hello" },
        ],
      },
    });
    expect(store.state.pendingCheckpoints[0]?.kind).toBe("content_apply");
  });

  it("worker SSE patches existing drawer row status", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);
    store.actions.updateWorker({
      worker_id: "w1",
      status: "complete",
      result: { summary: "done" },
    });
    expect(store.state.workers[0]?.status).toBe("complete");
  });

  it("upsertPendingCheckpoint is pure", () => {
    const list = upsertPendingCheckpoint([], {
      id: "a",
      session_id: "s",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t",
      tool_approval: toolApprovalFixture({ tool: "read", command: "" }),
    });
    expect(list).toHaveLength(1);
    const cleared = upsertPendingCheckpoint(list, {
      id: "a",
      session_id: "s",
      kind: "tool_approval",
      status: "rejected",
      issued_at: "t",
    });
    expect(cleared).toHaveLength(0);
  });
});
