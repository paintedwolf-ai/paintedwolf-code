import { describe, expect, it } from "vitest";
import type { WorkerTask } from "../api/types.ts";
import { reconcileWorkersFromBoard } from "./workers-board-sync.ts";

const baseBoard = {
  summary: "All workers complete.",
  repo: { languages: [], file_count: 0, generated_at: "t" },
  cost: null,
  pack_content_hash: "h",
  detail_level: "compact" as const,
  board: "",
  board_chars: 0,
  truncated: false,
  generated_at: "t",
  now_line: "Now: t",
};

describe("reconcileWorkersFromBoard", () => {
  const row = (id: string, extra: Partial<WorkerTask> = {}): WorkerTask => ({
    id,
    agent_type: "implementer",
    status: "complete",
    created_at: "t",
    ...extra,
  });

  it("applies the worker's identity to every round and its latest result to the latest round", () => {
    const workers = [row("job-1", { worker_record_id: "rec-1" }), row("job-2", { worker_record_id: "rec-1" })];
    const next = reconcileWorkersFromBoard(workers, {
      ...baseBoard,
      workers: [
        {
          worker_id: "rec-1",
          name: "parser",
          agent_type: "implementer",
          state: "delivered",
          child_session_id: "child-1",
          latest_job_id: "job-2",
          round: 2,
          result_handle: "parser#2",
          result_disposition: "landed",
        },
      ],
    });
    expect(next[0]).toMatchObject({ worker_name: "parser", child_session_id: "child-1" });
    expect(next[0]?.result_handle).toBeUndefined();
    expect(next[1]).toMatchObject({
      worker_name: "parser",
      round: 2,
      result_handle: "parser#2",
      result_disposition: "landed",
    });
  });

  it("returns the same workers array when board reconcile is a no-op", () => {
    const workers = [row("job-1", { worker_name: "parser", round: 1 })];
    const next = reconcileWorkersFromBoard(workers, {
      ...baseBoard,
      workers: [{ name: "parser", agent_type: "implementer", state: "idle", latest_job_id: "job-1", round: 1 }],
    });
    expect(next).toBe(workers);
  });

  it("returns the same workers array when the board has no workers", () => {
    const workers = [row("job-1", { status: "running" })];
    expect(reconcileWorkersFromBoard(workers, baseBoard)).toBe(workers);
  });
});
