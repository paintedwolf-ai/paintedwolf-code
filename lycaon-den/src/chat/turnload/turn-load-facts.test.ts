import { describe, expect, it } from "vitest";
import type { Message, TurnLoad } from "../../api/types.ts";
import { buildTurnLoadIndex, toolLoadFact } from "./turn-load-facts.ts";

const user = (id: string, ord: number): Message => ({
  id, ord, seq: ord, role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: id, created_at: "t",
});
const assistant = (id: string, ord: number): Message => ({
  id, ord, seq: ord, role: "assistant", origin: "model", authority: "none", trust_tier: "trusted", content: "", created_at: "t",
});
const engine = { name: "Bialy", model: "mmbert-base", head: "turn-load", label: "Bialy/mmbert-base#turn-load" };

const part = (tool: string, assistantMessageId: string, toolCallId = "") => ({ tool, assistantMessageId, toolCallId });

describe("tool load facts", () => {
  const messages = [user("u1", 1), assistant("a1", 2), assistant("a2", 3), user("u2", 4), assistant("a3", 5)];
  const loads: Record<string, TurnLoad[]> = {
    u1: [
      { session_id: "s1", trigger: "turn", opening_message_id: "u1", abstained: false, elapsed_ms: 400, engine,
        floor: ["read"], tools: [
          { tool: "git_commit", source: "predicted", p: 0.88, carried: false },
          { tool: "git_status", source: "companion", with: "git_commit", carried: false },
          { tool: "edit", source: "predicted", p: 0.71, carried: true },
          { tool: "web_search", source: "requested", need: "search the web", carried: true },
        ] },
      { session_id: "s1", trigger: "request", opening_message_id: "u1", tool_call_id: "c1", abstained: false, elapsed_ms: 310, engine,
        floor: [], tools: [], match: { need: "a worker to bisect the test", by: "engine", names: ["task"] } },
      { session_id: "s1", trigger: "lookup", opening_message_id: "u1", tool_call_id: "c2", abstained: false, elapsed_ms: 3,
        floor: [], tools: [], match: { need: "release-notes", by: "name", names: ["release-notes"] } },
      { session_id: "s1", trigger: "tool_event", opening_message_id: "u1", tool_call_id: "c4", abstained: false, elapsed_ms: 640, engine,
        floor: [], tools: [], match: { need: "capture_page", by: "engine", names: ["verify-visual-change"] },
        preloaded_skill: { name: "verify-visual-change", score: 3.6 } },
      { session_id: "s1", trigger: "tool_event", opening_message_id: "u1", tool_call_id: "c5", abstained: false, elapsed_ms: 520, engine,
        floor: [], tools: [], match: { need: "git_commit", by: "engine", names: ["commit-in-logical-groups"] } },
    ],
    u2: [
      { session_id: "s1", trigger: "turn", opening_message_id: "u2", abstained: true, reason: "engine unavailable", elapsed_ms: 1, floor: [], tools: [] },
      { session_id: "s1", trigger: "request", opening_message_id: "u2", tool_call_id: "c3", abstained: true, reason: "engine unavailable", elapsed_ms: 2,
        floor: [], tools: [], match: { need: "git tools", by: "none", names: [] } },
    ],
  };
  const index = buildTurnLoadIndex(messages, loads);

  it("names the tools the turn decision offered, on every assistant row of the turn", () => {
    expect(toolLoadFact(index, part("git_commit", "a1"))).toEqual({ kind: "offered", p: 0.88 });
    expect(toolLoadFact(index, part("git_commit", "a2"))).toEqual({ kind: "offered", p: 0.88 });
    expect(toolLoadFact(index, part("read", "a1"))).toBeUndefined();
  });

  it("names the predicted tool a companion loaded with", () => {
    expect(toolLoadFact(index, part("git_status", "a1"))).toEqual({ kind: "companion", with: "git_commit" });
  });

  it("says a tool stood from an earlier turn, whatever first loaded it", () => {
    expect(toolLoadFact(index, part("edit", "a1"))).toEqual({ kind: "kept" });
    expect(toolLoadFact(index, part("web_search", "a2"))).toEqual({ kind: "kept" });
  });

  it("names the request that offered a tool the decision had not, and what it picked", () => {
    expect(toolLoadFact(index, part("task", "a2"))).toEqual({ kind: "requested", need: "a worker to bisect the test" });
    expect(toolLoadFact(index, part("request_tools", "a2", "c1"))).toEqual({
      kind: "matched", need: "a worker to bisect the test", by: "engine", names: ["task"], elapsedMs: 310, missed: true,
    });
  });

  it("reads a lookup by its call", () => {
    expect(toolLoadFact(index, part("skills_read", "a2", "c2"))).toEqual({
      kind: "matched", need: "release-notes", by: "name", names: ["release-notes"], elapsedMs: 3, missed: false,
    });
  });

  it("states nothing for a turn without a decision, except how a request matched", () => {
    expect(toolLoadFact(index, part("git_commit", "a3"))).toBeUndefined();
    expect(toolLoadFact(index, part("request_tools", "a3", "c3"))).toEqual({
      kind: "matched", need: "git tools", by: "none", names: [], elapsedMs: 2, missed: false,
    });
  });

  it("names the skill the local AI chose when a call was the turn's first loadable tool", () => {
    expect(toolLoadFact(index, part("capture_page", "a2", "c4"))).toEqual({
      kind: "skill", name: "verify-visual-change", read: true, elapsedMs: 640,
    });
    expect(toolLoadFact(index, part("git_commit", "a2", "c5"))).toEqual({
      kind: "skill", name: "commit-in-logical-groups", read: false, elapsedMs: 520,
    });
  });

  it("indexes nothing without receipts", () => {
    expect(toolLoadFact(buildTurnLoadIndex(messages, undefined), part("git_commit", "a1"))).toBeUndefined();
  });
});
