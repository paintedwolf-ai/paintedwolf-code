import type {
  BoardEvent,
  EventEnvelope,
  BoardView,
  CoordinatorRunContext,
  FindingsDigest,
  ProgressDigest,
  WorkerTask,
} from "../src/api/types.ts";

export const COORDINATION_JOB_A = "job-a";
export const COORDINATION_JOB_B = "job-b";

export const coordinationProgressFixture = {
  steps: [
    {
      state: "pending",
      label: "Ship auth integration",
    },
  ],
  revision: 1,
} satisfies ProgressDigest;

export const coordinationFindingsFixture = {
  findings: [
    {
      agent: COORDINATION_JOB_A,
      summary: "Posted note",
      ref: "mine.go",
      recorded_at: "2026-01-02T12:00:00Z",
    },
    {
      agent: COORDINATION_JOB_B,
      summary: "Sibling note",
      ref: "peer.go",
      recorded_at: "2026-01-02T12:30:00Z",
    },
  ],
  revision: 1,
} satisfies FindingsDigest;

export const coordinationCoordinatorContextFixture = {
  batch_phase: "integrate",
  batch_seq: 2,
} satisfies CoordinatorRunContext;

export const coordinationBoardFixture = {
  summary: "ok",
  repo: {
    languages: [],
    file_count: 0,
    generated_at: "2026-01-02T00:00:00Z",
  },
  roster: [
    {
      worker_id: COORDINATION_JOB_A,
      agent_type: "implementer",
      status: "running",
      reservations: ["pkg/a.go"],
      max_tool_loops: 40,
      tool_loops_used: 34,
    },
    {
      worker_id: COORDINATION_JOB_B,
      agent_type: "implementer",
      status: "running",
    },
  ],
  cost: null,
  pack_content_hash: "e2e-coordination",
  detail_level: "compact",
  board: "",
  board_chars: 0,
  truncated: false,
  generated_at: "2026-01-02T12:00:00Z",
  now_line: "now",
} satisfies BoardView;

export function coordinationWorkersFixture(sessionId: string): WorkerTask[] {
  return [
    {
      id: COORDINATION_JOB_A,
      parent_session_id: sessionId,
      agent_type: "implementer",
      status: "running",
      brief: "Fix auth handler",
      created_at: "2026-01-02T11:00:00Z",
      max_tool_loops: 40,
      tool_loops_used: 34,
      tool_calls_used: 128,
      // Partial tool completion exercises the batch progress bar.
      turn_tool_calls: 4,
      turn_tools_done: 1,
    },
    {
      id: COORDINATION_JOB_B,
      parent_session_id: sessionId,
      agent_type: "implementer",
      status: "running",
      brief: "Wire reservations",
      created_at: "2026-01-02T11:05:00Z",
    },
  ];
}

export function coordinationSseStream(projectId: string): string {
  const boardEnvelope = {
    v: 1,
    topic: "board",
    event_id: "00000000-0000-4000-8000-000000000002",
    cursor: "coordination-board-cursor",
    published_at: "2026-01-02T12:00:00Z",
    scope: { kind: "project", project_id: projectId },
    data: {
      project_id: projectId,
      snapshot: coordinationBoardFixture,
      detail_level: "compact",
    } satisfies BoardEvent,
  } satisfies EventEnvelope;
  return `: connected\n\ndata: ${JSON.stringify(boardEnvelope)}\n\n: ping\n\n`;
}
