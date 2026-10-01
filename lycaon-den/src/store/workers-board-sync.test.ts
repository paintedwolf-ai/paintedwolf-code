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
  it("patches merge_status on a terminal merge", () => {
    const workers: WorkerTask[] = [
      {
        id: "job-1",
        agent_type: "implementer",
        status: "complete",
        merge_status: "pending",
        created_at: "t",
      },
    ];
    const next = reconcileWorkersFromBoard(workers, {
      ...baseBoard,
      roster: [
        {
          worker_id: "job-1",
          agent_type: "implementer",
          status: "complete",
          merge_status: "merged",
        },
      ],
    });
    expect(next[0]?.merge_status).toBe("merged");
  });

  it("patches merge_status when overlay is rejected", () => {
    const workers: WorkerTask[] = [
      {
        id: "job-1",
        agent_type: "implementer",
        status: "complete",
        merge_status: "pending",
        created_at: "t",
      },
    ];
    const next = reconcileWorkersFromBoard(workers, {
      ...baseBoard,
      roster: [
        {
          worker_id: "job-1",
          agent_type: "implementer",
          status: "complete",
          merge_status: "rejected",
        },
      ],
    });
    expect(next[0]?.merge_status).toBe("rejected");
  });

  it("returns the same workers array when board reconcile is a no-op", () => {
    const workers: WorkerTask[] = [
      {
        id: "job-1",
        agent_type: "implementer",
        status: "complete",
        merge_status: "pending",
        created_at: "t",
      },
    ];
    const next = reconcileWorkersFromBoard(workers, {
      ...baseBoard,
      roster: [
        {
          worker_id: "job-1",
          agent_type: "implementer",
          status: "complete",
          merge_status: "pending",
        },
      ],
    });
    expect(next).toBe(workers);
  });

  it("refuses to regress a terminal status back to running from a stale board snapshot", () => {
    // Ignore stale snapshots that would regress a terminal worker.
    const workers: WorkerTask[] = [
      {
        id: "job-1",
        agent_type: "web-researcher",
        status: "complete",
        created_at: "t",
      },
    ];
    const next = reconcileWorkersFromBoard(workers, {
      ...baseBoard,
      roster: [
        {
          worker_id: "job-1",
          agent_type: "web-researcher",
          status: "running",
        },
      ],
    });
    expect(next[0]?.status).toBe("complete");
  });

  it("still applies a forward transition into a terminal status", () => {
    const workers: WorkerTask[] = [
      {
        id: "job-1",
        agent_type: "web-researcher",
        status: "running",
        created_at: "t",
      },
    ];
    const next = reconcileWorkersFromBoard(workers, {
      ...baseBoard,
      roster: [
        {
          worker_id: "job-1",
          agent_type: "web-researcher",
          status: "complete",
        },
      ],
    });
    expect(next[0]?.status).toBe("complete");
  });

  it("returns the same workers array when board snapshot has no roster", () => {
    const workers: WorkerTask[] = [
      {
        id: "job-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ];
    const next = reconcileWorkersFromBoard(workers, baseBoard);
    expect(next).toBe(workers);
  });
});
