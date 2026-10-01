import { describe, expect, it } from "vitest";
import { at, keyAt } from "../../test/at.ts";
import fixtures from "../fixtures/tool-messages.json";
import type { Message } from "../../api/types.ts";
import {
  classifyToolKind,
  isBenignToolResult,
  isCoordinatorInternalTool,
  shouldHideCoordinatorInternalTool,
  shouldOmitToolPartFromTranscript,
  toolPartDisplayStatus,
  toolPartFromCall,
  toolCompletionLabel,
  toolPartSummaryTitle,
  toolResultText,
} from "./tool-part-model.ts";
import {
  applyBackgroundProcessEvent,
  applyBackgroundProcessOutputs,
  resetBackgroundProcessStoreForTests,
} from "./background-process-store.ts";
import { messagesToTranscriptItems } from "../transcript/projection/transcript-items.ts";
import { subagentType } from "../task/task-card-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";

const f = fixtures as Record<string, Message>;

// The fixture file is the contract for these tests; a missing key is a broken
// fixture, not a case to assert on.
const msg = (name: string): Message => keyAt(f, name);
const firstToolCall = (message: Message) => at(message.tool_calls ?? [], 0);

describe("tool-part-model", () => {
  it("keeps yielded command/verify results running until the process exits", () => {
    resetBackgroundProcessStoreForTests();
    const yielded: Message = {
      id: "msg-tool",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content:
        '[command#1]\n{"running":true,"handle":"h-1","stages":[{"command":"./task test:short","exit_code":-1}],"waited_ms":30000}',
      tool_result: {
        content:
          '[command#1]\n{"running":true,"handle":"h-1","stages":[{"command":"./task test:short","exit_code":-1}],"waited_ms":30000}',
        outcome: "completed",
        process: { handle: "h-1", running: true },
      },
      created_at: "t",
    };
    const part = toolPartFromCall(
      {
        id: "tc-cmd",
        name: "command",
        args: { command: "./task test:short" },
      },
      yielded,
      "msg-asst",
    );
    expect(part.status).toBe("running");
    expect(part.process?.handle).toBe("h-1");
    expect(toolResultText(part)).toContain('"running":true');
    expect(toolPartDisplayStatus(part, "s1")).toBe("running");

    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "h-1",
      stream: "stdout",
      text: "ok\n",
      end_offset: 3,
      running: true,
    });
    expect(toolPartDisplayStatus(part, "s1")).toBe("running");

    applyBackgroundProcessEvent({
      session_id: "s1",
      process_id: "h-1",
      stream: "exit",
      end_offset: 3,
      running: false,
      exit_code: 0,
    });
    expect(toolPartDisplayStatus(part, "s1")).toBe("completed");

    // After hydrate drops the process, treat as finished.
    resetBackgroundProcessStoreForTests();
    applyBackgroundProcessOutputs("s1", []);
    expect(toolPartDisplayStatus(part, "s1")).toBe("completed");
  });

  it("classifies native tool names", () => {
    expect(classifyToolKind("read")).toBe("read");
    expect(classifyToolKind("write")).toBe("write");
    expect(classifyToolKind("command")).toBe("command");
    expect(classifyToolKind("pack_board")).toBe("generic");
    expect(classifyToolKind("delegate_dispatch")).toBe("task");
    expect(classifyToolKind("unknown_tool")).toBe("generic");
  });

  it("subagentType labels delegate_dispatch as delegation", () => {
    const part: ToolPartView = {
      id: "tc-d",
      toolCallId: "tc-d",
      assistantMessageId: "assistant-message",
      messageId: "m1",
      tool: "delegate_dispatch",
      kind: "task",
      status: "completed",
      args: { leg_id: "leg-1" },
      output: null,
      error: null,
    };
    expect(subagentType(part)).toBe("delegation");
  });

  it("marks coordinator orchestration tools as internal to parent chat", () => {
    expect(isCoordinatorInternalTool("pack_board")).toBe(true);
    expect(isCoordinatorInternalTool("git_status")).toBe(true);
    expect(isCoordinatorInternalTool("update_progress")).toBe(true);
    expect(isCoordinatorInternalTool("read")).toBe(false);
  });

  it("hides orientation tools in parent chat only", () => {
    expect(shouldHideCoordinatorInternalTool("git_status", "chat")).toBe(true);
    expect(shouldHideCoordinatorInternalTool("git_status", "worker")).toBe(false);
    expect(shouldHideCoordinatorInternalTool("find", "chat")).toBe(false);
  });

  it("toolPartSummaryTitle includes the count of additional array targets", () => {
    const part: ToolPartView = {
      id: "tc-stat",
      toolCallId: "tc-stat",
      assistantMessageId: "assistant-message",
      messageId: "m1",
      tool: "stat",
      kind: "generic",
      status: "completed",
      title: "stat",
      args: { paths: ["internal/foo.go", "internal/bar.go"] },
      output: "{}",
      error: null,
    };
    expect(toolPartSummaryTitle(part)).toBe("internal/foo.go +1 more");
  });

  it("projects typed lifecycle completion without reading result text", () => {
    const part = toolPartFromCall(
      { id: "tc-workflow", name: "workflow_advance", args: {} },
      {
        id: "msg-tool",
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "completed",
        tool_result: {
          content: "completed",
          completion: { operation: "workflow_advance", state: "blocked" },
        },
        created_at: "t",
      },
      "msg-assistant",
    );
    expect(toolCompletionLabel(part)).toBe("workflow advance · blocked");
  });

  it("pairs tool call with tool result message", () => {
    const part = toolPartFromCall(
      firstToolCall(msg("assistantWithRead")),
      msg("toolReadResult"),
      msg("assistantWithRead").id,
    );
    expect(part.kind).toBe("read");
    expect(part.status).toBe("completed");
    expect(part.output).toContain("package main");
  });

  it("maps tool_result.outcome to part status", () => {
    const benignResultMsg: Message = {
      id: "msg-tool",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: '{"board_chars":0}',
      tool_result: {
        content: '{"board_chars":0}',
        outcome: "completed",
        codes: ["BOARD_EMPTY_SKIP_TO_VERIFY"],
        ui_visibility: "benign",
      },
      created_at: new Date().toISOString(),
    };
    const benignCompleted = toolPartFromCall(
      {
        id: "tc-board",
        name: "pack_board",
        args: {},
      },
      benignResultMsg,
      "msg-asst",
    );
    expect(benignCompleted.status).toBe("completed");
    expect(isBenignToolResult(benignResultMsg)).toBe(true);

    const rejected = toolPartFromCall(
      { id: "tc-read", name: "read", args: { path: "x" } },
      {
        id: "msg-tool",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "Rejected: COORDINATOR_READ_OUTSIDE_SCOPE\nCode: COORDINATOR_READ_OUTSIDE_SCOPE",
        tool_result: {
          content: "Rejected: COORDINATOR_READ_OUTSIDE_SCOPE\nCode: COORDINATOR_READ_OUTSIDE_SCOPE",
          outcome: "rejected",
          codes: ["COORDINATOR_READ_OUTSIDE_SCOPE"],
          ui_visibility: "benign",
        },
        created_at: "t",
      },
      "msg-asst",
    );
    expect(rejected.status).toBe("error");
  });

  it("uses invocation receipts as the result authority", () => {
    const result: Message = {
      id: "msg-tool",
      role: "tool",
      origin: "tool",
      authority: "none",
      trust_tier: "untrusted",
      content: "",
      tool_result: {
        content: "",
        outcome: "completed",
        invocation: {
          id: "inv-1",
          tool: "read",
          tool_call_id: "tc-read",
          contract_digest: "contract",
          args_digest: "args",
          owner: "filesystem",
          lifecycle: "read_only",
          reversibility: "reversible",
          evidence_policy: "result",
          recovery_policy: "none",
          status: "completed",
          invoked: true,
          evidence: { kind: "result", ref: "msg-tool" },
          started_at: "2026-08-13T00:00:00Z",
          settled_at: "2026-08-13T00:00:01Z",
        },
      },
      created_at: "2026-08-13T00:00:01Z",
    };
    const completed = toolPartFromCall(
      { id: "tc-read", name: "read", args: { path: "README.md" } },
      result,
      "msg-assistant",
    );
    expect(completed.status).toBe("completed");
    expect(completed.invocation?.owner).toBe("filesystem");

    if (result.tool_result?.invocation) {
      result.tool_result.invocation.status = "interrupted";
    }
    expect(
      toolPartFromCall(
        { id: "tc-read", name: "read", args: { path: "README.md" } },
        result,
        "msg-assistant",
      ).status,
    ).toBe("error");
  });

  it("builds transcript tool items from fixture sequence", () => {
    const items = messagesToTranscriptItems([
      msg("assistantWithRead"),
      msg("toolReadResult"),
      msg("assistantWithDelegate"),
      msg("toolDelegateResult"),
    ]);
    const tools = items.filter((i) => i.kind === "tool");
    expect(tools).toHaveLength(2);
    expect(tools.map((t) => t.part.tool)).toEqual(["read", "delegate_dispatch"]);
  });

  it("shows enqueued worker dispatch results stamped normal", () => {
    const result: Message = {
      id: "tr1",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: '{"job_id":"job-2","status":"enqueued"}',
      tool_result: {
        content: '{"job_id":"job-2","status":"enqueued"}',
        dispatch: { worker_id: "job-2" },
        outcome: "completed",
        codes: ["BANNER_TASK_QUEUED"],
        ui_visibility: "normal",
      },
      created_at: "t",
    };
    const chat = { verboseMode: false, layout: "chat" as const };
    expect(
      shouldOmitToolPartFromTranscript(result, "task", chat),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(result, "delegate_dispatch", chat),
    ).toBe(false);
  });

  it("shows only long-running tools in chat before a result", () => {
    const chat = { verboseMode: false, layout: "chat" as const };
    expect(shouldOmitToolPartFromTranscript(undefined, "read", chat)).toBe(
      true,
    );
    expect(shouldOmitToolPartFromTranscript(undefined, "task", chat)).toBe(true);
    expect(
      shouldOmitToolPartFromTranscript(undefined, "delegate_dispatch", chat),
    ).toBe(true);
    expect(
      shouldOmitToolPartFromTranscript(undefined, "summarize", chat),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(undefined, "command", chat),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(undefined, "verify", chat),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(undefined, "read", {
        verboseMode: true,
        layout: "chat",
      }),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(
        { id: "t", role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const, content: "ok", created_at: "t" },
        "read",
        chat,
      ),
    ).toBe(false);
  });

  it("hides only benign rejects for tools that were never shown in-flight", () => {
    const chat = { verboseMode: false, layout: "chat" as const };
    const benignRejected: Message = {
      id: "tr-rej-benign",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "Rejected: TOOL_ARGS_INVALID\nCode: TOOL_ARGS_INVALID",
      tool_result: {
        content: "Rejected: TOOL_ARGS_INVALID\nCode: TOOL_ARGS_INVALID",
        outcome: "rejected",
        codes: ["TOOL_ARGS_INVALID"],
        ui_visibility: "benign",
      },
      created_at: "t",
    };
    // Benign results hide for ordinary tools when verbose is off.
    expect(shouldOmitToolPartFromTranscript(benignRejected, "read", chat)).toBe(
      true,
    );
    expect(
      shouldOmitToolPartFromTranscript(benignRejected, "totally_fake_tool", chat),
    ).toBe(true);
    expect(
      shouldOmitToolPartFromTranscript(benignRejected, "read", {
        verboseMode: true,
        layout: "chat",
      }),
    ).toBe(false);

    // Long-running tools stay visible for any settled outcome.
    expect(
      shouldOmitToolPartFromTranscript(benignRejected, "summarize", chat),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(benignRejected, "survey_repo", chat),
    ).toBe(false);
    // Worker cards follow ui_visibility after settle — benign hides.
    expect(shouldOmitToolPartFromTranscript(benignRejected, "task", chat)).toBe(
      true,
    );
    expect(
      shouldOmitToolPartFromTranscript(benignRejected, "task", {
        verboseMode: true,
        layout: "chat",
      }),
    ).toBe(false);

    const benignCompletedDoomLoop: Message = {
      id: "tr-doom",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "ok\n>>> Tool feedback\nCode: DOOM_LOOP_REPEAT_WARN",
      tool_result: {
        content: "ok\n>>> Tool feedback\nCode: DOOM_LOOP_REPEAT_WARN",
        outcome: "completed",
        codes: ["DOOM_LOOP_REPEAT_WARN"],
        ui_visibility: "benign",
      },
      created_at: "t",
    };
    expect(
      shouldOmitToolPartFromTranscript(benignCompletedDoomLoop, "summarize", chat),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(
        benignCompletedDoomLoop,
        "survey_repo",
        chat,
      ),
    ).toBe(false);
    // Non-long-running completed+benign still hides when verbose is off.
    expect(
      shouldOmitToolPartFromTranscript(benignCompletedDoomLoop, "read", chat),
    ).toBe(true);

    // A capture row needs no visibility rule of its own: the host stamps any
    // result carrying a visual `normal`, so it lands on the ordinary path. The
    // still itself is folded into this row rather than shown beside it.
    const captureWithVisual: Message = {
      id: "tr-capture-visual",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "ok\n>>> Tool feedback\nCode: DOOM_LOOP_REPEAT_WARN",
      tool_result: {
        content: "ok\n>>> Tool feedback\nCode: DOOM_LOOP_REPEAT_WARN",
        outcome: "completed",
        codes: ["DOOM_LOOP_REPEAT_WARN"],
        ui_visibility: "normal",
        visual: {
          id: "art-1",
          mime: "image/png",
          store_ref: true,
          source: "capture",
          caption: "board",
        },
      },
      created_at: "t",
    };
    expect(
      shouldOmitToolPartFromTranscript(captureWithVisual, "capture_page", chat),
    ).toBe(false);

    // Normal visibility rejects stay on the transcript.
    const normalRejected: Message = {
      id: "tr-rej-normal",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "Rejected: tool not available",
      tool_result: {
        content: "Rejected: tool not available",
        outcome: "rejected",
        ui_visibility: "normal",
      },
      created_at: "t",
    };
    expect(
      shouldOmitToolPartFromTranscript(normalRejected, "totally_fake_tool", chat),
    ).toBe(false);
    expect(shouldOmitToolPartFromTranscript(normalRejected, "read", chat)).toBe(
      false,
    );

    const errored: Message = {
      id: "tr-err",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content: "file not found",
      tool_result: { content: "file not found", outcome: "error" },
      created_at: "t",
    };
    expect(shouldOmitToolPartFromTranscript(errored, "read", chat)).toBe(false);
  });

  it("worker layout shows benign and in-flight tools regardless of verbose", () => {
    const worker = { verboseMode: false, layout: "worker" as const };
    expect(
      shouldOmitToolPartFromTranscript(undefined, "read", worker),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(
        {
          id: "tr1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "Rejected",
          tool_result: { content: "Rejected", ui_visibility: "benign" },
          created_at: "t",
        },
        "read",
        worker,
      ),
    ).toBe(false);
    expect(
      shouldOmitToolPartFromTranscript(undefined, "git_status", worker),
    ).toBe(false);
  });

  it("hides host-stamped benign PROGRESS_MISSING write rejects when verbose is off", () => {
    const chat = { verboseMode: false, layout: "chat" as const };
    const rejected: Message = {
      id: "tr-pm",
      role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      content:
        "Rejected: You called write but no session ## Progress checklist exists yet.\n\nCode: PROGRESS_MISSING",
      tool_result: {
        content:
          "Rejected: You called write but no session ## Progress checklist exists yet.\n\nCode: PROGRESS_MISSING",
        outcome: "rejected",
        codes: ["PROGRESS_MISSING"],
        ui_visibility: "benign",
      },
      created_at: "t",
    };
    expect(shouldOmitToolPartFromTranscript(rejected, "write", chat)).toBe(true);
    expect(shouldOmitToolPartFromTranscript(rejected, "edit", chat)).toBe(true);
    expect(
      shouldOmitToolPartFromTranscript(rejected, "replace_lines", chat),
    ).toBe(true);
    expect(
      shouldOmitToolPartFromTranscript(rejected, "write", {
        verboseMode: true,
        layout: "chat",
      }),
    ).toBe(false);
    // Only host-stamped ui_visibility hides the row.
    const unstamped: Message = {
      ...rejected,
      id: "tr-pm-2",
      tool_result: {
        content: rejected.content,
        outcome: "rejected",
        codes: ["PROGRESS_MISSING"],
      },
    };
    expect(shouldOmitToolPartFromTranscript(unstamped, "write", chat)).toBe(
      false,
    );
  });
});
