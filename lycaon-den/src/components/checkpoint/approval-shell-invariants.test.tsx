import { describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";

const checkpoint: PendingCheckpoint = {
  checkpointId: "checkpoint-shell",
  sessionId: "session-shell",
  kind: "tool_approval",
  status: "pending",
  issuedAt: "2026-08-06T00:00:00Z",
  tool_approval: toolApprovalFixture(),
};

describe("approval shell invariants", () => {
  it("has one subject, one context, one action zone, and one redirect rail", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint}
        sessionId="session-shell"
        onToolApproval={vi.fn()}
        onContentApply={vi.fn()}
      />
    ));
    expect(view.container.querySelectorAll('[data-zone="subject"]')).toHaveLength(1);
    expect(view.container.querySelectorAll('[data-zone="context"]')).toHaveLength(1);
    expect(view.container.querySelectorAll('[data-zone="actions"]')).toHaveLength(1);
    expect(view.container.querySelectorAll('[data-zone="redirect-rail"]')).toHaveLength(1);
  });

  it("scrolls subject and context inside the card while actions stay outside", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint}
        sessionId="session-shell"
        onToolApproval={vi.fn()}
        onContentApply={vi.fn()}
      />
    ));
    const scroll = view.container.querySelector(".den-approval-card-scroll");
    expect(scroll?.querySelector('[data-zone="subject"]')).toBeTruthy();
    expect(scroll?.querySelector('[data-zone="context"]')).toBeTruthy();
    expect(scroll?.querySelector('[data-zone="actions"]')).toBeNull();
    expect(scroll?.querySelector('[data-zone="redirect-rail"]')).toBeNull();
  });

  it("keeps repeated-request links after context", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={{
          ...checkpoint,
          tool_approval: toolApprovalFixture({
            repeat: {
              reason_key: "authority_misuse:aws-cli/s3-remove-bucket",
              count: 4,
              subjects: ["aws s3 rb s3://a", "aws s3 rb s3://b", "aws s3 rb s3://c"],
            },
          }),
        }}
        sessionId="session-shell"
        onToolApproval={vi.fn()}
        onContentApply={vi.fn()}
      />
    ));
    const scroll = view.container.querySelector(".den-approval-card-body");
    const context = scroll?.querySelector('[data-zone="context"]');
    const repeat = scroll?.querySelector('[data-testid="approval-repeat"]');
    expect(context).toBeTruthy();
    expect(repeat).toBeTruthy();
    if (!context || !repeat) return;
    expect(context.compareDocumentPosition(repeat) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
