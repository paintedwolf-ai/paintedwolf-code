import { describe, expect, it } from "vitest";
import type { Message, TurnLoad } from "../../api/types.ts";
import { turnLoadRowSummary, turnLoadRows } from "./turn-load-rows.ts";

const user = (id: string, ord: number): Message => ({
  id, ord, seq: ord, role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: id, created_at: "t",
});

const assistant = (id: string, ord: number, calls: readonly [string, string][] = [], patch: Partial<Message> = {}): Message => ({
  id, ord, seq: ord, role: "assistant", origin: "model", authority: "none", trust_tier: "trusted", content: "",
  created_at: "t", tool_calls: calls.map(([callId, name]) => ({ id: callId, name, args: {} })), ...patch,
});

const result = (id: string, ord: number, assistantId: string, callId: string, tool: string): Message => ({
  id, ord, seq: ord, role: "tool", origin: "host", authority: "system", trust_tier: "trusted", content: "ok", created_at: "t",
  tool_result: { tool, tool_call_id: callId, assistant_message_id: assistantId, content: "ok", outcome: "completed" },
});

const engine = { name: "Bialy", model: "mmbert-base", head: "turn-load", label: "Bialy/mmbert-base#turn-load" };

const turn = (opening: string, patch: Partial<TurnLoad> = {}): TurnLoad => ({
  session_id: "s1", trigger: "turn", opening_message_id: opening, abstained: false, elapsed_ms: 438, engine,
  kind: { value: "change", confidence: 0.86 },
  floor: ["read"],
  tools: [{ tool: "git_commit", source: "predicted", p: 0.88, carried: false }, { tool: "git_status", source: "predicted", p: 0.91, carried: false }],
  guides: { rendered: 6, omitted: 9 },
  ...patch,
});

describe("turn load rows", () => {
  it("anchors a decided turn's rows to the first assistant row that called a tool", () => {
    const messages = [
      user("u1", 1),
      assistant("a1", 2, [], { content: "Looking." }),
      assistant("a2", 3, [["c1", "git_status"], ["c2", "git_commit"]]),
      result("r1", 4, "a2", "c1", "git_status"),
    ];
    const rows = turnLoadRows(messages, { u1: [turn("u1")] });
    expect(rows.map((row) => [row.role, row.assistantMessageId, row.anchorMessageId, row.sub])).toEqual([
      ["tools", "a2", "a2", 8],
    ]);
  });

  it("draws nothing for an abstained decision or a turn that never called a tool", () => {
    const messages = [user("u1", 1), assistant("a1", 2, [], { content: "Just an answer." })];
    expect(turnLoadRows(messages, { u1: [turn("u1")] })).toEqual([]);
    const withTools = [user("u1", 1), assistant("a1", 2, [["c1", "read"]])];
    expect(turnLoadRows(withTools, { u1: [turn("u1", { abstained: true, reason: "engine unavailable", engine: undefined })] })).toEqual([]);
  });

  it("ignores withdrawn proposals and lookup receipts", () => {
    const messages = [user("u1", 1), assistant("a0", 2, [["dead", "command"]], { kind: "draft", draft_status: "withdrawn" }), assistant("a1", 3, [["c1", "read"]])];
    const loads: TurnLoad[] = [turn("u1"), { session_id: "s1", trigger: "lookup", opening_message_id: "u1", tool_call_id: "c1", abstained: false, elapsed_ms: 12, floor: [], tools: [], match: { need: "commit-in-logical-groups", by: "name", names: ["commit-in-logical-groups"] } }];
    const rows = turnLoadRows(messages, { u1: loads });
    expect(rows.map((row) => [row.role, row.anchorMessageId, row.sub])).toEqual([["tools", "a1", 8]]);
  });

  it("summarizes rows the way a tool row reads", () => {
    const [tools] = turnLoadRows([user("u1", 1), assistant("a1", 2, [["c1", "read"]])], { u1: [turn("u1")] });
    expect(turnLoadRowSummary(tools!)).toEqual({ title: "git_commit, git_status", operation: "offered", state: "438 ms" });
    const [floor] = turnLoadRows([user("u1", 1), assistant("a1", 2, [["c1", "read"]])], {
      u1: [turn("u1", { tools: [], kind: { value: "inspect", confidence: 0.9 } })],
    });
    expect(turnLoadRowSummary(floor!)).toEqual({ title: "floor only · 9 guides left out", operation: "inspect", state: "438 ms" });
  });
});
