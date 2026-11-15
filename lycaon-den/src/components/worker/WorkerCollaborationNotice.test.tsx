import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { BoardView, WorkerTask } from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import {
  WorkerCollaborationNotice,
  collaborationNoticeForToolPart,
} from "./WorkerCollaborationNotice.tsx";

function basePart(overrides: Partial<ToolPartView>): ToolPartView {
  return {
    id: "a1:tc1",
    toolCallId: "tc1",
    assistantMessageId: "a1",
    messageId: "tr1",
    tool: "instruct_worker",
    kind: "generic",
    status: "completed",
    ...overrides,
  };
}

const sampleWorker: WorkerTask = {
  id: "job-2",
  worker_record_id: "rec-1",
  worker_name: "auth-impl",
  round: 2,
  child_session_id: "child-1",
  parent_session_id: "sess-1",
  agent_type: "implementer",
  status: "running",
  created_at: "2026-11-15T01:00:00Z",
};

const sampleBoard: BoardView = {
  session_id: "sess-1",
  roster: [],
  workers: [
    {
      worker_id: "rec-1",
      name: "auth-impl",
      agent_type: "implementer",
      state: "asking",
      round: 2,
      question: "Should refresh rotate signing keys?",
      unread_message_count: 1,
    },
  ],
};

describe("WorkerCollaborationNotice", () => {
  it("projects applied, queued, rejected, and asking instruct_worker receipts", () => {
    const applied = collaborationNoticeForToolPart(
      basePart({
        args: { worker: "auth-impl", instructions: "Cover expired refresh tokens." },
        output: JSON.stringify({
          worker: "auth-impl",
          outcome: "started",
          round: 2,
          instruction_seq: 4,
        }),
      }),
      { sessionWorkers: [sampleWorker] },
    );
    expect(applied).toEqual({
      kind: "instruction",
      workerLabel: "auth-impl",
      targetWorkerId: "job-2",
      headline: "Sent instruction to auth-impl · Round 2",
      detail: "Cover expired refresh tokens.",
      round: 2,
      messageSeq: 4,
      instructionStatus: "applied",
    });

    const queued = collaborationNoticeForToolPart(
      basePart({
        args: { worker: "auth-impl", instructions: "Also run integration tests." },
        output: JSON.stringify({
          worker: "auth-impl",
          outcome: "queued",
          instruction_seq: 5,
        }),
      }),
      { sessionWorkers: [sampleWorker] },
    );
    expect(queued?.instructionStatus).toBe("queued");
    expect(queued?.headline).toBe("Queued instruction for auth-impl");

    const rejected = collaborationNoticeForToolPart(
      basePart({
        status: "error",
        outcome: "rejected",
        args: { worker: "ghost", instructions: "Do work" },
        error: "Unknown worker ghost",
      }),
      { sessionWorkers: [sampleWorker] },
    );
    expect(rejected?.instructionStatus).toBe("not_applied");
    expect(rejected?.headline).toBe("Instruction to ghost was not applied");

    const asking = collaborationNoticeForToolPart(
      basePart({
        args: { worker: "auth-impl", instructions: "Proceed" },
        workerSummary: {
          worker_id: "job-2",
          worker_record_id: "rec-1",
          worker_name: "auth-impl",
          round: 2,
          agent_type: "implementer",
          status: "needs_decision",
          envelope: "<task />",
        },
      }),
      { sessionWorkers: [sampleWorker], board: sampleBoard },
    );
    expect(asking?.kind).toBe("question");
    expect(asking?.headline).toBe("auth-impl needs an answer");
    expect(asking?.detail).toBe("Should refresh rotate signing keys?");
  });

  it("projects answer_decision and repeat task rounds while leaving primary task cards untouched", () => {
    const answer = collaborationNoticeForToolPart(
      basePart({
        tool: "answer_decision",
        args: { job_id: "job-2", option: "Rotate keys on refresh" },
      }),
      { sessionWorkers: [sampleWorker] },
    );
    expect(answer).toMatchObject({
      kind: "answer",
      workerLabel: "auth-impl",
      targetWorkerId: "job-2",
      headline: "Answered auth-impl",
      detail: "Rotate keys on refresh",
    });

    const primaryTask = basePart({
      tool: "task",
      kind: "task",
      jobId: "job-2",
    });
    expect(
      collaborationNoticeForToolPart(primaryTask, {
        matchedWorker: sampleWorker,
        sessionWorkers: [sampleWorker],
      }),
    ).toBeNull();

    const repeatTask = collaborationNoticeForToolPart(
      {
        ...primaryTask,
        workerSummary: {
          worker_id: "job-2",
          worker_record_id: "rec-1",
          worker_name: "auth-impl",
          round: 2,
          agent_type: "implementer",
          status: "complete",
          envelope: "<task />",
        },
      },
      { sessionWorkers: [sampleWorker] },
    );
    expect(repeatTask).toMatchObject({
      kind: "round",
      targetWorkerId: "job-2",
      headline: "auth-impl · Round 2",
      round: 2,
    });
  });

  it("opens worker history at the targeted message sequence on click", () => {
    const onOpenWorker = vi.fn();
    render(() => (
      <WorkerCollaborationNotice
        notice={{
          kind: "instruction",
          workerLabel: "auth-impl",
          targetWorkerId: "job-2",
          headline: "Sent instruction to auth-impl · Round 2",
          detail: "Add key rotation check",
          round: 2,
          messageSeq: 7,
          instructionStatus: "applied",
        }}
        entryKey="a9:tc9"
        sessionId="sess-1"
        onOpenWorker={onOpenWorker}
      />
    ));

    const btn = screen.getByTestId("worker-collaboration-notice");
    expect(btn.tagName).toBe("BUTTON");
    expect(screen.getByTestId("worker-instruction-status-badge").textContent).toBe("Applied");
    fireEvent.click(btn);
    expect(onOpenWorker).toHaveBeenCalledWith("job-2", {
      scrollTo: "history",
      messageSeq: 7,
      roundOrdinal: 2,
    });
  });
});
