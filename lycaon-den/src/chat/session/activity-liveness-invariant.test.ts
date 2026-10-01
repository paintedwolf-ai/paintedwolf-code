import { describe, expect, it } from "vitest";
import type {
  ActivityEvent,
  CoordinatorBatchPhase,
  CoordinatorLoopProgress,
  LLMCallEvent,
  Message,
  ProgressStep,
  Session,
  WorkerTask,
  WorkflowRun,
} from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { type AppStore } from "../../store/app-state-model.ts";
import { isAnySessionActivityLive, isChatActivityLive, isPromptStarting } from "./session-activity.ts";
import {
  resolveThinkingActivityLabel,
  selectThinkingActivityCandidate,
  type ActivityLabelSource,
  type ThinkingActivityInput,
} from "./thinking-activity-label.ts";

/** One description of session state, projected into both modules under test. */
type SessionFixture = {
  sessionStatus?: Session["status"];
  llmCall?: LLMCallEvent;
  activities?: readonly ActivityEvent[];
  workers?: readonly WorkerTask[];
  messages?: readonly Message[];
  promptStarting?: boolean;
  stopping?: boolean;
  coordinatorLoop?: CoordinatorLoopProgress;
  progressSteps?: readonly ProgressStep[];
  progressRunning?: boolean;
  batchPhase?: CoordinatorBatchPhase;
  activeWorkflowRun?: WorkflowRun;
};

type SourceCase = {
  /** Label-only signals need an independent liveness fact. */
  kind: "liveness" | "label-only";
  /** State in which this source wins the label. */
  fixture: SessionFixture;
  /** This source's own signal, with no companion to carry liveness. */
  solo: SessionFixture;
};

const SESSION_ID = "11111111-1111-4111-8111-111111111111";
const PROJECT_ID = "22222222-2222-4222-8222-222222222222";

function session(status: Session["status"]): Session {
  return {
    id: SESSION_ID,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: PROJECT_ID,
    workspace_path: "/tmp/ws",
    posture: "build",
    status,
    created_at: "2026-08-19T00:00:00Z",
    activity_at: "2026-08-19T00:00:00Z",
    updated_at: "2026-08-19T00:00:00Z",
  };
}

function activeLLMCall(): LLMCallEvent {
  return {
    call_id: "33333333-3333-4333-8333-333333333333",
    session_id: SESSION_ID,
    provider: "anthropic-1",
    model: "test-model",
    status: "active",
    tokens: {},
  };
}

function lease(kind: ActivityEvent["kind"], extra: Partial<ActivityEvent> = {}): ActivityEvent {
  return {
    activity_id: `activity-${kind}`,
    session_id: SESSION_ID,
    kind,
    status: "active",
    started_at: "2026-08-19T00:00:00Z",
    ...extra,
  };
}

function runningWorker(): WorkerTask {
  return {
    id: "worker-1",
    parent_session_id: SESSION_ID,
    project_id: PROJECT_ID,
    workspace_path: "/tmp/ws",
    agent_type: "implementer",
    status: "running",
    execution_target: "local",
    created_at: "2026-08-19T00:00:00Z",
    brief: "Wire the parser",
  };
}

/** A user ask followed by an assistant tool call with no result yet. */
function runningToolMessages(): Message[] {
  return [
    { id: "m1", role: "user", content: "go" } as unknown as Message,
    {
      id: "m2",
      role: "assistant",
      content: "",
      tool_calls: [{ id: "call-1", name: "read", arguments: { path: "a.ts" } }],
    } as unknown as Message,
  ];
}

function runningWorkflowRun(): WorkflowRun {
  return {
    id: "run-1",
    session_id: SESSION_ID,
    workflow_id: "implement",
    workflow_version: "1.0.0",
    revision: 1,
    attach_policy: "session_create",
    status: "running",
    current_phase: "work",
    created_at: "2026-08-19T00:00:00Z",
    updated_at: "2026-08-19T00:00:00Z",
    ui: { current_phase_label: "Implementing" },
  } as unknown as WorkflowRun;
}

/** Liveness companion for `label-only` sources; adds no competing label. */
const BUSY: SessionFixture = { sessionStatus: "busy" };

const SOURCE_CASES: Record<ActivityLabelSource, SourceCase> = {
  provider_wait: {
    kind: "liveness",
    fixture: { llmCall: activeLLMCall() },
    solo: { llmCall: activeLLMCall() },
  },
  host_activity: {
    kind: "liveness",
    fixture: { activities: [lease("preparing_context")] },
    solo: { activities: [lease("preparing_context")] },
  },
  running_tool: {
    // A missing transcript result does not establish liveness.
    kind: "label-only",
    fixture: { ...BUSY, messages: runningToolMessages() },
    solo: { messages: runningToolMessages() },
  },
  provisional_closeout: {
    // An unnamed provider leaves the closeout label eligible.
    kind: "liveness",
    fixture: {
      llmCall: {
        ...activeLLMCall(),
        provider: "",
        coordinator_loop: { provisional_hidden: true },
      },
    },
    solo: {
      llmCall: {
        ...activeLLMCall(),
        provider: "",
        coordinator_loop: { provisional_hidden: true },
      },
    },
  },
  worker: {
    kind: "liveness",
    fixture: { workers: [runningWorker()] },
    solo: { workers: [runningWorker()] },
  },
  batch_phase: {
    kind: "label-only",
    fixture: { ...BUSY, batchPhase: "dispatch" },
    solo: { batchPhase: "dispatch" },
  },
  coordinator_surface: {
    kind: "label-only",
    fixture: {
      ...BUSY,
      coordinatorLoop: { surface: "implement", activity_label: "Reviewing the diff" },
    },
    solo: {
      coordinatorLoop: { surface: "implement", activity_label: "Reviewing the diff" },
    },
  },
  workflow_phase: {
    // A running run whose session is idle and holds no wake lease is a stalled
    // run, not a live one; the lease is the single liveness plane.
    kind: "label-only",
    fixture: { ...BUSY, activeWorkflowRun: runningWorkflowRun() },
    solo: { activeWorkflowRun: runningWorkflowRun() },
  },
  prompt_starting: {
    kind: "liveness",
    fixture: { promptStarting: true },
    solo: { promptStarting: true },
  },
  progress_step: {
    // The host emits pending/done/na only, so a row makes no claim about what is
    // executing now.
    kind: "label-only",
    fixture: {
      ...BUSY,
      progressSteps: [{ state: "pending", label: "Draft the parser" }],
      progressRunning: true,
    },
    solo: {
      progressSteps: [{ state: "pending", label: "Draft the parser" }],
      progressRunning: true,
    },
  },
  default: {
    // The floor. It is the one source that may win on an idle session, because
    // winning it means nothing else had anything to say.
    kind: "label-only",
    fixture: {},
    solo: {},
  },
};

function buildStore(fixture: SessionFixture): AppStore {
  const appStore = createAppStore();
  appStore.actions.setCurrentSession(session(fixture.sessionStatus ?? "idle"));
  if (fixture.llmCall) appStore.actions.setLLMCallStatus(fixture.llmCall);
  for (const activity of fixture.activities ?? []) appStore.actions.setActivity(activity);
  if (fixture.workers?.length) appStore.actions.setWorkers([...fixture.workers]);
  if (fixture.promptStarting) appStore.actions.holdPromptSubmission(SESSION_ID, "submission-1");
  if (fixture.stopping) appStore.actions.setSessionStopping(SESSION_ID, true);
  return appStore;
}

function buildInput(fixture: SessionFixture, appStore: AppStore): ThinkingActivityInput {
  const activity = appStore.state.sessionActivity[SESSION_ID] ?? {};
  return {
    sessionId: SESSION_ID,
    messages: fixture.messages ?? [],
    activities: Object.values(activity.activities ?? {}),
    coordinatorLoop: fixture.coordinatorLoop ?? null,
    progressSteps: fixture.progressSteps,
    progressRunning: fixture.progressRunning,
    batchPhase: fixture.batchPhase,
    activeWorkflowRun: fixture.activeWorkflowRun ?? null,
    workers: fixture.workers ?? [],
    llmTurnActivity: activity.llmTurn,
    promptStarting: isPromptStarting(appStore, SESSION_ID),
  };
}

describe("activity liveness invariant", () => {
  const entries = Object.entries(SOURCE_CASES) as [ActivityLabelSource, SourceCase][];

  it.each(entries)(
    "%s resolves a label that a live session can actually show",
    (source, testCase) => {
      const appStore = buildStore(testCase.fixture);
      const winner = selectThinkingActivityCandidate(
        buildInput(testCase.fixture, appStore),
      );

      expect(winner.source).toBe(source);
      // Reachability: no source may compute copy for a session Den calls idle.
      // `default` is exempt — it wins precisely when there is nothing to show.
      if (source !== "default") {
        expect(isChatActivityLive(appStore, SESSION_ID)).toBe(true);
      }
    },
  );

  it.each(entries)("%s carries liveness exactly as classified", (_source, testCase) => {
    const appStore = buildStore(testCase.solo);
    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(
      testCase.kind === "liveness",
    );
  });

  it("a prompt the host holds pending keeps every viewer live across the turn boundary", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("busy"));
    expect(selectThinkingActivityCandidate(buildInput({}, appStore)).source).not.toBe("prompt_starting");

    // One turn ends while the next prompt waits; no hold from this client is involved.
    appStore.actions.mergeSession({ id: SESSION_ID, status: "idle", prompt_pending: true });
    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(true);
    expect(selectThinkingActivityCandidate(buildInput({}, appStore)).source).toBe("prompt_starting");

    appStore.actions.mergeSession({ id: SESSION_ID, status: "busy" });
    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(true);
    expect(selectThinkingActivityCandidate(buildInput({}, appStore)).source).not.toBe("prompt_starting");

    appStore.actions.mergeSession({ id: SESSION_ID, status: "idle" });
    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(false);
  });

  it("an armed host wake keeps an idle session live for the whole sleep", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("busy"));
    appStore.actions.setActivity(
      lease("awaiting_wake", { wait_triggers: ["timer", "next_worker_done"] }),
    );

    // The turn that armed the sleep ends: idle lands while the lease stays open.
    appStore.actions.mergeSession({ id: SESSION_ID, status: "idle" } as never);

    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(true);
    const input = buildInput({}, appStore);
    expect(selectThinkingActivityCandidate(input).source).toBe("host_activity");
    expect(resolveThinkingActivityLabel(input)).toBe("Waiting for workers...");
  });

  it("the wake's terminal edge is what ends the wait, and it does", () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("idle"));
    appStore.actions.setActivity(lease("awaiting_wake", { wait_triggers: ["timer"] }));
    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(true);

    appStore.actions.setActivity(
      lease("awaiting_wake", { wait_triggers: ["timer"], status: "done" }),
    );
    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(false);
  });

  it("a subscription-less park keeps liveness but yields the copy", () => {
    // Unnamed leases provide liveness while the workflow supplies the label.
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("idle"));
    appStore.actions.setActivity(lease("awaiting_wake"));

    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(true);
    const input = buildInput({ activeWorkflowRun: runningWorkflowRun() }, appStore);
    expect(selectThinkingActivityCandidate(input).source).toBe("workflow_phase");
    expect(resolveThinkingActivityLabel(input)).toBe("Implementing...");
  });

  it("an armed wake does not silence the notice rail's announcements", () => {
    // Silent waits leave notice-rail announcements enabled.
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("idle"));
    appStore.actions.setActivity(lease("awaiting_wake", { wait_triggers: ["timer"] }));

    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(true);
    expect(isAnySessionActivityLive(appStore)).toBe(false);

    // A lease for work that is actually running still controls the region.
    appStore.actions.setActivity(lease("running_tool", { tool_name: "read" }));
    expect(isAnySessionActivityLive(appStore)).toBe(true);
  });

  it("a sleep the person must end publishes no lease, so the lane stays off", () => {
    // The host omits the lease for user-mover parks (pending ask, human
    // approval, idle host turn). Den sees an ordinary idle session.
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("busy"));
    appStore.actions.mergeSession({ id: SESSION_ID, status: "idle" } as never);
    expect(isChatActivityLive(appStore, SESSION_ID)).toBe(false);
  });
});
