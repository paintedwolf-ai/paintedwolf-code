import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import {
  approvalOptionFixture,
  toolApprovalFixture,
} from "../../chat/checkpoint/approval-test-fixtures.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";

/** The host orders secret choices and selects the primary action. */
function secretCard(): PendingCheckpoint {
  return {
    checkpointId: "secret-ladder-1",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-08-08T00:00:00Z",
    tool_approval: toolApprovalFixture({
      tool: "model_request",
      stage: "pre_send",
      subject: {
        kind: "secret",
        title: "Credential detected before sending this model request",
        targets: [{ kind: "secret", label: "GitHub Personal Access Token" }],
      },
      presentation: {
        action: "Send model request",
        gate: "secret_outbound",
        impact: "This action would expose a credential outside protected secret storage.",
      },
      recommendedOptionId: "send_unchanged",
      options: [
        approvalOptionFixture({
          id: "send_redacted",
          kind: "redacted",
          rung: "redacted",
          title: "Send redacted",
          decision_action: "redact",
        }),
        approvalOptionFixture({ id: "send_unchanged", rung: "unchanged", title: "Send unchanged" }),
        approvalOptionFixture({
          id: "release_day",
          kind: "lease",
          rung: "day",
          scope: "project",
          title: "Send unchanged for 1 day",
        }),
        approvalOptionFixture({
          id: "release_task",
          kind: "lease",
          rung: "chat",
          scope: "chat",
          title: "Send unchanged for this chat",
        }),
        approvalOptionFixture({
          id: "release_project",
          kind: "lease",
          rung: "project",
          scope: "project",
          title: "Send unchanged for this project for 7 days",
        }),
        approvalOptionFixture({
          id: "keep_redacting",
          kind: "lease",
          rung: "device",
          scope: "device",
          group: "Redaction",
          title: "Keep redacting on this device for 30 days",
          coverage: "this credential, stripped from every send on this device",
        }),
        approvalOptionFixture({
          id: "quiet_chat",
          kind: "quiet",
          rung: "chat",
          group: "Allow and stop asking",
          title: "For this chat",
          coverage: "GitHub PAT → api.example",
        }),
      ],
    }),
  };
}

describe("secret card ladders", () => {
  it("faces the host's send and offers redaction beneath it", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCard()}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    expect(view.getByTestId("approval-approve-primary").textContent).toContain(
      "Send unchanged",
    );

    fireEvent.click(view.getByTestId("approval-grant-face"));
    const menu = screen.getByTestId("approval-grant-menu");
    const items = [...menu.querySelectorAll('[role="menuitem"]')].map(
      (el) => el.textContent ?? "",
    );

    // Every release rung is reachable, and so is redaction.
    for (const want of [
      "Send redacted",
      "Send unchanged for 1 day",
      "Send unchanged for this chat",
      "Send unchanged for this project for 7 days",
    ]) {
      expect(items.some((t) => t.includes(want))).toBe(true);
    }
  });

  it("separates keeping redaction from stopping the ask", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCard()}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    fireEvent.click(view.getByTestId("approval-grant-face"));
    const menu = screen.getByTestId("approval-grant-menu");
    const groups = [
      ...menu.querySelectorAll('[data-testid="approval-grant-group"]'),
    ].map((el) => el.textContent ?? "");

    // Both stop the interruption; only one keeps stripping the credential, so
    // they sit in separate groups.
    expect(groups).toContain("Redaction");
    expect(groups).toContain("Allow and stop asking");
  });

  it("keeps Send redacted in the face slot when rewrite is unavailable", () => {
    const onToolApproval = vi.fn();
    const note =
      "Redaction is not offered here: replacing the value would run a command neither you nor the model wrote.";
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          checkpointId: "secret-unrewritable-1",
          sessionId: "session-1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-08-08T00:00:00Z",
          tool_approval: toolApprovalFixture({
            tool: "command",
            stage: "pre_send",
            subject: {
              kind: "secret",
              title: "Credential detected before sending this command",
              targets: [{ kind: "secret", label: "HTTP Basic Authorization Header" }],
            },
            presentation: {
              action: "Send command",
              gate: "secret_outbound",
              impact: "This action would expose a credential outside protected secret storage.",
              option_note: note,
            },
            recommendedOptionId: "send_redacted",
            options: [
              approvalOptionFixture({
                id: "send_redacted",
                kind: "redacted",
                rung: "redacted",
                title: "Send redacted",
                decision_action: "redact",
                disabled: true,
              }),
              approvalOptionFixture({
                id: "send_unchanged",
                rung: "unchanged",
                title: "Send unchanged",
              }),
            ],
          }),
        }}
        resolving={false}
        onToolApproval={onToolApproval}
        onContentApply={() => {}}
      />
    ));
    const primary = view.getByTestId(
      "approval-approve-primary",
    ) as HTMLButtonElement;
    expect(primary.textContent).toContain("Send redacted");
    expect(primary.disabled).toBe(true);
    fireEvent.click(primary);
    expect(onToolApproval).not.toHaveBeenCalled();

    fireEvent.click(view.getByTestId("approval-grant-face"));
    const menu = screen.getByTestId("approval-grant-menu");
    const items = [...menu.querySelectorAll('[role="menuitem"]')].map(
      (el) => el.textContent ?? "",
    );
    expect(items.some((t) => t.includes("Send unchanged"))).toBe(true);
  });

  it("never faces a quiet rung", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={secretCard()}
        resolving={false}
        onToolApproval={() => {}}
        onContentApply={() => {}}
      />
    ));
    const primary = view.getByTestId("approval-approve-primary").textContent ?? "";
    expect(primary).not.toContain("Stop asking");
    expect(primary).not.toContain("Redaction");
  });
});
