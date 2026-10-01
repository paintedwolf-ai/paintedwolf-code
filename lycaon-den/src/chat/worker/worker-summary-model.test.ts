import { describe, expect, it } from "vitest";
import type { Message, WorkerSummaryMeta, WorkerTask } from "../../api/types.ts";
import {
  workerSummaryTaskStatus,
} from "./worker-summary-model.ts";
import {
  buildWorkerEvidenceFallbackView,
  resolveCitationGrounding,
  workerSummaryMetaForJob,
} from "./worker-evidence-model.ts";

const worker: WorkerTask = {
  id: "job-1",
  agent_type: "implementer",
  status: "complete",
  brief: "Create a Python text adventure game",
  created_at: "2026-01-01T00:00:00Z",
};

function summaryMeta(
  overrides: Partial<WorkerSummaryMeta> & Pick<WorkerSummaryMeta, "status">,
): WorkerSummaryMeta {
  return {
    worker_id: "job-1",
    child_session_id: "child-1",
    agent_type: "implementer",
    envelope:
      '<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="complete"></task>',
    ...overrides,
  };
}

const summaryMessage: Message = {
  id: "m1",
  role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
  content: "**adventure.py** created with main loop",
  worker_summary: {
    worker_id: "job-1",
    child_session_id: "child-1",
    agent_type: "implementer",
    status: "complete",
    envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="complete"></task>',
  },
  created_at: "2026-01-01T00:00:00Z",
};

describe("worker-summary-model", () => {
  it("maps worker summary status to task-card status", () => {
    expect(workerSummaryTaskStatus(summaryMessage.worker_summary, worker)).toBe(
      "done",
    );
    expect(workerSummaryTaskStatus(summaryMeta({ status: "failed" }))).toBe("error");
    expect(workerSummaryTaskStatus(summaryMeta({ status: "partial" }))).toBe("partial");
    expect(workerSummaryTaskStatus(summaryMeta({ status: "open" }))).toBe("open");
  });

  it("finds worker summary meta by job id", () => {
    expect(
      workerSummaryMetaForJob([summaryMessage], "job-1")?.status,
    ).toBe("complete");
  });
});

describe("resolveCitationGrounding", () => {
  it("requires grounding.traced on the wire", () => {
    const traced = (meta?: WorkerSummaryMeta, row?: WorkerTask) =>
      resolveCitationGrounding(meta, row)?.traced === true;
    expect(
      traced(summaryMessage.worker_summary, worker),
    ).toBe(false);
    expect(
      traced(
        summaryMeta({
          status: "complete",
          grounding: { traced: true, checks: [] },
        }),
        worker,
      ),
    ).toBe(true);
    expect(
      traced(
        summaryMeta({
          status: "complete",
          grounding: { traced: false, hint_code: "WORKER_EVIDENCE_HANDLE_UNKNOWN", checks: [] },
        }),
        worker,
      ),
    ).toBe(false);
    expect(
      traced(undefined, {
        ...worker,
        result: { status: "complete" },
      }),
    ).toBe(false);
  });
});

describe("buildWorkerEvidenceFallbackView without grounding", () => {
  it("shows unavailable audit for terminal workers without grounding", () => {
    const view = buildWorkerEvidenceFallbackView(
      summaryMessage.worker_summary,
      worker,
    );
    expect(view?.outcome).toBe("pending");
    expect(view?.headline).toBe("Verification audit unavailable");
  });
});
