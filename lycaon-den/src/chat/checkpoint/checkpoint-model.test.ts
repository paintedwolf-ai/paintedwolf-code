import { describe, expect, it } from "vitest";
import { upsertPendingCheckpoint } from "./checkpoint-model.ts";
import { toolApprovalFixture } from "./approval-test-fixtures.ts";

describe("upsertPendingCheckpoint", () => {
  it("upserts by checkpoint id only and keeps distinct ids", () => {
    const a = upsertPendingCheckpoint([], {
      id: "chk-a",
      session_id: "s",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t1",
      tool_approval: toolApprovalFixture(),
    });
    const both = upsertPendingCheckpoint(a, {
      id: "chk-b",
      session_id: "s",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t2",
      // Identical titles and commands still have distinct checkpoint identities.
      tool_approval: toolApprovalFixture(),
    });
    expect(both.map((p) => p.checkpointId).sort()).toEqual(["chk-a", "chk-b"]);

    const updated = upsertPendingCheckpoint(both, {
      id: "chk-a",
      session_id: "s",
      kind: "tool_approval",
      status: "pending",
      issued_at: "t3",
      tool_approval: toolApprovalFixture({ joinedCount: 2 }),
    });
    expect(updated).toHaveLength(2);
    const refreshed = updated.find((p) => p.checkpointId === "chk-a");
    expect(refreshed?.tool_approval?.joined_count).toBe(2);
    expect(updated.some((p) => p.checkpointId === "chk-b")).toBe(true);
  });
});
