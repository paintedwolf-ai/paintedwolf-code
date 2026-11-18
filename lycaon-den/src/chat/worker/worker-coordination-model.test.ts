import { describe, expect, it } from "vitest";
import type { BoardView, FindingsDigest, WorkerTask } from "../../api/types.ts";
import {
  findingAgentLabel,
  findingsByWorker,
  reservationsFor,
  siblingNotesFor,
} from "./worker-coordination-model.ts";

const findings: FindingsDigest = {
  revision: 1,
  findings: [
    { agent: "job-a", summary: "Posted by A", ref: "a.go" },
    { agent: "job-b", summary: "Posted by B", ref: "b.go" },
    {
      agent: "job-c",
      summary: "Old batch note",
      ref: "old.go",
      recorded_at: "2026-01-01T00:00:00Z",
    },
    {
      agent: "job-c",
      summary: "Fresh sibling note",
      ref: "fresh.go",
      recorded_at: "2026-01-02T00:00:00Z",
    },
  ],
};

describe("worker-coordination-model", () => {
  it("findingsByWorker returns only this job's notes", () => {
    expect(findingsByWorker(findings, "job-a").map((row) => row.summary)).toEqual([
      "Posted by A",
    ]);
    expect(findingsByWorker(findings, "job-missing")).toEqual([]);
  });

  it("siblingNotesFor excludes own notes and optional pre-spawn findings", () => {
    expect(siblingNotesFor(findings, "job-a").map((row) => row.summary)).toEqual([
      "Posted by B",
      "Old batch note",
      "Fresh sibling note",
    ]);
    expect(
      siblingNotesFor(findings, "job-b", "2026-01-01T12:00:00Z").map(
        (row) => row.summary,
      ),
    ).toEqual(["Posted by A", "Fresh sibling note"]);
  });

  it("reservationsFor reads the board worker's live round reservations", () => {
    const board = {
      workers: [
        {
          name: "parser",
          agent_type: "implementer",
          state: "working",
          latest_job_id: "job-a",
          reservations: ["pkg/a.go", " "],
        },
      ],
    } as unknown as BoardView;
    const row = (id: string, name?: string): WorkerTask => ({
      id,
      agent_type: "implementer",
      status: "running",
      created_at: "t",
      worker_name: name,
    });
    expect(reservationsFor(board, row("job-a"))).toEqual(["pkg/a.go"]);
    expect(reservationsFor(board, row("job-old", "parser"))).toEqual(["pkg/a.go"]);
    expect(reservationsFor(board, row("job-b", "lexer"))).toEqual([]);
    expect(reservationsFor(undefined, row("job-a"))).toEqual([]);
  });

  it("findingAgentLabel names the author's worker, else its agent type, else a short job id", () => {
    const workers: WorkerTask[] = [
      { id: "job-a", agent_type: "code-reviewer", status: "running", created_at: "t", worker_name: "reviewer" },
      { id: "job-b", agent_type: "code-reviewer", status: "running", created_at: "t" },
    ];
    expect(findingAgentLabel(workers, "job-a")).toBe("reviewer");
    expect(findingAgentLabel(workers, "job-b")).toBe("code-reviewer");
    expect(
      findingAgentLabel(workers, "C7503D7A-EC68-4AE8-B6FC-CBDEC7350B17"),
    ).toBe("C7503D7A");
    expect(findingAgentLabel(workers, "")).toBe("");
    expect(findingAgentLabel(undefined, "short")).toBe("short");
  });
});
