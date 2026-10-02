import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";

const resolveCheckpointWithPresence = vi.hoisted(() => vi.fn());
vi.mock("../../platform/presence.ts", () => ({ resolveCheckpointWithPresence }));

import type { PendingCheckpoint } from "./checkpoint-model.ts";
import { toolApprovalFixture } from "./approval-test-fixtures.ts";
import { resolveToolApproval } from "./checkpoint-actions.ts";

function fixture() {
  const resolveCheckpoint = vi.fn().mockResolvedValue({});
  return {
    client: stubClient({ resolveCheckpoint }),
    resolveCheckpoint,
  };
}

const toolCheckpoint: PendingCheckpoint = {
  checkpointId: "checkpoint-tool",
  sessionId: "session-1",
  kind: "tool_approval",
  status: "pending",
  issuedAt: "2026-08-03T00:00:00Z",
  tool_approval: toolApprovalFixture(),
};

describe("checkpoint denial guidance", () => {
  it("attaches explicit composer guidance to a tool denial", async () => {
    const f = fixture();
    await resolveToolApproval(
      f.client,
      toolCheckpoint,
      "reject",
      { guidance: "Use the native read tool instead." },
    );
    expect(f.resolveCheckpoint).toHaveBeenCalledWith(
      "session-1",
      "checkpoint-tool",
      {
        kind: "tool_approval",
        action: "reject",
        guidance: "Use the native read tool instead.",
      },
    );
  });

  it("selects an opaque host-authored option", async () => {
    const f = fixture();
    await resolveToolApproval(
      f.client,
      toolCheckpoint,
      "approve",
      { optionId: "allow_write_root_for_task" },
    );
    expect(f.resolveCheckpoint).toHaveBeenCalledWith(
      "session-1",
      "checkpoint-tool",
      {
        kind: "tool_approval",
        action: "approve",
        option_id: "allow_write_root_for_task",
      },
    );
  });
});

describe("held release approvals", () => {
  const held: PendingCheckpoint = {
    ...toolCheckpoint,
    tool_approval: (() => {
      const payload = toolApprovalFixture();
      payload.plan.held_release = {
        secrets: [{ reference: "{{paintedwolf-secret:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}}", name: "Deploy key", version: 1 }],
        recipients: [{ label: "Local file: .env", surface: "file", kind: "file" }],
      };
      return payload;
    })(),
  };

  it("hands an approving option to the desktop shell, never the bearer API", async () => {
    resolveCheckpointWithPresence.mockReset().mockResolvedValue({});
    const f = fixture();
    const option = held.tool_approval!.plan.options.find((candidate) => candidate.decision_action === "approve")!;
    await resolveToolApproval(f.client, held, "approve", { optionId: option.id });
    expect(resolveCheckpointWithPresence).toHaveBeenCalledWith("session-1", "checkpoint-tool", option.id);
    expect(f.resolveCheckpoint).not.toHaveBeenCalled();
  });

  it("keeps No on the ordinary path", async () => {
    resolveCheckpointWithPresence.mockReset();
    const f = fixture();
    await resolveToolApproval(f.client, held, "reject");
    expect(resolveCheckpointWithPresence).not.toHaveBeenCalled();
    expect(f.resolveCheckpoint).toHaveBeenCalled();
  });
});
