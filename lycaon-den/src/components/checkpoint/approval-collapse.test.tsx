import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../../chat/stream/transcript-viewport.tsx";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { ApprovalCard } from "./ApprovalCard.tsx";

function checkpoint(overrides: Partial<PendingCheckpoint> = {}): PendingCheckpoint {
  return {
    checkpointId: "checkpoint-collapse",
    sessionId: "session-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "2026-09-03T00:00:00Z",
    tool_approval: toolApprovalFixture({ command: "git push origin main" }),
    ...overrides,
  };
}

function renderCard(props: Record<string, unknown> = {}) {
  return render(() => (
    <ApprovalCard
      checkpoint={checkpoint()}
      onToolApproval={vi.fn()}
      onContentApply={vi.fn()}
      {...props}
    />
  ));
}

describe("approval card collapse", () => {
  it("retracts to a strip that names the pending action", () => {
    const view = renderCard();
    const card = view.getByTestId("tool-approval-card");
    expect(card.hasAttribute("data-minimized")).toBe(false);

    fireEvent.click(view.getByTestId("approval-card-minimize"));

    expect(card.hasAttribute("data-minimized")).toBe(true);
    expect(view.queryByTestId("approval-card-minimize")).toBeNull();
    const strip = view.getByTestId("approval-card-expand");
    expect(strip.textContent).toContain("git push origin main");
    expect(strip.getAttribute("aria-label")).toBe(
      APPROVALS_COPY.card.collapse.expandLabel,
    );

    fireEvent.click(strip);
    expect(card.hasAttribute("data-minimized")).toBe(false);
    expect(view.queryByTestId("approval-card-expand")).toBeNull();
  });

  it("keeps the Files link mounted across a collapse round trip", () => {
    const view = renderCard({projectId:"project"});
    const link = view.getByRole("button", {name:"Approval details in Files"});
    fireEvent.click(view.getByTestId("approval-card-minimize"));
    fireEvent.click(view.getByTestId("approval-card-expand"));
    expect(view.getByRole("button", {name:"Approval details in Files"})).toBe(link);
  });

  it("stands the approval shortcuts down while minimized", () => {
    const onToolApproval = vi.fn();
    const view = renderCard({ onToolApproval });

    fireEvent.click(view.getByTestId("approval-card-minimize"));
    const card = view.getByTestId("tool-approval-card");
    card.focus();
    fireEvent.keyDown(card, { key: "Enter" });

    expect(onToolApproval).not.toHaveBeenCalled();
  });

  it("carries the risk band into the collapsed strip", () => {
    const view = render(() => (
      <ApprovalCard
        checkpoint={checkpoint({
          tool_approval: toolApprovalFixture({
            command: "aws s3 rb s3://bucket",
            presentation: { consequence_band: "high_risk" } as never,
          }),
        })}
        onToolApproval={vi.fn()}
        onContentApply={vi.fn()}
      />
    ));

    expect(view.queryByTestId("approval-peek-risk")).toBeNull();
    fireEvent.click(view.getByTestId("approval-card-minimize"));
    expect(view.getByTestId("approval-peek-risk").textContent).toContain(
      APPROVALS_COPY.card.highRisk.label,
    );
  });

  it("keeps following the transcript while the card minimizes", () => {
    const controller = createTranscriptViewportController({
      sessionId: () => "session-1",
    });
    const view = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ApprovalCard
          checkpoint={checkpoint()}
          onToolApproval={vi.fn()}
          onContentApply={vi.fn()}
        />
      </TranscriptViewportProvider>
    ));

    fireEvent.click(view.getByTestId("approval-card-minimize"));

    expect(controller.following()).toBe(true);
  });

  it("follows a controlled minimize state", () => {
    const onMinimizedChange = vi.fn();
    const view = renderCard({ minimized: true, onMinimizedChange });

    expect(
      view.getByTestId("tool-approval-card").hasAttribute("data-minimized"),
    ).toBe(true);
    fireEvent.click(view.getByTestId("approval-card-expand"));
    expect(onMinimizedChange).toHaveBeenCalledWith(false);
  });
});
