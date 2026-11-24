import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { fileEditPreviewFixture } from "../../../test/file-edit-fixture.ts";
import { toolApprovalFixture } from "../../checkpoint/approval-test-fixtures.ts";
import { noteToolCallShownInFlight } from "../../tool/tool-transcript-visibility.ts";
import { buildChatTranscriptBlocks } from "../../workflow/workflow-spans.ts";
import { createTranscriptDisplayProjector, retainTranscriptItemIdentity, transcriptItemsByKey } from "./transcript-display-projection.ts";
import type { ActivityGroupItem, DisplayTranscriptItem } from "./transcript-item-model.ts";
import { finalizeTranscriptItems, messagesToTranscriptItems } from "./transcript-items.ts";
import { sameWireValue } from "./wire-equal.ts";

function userMsg(id: string, ord: number, content = "Fix it"): Message {
  return {
    id,
    ord,
    role: "user",
    origin: "user",
    authority: "user",
    trust_tier: "trusted",
    content,
    created_at: "2026-11-23T00:00:00Z",
  };
}

function assistantCallsMsg(
  id: string,
  ord: number,
  calls: NonNullable<Message["tool_calls"]>,
  content = "",
): Message {
  return {
    id,
    ord,
    role: "assistant",
    origin: "model",
    authority: "none",
    trust_tier: "trusted",
    content,
    tool_calls: calls,
    created_at: "2026-11-23T00:00:01Z",
  };
}

function noteMsg(id: string, ord: number, content: string): Message {
  return {
    id,
    ord,
    role: "assistant",
    kind: "agent_note",
    origin: "model",
    authority: "none",
    trust_tier: "trusted",
    content,
    grounding: { traced: true, host_assembled: false, observed_path_count: 1, observed_paths_sample: ["a.go"] },
    created_at: "2026-11-23T00:00:02Z",
  };
}

function toolResultMsg(
  id: string,
  ord: number,
  assistantId: string,
  callId: string,
  tool: string,
  extra: Partial<NonNullable<Message["tool_result"]>> = {},
): Message {
  return {
    id,
    ord,
    role: "tool",
    origin: "tool",
    authority: "none",
    trust_tier: "untrusted",
    content: extra.content ?? "ok",
    tool_result: {
      assistant_message_id: assistantId,
      tool_call_id: callId,
      tool,
      content: extra.content ?? "ok",
      outcome: "completed",
      ...extra,
    },
    created_at: "2026-11-23T00:00:03Z",
  };
}

function projectChat(
  messages: readonly Message[],
  opts: {
    verboseMode?: boolean;
    sessionId?: string;
    pendingCheckpoints?: Parameters<typeof buildChatTranscriptBlocks>[3] extends infer O
      ? O extends { pendingCheckpoints?: infer P }
        ? P
        : never
      : never;
    turnLoads?: Parameters<typeof buildChatTranscriptBlocks>[3] extends infer O
      ? O extends { turnLoads?: infer T }
        ? T
        : never
      : never;
  } = {},
): DisplayTranscriptItem[] {
  const blocks = buildChatTranscriptBlocks(messages, [], undefined, {
    sessionId: opts.sessionId ?? "s1",
    verboseMode: opts.verboseMode ?? false,
    pendingCheckpoints: opts.pendingCheckpoints,
    turnLoads: opts.turnLoads,
  });
  const projector = createTranscriptDisplayProjector();
  return projector(messages, blocks.map((b) => b.items), {
    layout: "chat",
    sessionId: opts.sessionId ?? "s1",
    verboseMode: opts.verboseMode ?? false,
    turnLoads: opts.turnLoads,
  }).flat();
}

describe("projectActivityGroups", () => {
  it("anchors running-drawn calls before a crossed progress_update and result-drawn calls after it", () => {
    noteToolCallShownInFlight("s-ord", "a310:call-cmd");
    const messages: Message[] = [
      userMsg("u1", 300),
      assistantCallsMsg("a310", 310, [
        { id: "call-wait", name: "wait", args: { reason: "Waiting on build" } },
        { id: "call-cmd", name: "command", args: { command: "git status" } },
        { id: "call-write", name: "write", args: { path: "main.go" } },
      ]),
      toolResultMsg("r311", 311, "a310", "call-wait", "wait"),
      {
        id: "p312",
        ord: 312,
        role: "system",
        kind: "progress_update",
        origin: "host",
        authority: "system",
        trust_tier: "trusted",
        content: "",
        progress_update: {
          initial: true,
          steps: [{ label: "Inspect and fix", state: "pending" }],
          changes: [],
        },
        created_at: "2026-11-23T00:00:02Z",
      },
      toolResultMsg("r313", 313, "a310", "call-cmd", "command"),
      toolResultMsg("r314", 314, "a310", "call-write", "write", {
        file_edit_previews: [fileEditPreviewFixture({ path: "main.go", before: "a\n", after: "b\n" })],
      }),
    ];

    const quietItems = projectChat(messages, { verboseMode: false, sessionId: "s-ord" });
    expect(quietItems.map((i) => i.kind)).toEqual([
      "user",
      "activity_group",
      "progress_update",
      "activity_group",
    ]);
    const beforePlan = quietItems[1] as ActivityGroupItem;
    const afterPlan = quietItems[3] as ActivityGroupItem;
    expect(beforePlan.entries.map((e) => (e.kind === "tool" ? e.part.toolCallId : e.key))).toEqual([
      "call-wait",
      "call-cmd",
    ]);
    expect(afterPlan.entries.map((e) => (e.kind === "tool" ? e.part.toolCallId : e.key))).toEqual([
      "call-write",
    ]);
  });

  it("coalesces ungrounded carrier prose with its paired agent_note and omits internal wake rows", () => {
    const messages: Message[] = [
      userMsg("u1", 297),
      assistantCallsMsg("a350", 350, [{ id: "call-grep", name: "grep", args: { pattern: "auth" } }]),
      toolResultMsg("r351", 351, "a350", "call-grep", "grep"),
      assistantCallsMsg(
        "a363",
        363,
        [{ id: "call-wait", name: "wait", args: { seconds: 5 } }],
        "Starting wait for worker.",
      ),
      noteMsg("n364", 364, "Starting wait for worker."),
      toolResultMsg("r365", 365, "a363", "call-wait", "wait"),
      {
        id: "w366",
        ord: 366,
        role: "user",
        visibility: "internal",
        kind: "host_loop_wake",
        origin: "host",
        authority: "system",
        trust_tier: "trusted",
        content: "wake",
        created_at: "2026-11-23T00:00:05Z",
      },
    ];

    const items = projectChat(messages);
    expect(items.map((i) => i.kind)).toEqual([
      "user",
      "activity_group",
      "assistant",
      "activity_group",
    ]);
    expect((items[2] as Extract<DisplayTranscriptItem, { kind: "assistant" }>).key).toBe("n364");
    const groupAfterNote = items[3] as ActivityGroupItem;
    expect(groupAfterNote.entries).toHaveLength(1);
    expect(groupAfterNote.entries[0]?.kind === "tool" && groupAfterNote.entries[0].part.toolCallId).toBe(
      "call-wait",
    );
  });

  it("ignores empty non-initial progress_update rows and flushes around standalone intervening rows", () => {
    const messages: Message[] = [
      userMsg("u1", 1),
      assistantCallsMsg("a2", 2, [{ id: "c1", name: "grep", args: { pattern: "x" } }]),
      toolResultMsg("r3", 3, "a2", "c1", "grep"),
      {
        id: "p-empty",
        ord: 4,
        role: "system",
        kind: "progress_update",
        origin: "host",
        authority: "system",
        trust_tier: "trusted",
        content: "",
        progress_update: { initial: false, steps: [{ label: "Step", state: "pending" }], changes: [] },
        created_at: "2026-11-23T00:00:04Z",
      },
      assistantCallsMsg("a5", 5, [{ id: "c2", name: "read", args: { path: "b.go" } }]),
      toolResultMsg("r6", 6, "a5", "c2", "read"),
      assistantCallsMsg("a7", 7, [
        { id: "c-task", name: "task", args: { agent_type: "implementer", brief: { goal: "go", done_when: ["done"] } } },
      ]),
      toolResultMsg("r8", 8, "a7", "c-task", "task", { dispatch: { worker_id: "job-1" } }),
      {
        id: "wf9",
        ord: 9,
        role: "system",
        kind: "workflow_feedback",
        origin: "host",
        authority: "system",
        trust_tier: "trusted",
        content: "Proceed?",
        workflow_feedback: { phase_id: "p1", prompt: "Proceed?", response_type: "text" },
        created_at: "2026-11-23T00:00:09Z",
      },
      assistantCallsMsg("a10", 10, [{ id: "c3", name: "write", args: { path: "c.go" } }]),
      toolResultMsg("r11", 11, "a10", "c3", "write", {
        file_edit_previews: [fileEditPreviewFixture({ path: "c.go", before: "", after: "pkg" })],
      }),
    ];

    const items = projectChat(messages);
    expect(items.map((i) => i.kind)).toEqual([
      "user",
      "activity_group",
      "worker_group",
      "workflow_feedback",
      "activity_group",
    ]);
    const firstGroup = items[1] as ActivityGroupItem;
    expect(firstGroup.entries.map((e) => (e.kind === "tool" ? e.part.toolCallId : e.key))).toEqual([
      "c1",
      "c2",
    ]);
  });

  it("routes late file effects across closing boundaries while preserving toolCallId and replay parity", () => {
    noteToolCallShownInFlight("s1", "a11:cmd-bg");
    const messages: Message[] = [
      userMsg("u1", 10),
      assistantCallsMsg("a11", 11, [{ id: "cmd-bg", name: "command", args: { command: "gen.sh" } }]),
      {
        id: "n12",
        ord: 12,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "Started generator; inspecting results next.",
        created_at: "2026-11-23T00:00:02Z",
      },
      toolResultMsg("r13", 13, "a11", "cmd-bg", "command", {
        file_edit_previews: [fileEditPreviewFixture({ path: "gen.go", before: "v1", after: "v2" })],
        file_edits_omitted: 2,
      }),
      assistantCallsMsg("a14", 14, [{ id: "read-gen", name: "read", args: { path: "gen.go" } }]),
      toolResultMsg("r15", 15, "a14", "read-gen", "read"),
    ];

    const streamed: DisplayTranscriptItem[][] = [];
    const streamProjector = createTranscriptDisplayProjector();
    for (let len = 1; len <= messages.length; len++) {
      const prefix = messages.slice(0, len);
      const blocks = buildChatTranscriptBlocks(prefix, [], undefined, { sessionId: "s1", verboseMode: true });
      streamed.push(
        streamProjector(prefix, blocks.map((b) => b.items), {
          layout: "chat",
          sessionId: "s1",
          verboseMode: true,
        }).flat(),
      );
    }
    const finalStreamed = streamed[streamed.length - 1]!;
    const cold = projectChat(messages, { sessionId: "s1", verboseMode: true });
    expect(sameWireValue(finalStreamed, cold)).toBe(true);

    expect(cold.map((i) => i.kind)).toEqual([
      "user",
      "activity_group",
      "assistant",
      "activity_group",
    ]);
    const group1 = cold[1] as ActivityGroupItem;
    const group2 = cold[3] as ActivityGroupItem;
    expect(group1.sectionOrder).toEqual(["activities"]);
    expect(group1.folds).toEqual([]);
    expect(group2.sectionOrder).toEqual(["files", "activities"]);
    expect(group2.folds).toHaveLength(1);
    expect(group2.folds[0]?.steps[0]?.toolCallId).toBe("cmd-bg");
    expect(group2.filesOmitted).toBe(2);
  });

  it("computes deterministic sectionOrder and stable group key across pending-to-settled approvals", () => {
    const u1 = userMsg("u1", 1);
    const a2 = assistantCallsMsg("a2", 2, [
      { id: "call-write", name: "write", args: { path: "secret.go" } },
    ]);
    const pendingItems = projectChat([u1, a2], {
      pendingCheckpoints: [
        {
          checkpointId: "cp-write",
          sessionId: "s1",
          kind: "tool_approval",
          status: "pending",
          issuedAt: "2026-11-23T00:00:01Z",
          tool_approval: {
            ...toolApprovalFixture({ command: "write secret.go" }),
            tool_call_id: "call-write",
          },
        },
      ],
    });
    const pendingGroup = pendingItems[1] as ActivityGroupItem;
    expect(pendingGroup.key).toBe("activity-group:a2:call-write");
    expect(pendingGroup.sectionOrder).toEqual(["approvals"]);
    expect(pendingGroup.approvals[0]?.toolCallId).toBe("call-write");

    const settledMessages: Message[] = [
      u1,
      a2,
      toolResultMsg("r3", 3, "a2", "call-write", "write", {
        file_edit_previews: [fileEditPreviewFixture({ path: "secret.go", before: "", after: "ok" })],
        checkpoint_decision: {
          checkpoint_id: "cp-write",
          kind: "tool_approval",
          status: "approved",
          grant_title: "Allow once",
        },
      }),
    ];
    const settledItems = projectChat(settledMessages);
    const settledGroup = settledItems[1] as ActivityGroupItem;
    expect(settledGroup.key).toBe(pendingGroup.key);
    expect(settledGroup.sectionOrder).toEqual(["approvals", "activities", "files"]);

    const retained = retainTranscriptItemIdentity(transcriptItemsByKey(settledItems), projectChat(settledMessages));
    expect(retained[1]).toBe(settledGroup);
  });

  it("omits turn_load-only intervals and preserves worker layout spans unchanged", () => {
    const messages: Message[] = [
      userMsg("u1", 1),
      assistantCallsMsg("a2", 2, [{ id: "c-int", name: "request_tools", args: {} }]),
      toolResultMsg("r3", 3, "a2", "c-int", "request_tools", { ui_visibility: "benign" }),
    ];
    const items = projectChat(messages, {
      turnLoads: {
        u1: [
          {
            assistant_message_id: "a2",
            skills: ["go"],
            tools: ["grep"],
          } as never,
        ],
      },
    });
    expect(items.map((i) => i.kind)).toEqual(["user"]);

    const workerRaw = messagesToTranscriptItems(
      [
        userMsg("u1", 1),
        assistantCallsMsg("a2", 2, [{ id: "c1", name: "read", args: { path: "a.go" } }]),
        toolResultMsg("r3", 3, "a2", "c1", "read"),
      ],
      { layout: "worker" },
    );
    const workerDisplay = finalizeTranscriptItems(workerRaw, { layout: "worker" });
    expect(workerDisplay.map((i) => i.kind)).toEqual(["user", "activity_span"]);
  });
});
