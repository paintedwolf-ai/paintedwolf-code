import { describe, expect, it } from "vitest";
import type { WorkerTask } from "../../api/types.ts";
import {
  collapseWorkersByIdentity,
  findSessionWorker,
} from "./worker-identity.ts";

function taskRow(overrides: Partial<WorkerTask> & Pick<WorkerTask, "id">): WorkerTask {
  return {
    agent_type: "implementer",
    status: "complete",
    created_at: "2026-11-15T01:00:00Z",
    ...overrides,
  };
}

describe("worker-identity", () => {
  it("collapses multiple round jobs onto the latest round while preserving lifetime counters and brief", () => {
    const round1 = taskRow({
      id: "job-r1",
      worker_record_id: "rec-1",
      worker_name: "auth-impl",
      round: 1,
      child_session_id: "child-1",
      parent_session_id: "sess-1",
      project_id: "proj-1",
      brief: "Implement token refresh",
      tool_loops_used: 7,
      tool_calls_used: 18,
      result_handle: "res-1",
      result_disposition: "landed",
      created_at: "2026-11-15T01:00:00Z",
    });
    const round2 = taskRow({
      id: "job-r2",
      worker_record_id: "rec-1",
      round: 2,
      status: "running",
      tool_loops_used: 3,
      tool_calls_used: 22,
      created_at: "2026-11-15T01:05:00Z",
    });
    const other = taskRow({
      id: "job-scout",
      worker_record_id: "rec-2",
      worker_name: "scout",
      round: 1,
      parent_session_id: "sess-1",
      brief: "Inspect auth handlers",
      created_at: "2026-11-15T01:02:00Z",
    });

    const collapsed = collapseWorkersByIdentity([round1, other, round2]);
    expect(collapsed).toHaveLength(2);
    expect(collapsed[0]).toMatchObject({
      id: "job-r2",
      worker_record_id: "rec-1",
      worker_name: "auth-impl",
      child_session_id: "child-1",
      parent_session_id: "sess-1",
      project_id: "proj-1",
      round: 2,
      status: "running",
      brief: "Implement token refresh",
      tool_loops_used: 7,
      tool_calls_used: 22,
    });
    expect(collapsed[1]?.id).toBe("job-scout");
  });

  it("bridges transitive identity across child_session_id and worker_name within one session", () => {
    const first = taskRow({
      id: "job-a",
      child_session_id: "child-shared",
      parent_session_id: "sess-1",
      created_at: "2026-11-15T01:00:00Z",
    });
    const bridge = taskRow({
      id: "job-b",
      child_session_id: "child-shared",
      worker_name: "builder",
      parent_session_id: "sess-1",
      created_at: "2026-11-15T01:01:00Z",
    });
    const third = taskRow({
      id: "job-c",
      worker_name: "builder",
      parent_session_id: "sess-1",
      created_at: "2026-11-15T01:02:00Z",
    });
    const separateSession = taskRow({
      id: "job-other-sess",
      worker_name: "builder",
      parent_session_id: "sess-2",
      created_at: "2026-11-15T01:03:00Z",
    });

    const collapsed = collapseWorkersByIdentity([third, separateSession, first, bridge]);
    expect(collapsed.map((w) => w.id)).toEqual(["job-c", "job-other-sess"]);
  });

  it("resolves canonical workers and round job sets from earlier round job ids", () => {
    const round1 = taskRow({
      id: "job-1",
      worker_record_id: "rec-9",
      worker_name: "reviewer",
      child_session_id: "child-9",
      round: 1,
    });
    const round2 = taskRow({
      id: "job-2",
      worker_record_id: "rec-9",
      worker_name: "reviewer",
      child_session_id: "child-9",
      round: 2,
    });
    const sessionList = collapseWorkersByIdentity([round1, round2]);

    expect(findSessionWorker(sessionList, "")).toBeUndefined();
    expect(findSessionWorker(sessionList, "rec-9")?.id).toBe("job-2");
    expect(findSessionWorker(sessionList, "reviewer")?.id).toBe("job-2");
    expect(findSessionWorker(sessionList, "child-9")?.id).toBe("job-2");
    expect(findSessionWorker(sessionList, "job-1", [round1, round2])?.id).toBe("job-2");
    expect(findSessionWorker(sessionList, "unknown", [round1, round2])).toBeUndefined();
  });
});
