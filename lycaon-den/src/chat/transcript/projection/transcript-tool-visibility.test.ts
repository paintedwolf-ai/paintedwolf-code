import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { type TranscriptItem } from "./transcript-item-model.ts";
import { taskJobIdFromPart } from "../../task/task-result-model.ts";

describe("transcript-items", () => {

  it("hides benign tool cards when verbose mode is off", () => {
    const items = messagesToTranscriptItems(
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          tool_calls: [{ id: "tc1", name: "read", args: { path: "src/x.go" } }],
          created_at: "t",
        },
        {
          id: "tr1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "Rejected: COORDINATOR_READ_OUTSIDE_SCOPE\nCode: COORDINATOR_READ_OUTSIDE_SCOPE",
          tool_result: {
            content: "Rejected: COORDINATOR_READ_OUTSIDE_SCOPE\nCode: COORDINATOR_READ_OUTSIDE_SCOPE",
            tool: "read",
            tool_call_id: "tc1",
            assistant_message_id: "a1",
            outcome: "rejected",
            codes: ["COORDINATOR_READ_OUTSIDE_SCOPE"],
            ui_visibility: "benign",
          },
          created_at: "t",
        },
      ],
      { verboseMode: false },
    );
    expect(items.some((i) => i.kind === "tool")).toBe(false);
  });

  it("hides workflow gate blocks when verbose mode is off", () => {
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1", name: "workflow_advance", args: {} }],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "Gate blocked\nCode: WORKFLOW_GATE_BLOCKED",
        tool_result: {
          content: "Gate blocked\nCode: WORKFLOW_GATE_BLOCKED",
          tool: "workflow_advance",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          outcome: "rejected" as const,
          codes: ["WORKFLOW_GATE_BLOCKED"],
          ui_visibility: "benign" as const,
        },
        created_at: "t",
      },
    ];
    expect(
      messagesToTranscriptItems(messages, { verboseMode: false }).some(
        (i) => i.kind === "tool",
      ),
    ).toBe(false);
    expect(
      messagesToTranscriptItems(messages, { verboseMode: true }).some(
        (i) => i.kind === "tool",
      ),
    ).toBe(true);
  });

  it("hides PROGRESS_MISSING write cards when verbose mode is off", () => {
    const reject =
      "Rejected: You called **`write`** but no session **`## Progress`** checklist exists yet.\n\nCode: PROGRESS_MISSING";
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          { id: "tc1", name: "write", args: { path: "a.go", content: "x" } },
        ],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: reject,
        tool_result: {
          content: reject,
          outcome: "rejected" as const,
          codes: ["PROGRESS_MISSING"],
          tool: "write",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          ui_visibility: "benign" as const,
        },
        created_at: "t",
      },
    ];
    expect(
      messagesToTranscriptItems(messages, { verboseMode: false }).some(
        (i) => i.kind === "tool",
      ),
    ).toBe(false);
    expect(
      messagesToTranscriptItems(messages, { verboseMode: true }).some(
        (i) => i.kind === "tool" && i.part.tool === "write",
      ),
    ).toBe(true);
  });

  it("shows only long-running in-flight tools on an internal assistant row", () => {
    const items = messagesToTranscriptItems(
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          visibility: "internal",
          content: "",
          tool_calls: [
            { id: "tc-cmd", name: "command", args: { command: "ls" } },
            { id: "tc-read", name: "read", args: { path: "a.go" } },
          ],
          created_at: "t",
        },
      ],
      { verboseMode: false },
    );
    const tools = items.filter((i) => i.kind === "tool");
    expect(tools.map((i) => (i.kind === "tool" ? i.part.tool : ""))).toEqual(["command"]);
  });

  it("defers ordinary in-flight tools when verbose mode is off", () => {
    const items = messagesToTranscriptItems(
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          tool_calls: [{ id: "tc1", name: "read", args: { path: "src/x.go" } }],
          created_at: "t",
        },
      ],
      { verboseMode: false },
    );
    expect(items.some((i) => i.kind === "tool")).toBe(false);
  });

  it("shows worker cards after dispatch settles", () => {
    const dispatch: Message = {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "",
      tool_calls: [
        {
          id: "tc1",
          name: "task",
          args: { agent_type: "path-explorer", brief: { goal: "map file", done_when: ["Return results."] } },
        },
      ],
      created_at: "t",
    } as Message;

    const inFlight = messagesToTranscriptItems([dispatch], {
      verboseMode: false,
    });
    expect(
      inFlight.some((i) => i.kind === "tool" && i.part.tool === "task"),
    ).toBe(false);

    const enqueued = messagesToTranscriptItems(
      [
        dispatch,
        {
          id: "tr1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "enqueued",
          created_at: "t1",
          tool_result: {
            content: "enqueued",
            tool: "task",
            tool_call_id: "tc1",
            assistant_message_id: "a1",
            ui_visibility: "normal",
            outcome: "completed",
          },
        } as Message,
      ],
      { verboseMode: false },
    );
    expect(
      enqueued.some((i) => i.kind === "tool" && i.part.tool === "task"),
    ).toBe(true);
  });

  // Tool rows survive benign settles; worker cards follow ui_visibility.
  type VisibleToolRow = Extract<TranscriptItem, { kind: "tool" }>;
  const XML_TASK_ENVELOPE =
    '<task job_id="job-xml" child_session_id="child-1" agent_type="implementer" state="open"/>';
  it.each<{
    name: string;
    toolCall: Record<string, unknown>;
    toolResult: Record<string, unknown>;
    verify: (tool: VisibleToolRow) => void;
  }>([
    {
      name: "summarize survives a benign TOOL_ARGS_INVALID reject",
      toolCall: {
        id: "tc-sum",
        name: "summarize",
        args: { task: "path: lycaon/internal/session — how does abort work?" },
      },
      toolResult: {
        content: "Rejected: TOOL_ARGS_INVALID\nCode: TOOL_ARGS_INVALID",
        outcome: "rejected",
        codes: ["TOOL_ARGS_INVALID"],
        tool: "summarize",
        tool_call_id: "tc-sum",
        ui_visibility: "benign",
      },
      verify: (tool) => expect(tool.part.status).toBe("error"),
    },
    {
      name: "survey_repo survives a benign TOOL_ARGS_INVALID reject",
      toolCall: { id: "tc-sr", name: "survey_repo", args: { focus: "layout" } },
      toolResult: {
        content: "Rejected: TOOL_ARGS_INVALID\nCode: TOOL_ARGS_INVALID",
        outcome: "rejected",
        codes: ["TOOL_ARGS_INVALID"],
        tool: "survey_repo",
        tool_call_id: "tc-sr",
        ui_visibility: "benign",
      },
      verify: (tool) => expect(tool.part.status).toBe("error"),
    },
    {
      name: "task keeps its enqueued JSON card on worker_dispatch BANNER_TASK_QUEUED",
      toolCall: {
        id: "tc1",
        name: "task",
        args: { agent_type: "path-explorer", brief: { goal: "map file", done_when: ["Return results."] } },
      },
      toolResult: {
        content: '{"agent_type":"path-explorer","job_id":"job-2","status":"enqueued"}',
        outcome: "completed",
        codes: ["BANNER_TASK_QUEUED"],
        ui_visibility: "normal",
        dispatch: { worker_id: "job-2" },
      },
      verify: (tool) =>
        expect(tool.part.jobId).toBe("job-2"),
    },
    {
      name: "task keeps its enqueued XML card on worker_dispatch BANNER_TASK_QUEUED",
      toolCall: {
        id: "tc1",
        name: "task",
        args: { agent_type: "implementer", brief: { goal: "fix tests", done_when: ["Return results."] } },
      },
      toolResult: {
        content: XML_TASK_ENVELOPE,
        outcome: "completed",
        codes: ["BANNER_TASK_QUEUED"],
        ui_visibility: "normal",
        dispatch: { worker_id: "job-xml" },
      },
      verify: (tool) => expect(taskJobIdFromPart(tool.part)).toBe("job-xml"),
    },
  ])("keeps tool row after settle — $name", ({ toolCall, toolResult, verify }) => {
    const items = messagesToTranscriptItems(
      [
        { id: "a1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "", tool_calls: [toolCall], created_at: "t" },
        {
          id: "tr1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: toolResult.content as string,
          tool_result: {
            ...toolResult,
            tool: toolCall.name,
            tool_call_id: toolCall.id,
            assistant_message_id: "a1",
            tool_args: toolCall.args,
          },
          created_at: "t",
        },
      ] as unknown as Message[],
      { verboseMode: false },
    );
    const tool = items.find(
      (i): i is VisibleToolRow => i.kind === "tool",
    );
    expect(tool).toBeDefined();
    verify(tool!);
  });

  it("shows benign tool cards when verbose mode is on", () => {
    const items = messagesToTranscriptItems(
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          tool_calls: [{ id: "tc1", name: "read", args: { path: "src/x.go" } }],
          created_at: "t",
        },
        {
          id: "tr1",
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
      ],
      { verboseMode: true },
    );
    expect(items.filter((i) => i.kind === "tool")).toHaveLength(1);
  });

  it("transcript projector is the universal activity-span boundary", () => {
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          { id: "tc1", name: "read", args: { path: "a.go" } },
          { id: "tc2", name: "read", args: { path: "b.go" } },
        ],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "a",
        tool_result: { content: "a", tool: "read", tool_call_id: "tc1", assistant_message_id: "a1" },
        created_at: "t",
      },
      {
        id: "tr2",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "b",
        tool_result: { content: "b", tool: "read", tool_call_id: "tc2", assistant_message_id: "a1" },
        created_at: "t",
      },
    ];
    const raw = messagesToTranscriptItems(messages);
    expect(raw.filter((i) => i.kind === "tool")).toHaveLength(2);
    const display = createTranscriptDisplayProjector()(messages, undefined)[0]!;
    expect(display).toHaveLength(1);
    expect(display[0]?.kind).toBe("activity_span");
    if (display[0]?.kind === "activity_span") {
      expect(display[0].label).toBe("investigating");
    }
  });

  describe("running activity spans", () => {
    it("keeps the compact span collapsed while a call is running", () => {
      // command is long_running, so it renders live with no result yet.
      const messages: Message[] = [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          tool_calls: [
            { id: "tc1", name: "command", args: { cmd: "echo a" } },
            { id: "tc2", name: "command", args: { cmd: "echo b" } },
          ],
          created_at: "t",
        },
        {
          id: "tr1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "a",
          tool_result: { content: "a", tool: "command", tool_call_id: "tc1", assistant_message_id: "a1" },
          created_at: "t",
        },
        // tc2 has no tool_result yet — still running.
      ];
      const display = createTranscriptDisplayProjector()(messages, undefined)[0]!;
      expect(display).toHaveLength(1);
      expect(display[0]?.kind).toBe("activity_span");
    });
  });
});
