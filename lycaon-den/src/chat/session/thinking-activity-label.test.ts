import { describe, expect, it, beforeEach } from "vitest";
import type {
  Message,
  ProviderKindTemplate,
  ProviderMeta,
  WorkerTask,
  WorkflowRun,
} from "../../api/types.ts";
import {
  applyBackgroundProcessEvent,
  resetBackgroundProcessStoreForTests,
} from "../tool/background-process-store.ts";
import {
  ACTIVITY_LABEL_WEIGHTS,
  collectThinkingActivityCandidates,
  findLatestRunningToolInTurn,
  formatThinkingLabel,
  resolveThinkingActivityLabel,
  waitToolRunningLabel,
} from "./thinking-activity-label.ts";

function assistantWithTool(
  id: string,
  tool: string,
  args: Record<string, unknown> = {},
): Message {
  return {
    id,
    role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
    content: "",
    created_at: "t",
    tool_calls: [{ id: `${id}-call`, name: tool, args }],
  };
}

function worker(partial: Partial<WorkerTask> & Pick<WorkerTask, "id">): WorkerTask {
  return {
    agent_type: "explore",
    status: "running",
    created_at: "2026-01-01T00:00:00Z",
    ...partial,
    brief: partial.brief,
  };
}

describe("thinking-activity-label", () => {
  beforeEach(() => {
    resetBackgroundProcessStoreForTests();
  });

  it("always emits trailing ellipsis in resolved labels", () => {
    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "bg-1",
      stream: "stdout",
      text: "working",
      end_offset: 0,
      running: true,
    });
    const samples = [
      resolveThinkingActivityLabel({
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
          assistantWithTool("a1", "verify", { command: "./task check-fast" }),
        ],
      }),
      resolveThinkingActivityLabel({
        batchPhase: "integrate",
        workers: [worker({ id: "w1", agent_type: "explore" })],
      }),
      resolveThinkingActivityLabel({
        promptStarting: true,
      }),
      resolveThinkingActivityLabel({
        sessionId: "s1",
      }),
    ];
    for (const label of samples) {
      expect(label.endsWith("...")).toBe(true);
    }
  });

  it("prefers running tool over coordinator surface", () => {
    expect(
      resolveThinkingActivityLabel({
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "fix tests", created_at: "t" },
          assistantWithTool("a1", "verify", {
            command: "./task check-fast",
          }),
        ],
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_investigate",
        },
      }),
    ).toBe("Running tests · ./task check-fast...");
  });

  it("maps wait conditions to specific labels", () => {
    expect(
      waitToolRunningLabel({ conditions: [{ kind: "process_done" }] }),
    ).toBe("Waiting for a command to finish");
    expect(
      resolveThinkingActivityLabel({
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
          assistantWithTool("a1", "wait", {
            conditions: [{ kind: "all_workers_idle" }],
          }),
        ],
      }),
    ).toBe("Waiting for workers...");
  });

  it("uses wait reason when conditions have no mapped trigger", () => {
    expect(
      waitToolRunningLabel({
        reason: "parallel scouts finishing survey",
        until: ["timer"],
      }),
    ).toBe("parallel scouts finishing survey");
  });

  describe("cut marker vs thinking dots", () => {
    it("ends a whole line in thinking dots", () => {
      expect(formatThinkingLabel("Run tests")).toBe("Run tests...");
    });

    // Preserve an upstream cut marker within the larger display budget.
    it("keeps a cut marker the host already applied", () => {
      const clamped = `${"a".repeat(60)}…`;
      expect(
        resolveThinkingActivityLabel({
          progressSteps: [{ label: clamped, state: "pending" }],
          progressRunning: true,
        }),
      ).toBe(clamped);
    });

    it("does not stack dots on a label that ends in a period", () => {
      expect(formatThinkingLabel("Run tests.")).toBe("Run tests...");
    });

    it("collapses a multi-line value into the one-line lane", () => {
      expect(formatThinkingLabel("first line\n  second line")).toBe(
        "first line second line...",
      );
    });

    it("falls back to Thinking on empty input", () => {
      expect(formatThinkingLabel("   ")).toBe("Thinking...");
    });
  });

  // Structured host activity outranks checklist order.
  it("uses batch phase over the first open progress step", () => {
    expect(
      resolveThinkingActivityLabel({
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_investigate",
        },
        progressSteps: [
          { label: "Run check-fast", state: "pending" },
          { label: "Commit changes", state: "pending" },
        ],
        progressRunning: true,
        batchPhase: "integrate",
      }),
    ).toBe("Combining results...");
  });

  it("uses coordinator surface over the first open progress step", () => {
    expect(
      resolveThinkingActivityLabel({
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_investigate",
          activity_label: "Exploring the project",
        },
        progressSteps: [{ label: "Run check-fast", state: "pending" }],
        progressRunning: true,
      }),
    ).toBe("Exploring the project...");
  });

  it("prefers an open host activity lease over progress", () => {
    expect(
      resolveThinkingActivityLabel({
        activities: [
          {
            activity_id: "activity-1",
            session_id: "s1",
            kind: "preparing_context",
            status: "active",
            started_at: "2026-01-01T00:00:00Z",
          },
        ],
        progressSteps: [{ label: "Run check-fast", state: "pending" }],
        progressRunning: true,
      }),
    ).toBe("Preparing context...");
  });

  it("shows the local decision over the turn's preparing lease", () => {
    expect(
      resolveThinkingActivityLabel({
        activities: [
          {
            activity_id: "activity-1",
            session_id: "s1",
            kind: "preparing_context",
            status: "active",
            started_at: "2026-01-01T00:00:00Z",
          },
          {
            activity_id: "activity-2",
            session_id: "s1",
            kind: "deciding",
            status: "active",
            started_at: "2026-01-01T00:00:01Z",
            decision_trigger: "turn",
          },
        ],
      }),
    ).toBe("Local AI is deciding what to load...");
  });

  it("labels a decision by what asked it", () => {
    expect(
      resolveThinkingActivityLabel({
        activities: [
          {
            activity_id: "activity-1",
            session_id: "s1",
            kind: "deciding",
            status: "active",
            started_at: "2026-01-01T00:00:00Z",
            decision_trigger: "request",
            tool_call_id: "call-1",
          },
        ],
      }),
    ).toBe("Local AI is matching tools...");
  });

  it("labels the workflow-start boot window from its host lease", () => {
    expect(
      resolveThinkingActivityLabel({
        activities: [
          {
            activity_id: "activity-1",
            session_id: "s1",
            kind: "starting_workflow",
            status: "active",
            started_at: "2026-01-01T00:00:00Z",
          },
        ],
        progressSteps: [{ label: "Run check-fast", state: "pending" }],
        progressRunning: true,
      }),
    ).toBe("Starting workflow...");
  });

  it("uses the canonical tool call args for an active tool lease", () => {
    expect(
      resolveThinkingActivityLabel({
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
          assistantWithTool("a1", "verify", { command: "./task check-fast" }),
        ],
        activities: [
          {
            activity_id: "activity-1",
            session_id: "s1",
            kind: "running_tool",
            status: "active",
            started_at: "2026-01-01T00:00:00Z",
            tool_name: "verify",
            tool_call_id: "a1-call",
          },
        ],
      }),
    ).toBe("Running tests · ./task check-fast...");
  });

  it("skips checked-off rows when picking the first open one", () => {
    expect(
      resolveThinkingActivityLabel({
        progressSteps: [
          { label: "Map the auth hooks", state: "done" },
          { label: "Wire middleware", state: "pending" },
        ],
        progressRunning: true,
      }),
    ).toBe("Wire middleware...");
  });

  it("renders host-measured search progress", () => {
    const activity = {
      activity_id: "search-1",
      session_id: "s1",
      kind: "running_tool" as const,
      status: "active" as const,
      started_at: "2026-01-01T00:00:00Z",
      tool_name: "grep",
      progress: { phase: "searching" as const, files_searched: 120, files_skipped: 8, matches: 3 },
    };
    expect(resolveThinkingActivityLabel({ activities: [activity] }))
      .toBe("Searching files · 120 searched · 3 matches · 8 skipped...");
    expect(resolveThinkingActivityLabel({ activities: [{ ...activity, progress: { ...activity.progress, phase: "selecting" } }] }))
      .toBe("Selecting files for search...");
    expect(resolveThinkingActivityLabel({ activities: [{ ...activity, status: "done" }] }))
      .not.toContain("120 searched");
  });

  // Live workers outrank planned checklist rows.
  it("lets a running worker outrank the first open plan row", () => {
    expect(
      resolveThinkingActivityLabel({
        progressSteps: [{ label: "Wire middleware", state: "pending" }],
        progressRunning: true,
        workers: [
          worker({
            id: "w1",
            agent_type: "repo-researcher",
            brief: "Survey auth module boundaries",
          }),
        ],
      }),
    ).toBe("Survey auth module boundaries...");
  });

  it("uses batch phase when it beats investigate surface copy", () => {
    expect(
      resolveThinkingActivityLabel({
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_investigate",
        },
        batchPhase: "integrate",
      }),
    ).toBe("Combining results...");
  });

  it("prefers batch phase over weaker synthesis surface copy", () => {
    expect(
      resolveThinkingActivityLabel({
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_synthesis",
        },
        batchPhase: "synthesize",
      }),
    ).toBe("Putting it together...");
  });

  // This gap equals BATCH_PHASE_COMBINE_GAP.
  it("combines batch phase with a close runner-up worker label", () => {
    expect(
      resolveThinkingActivityLabel({
        batchPhase: "dispatch",
        workers: [worker({ id: "w1", agent_type: "explore" })],
      }),
    ).toBe("Starting workers · Running a worker...");
  });

  it("uses rich worker label with task brief", () => {
    expect(
      resolveThinkingActivityLabel({
        workers: [
          worker({
            id: "w1",
            agent_type: "repo-researcher",
            brief: "Survey auth module boundaries",
          }),
        ],
      }),
    ).toBe("Survey auth module boundaries...");
  });

  it("counts several in-flight workers", () => {
    expect(
      resolveThinkingActivityLabel({
        workers: [
          worker({
            id: "w1",
            agent_type: "path-explorer",
            brief: "Map auth entrypoints",
            started_at: "2026-01-01T00:00:02Z",
          }),
          worker({
            id: "w2",
            agent_type: "repo-researcher",
            brief: "Survey session ledger",
            started_at: "2026-01-01T00:00:01Z",
          }),
        ],
      }),
    ).toBe("Running 2 workers · Map auth entrypoints...");
  });

  it("defers to coordinator surface over workflow phase on host turns", () => {
    const run: WorkflowRun = {
      id: "run-1",
      session_id: "s1",
      workflow_id: "plan",
      workflow_version: "1",
	  revision: 1,
      status: "running",
      current_phase: "research",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    expect(
      resolveThinkingActivityLabel({
        sessionId: "s1",
        activeWorkflowRun: run,
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_investigate",
          activity_label: "Exploring the project",
        },
      }),
    ).toBe("Exploring the project...");
  });

  it("maps implement boot and work phases to plain-language labels", () => {
    const boot: WorkflowRun = {
      id: "run-1",
      session_id: "s1",
      workflow_id: "implement",
      workflow_version: "1",
	  revision: 1,
      status: "running",
      current_phase: "boot",
      ui: { current_phase_label: "Looking around" },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const work = {
      ...boot,
      current_phase: "work",
      ui: { current_phase_label: "Diving in" },
    };
    expect(
      resolveThinkingActivityLabel({ sessionId: "s1", activeWorkflowRun: boot }),
    ).toBe("Looking around...");
    expect(
      resolveThinkingActivityLabel({ sessionId: "s1", activeWorkflowRun: work }),
    ).toBe("Diving in...");
  });

  it("uses workflow phase as a weak fallback when no live signal is present", () => {
    const run: WorkflowRun = {
      id: "run-1",
      session_id: "s1",
      workflow_id: "plan",
      workflow_version: "1",
	  revision: 1,
      status: "running",
      current_phase: "research",
      ui: { current_phase_label: "Researching the project" },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    expect(
      resolveThinkingActivityLabel({
        sessionId: "s1",
        activeWorkflowRun: run,
      }),
    ).toBe("Researching the project...");
  });

  it.each(["approval", "answer"])("shows the %s hold above stale phase and checklist activity", (kind) => {
    const run: WorkflowRun = {
      id: "review",
      session_id: "s1",
      workflow_id: "custom-review",
      workflow_version: "1",
      revision: 2,
      status: "running",
      current_phase: "review",
      ui: {
        current_phase_label: "Reviewing",
        human_approval_awaiting: kind === "approval",
        pending_feedback: kind === "answer" ? { phase_id: "review", prompt: "Which time?" } : undefined,
      },
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const input = {
      sessionId: "s1",
      activeWorkflowRun: run,
      coordinatorLoop: { host_turn: true, activity_label: "Getting the plan ready" },
      batchPhase: "integrate" as const,
      progressSteps: [{ label: "Revise the plan", state: "pending" as const }],
      progressRunning: false,
    };
    expect(resolveThinkingActivityLabel(input)).toBe(`Waiting for your ${kind}...`);
    // A real read while answering feedback remains visible.
    expect(resolveThinkingActivityLabel({
      ...input,
      messages: [assistantWithTool("read-review", "read", { path: "blueprint.md" })],
    })).toBe("Reading · blueprint.md...");
    for (const status of ["complete", "canceled", "failed", "interrupted"] as const) {
      expect(collectThinkingActivityCandidates({
        ...input,
        activeWorkflowRun: { ...run, status },
      }).some((candidate) => candidate.weight === ACTIVITY_LABEL_WEIGHTS.humanReview)).toBe(false);
    }
    expect(collectThinkingActivityCandidates({
      ...input,
      sessionId: "another-session",
    }).some((candidate) => candidate.weight === ACTIVITY_LABEL_WEIGHTS.humanReview)).toBe(false);
  });

  it("shows provisional closeout copy during guarded synthesis turns", () => {
    expect(
      resolveThinkingActivityLabel({
        llmTurnActivity: {
          status: "active",
          provisionalHidden: true,
        },
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_synthesis",
          provisional_hidden: true,
        },
      }),
    ).toBe("Putting it together...");
  });

  // The explicit visibility flag is the sole closeout signal.
  it("ignores a guarded closeout surface that does not declare provisional_hidden", () => {
    expect(
      resolveThinkingActivityLabel({
        llmTurnActivity: { status: "active", guarded: true },
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_synthesis",
          guarded: true,
        },
      }),
    ).not.toBe("Putting it together...");
  });

  it("labels the foreground tool while a retained process is also running", () => {
    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "bg-1",
      stream: "stdout",
      text: "working",
      end_offset: 0,
      running: true,
    });
    expect(
      resolveThinkingActivityLabel({
        sessionId: "s1",
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
          assistantWithTool("a1", "grep", { pattern: "foo" }),
        ],
      }),
    ).toBe("Searching · foo...");
  });

  it("does not present a retained process as current turn activity", () => {
    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "bg-1",
      stream: "stdout",
      text: "working",
      end_offset: 0,
      running: true,
    });
    expect(
      resolveThinkingActivityLabel({
        sessionId: "s1",
      }),
    ).toBe("Thinking...");
  });

  it("omits any iteration stamp on host turns", () => {
    expect(
      resolveThinkingActivityLabel({
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
          assistantWithTool("a1", "grep", { pattern: "foo" }),
        ],
        coordinatorLoop: {
          host_turn: true,
          surface: "implement_investigate",
          iteration: 3,
        },
      }),
    ).toBe("Searching · foo...");
  });

  describe("waiting on the provider", () => {
    // Canonical kind labels take precedence over user-authored instance labels.
    const providers = [
      { id: "anthropic-1", kind: "anthropic", label: "Anthropic 1" },
      { id: "ollama-gpubox", kind: "ollama", label: "Ollama (gpubox)" },
    ] as unknown as ProviderMeta[];
    const providerKinds = [
      { kind: "anthropic", label: "Anthropic" },
      { kind: "ollama", label: "Ollama" },
    ] as unknown as ProviderKindTemplate[];

    it("names the provider and model while a call is in flight", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic...");
    });

    it("outranks a running tool", () => {
      expect(
        resolveThinkingActivityLabel({
          messages: [
            { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
            assistantWithTool("a1", "command", { command: "go test ./..." }),
          ],
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic...");
    });

    it("counts retries so a throttled provider reads apart from a slow one", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
            retry: { attempt: 2, maxAttempts: 3, reason: "status" },
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic · retrying 2/3...");
    });

    it("counts capacity retries the same way as throttle retries", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
            retry: { attempt: 2, maxAttempts: 3, reason: "capacity" },
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic · retrying 2/3...");
    });

    it("says a provider went quiet, and for how long, rather than just retrying", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
            retry: {
              attempt: 2,
              maxAttempts: 2,
              reason: "silent",
              silenceMs: 30_000,
            },
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic · went quiet 30s, retrying 2/2...");
    });

    it("still names silence when the host reported no duration", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
            retry: { attempt: 2, maxAttempts: 2, reason: "silent" },
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic · went quiet, retrying 2/2...");
    });

    it("separates a request that never landed from one that went unanswered", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
            retry: { attempt: 2, maxAttempts: 2, reason: "unreachable" },
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic · no answer, retrying 2/2...");
    });

    it("names a turn hold as still busy rather than another HTTP retry", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
            retry: { attempt: 1, maxAttempts: 2, reason: "hold" },
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic · still busy...");
    });

    it("names a cooling slot as still busy", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
            retry: { attempt: 1, maxAttempts: 1, reason: "cooldown" },
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic · still busy...");
    });

    it("drops the retry tail once the call stops being reissued", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "active",
            provider: "anthropic-1",
          },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Anthropic...");
    });

    // Waiting labels use the canonical provider kind.
    it("names the provider, not the instance", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: { status: "active", provider: "ollama-gpubox" },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on Ollama...");
    });

    it("falls back to the instance label when the kind has no template", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: { status: "active", provider: "anthropic-1" },
          providers,
          providerKinds: [],
        }),
      ).toBe("Waiting on Anthropic 1...");
    });

    it("falls back to the provider id when the catalog has no entry", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: { status: "active", provider: "ollama-spare" },
          providers,
          providerKinds,
        }),
      ).toBe("Waiting on ollama-spare...");
    });

    it("contributes nothing once the call is no longer active", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: {
            status: "done",
            provider: "anthropic-1",
          },
          providers,
          providerKinds,
        }),
      ).toBe("Thinking...");
    });

    // The turn label resumes after the provider request completes.
    it("yields to the turn's own label between calls", () => {
      expect(
        resolveThinkingActivityLabel({
          llmTurnActivity: { status: "done", provider: "anthropic-1" },
          coordinatorLoop: {
            host_turn: true,
            surface: "implement_synthesis",
            provisional_hidden: true,
          },
          providers,
          providerKinds,
        }),
      ).toBe("Putting it together...");
    });
  });

  describe("running tool names its object", () => {
    const turn = (tool: string, args: Record<string, unknown>): Message[] => [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      assistantWithTool("a1", tool, args),
    ];

    it("pairs the pack verb with the chicklet object", () => {
      expect(
        resolveThinkingActivityLabel({
          messages: turn("command", { command: "go test ./internal/confine/..." }),
        }),
      ).toBe("Running command · go test ./internal/confine/...");
    });

    it("uses the kind verb when the pack declares no running label", () => {
      expect(
        resolveThinkingActivityLabel({
          messages: turn("read", { path: "src/components/chatview/Composer.tsx" }),
        }),
      ).toBe("Reading · src/components/chatview/Composer.tsx...");
      // Pack labels override kind verbs.
      expect(
        resolveThinkingActivityLabel({
          messages: turn("edit", { path: "chat-utilities.css" }),
        }),
      ).toBe("Editing · chat-utilities.css...");
    });

    it("falls back to the standalone verb when no object resolves", () => {
      // Kind verbs cover tools without pack labels.
      expect(
        resolveThinkingActivityLabel({ messages: turn("read", {}) }),
      ).toBe("Reading files...");
      // Object-free pack labels remain complete phrases.
      expect(
        resolveThinkingActivityLabel({ messages: turn("stat", {}) }),
      ).toBe("Checking files...");
    });

    // Uncatalogued tools use the generic verb.
    it("uses the generic verb only for an uncatalogued tool", () => {
      expect(
        resolveThinkingActivityLabel({
          messages: turn("mcp__thing__do", {}),
        }),
      ).toBe("Using a tool...");
    });

    it("keeps the background-command verb and still names the command", () => {
      expect(
        resolveThinkingActivityLabel({
          messages: turn("command", { command: "npm run dev", background: true }),
        }),
      ).toBe("Running a background command · npm run dev...");
    });

    // A cut marker replaces the thinking suffix.
    it("marks a composed line that had to be cut", () => {
      const label = resolveThinkingActivityLabel({
        messages: turn("command", { command: "x".repeat(200) }),
      });
      expect(Array.from(label)).toHaveLength(72);
      expect(label.startsWith("Running command · xxx")).toBe(true);
      expect(label.endsWith("…")).toBe(true);
      expect(label.endsWith("...")).toBe(false);
    });

    // Code-point slicing keeps surrogate pairs intact.
    it("cuts on code points, not UTF-16 units", () => {
      const label = resolveThinkingActivityLabel({
        messages: turn("command", { command: "🙂".repeat(200) }),
      });
      expect(label).not.toContain("�");
      expect(label.endsWith("…")).toBe(true);
      // The rendered line contains no lone surrogate.
      expect(/[\uD800-\uDFFF]/u.test(label.replace(/[\uD800-\uDBFF][\uDC00-\uDFFF]/gu, "")))
        .toBe(false);
    });

    it("wait keeps its structured reason and gains no object", () => {
      expect(
        resolveThinkingActivityLabel({
          messages: turn("wait", { conditions: [{ kind: "process_done" }] }),
        }),
      ).toBe("Waiting for a command to finish...");
    });
  });

  it("findLatestRunningToolInTurn skips orientation tools", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      assistantWithTool("a1", "update_progress", { content: "- [ ] x" }),
      assistantWithTool("a2", "read", { path: "README.md" }),
    ];
    expect(findLatestRunningToolInTurn(messages)?.tool).toBe("read");
  });

  it("collectThinkingActivityCandidates orders by weight", () => {
    const candidates = collectThinkingActivityCandidates({
      messages: [
        { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
        assistantWithTool("a1", "command", { command: "sleep 1" }),
      ],
      coordinatorLoop: { host_turn: true, surface: "implement_investigate" },
      progressSteps: [{ label: "Ship fix", state: "pending" }],
      progressRunning: true,
      batchPhase: "integrate",
      workers: [worker({ id: "w1" })],
      llmTurnActivity: { status: "active" },
    });

    const top = candidates.reduce((best, next) =>
      next.weight > best.weight ? next : best,
    );
    expect(top.source).toBe("running_tool");
    expect(top.weight).toBe(ACTIVITY_LABEL_WEIGHTS.runningTool);
  });

  it("falls back to Thinking when no stronger signal exists", () => {
    expect(resolveThinkingActivityLabel({})).toBe("Thinking...");
  });

  it("uses prompt in flight when it is the only live signal", () => {
    expect(
      resolveThinkingActivityLabel({
        promptStarting: true,
        llmTurnActivity: undefined,
      }),
    ).toBe("Sending your message...");
  });

  it("labels local loopback http_request as Testing endpoint", () => {
    expect(
      resolveThinkingActivityLabel({
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "test", created_at: "t" },
          assistantWithTool("a1", "http_request", { url: "http://localhost:3000/signin" }),
        ],
      }),
    ).toBe("Testing endpoint · http://localhost:3000/signin...");
  });

  it("labels remote http_request as Sending HTTP request", () => {
    expect(
      resolveThinkingActivityLabel({
        messages: [
          { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "test", created_at: "t" },
          assistantWithTool("a1", "http_request", { url: "https://api.github.com/repos" }),
        ],
      }),
    ).toBe("Sending HTTP request · https://api.github.com/repos...");
  });
});
