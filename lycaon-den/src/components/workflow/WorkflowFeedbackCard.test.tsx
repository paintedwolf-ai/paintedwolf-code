import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";
import { WorkflowFeedbackCard } from "./WorkflowFeedbackCard.tsx";
import type { LycaonClient } from "../../api/client.ts";
import type { WorkflowFeedbackMeta } from "../../api/types.ts";

function client(overrides: Partial<LycaonClient> = {}): LycaonClient {
  return stubClient({
    resolveWorkflowFeedback: vi.fn().mockResolvedValue({}),
    resolveWorkflowDecision: vi.fn().mockResolvedValue({}),
    ...overrides,
  });
}

const base = {
  sessionId: "sess-1",
  runId: "run-1",
};

describe("WorkflowFeedbackCard", () => {
  it("open path shows muted marker only — no options, textarea, or remaining", () => {
    const meta: WorkflowFeedbackMeta = {
		phase_id: "pick",
		prompt: "Pick one",
		response_type: "single_choice",
		options: ["red", "blue"],
	};
    const { getByTestId, queryByTestId } = render(() => (
      <WorkflowFeedbackCard meta={meta} client={client()} {...base} />
    ));
    expect(getByTestId("workflow-feedback-open-marker").textContent).toContain(
      "Waiting for your answer in the composer…",
    );
    expect(queryByTestId("workflow-feedback-card")).toBeNull();
    expect(queryByTestId("workflow-feedback-option")).toBeNull();
    expect(queryByTestId("workflow-feedback-other")).toBeNull();
    expect(queryByTestId("workflow-feedback-text")).toBeNull();
    expect(queryByTestId("workflow-feedback-remaining")).toBeNull();
    expect(queryByTestId("workflow-feedback-submit")).toBeNull();
  });

  it("collapses resolved asks into a chicklet", () => {
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick one",
      response_type: "single_choice",
      options: ["red", "blue"],
      answer: "blue",
    };
    const { getByTestId, queryByTestId } = render(() => (
      <WorkflowFeedbackCard meta={meta} client={client()} {...base} />
    ));
    expect(getByTestId("workflow-feedback-chicklet")).toBeTruthy();
    expect(queryByTestId("workflow-feedback-open-marker")).toBeNull();
  });

  it("closes superseded unanswered cards when another phase is pending", () => {
    const meta: WorkflowFeedbackMeta = {
      phase_id: "old",
      prompt: "Old?",
      response_type: "text",
    };
    const { getByTestId, queryByTestId } = render(() => (
      <WorkflowFeedbackCard
        meta={meta}
        client={client()}
        {...base}
        activePendingPhaseId="new"
      />
    ));
    expect(queryByTestId("workflow-feedback-open-marker")).toBeNull();
    expect(getByTestId("workflow-feedback-chicklet")).toBeTruthy();
  });
});
