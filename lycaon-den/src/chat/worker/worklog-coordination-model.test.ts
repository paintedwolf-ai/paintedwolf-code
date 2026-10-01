import { describe, expect, it } from "vitest";
import { reservationsByWorker } from "./worklog-coordination-model.ts";
import type { BoardView } from "../../api/types.ts";

function boardFixture(
  roster: BoardView["roster"],
): BoardView {
  return {
    summary: "ok",
    repo: { languages: [], file_count: 0, generated_at: "2026-01-01T00:00:00Z" },
    roster,
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
  it("folds roster reservations by worker", () => {
    const groups = reservationsByWorker(
      boardFixture([
        {
          worker_id: "job-a",
          agent_type: "implementer",
          status: "running",
          reservations: ["pkg/a.go", "pkg/b.go"],
        },
        { worker_id: "job-b", agent_type: "implementer", status: "running" },
      ]),
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]?.paths).toEqual(["pkg/a.go", "pkg/b.go"]);
  });
});
