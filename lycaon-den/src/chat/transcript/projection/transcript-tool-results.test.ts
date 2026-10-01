import { describe, expect, it } from "vitest";
import { finalizeTranscriptItems, messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { taskJobIdFromPart } from "../../task/task-result-model.ts";

describe("transcript-items", () => {

  it("includes provisional internal tool rows in span prebuild until commit", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Running a read.",
        visibility: "internal" as const,
        tool_calls: [{ name: "read", id: "call_1", args: { path: "main.go" } }],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "package main",
        tool_result: {
          outcome: "completed" as const,
          content: "package main",
          tool: "read",
          tool_call_id: "call_1",
          assistant_message_id: "a1",
        },
        created_at: "t2",
      },
    ];
    const withoutProvisional = messagesToTranscriptItems(messages);
    // Completed wire results stay visible after navigation clears live-turn signals.
    expect(withoutProvisional.some((item) => item.kind === "tool")).toBe(true);
    const runningOnly = messagesToTranscriptItems([
      messages[0]!,
      {
        ...messages[1]!,
        tool_calls: [{ name: "read", id: "call_2", args: { path: "go.mod" } }],
      },
    ]);
    expect(runningOnly.some((item) => item.kind === "tool")).toBe(false);
    const withProvisional = messagesToTranscriptItems(messages);
    expect(withProvisional.some((item) => item.kind === "tool")).toBe(true);
    const items = createTranscriptDisplayProjector()(messages, [withProvisional])[0]!;
    const spans = items.filter((item) => item.kind === "activity_span");
    expect(spans).toHaveLength(1);
    expect(spans[0]?.entries).toHaveLength(1);
  });

  it("keeps prior tool batches when the host replaces assistant tool_calls on one row", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          { name: "read", id: "call_read_1", args: { path: "README.md" } },
          { name: "read", id: "call_read_2", args: { path: "AGENTS.md" } },
        ],
        created_at: "t1",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '[read#1]\n{"content":"readme"}',
        tool_result: {
          tool: "read",
          tool_call_id: "call_read_1",
          assistant_message_id: "a1",
          outcome: "completed" as const,
          content: '[read#1]\n{"content":"readme"}',
        },
        created_at: "t2",
      },
      {
        id: "t2",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '[read#2]\n{"content":"agents"}',
        tool_result: {
          tool: "read",
          tool_call_id: "call_read_2",
          assistant_message_id: "a1",
          outcome: "completed" as const,
          content: '[read#2]\n{"content":"agents"}',
        },
        created_at: "t3",
      },
    ];
    const items = messagesToTranscriptItems(messages);
    const tools = items.filter((item) => item.kind === "tool");
    expect(tools).toHaveLength(2);
    expect(tools.map((item) => item.key)).toEqual([
      "a1:call_read_1",
      "a1:call_read_2",
    ]);
    if (tools[0]?.kind === "tool" && tools[1]?.kind === "tool") {
      expect(tools[0].part.tool).toBe("read");
      expect(tools[1].part.tool).toBe("read");
    }
  });

  it("binds tool results to their authoritative assistant and call", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t0" },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "draft",
        visibility: "internal" as const,
        created_at: "t1",
        tool_calls: [{ id: "call_1", name: "list_dir", args: { path: "." } }],
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "[list#1]\n{}",
        created_at: "t2",
        tool_result: {
          tool_call_id: "call_1",
          tool: "list_dir",
          assistant_message_id: "a1",
          outcome: "completed" as const,
          content: "[list#1]\n{}",
        },
      },
    ];
    expect(messagesToTranscriptItems(messages).filter((item) => item.kind === "tool"))
      .toMatchObject([{ key: "a1:call_1", part: { assistantMessageId: "a1", toolCallId: "call_1", messageId: "t1" } }]);
  });

  it("does not infer a tool parent when the authoritative call is absent", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t0" },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "[list#1]\n{}",
        created_at: "t2",
        tool_result: {
          tool_call_id: "call_1",
          tool: "list_dir",
          outcome: "completed" as const,
          content: "[list#1]\n{}",
        },
      },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "final",
        visibility: "transcript" as const,
        created_at: "t3",
      },
    ];
    expect(messagesToTranscriptItems(messages).filter((item) => item.kind === "tool"))
      .toEqual([]);
  });

  it("keeps a paged tool result when its issuing assistant row is outside the window", () => {
    const items = messagesToTranscriptItems([
      {
        id: "t1",
        ord: 22,
        role: "tool" as const,
        origin: "tool" as const,
        authority: "none" as const,
        trust_tier: "untrusted" as const,
        content: "found it",
        tool_result: {
          assistant_message_id: "a-outside-window",
          tool_call_id: "call-1",
          tool: "read",
          content: "found it",
          outcome: "completed" as const,
        },
        created_at: "t",
      },
    ]);
    expect(items).toMatchObject([
      {
        kind: "tool",
        key: "a-outside-window:call-1",
        part: {
          assistantMessageId: "a-outside-window",
          messageId: "t1",
          tool: "read",
        },
      },
    ]);
  });

  it("shows orchestration tool cards from internal assistant rows during a live transcript", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Answering the paused worker, then waiting.",
        visibility: "internal" as const,
        tool_calls: [
          {
            name: "answer_decision",
            id: "call_1",
            args: { job_id: "j1", option: "2" },
          },
          { name: "wait", id: "call_2", args: { minutes: 5 } },
        ],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"status":"resumed"}',
        tool_result: {
          content: '{"status":"resumed"}',
          tool: "answer_decision",
          tool_call_id: "call_1",
          assistant_message_id: "a1",
        },
        created_at: "t",
      },
      {
        id: "t2",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"status":"sleeping"}',
        tool_result: {
          content: '{"status":"sleeping"}',
          tool: "wait",
          tool_call_id: "call_2",
          assistant_message_id: "a1",
        },
        created_at: "t",
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, undefined)[0]!;
    const tools = items.flatMap((item) =>
      item.kind === "activity_span"
        ? item.entries.flatMap((entry) =>
            entry.kind === "tool" ? [entry.part.tool] : [],
          )
        : [],
    );
    expect(tools).toEqual(["answer_decision", "wait"]);
  });

  it("pairs task tool results with the preceding assistant tool call", () => {
    const items = messagesToTranscriptItems([
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build game", created_at: "t" },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "tc1",
            name: "task",
            args: { agent_type: "implementer", brief: { goal: "build", done_when: ["Return results."] } },
          },
        ],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content:
          '{"agent_type":"implementer","job_id":"job-1","status":"enqueued"}',
        tool_result: {
          content:
            '{"agent_type":"implementer","job_id":"job-1","status":"enqueued"}',
          tool: "task",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          dispatch: { worker_id: "job-1" },
        },
        created_at: "t",
      },
    ]);
    const task = items.find((i) => i.kind === "tool");
    expect(task?.part.output).toBeTruthy();
    expect(task!.part.jobId).toBe("job-1");
  });

  it("keeps task cards before later user turns in message order", () => {
    // One job, one row — the task tool-result row carries the worker_summary
    // metadata in place; there is no separate assistant worker_summary message.
    const items = messagesToTranscriptItems([
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "build game", created_at: "t" },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "tc1",
            name: "task",
            args: { agent_type: "implementer", brief: { goal: "build", done_when: ["Return results."] } },
          },
        ],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content:
          '{"agent_type":"implementer","job_id":"job-1","status":"enqueued"}',
        tool_result: {
          content:
            '{"agent_type":"implementer","job_id":"job-1","status":"enqueued"}',
          tool: "task",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          dispatch: { worker_id: "job-1" },
        },
        worker_summary: {
          worker_id: "job-1",
          child_session_id: "child-1",
          agent_type: "implementer",
          status: "partial",
          envelope: '<task job_id="job-1" child_session_id="child-1" agent_type="implementer" state="partial"></task>',
        },
        created_at: "t",
      },
      {
        id: "a2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Worker finished.",
        created_at: "t",
      },
      { id: "u2", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "what happened next?", created_at: "t" },
    ]);

    const display = finalizeTranscriptItems(items);
    const taskIdx = display.findIndex((i) => i.kind === "worker_group");
    const followUpIdx = display.findIndex(
      (i) => i.kind === "user" && i.text === "what happened next?",
    );
    expect(taskIdx).toBeGreaterThanOrEqual(0);
    expect(followUpIdx).toBeGreaterThan(taskIdx);
  });

  it("messagesToTranscriptItems skips tool_calls until provider name is present", () => {
    const items = messagesToTranscriptItems([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "functions.task:6", name: "", args: {} }],
        created_at: "t",
      },
    ]);
    expect(items.some((i) => i.kind === "tool")).toBe(false);
  });

  it("keys tool rows uniquely when a provider reuses tool_call ids across turns", () => {
    // Reused call ids remain unique across assistant turns.
    const items = messagesToTranscriptItems([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "call_0", name: "command", args: { command: "ls" } }],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        tool_result: { content: "ok", tool: "command", tool_call_id: "call_0", assistant_message_id: "a1" },
        created_at: "t",
      },
      {
        id: "a2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "call_0", name: "command", args: { command: "pwd" } }],
        created_at: "t",
      },
      {
        id: "tr2",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "ok",
        tool_result: { content: "ok", tool: "command", tool_call_id: "call_0", assistant_message_id: "a2" },
        created_at: "t",
      },
    ]);
    const tools = items.filter((i) => i.kind === "tool");
    expect(tools).toHaveLength(2);
    const keys = tools.map((i) => i.key);
    expect(new Set(keys).size).toBe(2);
    // Wire tool_call_id is preserved separately for checkpoint anchoring.
    for (const item of tools) {
      if (item.kind === "tool") expect(item.part.toolCallId).toBe("call_0");
    }
  });

  it("pairs tool results after host-injected user rows", () => {
    const items = messagesToTranscriptItems([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "main.go" } }],
        created_at: "t",
      },
      {
        id: "h1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "[host:loop-wake] continue",
        visibility: "internal",
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "package main",
        tool_result: { content: "package main", tool: "read", tool_call_id: "tc1", assistant_message_id: "a1" },
        created_at: "t",
      },
    ]);
    const tool = items.find((i) => i.kind === "tool");
    expect(tool?.part.output).toBe("package main");
    expect(tool?.part.status).toBe("completed");
  });

  it("transcript projector reconciles stale span tool rows from messages", () => {
    const envelope =
      '<task job_id="job-cv" child_session_id="child-1" agent_type="implementer" state="complete"/>';
    const prebuilt = [
      {
        kind: "tool" as const,
        key: "a1:tc1",
        part: {
          id: "a1:tc1",
          toolCallId: "tc1",
          assistantMessageId: "assistant-message",
          messageId: "tr1",
          tool: "task",
          kind: "task" as const,
          status: "completed" as const,
          args: {
            agent_type: "implementer",
            brief: { goal: "verify html", done_when: ["Return results."] },
          },
          output: null,
          error: null,
        },
      },
    ];
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          {
            id: "tc1",
            name: "task",
            args: { agent_type: "implementer", brief: { goal: "verify html", done_when: ["Return results."] } },
          },
        ],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: envelope,
        tool_result: {
          content: envelope,
          tool: "task",
          tool_call_id: "tc1",
          assistant_message_id: "a1",
          outcome: "completed" as const,
          codes: ["BANNER_TASK_QUEUED"],
          ui_visibility: "normal" as const,
          dispatch: { worker_id: "job-cv" },
        },
        created_at: "t",
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, [prebuilt])[0]!;
    const task = items.find((item) => item.kind === "worker_group");
    expect(task?.kind).toBe("worker_group");
    if (task?.kind !== "worker_group") return;
    expect(task.parts).toHaveLength(1);
    expect(taskJobIdFromPart(task.parts[0]!)).toBe("job-cv");
    expect(task.parts[0]!.output).toBe(envelope);
  });
});
