import { describe, expect, it } from "vitest";
import type {
  Message,
  PendingFeedback,
  WorkflowFeedbackMeta,
  WorkflowRun,
} from "../api/types.ts";
import {
  askUserComposerPlaceholder,
  askUserDockMeta,
  pendingAskFeedbackEntry,
  pendingWorkflowFeedback,
  retainAskDockMeta,
  workflowFeedbackComposerPlaceholder,
} from "./workflow-feedback-model.ts";

function run(overrides: Partial<WorkflowRun>): WorkflowRun {
  return {
    id: "run-1",
    session_id: "sess-1",
    workflow_id: "options",
    workflow_version: "1",
    status: "running",
    current_phase: "intake",
    created_at: "2026-06-30T00:00:00Z",
    updated_at: "2026-06-30T00:00:00Z",
    ...overrides,
	revision: overrides.revision ?? 1,
  };
}

describe("pendingWorkflowFeedback", () => {
  it("returns the feedback when a running run awaits an answer", () => {
    const r = run({
      ui: { current_phase_label: "Waiting for input", pending_feedback: { phase_id: "intake", prompt: "What now?" } },
    });
    expect(pendingWorkflowFeedback(r)).toEqual({
      phase_id: "intake",
      prompt: "What now?",
    });
  });

  it("ignores non-running runs", () => {
    const r = run({
      status: "paused",
      ui: { current_phase_label: "Waiting for input", pending_feedback: { phase_id: "intake", prompt: "What now?" } },
    });
    expect(pendingWorkflowFeedback(r)).toBeUndefined();
  });

  it("ignores blank or absent prompts", () => {
    expect(pendingWorkflowFeedback(run({ ui: { current_phase_label: "Working" } }))).toBeUndefined();
    expect(
      pendingWorkflowFeedback(
        run({ ui: { current_phase_label: "Waiting for input", pending_feedback: { phase_id: "intake", prompt: "  " } } }),
      ),
    ).toBeUndefined();
  });

  it("handles null/undefined runs", () => {
    expect(pendingWorkflowFeedback(null)).toBeUndefined();
    expect(pendingWorkflowFeedback(undefined)).toBeUndefined();
  });
});

describe("askUserComposerPlaceholder", () => {
  it("uses Type your answer for text / no options", () => {
    expect(askUserComposerPlaceholder({ response_type: "text" })).toBe("Type your answer…");
    expect(askUserComposerPlaceholder({ response_type: "single_choice", options: [] })).toBe(
      "Type your answer…",
    );
  });

  it("uses Describe another answer when options are present", () => {
    expect(
      askUserComposerPlaceholder({
        response_type: "single_choice",
        options: ["a", "b"],
      }),
    ).toBe("Describe another answer…");
  });
});

describe("workflowFeedbackComposerPlaceholder", () => {
  it("signposts the composer only while feedback is pending", () => {
    const r = run({
      ui: { current_phase_label: "Waiting for input", pending_feedback: { phase_id: "intake", prompt: "What now?" } },
    });
    expect(workflowFeedbackComposerPlaceholder(r)).toBe("Type your answer…");
    expect(
      workflowFeedbackComposerPlaceholder(r, {
        response_type: "single_choice",
        options: ["a"],
      }),
    ).toBe("Describe another answer…");
    expect(workflowFeedbackComposerPlaceholder(run({ ui: { current_phase_label: "Working" } }))).toBeUndefined();
  });
});

describe("pendingAskFeedbackEntry", () => {
  it("finds the latest unanswered message for the phase", () => {
    const messages: Message[] = [
      {
        id: "m1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_feedback",
        content: "old",
        workflow_run_id: "run-1",
        workflow_feedback: {
          phase_id: "ask-1",
          prompt: "old",
          response_type: "text",
          answer: "done",
        },
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "m2",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_feedback",
        content: "live",
        workflow_run_id: "run-1",
        workflow_feedback: {
          phase_id: "ask-1",
          prompt: "live",
          response_type: "single_choice",
          options: ["a", "b"],
        },
        created_at: "2026-01-01T00:01:00Z",
      },
    ];
    expect(pendingAskFeedbackEntry(messages, "ask-1")).toEqual({
      meta: messages[1]!.workflow_feedback,
      entryKey: "m2",
      runId: "run-1",
    });
  });
});

describe("askUserDockMeta", () => {
  it("prefers transcript meta when phase matches", () => {
    const pending = { phase_id: "ask-1", prompt: "from latch" };
    const entry = {
      phase_id: "ask-1",
      prompt: "from card",
      response_type: "single_choice" as const,
      options: ["a"],
    };
    expect(askUserDockMeta(pending, entry)).toEqual(entry);
  });

  it("falls back to text meta from the latch prompt", () => {
    expect(askUserDockMeta({ phase_id: "ask-1", prompt: "Scope?" })).toEqual({
      phase_id: "ask-1",
      prompt: "Scope?",
      response_type: "text",
    });
  });
});

describe("retainAskDockMeta", () => {
  const pending: PendingFeedback = {
    phase_id: "intake",
    prompt: "Pick one",
    response_type: "single_choice",
    options: ["red", "blue"],
  };
  const entryMeta: WorkflowFeedbackMeta = {
    phase_id: "intake",
    prompt: "Pick one",
    response_type: "single_choice",
    options: ["red", "blue"],
  };

  it("keeps the held object when the transcript row drops out from under it", () => {
    const first = askUserDockMeta(pending, entryMeta);
    const rebuilt = askUserDockMeta(pending, undefined);
    expect(rebuilt).not.toBe(first);

    const held = retainAskDockMeta(undefined, first, "intake");
    expect(held).toBe(first);
    expect(retainAskDockMeta(held, rebuilt, "intake")).toBe(first);
  });

  it("keeps the held object when the row returns", () => {
    const held = retainAskDockMeta(
      undefined,
      askUserDockMeta(pending, undefined),
      "intake",
    );
    expect(retainAskDockMeta(held, askUserDockMeta(pending, entryMeta), "intake")).toBe(
      held,
    );
  });

  it("adopts a genuinely changed question for the same phase", () => {
    const held = retainAskDockMeta(undefined, entryMeta, "intake");
    const changed: WorkflowFeedbackMeta = { ...entryMeta, options: ["red", "green"] };
    expect(retainAskDockMeta(held, changed, "intake")).toBe(changed);
  });

  it("drops everything when the phase changes or the dock cannot mount", () => {
    const held = retainAskDockMeta(undefined, entryMeta, "intake");
    const next: WorkflowFeedbackMeta = { ...entryMeta, phase_id: "review" };
    expect(retainAskDockMeta(held, next, "review")).toBe(next);
    expect(retainAskDockMeta(held, next, null)).toBeUndefined();
    // A phase with no row yet keeps nothing from the previous phase.
    expect(retainAskDockMeta(held, undefined, "review")).toBeUndefined();
  });
});
