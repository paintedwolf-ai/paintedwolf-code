import { wireProject } from "../../api/mocks/project-fixture.ts";
import { HttpResponse } from "msw";
import { MSW_API_BASE, operationHandler } from "../../api/mocks/operation-handler.ts";
import type {
  BoardView,
  Message,
  Session,
  SessionBootstrap,
  WorkflowRun,
} from "../../api/types.ts";

export { MSW_API_BASE };

export const demoSession: Session = {
  id: "sess-empty",
  owner_person_id: "00000000-0000-4000-8000-000000000002",
  project_id: "00000000-0000-4000-8000-000000000001",
  workspace_path: "/tmp/demo",
  posture: "build",
  status: "idle",
  created_at: "2025-01-01T00:00:00Z",
  activity_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
};

export const ambientImplementRun: WorkflowRun = {
  id: "run-ambient",
  session_id: demoSession.id,
  workflow_id: "implement",
  workflow_version: "1.0.0",
	revision: 1,
  attach_policy: "session_create",
  status: "running",
  current_phase: "work",
  created_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
};

export const terminalImplementRun: WorkflowRun = {
  id: "run-done",
  session_id: demoSession.id,
  workflow_id: "implement",
  workflow_version: "1.0.0",
	revision: 1,
  attach_policy: "session_create",
  status: "complete",
  current_phase: "work",
  created_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
};

/** Session hydration HTTP handlers. */
export function sessionTranscriptHandlers(
  messages: Message[],
  runs: WorkflowRun[] = [],
  opts?: { active?: WorkflowRun | null; session?: Session; watermark?: number },
) {
  const session = opts?.session ?? demoSession;
  const active =
    opts && "active" in (opts ?? {}) ? opts.active : ambientImplementRun;
  const watermark =
    opts?.watermark ?? messages.reduce((max, m) => Math.max(max, m.seq ?? 0), 0);
  return [
    operationHandler("getSessionBootstrap", () => ({
      event_cursor: "",
      session,
      transcript: {
        messages,
        watermark,
        turn_clocks: {},
        turn_loads: {},
      },
      progress: { steps: [], revision: 0 },
      turn_clock: { session_id: session.id, active_ms: 0, work_ms: 0, running: false },
      findings: { findings: [], revision: 0 },
      queue: { queue_items: [], hold: false, sending: false, revision: 0 },
      coordinator: {},
      background_outputs: [],
      previews: [],
      workers: [],
      checkpoints: [],
    } satisfies SessionBootstrap)),
    operationHandler("listCheckpoints", () => ({ checkpoints: [] })),
    operationHandler("getActiveWorkflowRun", () => active ? { run: active } : HttpResponse.json({ run: null })),
    operationHandler("listSessionWorkflowRuns", () => ({ runs })),
    operationHandler("getWorkflowRun", ({ params }) =>
      runs.find((r) => r.id === params.id) ?? terminalImplementRun),
    operationHandler("listProjects", () => ({
      projects: [wireProject(session.workspace_path ?? "/tmp/demo", session.project_id)],
    })),
    operationHandler("listWorkflows", () => ({ workflows: [] })),
    operationHandler("listBlueprints", () => ({ blueprints: [] })),
    operationHandler("listWorkers", () => ({ workers: [] })),
    operationHandler("getBoard", () => ({
      summary: "",
      repo: { languages: [], file_count: 0, generated_at: "2026-01-01T00:00:00Z" },
      roster: [],
      cost: null,
      pack_content_hash: "msw",
      detail_level: "compact",
      board: "",
      board_chars: 0,
      truncated: false,
      generated_at: "2026-01-01T00:00:00Z",
      now_line: "now",
    } satisfies BoardView)),
    operationHandler("listGitRepos", () => ({ repos: [] })),
    operationHandler("getGitStatus", () => ({
      available: false,
      repo_id: "",
      root_ids: [],
      revision: 0,
      refreshing: false,
      ahead: 0,
      behind: 0,
      dirty: false,
      staged_count: 0,
      unstaged_count: 0,
      changed_count: 0,
    })),
  ];
}
