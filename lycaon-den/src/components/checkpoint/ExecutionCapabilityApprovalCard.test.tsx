import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { approvalOptionFixture, toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";

describe("execution capability approval cards", () => {
  it.each(["process_control", "host_execution"] as const)("uses the existing chat primary action for %s", (kind) => {
    const checkpoint: PendingCheckpoint = {
      checkpointId: "execution-approval", sessionId: "chat", kind: "tool_approval",
      status: "pending", issuedAt: "2026-09-19T00:00:00Z",
      tool_approval: toolApprovalFixture({
        command: "sudo -n /usr/bin/id",
        subject: { kind, title: "Approve execution access", targets: [{ kind, label: "sudo -n /usr/bin/id" }] },
        recommendedOptionId: "execution_chat",
        elevatedEffects: [kind],
        options: [
          approvalOptionFixture(),
          approvalOptionFixture({ id: "execution_chat", kind: "lease", rung: "chat", scope: "chat", title: "Allow for this chat", coverage: "commands using this capability for this chat" }),
        ],
      }),
    };
    const approve = vi.fn();
    const view = render(() => <ApprovalCard projectId="project" checkpoint={checkpoint} onToolApproval={approve} onContentApply={vi.fn()} />);
    const primary = view.getByTestId("approval-approve-primary");
    const elevated = view.getByTestId("approval-elevated-access");
    expect(elevated.getAttribute("data-tip")).toContain(kind === "host_execution" ? "host execution" : "process control");
    expect(elevated.getAttribute("data-tip")).toContain("Allow once does not save access");
    fireEvent.click(view.getByTestId("approval-card-minimize"));
    expect(view.getByTestId("approval-elevated-access").getAttribute("data-tip")).toContain("unlock icon appears");
    fireEvent.click(view.getByTestId("approval-card-expand"));
    expect(primary.textContent).toContain("Allow for this chat");
    fireEvent.click(primary);
    expect(approve).toHaveBeenCalledWith("approve", { optionId: "execution_chat" });
  });
  it("does not mark a one-time action as saved elevated access", () => {
    const checkpoint: PendingCheckpoint = {
      checkpointId: "once-only", sessionId: "chat", kind: "tool_approval",
      status: "pending", issuedAt: "2026-09-19T00:00:00Z",
      tool_approval: toolApprovalFixture({
        subject: { kind: "host_execution", title: "Host execution", targets: [{ kind: "host_execution", label: "id" }] },
        options: [approvalOptionFixture()],
      }),
    };
    const view = render(() => <ApprovalCard checkpoint={checkpoint} onToolApproval={vi.fn()} onContentApply={vi.fn()} />);
    expect(view.queryByTestId("approval-elevated-access")).toBeNull();
  });
  it("shows signal and identity details for a single process through the existing target list", () => {
    const checkpoint: PendingCheckpoint = {
      checkpointId: "signal-approval", sessionId: "chat", kind: "tool_approval",
      status: "pending", issuedAt: "2026-09-19T00:00:00Z",
      tool_approval: toolApprovalFixture({
        tool: "process_signal", command: "",
        subject: {
          kind: "action_set", title: "Signal host processes",
          targets: [{ kind: "process", label: "Send STOP to worker (PID 42)", details: {
            // The host encodes these args as a Go map, so they arrive in sorted key order.
            args: { executable: "/usr/bin/worker", instance: "42:7", pid: 42, signal: "STOP", uid: 501 },
          } }],
        },
      }),
    };
    const view = render(() => <ApprovalCard projectId="project" checkpoint={checkpoint} onToolApproval={vi.fn()} onContentApply={vi.fn()} />);
    const target = view.getByTestId("approval-target-0");
    expect(target.textContent).toContain("Send STOP to worker (PID 42)");
    expect(target.textContent).toContain("signal: STOP");
    expect(target.textContent).toContain("instance: 42:7");
    expect(target.textContent).toContain("executable: /usr/bin/worker");
  });
});
