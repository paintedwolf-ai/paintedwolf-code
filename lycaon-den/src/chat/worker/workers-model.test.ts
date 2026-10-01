import { describe, expect, it } from "vitest";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import type { WorkerTask } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { sessionWorkers } from "../actions/chat-actions.ts";
import {
  canCancelWorker,
  reconcileWorkerFromListRefresh,
  reconcileSessionWorkersFromList,
  dedupeWorkersById,
  matchWorkerForTask,
  normalizeWorkerEvent,
  sortWorkerTasks,
  syncWorkerFromTaskToolMessage,
  taskWorkerMatchesForTranscript,
  workerDispatchParamRows,
  workerDispatchBrief,
  workerRowTitle,
  workerRowTitleDisambiguated,
  workersForSession,
  workerRowStatus,
  workerDependencyLabel,
  workerStoreFingerprint,
  workerTasksSliceEqual,
  workerEventFromTaskToolMessage,
  failedDispatchWorkersFromMessages,
  failedDispatchWorkerId,
  sessionWorkerRows,
  liveWorkerRows,
  taskDispatchCallForWorker,
} from "./workers-model.ts";
import type { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";

function worker(
  id: string,
  status: WorkerTask["status"],
  createdAt = "2026-01-01T00:00:00Z",
): WorkerTask {
  return {
    id,
    parent_session_id: "sess-1",
    agent_type: "implementer",
    status,
    created_at: createdAt,
  };
}

describe("workers-model", () => {
  it("summarizes waiting dependencies and makes readiness part of row identity", () => {
    const pending = { ...worker("consumer", "pending"), dependencies: [{ worker_id: "p", state: "waiting" as const }] };
    const blocked = { ...pending, dependencies: [{ worker_id: "p", state: "blocked" as const }] };
    expect(workerDependencyLabel(pending)).toBe("Waiting for 1 producer");
    expect(workerDependencyLabel(blocked)).toBe("1 producer needs attention");
    expect(workerStoreFingerprint(pending)).not.toBe(workerStoreFingerprint(blocked));
    expect(workerDependencyLabel({ ...pending, status: "canceled" })).toBeUndefined();
  });
  it.each([
    ["pending", true], ["running", true], ["waiting", true], ["held", true],
    ["complete", false], ["failed", false], ["canceled", false],
  ] as const)("offers cancellation for %s exactly while the job is active", (status, expected) => {
    expect(canCancelWorker(worker("job", status))).toBe(expected);
  });

  it("dedupes workers by job id", () => {
    const rows = dedupeWorkersById([
      worker("job-1", "running", "2026-01-01T00:00:00Z"),
      worker("job-1", "complete", "2026-01-02T00:00:00Z"),
      worker("job-2", "pending"),
    ]);
    expect(rows.map((w) => w.id).sort()).toEqual(["job-1", "job-2"]);
    expect(rows.find((w) => w.id === "job-1")?.status).toBe("complete");
  });

  it("workerTasksSliceEqual ignores object identity when fingerprints match", () => {
    const row = worker("job-1", "running");
    expect(workerStoreFingerprint(row)).toBe(workerStoreFingerprint({ ...row }));
    expect(workerTasksSliceEqual([row], [{ ...row }])).toBe(true);
    expect(
      workerTasksSliceEqual([row], [{ ...row, status: "complete" }]),
    ).toBe(false);
    expect(
      workerTasksSliceEqual(
        [{ ...row, max_tool_loops: 40, tool_loops_used: 1 }],
        [{ ...row, max_tool_loops: 40, tool_loops_used: 2 }],
      ),
    ).toBe(false);
    expect(
      workerStoreFingerprint({ ...row, brief: "a" }) ===
        workerStoreFingerprint({ ...row, brief: "b" }),
    ).toBe(false);
    expect(
      workerStoreFingerprint({
        ...row,
        context_usage: { prompt_tokens: 1, window: 100 },
      }) ===
        workerStoreFingerprint({
          ...row,
          context_usage: { prompt_tokens: 1, window: 200 },
        }),
    ).toBe(false);
  });

  it("reconcileWorkerFromListRefresh keeps higher live tool_loops_used", () => {
    const existing: WorkerTask = {
      ...worker("job-1", "running"),
      max_tool_loops: 40,
      tool_loops_used: 12,
      context_usage: {
        prompt_tokens: 8000,
        window: 128000,
        compaction_threshold: 100000,
      },
    };
    const incoming: WorkerTask = {
      ...existing,
      tool_loops_used: 0,
      context_usage: undefined,
    };
    const merged = reconcileWorkerFromListRefresh(existing, incoming);
    expect(merged.tool_loops_used).toBe(12);
    expect(merged.context_usage?.prompt_tokens).toBe(8000);
  });

  it("reconcileSessionWorkersFromList merges per job id", () => {
    const existing = [
      {
        ...worker("job-1", "running"),
        max_tool_loops: 40,
        tool_loops_used: 7,
      },
    ];
    const incoming = [
      {
        ...worker("job-1", "running"),
        max_tool_loops: 40,
      },
    ];
    expect(reconcileSessionWorkersFromList(existing, incoming)[0]?.tool_loops_used).toBe(
      7,
    );
  });

  it("uses the persisted brief for drawer titles", () => {
    expect(
      workerRowTitle({
        ...worker("job-1", "complete"),
        brief: "Verify the upgraded /path/index.html more thoroughly.",
      }),
    ).toBe("Verify the upgraded /path/index.html more thoroughly.");
  });

  it("uses the agent label when brief is absent", () => {
    expect(
      workerRowTitle({
        ...worker("job-1", "complete"),
      }),
    ).toBe("implementer");
  });

  it("disambiguates duplicate drawer titles", () => {
    const peers = [
      { ...worker("job-aaaa-bbbb-cccc", "running"), brief: "Build game" },
      { ...worker("job-dddd-eeee-ffff", "complete"), brief: "Build game" },
    ];
    expect(workerRowTitleDisambiguated(peers[0]!, peers)).toContain("job-aaaa");
  });

  it("maps complete job with partial result to partial row status", () => {
    expect(
      workerRowStatus({
        ...worker("job-p", "complete"),
        result: { status: "partial", summary: "no disk proof" },
      }),
    ).toBe("partial");
  });

  it("maps merged branch to done even when result status is partial", () => {
    expect(
      workerRowStatus({
        ...worker("job-m", "complete"),
        merge_status: "merged",
        result: { status: "partial", summary: "survey only" },
      }),
    ).toBe("done");
  });

  it("maps orphaned overlay to partial row status", () => {
    // An orphaned overlay (parent was rejected) landed nothing on primary
    // and needs coordinator action — rebase onto primary, reject, or recreate.
    expect(
      workerRowStatus({
        ...worker("job-s", "complete"),
        merge_status: "orphaned",
      }),
    ).toBe("partial");
  });

  it("maps rejected overlay to done row status", () => {
    expect(
      workerRowStatus({
        ...worker("job-r", "complete"),
        merge_status: "rejected",
      }),
    ).toBe("done");
  });

  it("maps rebasing overlay to open row status", () => {
    expect(
      workerRowStatus({
        ...worker("job-rb", "complete"),
        merge_status: "rebasing",
      }),
    ).toBe("open");
  });

  it("maps complete branch pending promote to open row status", () => {
    expect(
      workerRowStatus({
        ...worker("job-m", "complete"),
        merge_status: "pending",
      }),
    ).toBe("open");
  });

  it("maps worker_summary open status from result", () => {
    expect(
      workerRowStatus({
        ...worker("job-u", "complete"),
        result: { status: "open", summary: "Created game.py" },
      }),
    ).toBe("open");
  });

  it("sorts running workers before completed", () => {
    const sorted = sortWorkerTasks([
      worker("done", "complete"),
      worker("run", "running"),
    ]);
    expect(sorted[0]?.id).toBe("run");
    expect(workerRowStatus(sorted[0]!)).toBe("running");
    expect(workerRowStatus(sorted[1]!)).toBe("done");
  });

  it("workersForSession filters by parent_session_id", () => {
    const rows = workersForSession(
      [
        worker("a", "running"),
        { ...worker("b", "pending"), parent_session_id: "other" },
      ],
      "sess-1",
    );
    expect(rows.map((w) => w.id)).toEqual(["a"]);
  });


  it("syncWorkerFromTaskToolMessage upserts roster row from task tool result", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "busy",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.installTranscriptBaseline(
      "sess-1",
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          tool_calls: [
            {
              id: "tc1",
              name: "task",
              args: {
                agent_type: "path-explorer",
                brief: { goal: "Map src/ layout", done_when: ["Return results."] },
                scope: { mode: "read", paths: ["src/"] },
                max_tool_loops: 12,
              },
            },
          ],
          created_at: "t",
        },
        {
          id: "t1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content:
            '{"job_id":"job-1","child_session_id":"child-1","agent_type":"path-explorer","status":"enqueued"}',
          tool_result: {
            content:
              '{"job_id":"job-1","child_session_id":"child-1","agent_type":"path-explorer","status":"enqueued"}',
            dispatch: { worker_id: "job-1", child_session_id: "child-1", agent_type: "path-explorer" },
            tool: "task",
            tool_call_id: "tc1",
            assistant_message_id: "a1",
            tool_args: {
              agent_type: "path-explorer",
              brief: { goal: "Map src/ layout", done_when: ["Return results."] },
              scope: { mode: "read", paths: ["src/"] },
              max_tool_loops: 12,
            },
          },
          created_at: "t",
        },
      ],
      0,
    );
    syncWorkerFromTaskToolMessage(store, "sess-1", store.state.messages[1]!);
    expect(store.state.workers[0]).toMatchObject({
      id: "job-1",
      parent_session_id: "sess-1",
      child_session_id: "child-1",
      brief: "Map src/ layout",
    });
    expect(sessionWorkers(store, "sess-1")).toHaveLength(1);
  });

  it("resolves the persisted brief and parent dispatch params", () => {
    const row = {
      ...worker("job-1", "running"),
      agent_type: "implementer",
      brief: "## Goal\nAdd tests",
    };
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "tc1",
            name: "task",
            args: {
              agent_type: "implementer",
              brief: { goal: "## Goal\nAdd tests", done_when: ["Return results."] },
              files: ["src/foo.ts"],
              scope: { mode: "write", paths: ["src/foo.ts"] },
            },
          },
        ],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-1","status":"enqueued"}',
        tool_result: {
          content: '{"job_id":"job-1","status":"enqueued"}',
          dispatch: { worker_id: "job-1", agent_type: "implementer" },
          tool: "task",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          tool_args: {
            agent_type: "implementer",
            brief: { goal: "## Goal\nAdd tests", done_when: ["Return results."] },
            files: ["src/foo.ts"],
            scope: { mode: "write", paths: ["src/foo.ts"] },
          },
        },
        created_at: "t",
      },
    ];
    expect(workerDispatchBrief(row)).toBe("## Goal\nAdd tests");
    expect(workerDispatchParamRows(row, messages)).toEqual([
      { label: "Agent", value: "implementer" },
      { label: "Scope", value: "write · focus: src/foo.ts" },
      { label: "Files", value: "src/foo.ts" },
    ]);
  });

  it("uses the persisted worker brief", () => {
    const row = {
      ...worker("job-1", "running"),
      brief: "Composed worker brief",
    };
    expect(workerDispatchBrief(row)).toBe("Composed worker brief");
  });

  it("normalizeWorkerEvent fills parent_session_id from active session", () => {
    expect(
      normalizeWorkerEvent(
        { worker_id: "job-1", status: "running" },
        "sess-1",
      ).parent_session_id,
    ).toBe("sess-1");
  });

  it("workerEventFromTaskToolMessage seeds max_tool_loops from task() args", () => {
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "tc1",
            name: "task",
            args: {
              agent_type: "implementer",
              brief: { goal: "Build feature", done_when: ["Return results."] },
              max_tool_loops: 25,
            },
          },
        ],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"job_id":"job-1","status":"enqueued"}',
        tool_result: {
          content: '{"job_id":"job-1","status":"enqueued"}',
          dispatch: { worker_id: "job-1", agent_type: "implementer" },
          tool: "task",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          tool_args: {
            agent_type: "implementer",
            brief: { goal: "Build feature", done_when: ["Return results."] },
            max_tool_loops: 25,
          },
        },
        created_at: "t",
      },
    ];
    const ev = workerEventFromTaskToolMessage("sess-1", messages, messages[1]!);
    expect(ev?.max_tool_loops).toBe(25);
  });

  it("matchWorkerForTask returns undefined without wire job_id or child_session_id", () => {
    const w1 = worker("job-1", "running");
    const part: ToolPartView = {
      id: "tc1",
      toolCallId: "tc1",
      assistantMessageId: "assistant-message",
      messageId: "m1",
      tool: "task",
      kind: "task",
      status: "running",
      args: {
        agent_type: "repo-researcher",
        brief: { goal: "map repo", done_when: ["Return results."] },
      },
      output: null,
      error: null,
    };
    expect(
      matchWorkerForTask(part, [w1], { sessionId: "sess-1" }),
    ).toBeUndefined();
  });

  it("matchWorkerForTask binds distinct workers for two task cards", () => {
    const w1 = worker("job-1", "complete", "2026-01-01T00:00:00Z");
    const w2 = worker("job-2", "running", "2026-01-02T00:00:00Z");
    const part = (jobId: string): ToolPartView => ({
      id: `tc-${jobId}`,
      toolCallId: `tc-${jobId}`,
      assistantMessageId: "assistant-message",
      messageId: "m1",
      tool: "task",
      kind: "task",
      status: "completed",
      args: {
        agent_type: "implementer",
        brief: { goal: "build game", done_when: ["Return results."] },
      },
      output: JSON.stringify({ job_id: jobId, status: "enqueued" }),
      error: null,
      jobId,
    });
    const items: TranscriptItem[] = [
      { kind: "tool", key: "t1", part: part("job-1") },
      { kind: "tool", key: "t2", part: part("job-2") },
    ];
    const matches = taskWorkerMatchesForTranscript(items, [w1, w2], "sess-1");
    expect(matches.get("t1")?.id).toBe("job-1");
    expect(matches.get("t2")?.id).toBe("job-2");
    expect(
      matchWorkerForTask(part("job-2"), [w1, w2], {
        sessionId: "sess-1",
        excludeIds: new Set(["job-1"]),
      })?.id,
    ).toBe("job-2");
  });

  it("derives failed-dispatch workers from rejected task tools", () => {
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "call_task_1",
            name: "task",
            args: {
              agent_type: "repo-researcher",
              brief: { goal: "Focus on the CLI.", done_when: ["Return results."] },
            },
          },
        ],
        created_at: "t1",
      },
      {
        id: "tr1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content:
          "Rejected: You called task but no progress checklist exists yet.\n\nCode: PROGRESS_MISSING",
        tool_result: {
          content:
            "Rejected: You called task but no progress checklist exists yet.\n\nCode: PROGRESS_MISSING",
          outcome: "rejected" as const,
          feedback: [{ code: "PROGRESS_MISSING" }],
          tool: "task",
          tool_call_id: "call_task_1",
          assistant_message_id: "a1",
          tool_args: {
            agent_type: "repo-researcher",
            brief: { goal: "Focus on the CLI.", done_when: ["Return results."] },
          },
        },
        created_at: "t2",
      },
    ];
    const derived = failedDispatchWorkersFromMessages("sess-1", messages);
    expect(derived).toHaveLength(1);
    expect(derived[0]?.id).toBe(failedDispatchWorkerId("call_task_1"));
    expect(derived[0]?.status).toBe("failed");
    expect(derived[0]?.agent_type).toBe("repo-researcher");
    expect(derived[0]?.failure?.code).toBe("PROGRESS_MISSING");
    expect(
      taskDispatchCallForWorker(messages, derived[0]!.id)?.args.agent_type,
    ).toBe("repo-researcher");
    const part: ToolPartView = {
      id: "a1:call_task_1",
      toolCallId: "call_task_1",
      assistantMessageId: "assistant-message",
      messageId: "a1",
      tool: "task",
      kind: "task",
      status: "error",
      args: {
        agent_type: "repo-researcher",
        brief: { goal: "Focus on the CLI.", done_when: ["Return results."] },
      },
      error: messages[1]!.content,
    };
    expect(matchWorkerForTask(part, derived, { sessionId: "sess-1" })?.id).toBe(
      derived[0]!.id,
    );
  });

  it("sessionWorkerRows keeps history and gates failed on verbose", () => {
    const rows = [
      worker("job-run", "running", "2026-01-01T00:02:00Z"),
      worker("job-done", "complete", "2026-01-01T00:01:00Z"),
      worker("job-fail", "failed", "2026-01-01T00:00:00Z"),
    ];
    expect(liveWorkerRows(rows).map((w) => w.id)).toEqual(["job-run"]);
    expect(sessionWorkerRows(rows, { verboseMode: false }).map((w) => w.id)).toEqual([
      "job-run",
      "job-done",
    ]);
    expect(sessionWorkerRows(rows, { verboseMode: true }).map((w) => w.id)).toEqual([
      "job-run",
      "job-done",
      "job-fail",
    ]);
  });
});
