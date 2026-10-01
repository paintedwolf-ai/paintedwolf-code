import {
  describe,
  expect,
  it,
} from "vitest";

import { createAppStore } from "./app-state.ts";

describe("createAppStore", () => {

  it("updates dependency readiness from worker events and preserves it on unrelated events", () => {
    const store = createAppStore();
    store.actions.setWorkers([{ id: "consumer", agent_type: "implementer", status: "pending", created_at: "t", dependencies: [{ worker_id: "producer", state: "waiting" }] }]);
    store.actions.updateWorker({ worker_id: "consumer", status: "pending", dependencies: [{ worker_id: "producer", state: "blocked" }] });
    expect(store.state.workers[0]?.dependencies?.[0]?.state).toBe("blocked");
    store.actions.updateWorker({ worker_id: "consumer", status: "pending" });
    expect(store.state.workers[0]?.dependencies?.[0]?.state).toBe("blocked");
    store.actions.updateWorker({ worker_id: "consumer", status: "running", dependencies: [{ worker_id: "producer", state: "ready" }] });
    expect(store.state.workers[0]?.dependencies?.[0]?.state).toBe("ready");
  });

  it("updateWorker patches worker row", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);
    store.actions.updateWorker({
      worker_id: "w1",
      status: "complete",
      merge_status: "merged",
    });
    expect(store.state.workers[0]?.status).toBe("complete");
    expect(store.state.workers[0]?.merge_status).toBe("merged");
  });

  it("updateWorker merges live tool-loop budget from worker SSE", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        agent_type: "web-researcher",
        status: "running",
        max_tool_loops: 40,
        created_at: "t",
      },
    ]);
    store.actions.updateWorker({
      worker_id: "w1",
      status: "running",
      max_tool_loops: 40,
      tool_loops_used: 7,
    });
    expect(store.state.workers[0]?.tool_loops_used).toBe(7);
    expect(store.state.workers[0]?.max_tool_loops).toBe(40);

    store.actions.updateWorker({ worker_id: "w1", status: "complete" });
    expect(store.state.workers[0]?.tool_loops_used).toBe(7);
    expect(store.state.workers[0]?.max_tool_loops).toBe(40);
  });

  it("updateWorker keeps the higher tool_loops_used", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        agent_type: "implementer",
        status: "running",
        max_tool_loops: 40,
        tool_loops_used: 12,
        created_at: "t",
      },
    ]);
    store.actions.updateWorker({
      worker_id: "w1",
      status: "running",
      max_tool_loops: 40,
      tool_loops_used: 3,
    });
    expect(store.state.workers[0]?.tool_loops_used).toBe(12);
  });

  it("updateWorker keeps the higher tool_calls_used", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        agent_type: "implementer",
        status: "running",
        max_tool_loops: 40,
        tool_loops_used: 12,
        tool_calls_used: 96,
        created_at: "t",
      },
    ]);
    store.actions.updateWorker({
      worker_id: "w1",
      status: "running",
      max_tool_loops: 40,
      tool_loops_used: 12,
      tool_calls_used: 40,
    });
    expect(store.state.workers[0]?.tool_calls_used).toBe(96);
  });

  // Worker events omit batch fields when the batch ends.
  it("updateWorker replaces the in-flight tool batch and clears it when absent", () => {
    const store = createAppStore();
    store.actions.updateWorker({
      worker_id: "w-batch",
      status: "running",
      max_tool_loops: 40,
      tool_loops_used: 3,
      turn_tool_calls: 6,
      turn_tools_done: 2,
    });
    expect(store.state.workers[0]?.turn_tools_done).toBe(2);

    store.actions.updateWorker({
      worker_id: "w-batch",
      status: "running",
      max_tool_loops: 40,
      tool_loops_used: 3,
      turn_tool_calls: 6,
      turn_tools_done: 5,
    });
    expect(store.state.workers[0]?.turn_tools_done).toBe(5);

    store.actions.updateWorker({
      worker_id: "w-batch",
      status: "running",
      max_tool_loops: 40,
      tool_loops_used: 4,
    });
    expect(store.state.workers[0]?.turn_tool_calls).toBeUndefined();
    expect(store.state.workers[0]?.turn_tools_done).toBeUndefined();
  });

  it("updateWorker installs and clears workspace preparation progress", () => {
    const store = createAppStore();
    store.actions.updateWorker({
      worker_id: "w-preparing",
      status: "running",
      workspace_preparation: {
        strategy: "direct_copy",
        stage: "snapshotting_branch",
        files: 100,
        bytes: 1024,
      },
    });
    expect(store.state.workers[0]?.workspace_preparation?.bytes).toBe(1024);
    store.actions.updateWorker({
      worker_id: "w-preparing",
      status: "running",
    });
    expect(store.state.workers[0]?.workspace_preparation?.bytes).toBe(1024);
  });

  it("mergeSessionWorkers preserves live tool budget from stale list refresh", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w-live",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "running",
        max_tool_loops: 40,
        tool_loops_used: 12,
        created_at: "t",
      },
    ]);
    store.actions.mergeSessionWorkers("sess-1", [
      {
        id: "w-live",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "running",
        max_tool_loops: 40,
        created_at: "t",
      },
    ], 0);
    expect(store.state.workers[0]?.tool_loops_used).toBe(12);
  });

  // List snapshots omit live batch progress.
  it("mergeSessionWorkers preserves the live tool batch across a list refresh", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w-live",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "running",
        max_tool_loops: 40,
        tool_loops_used: 12,
        tool_calls_used: 96,
        turn_tool_calls: 6,
        turn_tools_done: 2,
        created_at: "t",
      },
    ]);
    store.actions.mergeSessionWorkers("sess-1", [
      {
        id: "w-live",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "running",
        max_tool_loops: 40,
        tool_loops_used: 12,
        tool_calls_used: 96,
        created_at: "t",
      },
    ], 0);
    expect(store.state.workers[0]?.turn_tool_calls).toBe(6);
    expect(store.state.workers[0]?.turn_tools_done).toBe(2);
    expect(store.state.workers[0]?.tool_calls_used).toBe(96);
  });

  it("mergeSessionWorkers replaces only the active session slice", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w-other",
        parent_session_id: "sess-other",
        agent_type: "implementer",
        status: "complete",
        created_at: "t",
      },
      {
        id: "w-old",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "complete",
        created_at: "t",
      },
    ]);
    store.actions.applyWorkerTranscriptRows("w-old", [
      { id: "m1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "gone", created_at: "t" },
    ]);
    store.actions.mergeSessionWorkers("sess-1", [
      {
        id: "w-new",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t2",
      },
    ], 0);
    expect(store.state.workers.map((w) => w.id).sort()).toEqual([
      "w-new",
      "w-other",
    ]);
    expect(store.state.workerTranscripts["w-old"]).toBeUndefined();
  });

  it("updateWorker skips store write when worker row is unchanged", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);
    const before = store.state.workers;
    store.actions.updateWorker({ worker_id: "w1", status: "running" });
    expect(store.state.workers).toBe(before);
  });

  it("mergeSessionWorkers skips store write when session slice is unchanged", () => {
    const store = createAppStore();
    const rows = [
      {
        id: "w1",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        status: "running" as const,
        created_at: "t",
      },
    ];
    store.actions.setWorkers(rows);
    const before = store.state.workers;
    store.actions.mergeSessionWorkers("sess-1", [{ ...rows[0]! }], 0);
    expect(store.state.workers).toBe(before);
  });

  it("updateWorker upserts new row and patches existing row", () => {
    const store = createAppStore();
    store.actions.updateWorker({
      worker_id: "w-new",
      status: "running",
      agent_type: "implementer",
      child_session_id: "child-1",
    });
    expect(store.state.workers).toHaveLength(1);
    expect(store.state.workers[0]?.id).toBe("w-new");
    expect(store.state.workers[0]?.child_session_id).toBe("child-1");
    store.actions.updateWorker({ worker_id: "w-new", status: "complete" });
    expect(store.state.workers[0]?.status).toBe("complete");
  });
});
