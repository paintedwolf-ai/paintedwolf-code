import {
  describe,
  expect,
  it,
} from "vitest";

import { createAppStore } from "./app-state.ts";

describe("createAppStore", () => {

  it("setBoard skips workers write when board reconcile is unchanged", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        agent_type: "implementer",
        status: "complete",
        merge_status: "pending",
        created_at: "t",
      },
    ]);
    const before = store.state.workers;
    store.actions.setBoard({
      summary: "All workers complete.",
      repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: null,
      pack_content_hash: "h",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "t",
      now_line: "Now: t",
      roster: [
        {
          worker_id: "w1",
          agent_type: "implementer",
          status: "complete",
          merge_status: "pending",
        },
      ],
    });
    expect(store.state.workers).toBe(before);
  });

  it("setBoard applies active_workflow_run pending_feedback for AskUserDock", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/proj",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setWorkflowState("sess-a", store.state.sessionViewEpoch, {
      activeWorkflowRun: {
        id: "run-1",
        session_id: "sess-a",
        project_id: "00000000-0000-4000-8000-000000000001",
        workflow_id: "implement",
        workflow_version: "1.0.0",
		revision: 1,
        status: "running",
        current_phase: "work",
        created_at: "t",
        updated_at: "t",
      },
      workflowRuns: [],
      workflowCatalog: [],
      blueprints: [],
    });
    store.actions.setBoard({
      summary: "Board with ask latch.",
      session_id: "sess-a",
      repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: null,
      pack_content_hash: "a",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "t",
      now_line: "Now: t",
      active_workflow_run: {
        id: "run-1",
        session_id: "sess-a",
        project_id: "00000000-0000-4000-8000-000000000001",
        workflow_id: "implement",
        workflow_version: "1.0.0",
		revision: 1,
        status: "running",
        current_phase: "work",
        created_at: "t",
        updated_at: "t",
        ui: {
          current_phase_label: "Waiting for input",
          pending_feedback: {
            phase_id: "ask-1",
            prompt: "Pick toppings.",
          },
        },
      },
    });
    expect(store.state.activeWorkflowRun?.ui?.pending_feedback).toEqual({
      phase_id: "ask-1",
      prompt: "Pick toppings.",
    });
  });

  it("keeps host-enriched workflow UI when a durable core event replays the revision", () => {
    const store = createAppStore();
    const session = {
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/proj",
      posture: "build" as const,
      status: "idle" as const,
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    };
    const enriched = {
      id: "run-1",
      session_id: session.id,
      project_id: session.project_id,
      workflow_id: "implement",
      workflow_version: "1",
      revision: 2,
      status: "running" as const,
      current_phase: "review",
      created_at: "t",
      updated_at: "t",
      ui: { current_phase_label: "Review changes" },
    };
    store.actions.setCurrentSession(session);
    store.actions.setWorkflowState(session.id, store.state.sessionViewEpoch, {
      activeWorkflowRun: enriched,
      workflowRuns: [enriched],
      workflowCatalog: [],
      blueprints: [],
    });
    store.actions.applyWorkflowRunEvent({ run: { ...enriched, ui: undefined } });
    expect(store.state.workflowRuns[0]?.ui).toEqual(enriched.ui);
    expect(store.state.activeWorkflowRun?.ui).toEqual(enriched.ui);
  });

  it("setBoard ignores board snapshots for a different session", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/proj",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.setBoard({
      summary: "Session A board.",
      session_id: "sess-a",
      repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: null,
      pack_content_hash: "a",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "t",
      now_line: "Now: t",
    });
    store.actions.setBoard({
      summary: "Foreign board.",
      session_id: "sess-b",
      repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: null,
      pack_content_hash: "b",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "t",
      now_line: "Now: t",
    });
    expect(store.state.board?.pack_content_hash).toBe("a");
  });

  it("setBoard reconciles worker merge_status from snapshot", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "w1",
        agent_type: "implementer",
        status: "complete",
        merge_status: "pending",
        created_at: "t",
      },
    ]);
    store.actions.setBoard({
      summary: "All workers complete.",
      repo: { languages: [], file_count: 0, generated_at: "t" },
      cost: null,
      pack_content_hash: "h",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "t",
      now_line: "Now: t",
      roster: [
        {
          worker_id: "w1",
          agent_type: "implementer",
          status: "complete",
          merge_status: "merged",
        },
      ],
    });
    expect(store.state.workers[0]?.merge_status).toBe("merged");
  });
});
