import { describe, expect, it } from "vitest";
import { LycaonApiError } from "../api/http.ts";
import {
  composeFeedbackSubmitAnswer,
  isFeedbackNoLongerPendingError,
  workflowFeedbackCardHint,
  workflowFeedbackCardLabel,
  workflowFeedbackChickletDetail,
  workflowFeedbackCardView,
} from "./workflow-feedback-card-model.ts";

describe("workflowFeedbackCardView", () => {
  it("stays open while unanswered and latch unknown", () => {
    expect(workflowFeedbackCardView({ phase_id: "ask-1" })).toEqual({ kind: "open" });
  });

  it("stays open while unanswered and this phase is pending", () => {
    expect(
      workflowFeedbackCardView({ phase_id: "ask-1", answer: "" }, "ask-1"),
    ).toEqual({ kind: "open" });
  });

  it("stays open while unanswered and latch is empty (wait for stamp)", () => {
    expect(workflowFeedbackCardView({ phase_id: "ask-1" }, null)).toEqual({
      kind: "open",
    });
  });

  it("closes when another phase is the live pending ask", () => {
    expect(workflowFeedbackCardView({ phase_id: "ask-1" }, "ask-2")).toEqual({
      kind: "closed",
    });
  });

  it("prefers a stamped user answer over a newer pending question", () => {
    expect(
      workflowFeedbackCardView(
        { phase_id: "ask-1", answer: "REST", resolved_by: "user" },
        "ask-2",
      ),
    ).toEqual({ kind: "answered", resolvedBy: "user" });
  });
});

describe("workflowFeedbackCardLabel / hint", () => {
  it("labels closed cards as no longer needed", () => {
    const view = { kind: "closed" as const };
    expect(workflowFeedbackCardLabel(view)).toBe("No longer needed");
    expect(workflowFeedbackCardHint(view)).toMatch(/moved on/i);
  });
});

describe("workflowFeedbackChickletDetail", () => {
  it("prefers the answer over the prompt for the chicklet summary", () => {
    expect(workflowFeedbackChickletDetail("Pick one", "blue")).toBe("blue");
    expect(workflowFeedbackChickletDetail("Old question?", "")).toBe("Old question?");
    expect(workflowFeedbackChickletDetail("  ", "  ")).toBeUndefined();
  });
});

describe("isFeedbackNoLongerPendingError", () => {
  it("branches on structured LycaonApiError codes only", () => {
    expect(
      isFeedbackNoLongerPendingError(new LycaonApiError("gone", 409, "feedback_not_pending")),
    ).toBe(true);
    expect(
      isFeedbackNoLongerPendingError(new LycaonApiError("gone", 409, "decision_not_pending")),
    ).toBe(true);
    expect(
      isFeedbackNoLongerPendingError(new LycaonApiError("ghost", 409, "not_choice_phase")),
    ).toBe(true);
    expect(
      isFeedbackNoLongerPendingError(new Error("feedback_not_pending")),
    ).toBe(false);
    expect(
      isFeedbackNoLongerPendingError(new LycaonApiError("other", 409, "internal_error")),
    ).toBe(false);
  });
});

describe("composeFeedbackSubmitAnswer", () => {
  it("joins selected choices", () => {
    expect(composeFeedbackSubmitAnswer("text", "hello", [])).toBe("hello");
    expect(composeFeedbackSubmitAnswer("single_choice", "", ["B"])).toBe("B");
    expect(composeFeedbackSubmitAnswer("multi_choice", "", ["a", "b"])).toBe("a, b");
  });
});
