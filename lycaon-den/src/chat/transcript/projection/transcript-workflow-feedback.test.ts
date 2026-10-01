import { describe, expect, it } from "vitest";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";

describe("transcript-items", () => {

  it("maps a workflow_feedback message to an interactive question item in order", () => {
    const messages = [
      {
        id: "q1",
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_feedback" as const,
        content: "Pick one",
        workflow_run_id: "run-1",
        workflow_feedback: {
          phase_id: "intake",
          prompt: "Pick one",
          response_type: "single_choice" as const,
          options: ["a", "b"],
        },
        created_at: "t1",
      },
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "done", created_at: "t2" },
    ];
    const items = messagesToTranscriptItems(messages);
    expect(items).toEqual([
      {
        kind: "workflow_feedback",
        key: "q1",
        runId: "run-1",
        meta: {
          phase_id: "intake",
          prompt: "Pick one",
          response_type: "single_choice",
          options: ["a", "b"],
        },
      },
      { kind: "user", key: "u1", text: "done" },
    ]);
  });

  it("maps synthetic ask_user phase_id workflow_feedback to an interactive item", () => {
    const items = messagesToTranscriptItems([
      {
        id: "q-ask",
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_feedback" as const,
        content: "Scope?",
        workflow_run_id: "run-1",
        workflow_feedback: {
          phase_id: "ask-test-1",
          prompt: "Scope?",
          response_type: "text" as const,
        },
        created_at: "t1",
      },
    ]);
    expect(items).toEqual([
      {
        kind: "workflow_feedback",
        key: "q-ask",
        runId: "run-1",
        meta: {
          phase_id: "ask-test-1",
          prompt: "Scope?",
          response_type: "text",
        },
      },
    ]);
  });

  it("maps ask_user visual review meta with artifact_id", () => {
    const items = messagesToTranscriptItems([
      {
        id: "q-vis",
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_feedback" as const,
        content: "Approve this layout?",
        workflow_run_id: "run-1",
        workflow_feedback: {
          phase_id: "ask-vis-1",
          prompt: "Approve this layout?",
          response_type: "single_choice" as const,
          options: ["Approve", "Reject"],
          artifact_id: "art-1",
          purpose: "review",
        },
        created_at: "t1",
      },
    ]);
    expect(items).toEqual([
      {
        kind: "workflow_feedback",
        key: "q-vis",
        runId: "run-1",
        meta: {
          phase_id: "ask-vis-1",
          prompt: "Approve this layout?",
          response_type: "single_choice",
          options: ["Approve", "Reject"],
          artifact_id: "art-1",
          purpose: "review",
        },
      },
    ]);
  });

  it("falls back to content when a workflow_feedback message lacks meta", () => {
    const items = messagesToTranscriptItems([
      {
        id: "q1",
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_feedback" as const,
        content: "What now?",
        created_at: "t1",
      },
    ]);
    expect(items).toEqual([
      {
        kind: "workflow_feedback",
        key: "q1",
        runId: undefined,
        meta: { phase_id: "", prompt: "What now?", response_type: "text" },
      },
    ]);
  });

  it("transcript projector refreshes prebuilt workflow_feedback when answer is stamped", () => {
    const prebuiltItems: ReturnType<typeof messagesToTranscriptItems> = [
      {
        kind: "workflow_feedback",
        key: "fb1",
        meta: {
          phase_id: "ask-1",
          prompt: "Which API?",
          response_type: "text",
        },
        runId: "run-1",
      },
    ];
    const messages = [
      {
        id: "fb1",
        role: "system" as const, origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        kind: "workflow_feedback" as const,
        content: "Which API?",
        workflow_run_id: "run-1",
        workflow_feedback: {
          phase_id: "ask-1",
          prompt: "Which API?",
          response_type: "text" as const,
          answer: "REST",
          resolved_by: "user",
        },
        created_at: "t",
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, [prebuiltItems])[0]!;
    const card = items.find((i) => i.kind === "workflow_feedback");
    expect(card?.kind).toBe("workflow_feedback");
    if (card?.kind === "workflow_feedback") {
      expect(card.meta.answer).toBe("REST");
      expect(card.meta.resolved_by).toBe("user");
    }
  });
});
