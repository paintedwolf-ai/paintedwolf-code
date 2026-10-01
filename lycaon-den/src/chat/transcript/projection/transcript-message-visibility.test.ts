import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";

describe("transcript-items", () => {

  it("hides host-injected user nudges", () => {
    const items = messagesToTranscriptItems([
      {
        id: "h1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "[host:workflow-start] start workflow",
        visibility: "internal",
        created_at: "t",
      },
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "Build a game", created_at: "t" },
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "user", text: "Build a game" });
  });

  it("hides internal guard kicks (retracted closeout feedback)", () => {
    const items = messagesToTranscriptItems([
      {
        id: "kick",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content:
          "[host:coordinator-citation-grounding]\n\nRejected: Investigate cited 6 path(s) not observed.\n\nCode: INVEST_HANDLE_NOT_OBSERVED",
        visibility: "internal",
        created_at: "t",
      },
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "continue", created_at: "t" },
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "user", text: "continue" });
  });

  it("hides coordinator kick nudges marked internal on the wire", () => {
    const items = messagesToTranscriptItems([
      {
        id: "kick",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content:
          "Worker task finished — chain `task()` per policy below; the host digest is authoritative for disk proof and survey receipts.",
        visibility: "internal",
        created_at: "t",
      },
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "continue", created_at: "t" },
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "user", text: "continue" });
  });

  it("renders transcript-visible workflow_boundary rows by ord", () => {
    const items = messagesToTranscriptItems([
      {
        id: "b-start",
        ord: 1,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "transcript",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        created_at: "t",
      },
      { id: "u1", ord: 2, role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "Build a game", created_at: "t" },
      {
        id: "b-end",
        ord: 3,
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "transcript",
        workflow_boundary: { event: "canceled", workflow_id: "plan" },
        created_at: "t",
      },
    ]);
    expect(items.map((item) => item.kind)).toEqual([
      "workflow_boundary",
      "user",
      "workflow_boundary",
    ]);
    expect(items[0]).toMatchObject({
      kind: "workflow_boundary",
      key: "b-start",
      label: "Plan started",
    });
    expect(items[2]).toMatchObject({
      kind: "workflow_boundary",
      key: "b-end",
      label: "Plan canceled",
    });
  });

  it("hides internal workflow_boundary rows from chat transcript", () => {
    const items = messagesToTranscriptItems([
      {
        id: "b1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "internal",
        workflow_boundary: { event: "started", workflow_id: "implement" },
        created_at: "t",
      },
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "Build a game", created_at: "t" },
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "user", text: "Build a game" });
  });

  it("hides coordinator-internal orientation and plan tool calls", () => {
    const planContent =
      "## Progress\n- [ ] ChessCore module (rules, board state, moves)\n- [ ] Verify build";
    const items = messagesToTranscriptItems([
      {
        id: "a-plan",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          { id: "tc-plan", name: "update_progress", args: { content: planContent } },
        ],
        created_at: "t",
      },
      {
        id: "tr-plan",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"status":"updated"}',
        tool_result: { content: '{"status":"updated"}', tool: "update_progress", tool_call_id: "tc-plan", assistant_message_id: "a-plan" },
        created_at: "t",
      },
      {
        id: "a0",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc0", name: "git_status", args: {} }],
        created_at: "t",
      },
      {
        id: "tr0",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"branch":"main"}',
        tool_result: { content: '{"branch":"main"}', tool: "git_status", tool_call_id: "tc0", assistant_message_id: "a0" },
        created_at: "t",
      },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1", name: "pack_board", args: {} }],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"board":"orientation"}',
        tool_result: { content: '{"board":"orientation"}', tool: "pack_board", tool_call_id: "tc1", assistant_message_id: "a1" },
        created_at: "t",
      },
      {
        id: "a1b",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1b", name: "git_status", args: {} }],
        created_at: "t",
      },
      {
        id: "tr1b",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"branch":"main","dirty":false}',
        tool_result: { content: '{"branch":"main","dirty":false}', tool: "git_status", tool_call_id: "tc1b", assistant_message_id: "a1b" },
        created_at: "t",
      },
      {
        id: "a2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc2", name: "read", args: { path: "main.go" } }],
        created_at: "t",
      },
      {
        id: "tr2",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "package main",
        tool_result: { content: "package main", tool: "read", tool_call_id: "tc2", assistant_message_id: "a2" },
        created_at: "t",
      },
    ]);
    const tools = items.filter((i) => i.kind === "tool");
    expect(tools).toHaveLength(1);
    expect(tools[0]?.part.tool).toBe("read");
    expect(tools[0]?.part.output).toBe("package main");
  });

  it("emits no row when an assistant chat turn is orientation tools only", () => {
    const items = messagesToTranscriptItems([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc1", name: "pack_board", args: {} }],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"board":"orientation"}',
        tool_result: { content: '{"board":"orientation"}' },
        created_at: "t",
      },
    ]);
    expect(items).toEqual([]);
  });

  it("worker layout shows git_status tool cards without coordinator placeholder", () => {
    const items = messagesToTranscriptItems(
      [
        {
          id: "a1",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          tool_calls: [
            { id: "tc1", name: "find", args: { path: "." } },
            { id: "tc2", name: "git_status", args: {} },
          ],
          created_at: "t",
        },
        {
          id: "tr1",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: '{"paths":[]}',
          tool_result: { content: '{"paths":[]}' },
          created_at: "t",
        },
        {
          id: "tr2",
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "branch main",
          tool_result: { content: "branch main" },
          created_at: "t",
        },
      ],
      { layout: "worker" },
    );
    expect(items.some((i) => i.kind === "assistant")).toBe(false);
    const tools = items.filter((i) => i.kind === "tool");
    expect(tools.map((t) => t.part.tool)).toEqual(["find", "git_status"]);
  });

  it("chat layout shows only visible tools when a turn mixes worker and orientation tools", () => {
    const items = messagesToTranscriptItems([
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [
          { id: "tc1", name: "find", args: { path: "." } },
          { id: "tc2", name: "git_status", args: {} },
        ],
        created_at: "t",
      },
      {
        id: "tr1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: '{"paths":[]}',
        tool_result: { content: '{"paths":[]}', tool: "find", tool_call_id: "tc1", assistant_message_id: "a1" },
        created_at: "t",
      },
      {
        id: "tr2",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "branch main",
        tool_result: { content: "branch main", tool: "git_status", tool_call_id: "tc2", assistant_message_id: "a1" },
        created_at: "t",
      },
    ]);
    expect(items.some((i) => i.kind === "assistant")).toBe(false);
    expect(
      items
        .filter((i) => i.kind === "tool")
        .map((t) => t.part.tool),
    ).toEqual(["find"]);
  });

  it("hides surface_note transport but keeps its visible note", () => {
    const messages: Message[] = [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        tool_calls: [{ id: "tc-note", name: "surface_note", args: { summary: "Milestone" } }],
        created_at: "t",
      },
      {
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: `{"status":"noted"}`,
        tool_result: {
          tool: "surface_note",
          tool_call_id: "tc-note",
          assistant_message_id: "a1",
          content: `{"status":"noted"}`,
          outcome: "completed",
          ui_visibility: "benign",
        },
        created_at: "t",
      },
      {
        id: "note-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "agent_note",
        content: "A verified milestone.",
        grounding: { traced: true },
        created_at: "t",
      },
    ];
    const quiet = createTranscriptDisplayProjector()(messages, undefined, { verboseMode: false })[0]!;
    const verbose = createTranscriptDisplayProjector()(messages, undefined, { verboseMode: true })[0]!;
    expect(quiet.some((item) => item.kind === "assistant" && item.key === "note-1")).toBe(true);
    expect(verbose.some((item) => item.kind === "activity_span")).toBe(true);
  });
});
