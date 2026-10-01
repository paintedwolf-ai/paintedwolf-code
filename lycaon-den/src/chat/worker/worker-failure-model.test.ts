import { describe, expect, it } from "vitest";
import type { WorkerTask } from "../../api/types.ts";
import {
  workerFailureDisplay,
  workerFailureFromDispatchReject,
  workerFailureStatusLines,
} from "./worker-failure-model.ts";

describe("workerFailureDisplay", () => {
  it("renders structured closeout exhausted copy from wire failure", () => {
    const worker: WorkerTask = {
      id: "job-1",
      agent_type: "code-reviewer",
      status: "failed" as const,
      created_at: "2026-01-01T00:00:00Z",
      failure: {
        code: "worker_closeout_exhausted",
        title: "Worker could not finish",
        message:
          "The worker used every tool turn and both closing attempts, but the model did not finish its summary before the time limit.",
        suggested_action: "Retry with a narrower task() brief.",
      },
      error: "llm turn timed out: context deadline exceeded",
    };
    const display = workerFailureDisplay(worker);
    expect(display?.headline).toBe("Worker could not finish");
    expect(display?.message).toContain("closing attempts");
    expect(workerFailureStatusLines(display)).toEqual([
      display!.message,
      "Retry with a narrower task() brief.",
    ]);
  });

  it("falls back to error string when failure is absent", () => {
    const display = workerFailureDisplay({
      id: "job-2",
      agent_type: "path-explorer",
      status: "failed" as const,
      created_at: "2026-01-01T00:00:00Z",
      error: "context canceled",
    });
    expect(display?.headline).toBe("Worker failed");
    expect(display?.message).toBe("context canceled");
  });

  it("maps dispatch rejects to structured failure copy", () => {
    const failure = workerFailureFromDispatchReject("PROGRESS_MISSING");
    expect(failure.code).toBe("PROGRESS_MISSING");
    expect(failure.title).toBe("Worker dispatch rejected");
    expect(failure.message).toBe("The coordinator called a worker with arguments the host rejected.");
  });
});
