import { describe, expect, it } from "vitest";
import { reservationsByWorker } from "./worklog-coordination-model.ts";
import type { BoardView } from "../../api/types.ts";

function boardFixture(workers: BoardView["workers"]): BoardView {
  return {
    summary: "ok",
    repo: { languages: [], file_count: 0, generated_at: "2026-01-01T00:00:00Z" },
    workers,
    cost: null,
    pack_content_hash: "hash",
    detail_level: "compact",
    board: "",
    board_chars: 0,
    truncated: false,
    generated_at: "2026-01-01T00:00:00Z",
    now_line: "now",
  };
}

describe("worklog-coordination-model", () => {
  it("groups reserved paths by the worker holding them", () => {
    const groups = reservationsByWorker(
      boardFixture([
        {
          name: "parser",
          agent_type: "implementer",
          state: "working",
          latest_job_id: "job-a",
          reservations: ["pkg/a.go", "pkg/b.go"],
        },
        { name: "lexer", agent_type: "implementer", state: "working", latest_job_id: "job-b" },
      ]),
    );
    expect(groups).toEqual([
      { jobId: "job-a", worker: "parser", paths: ["pkg/a.go", "pkg/b.go"] },
    ]);
    expect(reservationsByWorker(boardFixture(undefined))).toEqual([]);
  });
});
