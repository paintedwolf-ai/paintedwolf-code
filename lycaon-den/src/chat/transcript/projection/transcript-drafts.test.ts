import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";

describe("transcript-items", () => {

  it("maps each live draft frame directly from host state", () => {
    const withProse = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "slot-1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "I'll research the coordination plane.",
        visibility: "internal" as const,
        status: "streaming" as const,
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(withProse);
    expect(items.find((i) => i.key === "slot-1")).toMatchObject({
      key: "slot-1",
      text: "I'll research the coordination plane.",
    });
    const blanked = [
      withProse[0]!,
      { ...withProse[1]!, content: "" },
    ];
    const blankedItems = messagesToTranscriptItems(blanked);
    expect(blankedItems.find((i) => i.key === "slot-1")).toMatchObject({
      key: "slot-1",
      text: "",
    });
  });

  it("keeps draft_status=live rows in items through tool-only blank frames", () => {
    const blankToolBatch = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "slot-1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "transcript" as const,
        draft_status: "live" as const,
        tool_calls: [{ id: "tc1", name: "read", args: { path: "a.go" } }],
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(blankToolBatch);
    expect(items.some((item) => item.key === "slot-1" && item.kind === "draft")).toBe(
      true,
    );
    const draft = items.find((i) => i.key === "slot-1" && i.kind === "draft");
    expect(draft?.kind === "draft" ? draft.text : "missing").toBe("");
  });

  it("reconciles a prebuilt draft body to the current host frame", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "slot-1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "internal" as const,
        draft_status: "live" as const,
        tool_calls: [
          {
            id: "tc1",
            name: "task",
            args: { brief: { goal: "go", done_when: ["Return results."] }, agent_type: "implementer" },
          },
        ],
        created_at: "t",
      },
    ];
    const prebuilt = [
      {
        kind: "draft" as const,
        key: "slot-1",
        text: "",
        live: true,
        draftVersionCount: 1,
        draftStatus: "live" as const,
      },
      {
        kind: "tool" as const,
        key: "slot-1:tc1",
        part: {
          id: "slot-1:tc1",
          toolCallId: "tc1",
          assistantMessageId: "assistant-message",
          messageId: "slot-1",
          tool: "task",
          kind: "generic" as const,
          status: "running" as const,
          args: {
            agent_type: "implementer",
            brief: { goal: "go", done_when: ["Return results."] },
          },
        },
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, [prebuilt], { verboseMode: true })[0]!;
    expect(items.find((item) => item.key === "slot-1" && item.kind === "draft")).toMatchObject({
      kind: "draft",
      key: "slot-1",
      text: "",
    });
  });

  // Exercise each wire field that controls draft visibility.
  const SCAN_PROSE = "The scan completed and the build baseline was collected.";
  function draftMessages(assistant: Partial<Message>): Message[] {
    return [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        visibility: "internal",
        content: "",
        created_at: "t",
        ...assistant,
      },
    ];
  }

  it.each<{
    name: string;
    assistant: Partial<Message>;
    expected: Record<string, unknown>;
  }>([
    {
      name: "streaming internal assistant → live draft",
      assistant: { content: SCAN_PROSE, status: "streaming" },
      expected: { kind: "draft", key: "a1", text: SCAN_PROSE, live: true },
    },
    {
      name: "settled internal assistant → non-live draft",
      assistant: { content: SCAN_PROSE, status: "complete" },
      expected: { kind: "draft", key: "a1", live: false },
    },
    {
      name: "internal assistant with grounding stays private until commit",
      assistant: {
        content: "The fix landed in `engine.py`.",
        grounding: {
          traced: true,
          checks: [
            { id: "path_citations", label: "Path citations", status: "passed" as const, matched: ["`engine.py`"] },
          ],
        },
      },
      expected: { kind: "draft", key: "a1" },
    },
    {
      name: "envelope-shaped internal content → draft (disposition is host-declared)",
      assistant: { content: '{"synthesis":"streaming attempt"}' },
      expected: { kind: "draft", key: "a1" },
    },
    {
      name: "internal orchestration tool_calls → visible but not live draft",
      assistant: {
        content:
          "The scan completed and the build baseline was collected. I now have the Go/Den findings.",
        tool_calls: [{ name: "answer_decision", id: "call_1", args: { job_id: "j1", option: "3" } }],
      },
      expected: { kind: "draft", key: "a1", live: false },
    },
    {
      name: "draft_status=live slot → draft",
      assistant: { content: "live draft", draft_status: "live", status: "streaming" },
      expected: { kind: "draft", key: "a1" },
    },
    {
      name: "committed slot with grounding → assistant bubble",
      assistant: {
        content: "## Final answer",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 2,
        status: "complete",
        grounding: { traced: true, checks: [] },
      },
      expected: { kind: "assistant", key: "a1" },
    },
  ])("draft matrix (messages) — $name", ({ assistant, expected }) => {
    const items = messagesToTranscriptItems(draftMessages(assistant));
    expect(items.find((i) => i.key === "a1")).toMatchObject(expected);
  });

  it.each<{
    name: string;
    assistant: Partial<Message>;
    kind: "draft" | "assistant";
  }>([
    {
      name: "streaming internal assistant stays a draft",
      assistant: { content: SCAN_PROSE, status: "streaming" },
      kind: "draft",
    },
    {
      name: "committed kind=draft row stays a draft",
      assistant: { content: SCAN_PROSE, visibility: "transcript", kind: "draft" },
      kind: "draft",
    },
    {
      name: "internal assistant through LLM ok + tool execution stays a draft",
      assistant: {
        content: "The first Go scout and the SAST scan are complete.",
        tool_calls: [{ name: "task", id: "call_1", args: { job_id: "j1", brief: { goal: "scan", done_when: ["Return results."] } } }],
      },
      kind: "draft",
    },
    {
      name: "internal assistant with grounding stays a draft until committed",
      assistant: {
        content: "The fix landed in `engine.py`.",
        grounding: {
          traced: true,
          checks: [
            { id: "path_citations", label: "Path citations", status: "passed" as const, matched: ["`engine.py`"] },
          ],
        },
      },
      kind: "draft",
    },
    {
      name: "internal assistant from persisted messages renders unconditionally",
      assistant: { content: "streaming synthesis attempt" },
      kind: "draft",
    },
  ])("draft matrix (display) — $name", ({ assistant, kind }) => {
    const items = createTranscriptDisplayProjector()(draftMessages(assistant), undefined, {
      verboseMode: true,
    })[0]!;
    expect(items.some((i) => i.key === "a1" && i.kind === kind)).toBe(true);
  });

  it("promotes committed kind=draft rows to assistant transcript items", () => {
    const items = messagesToTranscriptItems([
      {
        id: "slot-1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft" as const,
        content: "final body",
        draft_version_count: 3,
        draft_status: "committed" as const,
        created_at: "t",
      },
    ]);
    expect(items[0]).toMatchObject({
      kind: "assistant",
      key: "slot-1",
      text: "final body",
    });
  });

  it("does not invent an answer for an empty committed retry step", () => {
    const committedRejectedSlot = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "review", created_at: "t" },
      {
        id: "slot-1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "transcript" as const,
        draft_status: "committed" as const,
        draft_version_count: 2,
        tool_calls: [{ name: "read", id: "call_1", args: { path: "x" } }],
        created_at: "t",
      },
    ];

    const items = messagesToTranscriptItems(committedRejectedSlot);
    expect(items.some((item) => item.kind === "assistant")).toBe(false);
  });

  it("renders a committed row without kind=draft as a plain assistant item", () => {
    // The host distinguishes orchestration prose with kind=draft.
    const items = messagesToTranscriptItems([
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content:
          "The workers are now resumed. I need to wait for them to finish.",
        visibility: "transcript" as const,
        tool_calls: [{ name: "wait", id: "call_wait", args: { minutes: 5 } }],
        created_at: "t",
      },
      { id: "t1", role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const, content: "waiting", created_at: "t" },
    ]);
    expect(items.filter((item) => item.kind === "draft")).toHaveLength(0);
    expect(items.filter((item) => item.kind === "assistant")).toHaveLength(1);
  });

  it("renders explicit draft kind with tool calls as draft plus tools", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft" as const,
        content: "The first Go scout and the SAST scan are complete.",
        visibility: "transcript" as const,
        tool_calls: [
          { name: "wait", id: "call_wait", args: { minutes: 5 } },
        ],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "waiting",
        tool_result: {
          content: "waiting",
          tool: "wait",
          tool_call_id: "call_wait",
          assistant_message_id: "a1",
        },
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(messages);
    expect(items.filter((item) => item.kind === "draft")).toHaveLength(1);
    expect(items.filter((item) => item.kind === "assistant")).toHaveLength(0);
    expect(items.filter((item) => item.kind === "tool")).toHaveLength(1);
  });

  it("renders tool-call-only committed steps as tools without a draft rail", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "tool-only-step",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        draft_status: "committed" as const,
        visibility: "transcript" as const,
        tool_calls: [{ name: "list_dir", id: "call_1", args: { path: "." } }],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "[list#1]\n{}",
        created_at: "t",
        tool_result: {
          content: "[list#1]\n{}",
          tool: "list_dir",
          tool_call_id: "call_1",
          assistant_message_id: "tool-only-step",
          outcome: "completed" as const,
        },
      },
    ];
    const items = messagesToTranscriptItems(messages);
    expect(items.filter((item) => item.kind === "draft")).toHaveLength(0);
    expect(items.filter((item) => item.kind === "tool")).toHaveLength(1);
  });

  it("keeps a host-declared empty committed draft row", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "tool-step",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft" as const,
        content: "",
        draft_status: "committed" as const,
        visibility: "transcript" as const,
        tool_calls: [{ name: "grep", id: "call_1", args: { pattern: "foo" } }],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "matches",
        created_at: "t",
        tool_result: {
          content: "matches",
          tool: "grep",
          tool_call_id: "call_1",
          assistant_message_id: "tool-step",
          outcome: "completed" as const,
        },
      },
    ];
    const items = messagesToTranscriptItems(messages);
    expect(items.filter((item) => item.kind === "draft")).toHaveLength(1);
    expect(items.filter((item) => item.kind === "tool")).toHaveLength(1);
  });

  it("worker layout renders accepted completion prose as assistant markdown, not draft", () => {
    const items = messagesToTranscriptItems(
      [
        {
          id: "a1",
          role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          kind: "draft" as const,
          content:
            '{"leg_status":"complete","brief":"## Summary\\n\\nAll set."}',
          created_at: "t",
        },
      ],
      { layout: "worker" },
    );
    expect(items).toEqual([
      {
        kind: "assistant",
        key: "a1",
        text: '{"leg_status":"complete","brief":"## Summary\\n\\nAll set."}',
        grounding: undefined,
      },
    ]);
  });

  it("reconciles a stale span assistant row into draft when the message is host-stamped draft", () => {
    const messages = [
      { id: "u1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft" as const,
        content: "Two workers paused for decisions. I need to answer them.",
        visibility: "transcript" as const,
        tool_calls: [{ name: "wait", id: "call_wait", args: { minutes: 5 } }],
        created_at: "t",
      },
      { id: "t1", role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const, content: "waiting", created_at: "t" },
    ];
    const prebuilt = [
      {
        kind: "assistant" as const,
        key: "a1",
        text: messages[1]!.content,
      },
      {
        kind: "tool" as const,
        key: "call_wait",
        part: {
          id: "call_wait",
          toolCallId: "call_wait",
          assistantMessageId: "assistant-message",
          messageId: "a1",
          tool: "wait",
          kind: "generic" as const,
          status: "completed" as const,
          output: "waiting",
        },
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, [prebuilt], { verboseMode: true })[0]!;
    expect(items.find((item) => item.key === "a1")?.kind).toBe("draft");
  });

  it("renders host-stamped draft with grounding as an assistant row in chat", () => {
    const items = messagesToTranscriptItems([
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft" as const,
        content: "The fix landed in `engine.py`.",
        created_at: "t",
        grounding: {
          traced: true,
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              matched: ["`engine.py`"],
            },
          ],
        },
      },
    ]);
    expect(items.filter((item) => item.kind === "draft")).toHaveLength(0);
    const assistant = items.find((item) => item.kind === "assistant");
    expect(assistant?.kind).toBe("assistant");
    if (assistant?.kind === "assistant") {
      expect(assistant.grounding?.traced).toBe(true);
    }
  });

  it("transcript projector refreshes prebuilt assistant row when grounding arrives", () => {
    // Prebuilt row from the live stream: same text it will commit to, no grounding.
    const prebuiltItems: ReturnType<typeof messagesToTranscriptItems> = [
      { kind: "assistant", key: "a1", text: "the fix landed" },
    ];
    // Reconciliation replaces the prebuilt row when grounding arrives.
    const messages = [
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "the fix landed",
        created_at: "t",
        grounding: {
          traced: true,
          checks: [
            {
              id: "path_citations",
              label: "Path citations",
              status: "passed" as const,
              matched: ["`engine.py`"],
            },
          ],
        },
      },
    ];
    const items = createTranscriptDisplayProjector()(messages, [prebuiltItems])[0]!;
    const assistant = items.find((i) => i.kind === "assistant");
    expect(assistant?.kind).toBe("assistant");
    if (assistant?.kind === "assistant") {
      expect(assistant.grounding?.traced).toBe(true);
    }
  });

  it("shows coordinator draft prose only in verbose mode", () => {
    const messages: Message[] = [
      {
        id: "draft-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: "private orchestration prose",
        draft_status: "committed",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "a.go" } }],
        created_at: "t",
      },
    ];
    const quiet = createTranscriptDisplayProjector()(messages, undefined, { verboseMode: false })[0]!;
    const verbose = createTranscriptDisplayProjector()(messages, undefined, { verboseMode: true })[0]!;
    expect(quiet.some((item) => item.kind === "draft")).toBe(false);
    expect(verbose.some((item) => item.kind === "draft")).toBe(true);
  });
});
