import { describe, expect, it } from "vitest";
import type { WorkerTask } from "../../api/types.ts";
import type { PendingCheckpoint } from "./checkpoint-model.ts";
import { toolApprovalFixture } from "./approval-test-fixtures.ts";
import {
  checkpointAppliesToSessionView,
  childSessionIdsForParent,
  pendingCheckpointsForSessionView,
} from "./checkpoint-session-scope.ts";

const workers: WorkerTask[] = [
  {
    id: "job-1",
    parent_session_id: "parent-1",
    child_session_id: "child-1",
    agent_type: "implementer",
    status: "running",
    created_at: "t",
  },
  {
    id: "job-2",
    parent_session_id: "parent-2",
    child_session_id: "child-other",
    agent_type: "implementer",
    status: "running",
    created_at: "t",
  },
];

const pending: PendingCheckpoint[] = [
  {
    checkpointId: "cp-parent",
    sessionId: "parent-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "t",
  },
  {
    checkpointId: "cp-child",
    sessionId: "child-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "t",
    tool_approval: { ...toolApprovalFixture(), tool_call_id: "call-1" },
  },
  {
    checkpointId: "cp-other",
    sessionId: "child-other",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "t",
  },
];

describe("checkpoint session scope", () => {
  it("childSessionIdsForParent returns child session ids for the parent", () => {
    expect(childSessionIdsForParent(workers, "parent-1")).toEqual(["child-1"]);
  });

  it("checkpointAppliesToSessionView accepts parent and worker child sessions", () => {
    expect(checkpointAppliesToSessionView("parent-1", "parent-1", workers)).toBe(
      true,
    );
    expect(checkpointAppliesToSessionView("child-1", "parent-1", workers)).toBe(
      true,
    );
    expect(
      checkpointAppliesToSessionView("child-other", "parent-1", workers),
    ).toBe(false);
  });

  it("pendingCheckpointsForSessionView includes worker child checkpoints", () => {
    expect(
      pendingCheckpointsForSessionView(pending, "parent-1", workers).map(
        (cp) => cp.checkpointId,
      ),
    ).toEqual(["cp-parent", "cp-child"]);
  });

  it("pendingCheckpointsForSessionView exact mode matches one session only", () => {
    expect(
      pendingCheckpointsForSessionView(pending, "child-1", workers, {
        exact: true,
      }).map((cp) => cp.checkpointId),
    ).toEqual(["cp-child"]);
  });
});
