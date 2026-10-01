import { describe, expect, it } from "vitest";
import type { BoardView, WorkerTask } from "../../api/types.ts";
import type { PendingCheckpoint } from "../checkpoint/checkpoint-model.ts";
import { toolApprovalFixture } from "../checkpoint/approval-test-fixtures.ts";
import {
  boardWithWorkerApprovalState,
  pendingWorkerApprovals,
} from "./worker-approval-model.ts";

const worker: WorkerTask = {
  id: "job-1",
  child_session_id: "child-1",
  parent_session_id: "parent-1",
  agent_type: "implementer",
  status: "running",
  created_at: "2026-01-01T00:00:00Z",
};

const pending: PendingCheckpoint = {
  checkpointId: "checkpoint-1",
  sessionId: "child-1",
  kind: "tool_approval",
  status: "pending",
  issuedAt: "2026-01-01T00:00:01Z",
  tool_approval: toolApprovalFixture({ command: "git push origin main" }),
};

describe("worker approval model", () => {
  it("joins pending child checkpoints to worker jobs", () => {
    expect(pendingWorkerApprovals([worker], [pending])).toEqual([
      {
        jobId: "job-1",
        childSessionId: "child-1",
        checkpointId: "checkpoint-1",
        kind: "tool_approval",
        commandSummary: "git push origin main",
      },
    ]);
  });

  it("projects blocked_on_approval onto the board roster and clears it", () => {
    const board = {
      roster: [{ worker_id: "job-1", agent_type: "implementer", status: "running" }],
    } as unknown as BoardView;
    expect(
      boardWithWorkerApprovalState(board, [worker], [pending])?.roster?.[0]
        ?.blocked_on_approval,
    ).toBe(true);
    expect(
      boardWithWorkerApprovalState(board, [worker], [])?.roster?.[0]
        ?.blocked_on_approval,
    ).toBeUndefined();
  });

  it("ignores parent, resolved, and unrelated checkpoint kinds", () => {
    expect(
      pendingWorkerApprovals([worker], [
        { ...pending, sessionId: "parent-1" },
        { ...pending, status: "approved" },
        { ...pending, kind: "content_apply" },
      ]),
    ).toEqual([]);
  });

});
