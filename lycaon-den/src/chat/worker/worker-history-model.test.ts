import { describe, expect, it } from "vitest";
import {
  mergeWorkerHistoryMessages,
  mergeWorkerHistoryRounds,
  workerHistorySectionId,
  workerHistorySenderLabel,
  workerInstructionStatusLabel,
  workerRoundDisposition,
} from "./worker-history-model.ts";

describe("worker-history-model", () => {
  it("formats sender roles, instruction statuses, section ids, and round dispositions", () => {
    expect(workerHistorySectionId("job-1")).toBe("worker-history-job-1");
    expect(workerHistorySenderLabel("coordinator")).toBe("Coordinator");
    expect(workerHistorySenderLabel("worker")).toBe("Worker");
    expect(workerHistorySenderLabel("person")).toBe("You");
    expect(workerHistorySenderLabel("host")).toBe("Host");
    expect(workerInstructionStatusLabel("applied")).toBe("Applied");
    expect(workerInstructionStatusLabel("queued")).toBe("Queued");
    expect(workerInstructionStatusLabel("not_applied")).toBe("Not applied");

    expect(
      workerRoundDisposition(
        {
          ordinal: 1,
          worker_id: "job-1",
          result_disposition: "shelved",
          shelve_reason: "hold for release",
          created_at: "2026-11-15T01:00:00Z",
        },
        "implementer",
      ),
    ).toEqual({
      disposition: "shelved",
      label: "Shelved · hold for release",
      shelveReason: "hold for release",
    });
  });

  it("merges paged messages and rounds in ascending order without duplicates", () => {
    const mergedMessages = mergeWorkerHistoryMessages(
      [
        {
          seq: 3,
          sender_role: "coordinator",
          body: "updated",
          delivered: true,
          instruction_status: "queued",
          created_at: "2026-11-15T01:03:00Z",
        },
      ],
      [
        {
          seq: 1,
          sender_role: "worker",
          body: "question",
          delivered: true,
          needs_reply: true,
          created_at: "2026-11-15T01:01:00Z",
        },
        {
          seq: 3,
          sender_role: "coordinator",
          body: "updated",
          delivered: true,
          instruction_status: "applied",
          created_at: "2026-11-15T01:03:00Z",
        },
      ],
    );
    expect(mergedMessages.map((m) => [m.seq, m.instruction_status])).toEqual([
      [1, undefined],
      [3, "applied"],
    ]);

    const mergedRounds = mergeWorkerHistoryRounds(
      [{ ordinal: 2, worker_id: "job-2", created_at: "2026-11-15T01:05:00Z" }],
      [
        { ordinal: 1, worker_id: "job-1", created_at: "2026-11-15T01:00:00Z" },
        {
          ordinal: 2,
          worker_id: "job-2",
          result_disposition: "landed",
          created_at: "2026-11-15T01:05:00Z",
        },
      ],
    );
    expect(mergedRounds.map((r) => [r.ordinal, r.result_disposition])).toEqual([
      [1, undefined],
      [2, "landed"],
    ]);
  });
});
