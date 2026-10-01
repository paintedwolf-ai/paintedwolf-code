import { describe, expect, it } from "vitest";
import {
  taskActivityDisplay,
  taskAgentType,
  taskCardStatus,
  taskStatus,
} from "./task-card-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import type { Message, WorkerSummaryMeta, WorkerTask } from "../../api/types.ts";

function taskPart(overrides: Partial<ToolPartView> = {}): ToolPartView {
  return {
    id: "tc1",
    toolCallId: "tc1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "task",
    kind: "task",
    status: "completed",
    output: `<task job_id="job-1" state="partial"><summary>x</summary></task>`,
    error: null,
    ...overrides,
  };
}

function workerSummary(status: WorkerSummaryMeta["status"]): WorkerSummaryMeta {
  return {
    worker_id: "job-1",
    child_session_id: "child-1",
    agent_type: "implementer",
    status,
    envelope: `<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="${status}"></task>`,
  };
}

describe("task-card-model", () => {
  it.each([
    ["canceled", "canceled"],
    ["failed", "error"],
  ] as const)("preserves current %s outcomes across delayed transcript projections", (status, expected) => {
    const worker: WorkerTask = {
      id: "job-1",
      parent_session_id: "sess-1",
      agent_type: "implementer",
      status,
      created_at: "2026-01-01T00:00:00Z",
    };
    for (const toolStatus of ["running", "completed", "error"] as const) {
      for (const summary of [undefined, workerSummary("partial"), workerSummary("failed")]) {
        expect(taskCardStatus(taskPart({ status: toolStatus, workerSummary: summary }), worker)).toBe(expected);
      }
    }
  });

  it("taskCardStatus ignores completion envelope state in tool output", () => {
    const partialEnvelope =
      '<task job_id="job-1" state="partial"><summary>no proof</summary></task>';
    expect(taskCardStatus(taskPart({ output: partialEnvelope }))).toBe("done");
    expect(taskCardStatus(taskPart({ status: "running" }))).toBe("running");
  });

  it("taskCardStatus reads the persisted summary without current merge state", () => {
    expect(
      taskCardStatus(
        taskPart({
          workerSummary: workerSummary("partial"),
        }),
      ),
    ).toBe("partial");
    expect(
      taskCardStatus(
        taskPart({
          workerSummary: workerSummary("needs_decision"),
        }),
      ),
    ).toBe("needs_decision");
    expect(
      taskCardStatus(
        taskPart({
          workerSummary: workerSummary("canceled"),
        }),
      ),
    ).toBe("canceled");
    expect(taskCardStatus(taskPart())).toBe("done");
    const runningWorker: WorkerTask = {
      id: "job-1",
      parent_session_id: "sess-1",
      agent_type: "implementer",
      status: "running",
      created_at: "2026-01-01T00:00:00Z",
    };
    expect(taskCardStatus(taskPart(), runningWorker)).toBe("running");
    expect(taskCardStatus(
      taskPart({ workerSummary: workerSummary("partial") }),
      runningWorker,
    )).toBe("partial");
    expect(taskCardStatus(
      taskPart({ workerSummary: workerSummary("needs_decision") }),
      { ...runningWorker, status: "held", merge_status: "pending" },
    )).toBe("needs_decision");
  });

  it.each([
    ["pending", "open"],
    ["applying", "open"],
    ["rebasing", "open"],
    ["merged", "done"],
    ["rejected", "done"],
    ["orphaned", "partial"],
  ] as const)("uses current %s merge state over an open summary", (mergeStatus, expected) => {
    const worker: WorkerTask = {
      id: "job-1",
      agent_type: "implementer",
      status: "complete",
      merge_status: mergeStatus,
      result: { status: "open" },
      created_at: "2026-01-01T00:00:00Z",
    };
    expect(taskCardStatus(taskPart({ workerSummary: workerSummary("open") }), worker))
      .toBe(expected);
  });

  it("taskAgentType uses durable worker identity and immutable call args", () => {
    const worker: WorkerTask = {
      id: "job-cv",
      parent_session_id: "sess-1",
      agent_type: "implementer",
      status: "complete",
      created_at: "2026-01-01T00:00:00Z",
    };
    expect(
      taskAgentType(taskPart({ args: { agent_type: "code-reviewer" } }), worker),
    ).toBe("implementer");
    expect(
      taskAgentType(taskPart({ args: { agent_type: "code-reviewer" } })),
    ).toBe("code-reviewer");
    expect(
      taskAgentType(taskPart({
        args: {},
        output:
          '<task job_id="job-cv" agent_type="implementer" state="complete"><summary>ok</summary></task>',
      })),
    ).toBe(
      "worker",
    );
  });

  it("taskStatus maps tool part status only", () => {
    expect(taskStatus(taskPart({ status: "error" }))).toBe("error");
  });

  it("taskActivityDisplay surfaces the live token heartbeat over a stale tool line", () => {
    const part = taskPart({ status: "running" });
    const toolLine: Message = {
      id: "w-tool",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [{ id: "c1", name: "read", args: { path: "index.html" } }],
      created_at: "2026-01-01T00:00:00Z",
    };
    const streaming: Message = {
      id: "w-stream",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "rewriting the whole file…",
      status: "streaming",
      generating_tokens: 8200,
      created_at: "2026-01-01T00:00:01Z",
    };
    expect(
      taskActivityDisplay(part, { workerMessages: [toolLine, streaming] }),
    ).toBe("generating ~8.2k tokens");
    expect(
      taskActivityDisplay(part, {
        workerMessages: [toolLine, { ...streaming, status: "complete" }],
      }),
    ).toBe("read · index.html");
  });

  it("taskActivityDisplay surfaces large workspace preparation", () => {
    const worker: WorkerTask = {
      id: "job-1",
      agent_type: "implementer",
      status: "running",
      workspace_preparation: {
        strategy: "bridge_cow",
        stage: "materializing_seed",
        files: 125_000,
        bytes: 12 * 1024 ** 3,
        total_bytes: 30 * 1024 ** 3,
      },
      created_at: "2026-08-01T00:00:00Z",
    };
    expect(taskActivityDisplay(taskPart(), { worker })).toBe(
      "Building reusable workspace cache · 12.0 GB of 30.0 GB",
    );
  });
});
