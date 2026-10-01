import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import type { Message } from "../../api/types.ts";
import { buildChatTranscriptBlocks } from "../workflow/workflow-spans.ts";
import { applySessionTranscriptSnapshot } from "./session-transcript-hydrate.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";

describe("applySessionTranscriptSnapshot", () => {
  it("sets workflow runs before messages so transcript render is safe", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ id: "sess-1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "proj-1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    const ambientRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
      revision: 1,
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const messages = [
      {
        id: "m1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello",
        workflow_run_id: "run-1",
        created_at: "t",
      },
    ];
    const callOrder: string[] = [];
    const client = stubClient({
      getActiveWorkflowRun: vi.fn(async () => {
        callOrder.push("workflow");
        return ambientRun;
      }),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });
    const originalInstall = appStore.actions.installTranscriptBaseline.bind(
      appStore.actions,
    );
    vi.spyOn(appStore.actions, "installTranscriptBaseline").mockImplementation(
      (sessionId, next, watermark) => {
        callOrder.push("installBaseline");
        expect(() =>
          buildChatTranscriptBlocks(
            next,
            appStore.state.workflowRuns,
            appStore.state.activeWorkflowRun,
          ),
        ).not.toThrow();
        originalInstall(sessionId, next, watermark);
      },
    );

    await applySessionTranscriptSnapshot(
      appStore,
      client,
      "sess-1",
      "/tmp/p",
      {
        messages,
        watermark: 0,
        turn_clocks: {},
        turn_loads: {},
      },
      emptyProjects,
    );

    expect(callOrder.indexOf("workflow")).toBeLessThan(
      callOrder.indexOf("installBaseline"),
    );
  });

  it("prefers the fresh workflow fetch over the older transcript page on id collision", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ id: "sess-1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "proj-1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    const staleRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
      revision: 3,
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const freshRun = { ...staleRun, revision: 7 };
    const client = stubClient({
      getActiveWorkflowRun: vi.fn(async () => freshRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [freshRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    await applySessionTranscriptSnapshot(
      appStore,
      client,
      "sess-1",
      "/tmp/p",
      {
        messages: [],
        watermark: 0,
        turn_clocks: {},
        turn_loads: {},
      },
      emptyProjects,
    );

    const installed = appStore.state.workflowRuns.find((run) => run.id === "run-1");
    // A stale revision here would 409 the next revision-carrying command.
    expect(installed?.revision).toBe(7);
  });

  it("seeds worker roster rows from task tool results in the snapshot", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({ id: "sess-1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "proj-1", posture: "build", status: "idle", created_at: "t", activity_at: "t", updated_at: "t" });
    const ambientRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
      revision: 1,
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const messages: Message[] = [
      {
        id: "m1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello",
        workflow_run_id: "run-1",
        created_at: "t",
      },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        workflow_run_id: "run-1",
        tool_calls: [
          {
            id: "tc1",
            name: "task",
            args: { agent_type: "implementer", brief: { goal: "build game", done_when: ["Return results."] } },
          },
        ],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: JSON.stringify({
          job_id: "job-1",
          child_session_id: "child-1",
          status: "enqueued",
        }),
        tool_result: {
          assistant_message_id: "a1",
          tool_call_id: "tc1",
          tool: "task",
          tool_args: { agent_type: "implementer", brief: { goal: "build game", done_when: ["Return results."] } },
          content: JSON.stringify({
            job_id: "job-1",
            child_session_id: "child-1",
            status: "enqueued",
          }),
          dispatch: {
            worker_id: "job-1",
            child_session_id: "child-1",
          },
        },
        created_at: "t",
      },
    ];
    const client = stubClient({
      getActiveWorkflowRun: vi.fn(async () => ambientRun),
      listSessionWorkflowRuns: vi.fn(async () => ({ runs: [ambientRun] })),
      listWorkflows: vi.fn(async () => []),
      listBlueprints: vi.fn(async () => []),
    });

    await applySessionTranscriptSnapshot(
      appStore,
      client,
      "sess-1",
      "/tmp/p",
      {
        messages,
        watermark: 0,
        turn_clocks: {},
        turn_loads: {},
      },
      emptyProjects,
    );

    expect(appStore.state.workers).toEqual([
      expect.objectContaining({
        id: "job-1",
        parent_session_id: "sess-1",
        agent_type: "implementer",
        brief: "build game",
      }),
    ]);
  });
});
