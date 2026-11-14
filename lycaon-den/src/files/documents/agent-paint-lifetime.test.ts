import { describe, expect, it } from "vitest";
import type { AgentIntent, AgentRead } from "../../api/types.ts";
import { AGENT_LINGER_MS, type AgentMark } from "../components/agent-presence.ts";
import {
  AGENT_FADE_IN_MS,
  AGENT_FADE_OUT_MS,
  AGENT_PAST_MARKS_MAX,
  agentMarkPhase,
  currentAgentSteps,
  nextAgentPaint,
  nextAgentPaintChange,
  type PaintedAgentMark,
  type ReportedAgentMark,
} from "./agent-paint-lifetime.ts";

const chat = { sessionId: "s1", title: "Fix login", slot: 0 };

const readItem = (sequence: number): AgentRead => ({
  id: `read-${sequence}`, sequence, tool_call_id: `call-${sequence}`, tool: "grep", root_id: "r1", path: "a.ts",
  extent: "matches", ranges: [{ start_line: sequence, end_line: sequence }], stale: false,
});

function match(sequence: number, sessionId = "s1"): ReportedAgentMark {
  const mark: AgentMark = { source: { kind: "read", item: readItem(sequence) }, key: `${sessionId}:read-${sequence}:0`,
    chat: { ...chat, sessionId }, kind: "match", stale: false, caret: false, range: { start_line: sequence, end_line: sequence } };
  return { mark, from: sequence, to: sequence + 1, color: undefined };
}

function read(sequence: number): ReportedAgentMark {
  const item = { ...readItem(sequence), extent: "range" as const };
  const mark: AgentMark = { source: { kind: "read", item }, key: `read-${sequence}:0`, chat, kind: "read", stale: false,
    caret: true, range: { start_line: sequence, end_line: sequence } };
  return { mark, from: sequence, to: sequence + 1, color: undefined };
}

function intent(id: string): ReportedAgentMark {
  const item: AgentIntent = { id, tool_call_id: id, tool: "edit", operation: "edit", state: "pending", root_id: "r1",
    path: "a.ts", extent: "range", ranges: [{ start_line: 1, end_line: 1 }] };
  const mark: AgentMark = { source: { kind: "intent", item }, key: `${id}:0`, chat, kind: "pending", stale: false,
    caret: false, range: { start_line: 1, end_line: 1 }, label: "about to edit" };
  return { mark, from: 0, to: 1, color: undefined };
}

const keys = (marks: readonly PaintedAgentMark[]) => marks.map((painted) => painted.mark.key);

describe("agent paint lifetime", () => {
  it("keeps a match its chat moved on from as past, then fades it, while the gutter keeps its read", () => {
    let paint = nextAgentPaint([], [match(1)], 0);
    paint = nextAgentPaint(paint, [match(1), match(2)], 1_000);
    const first = paint.find((painted) => painted.mark.key === "s1:read-1:0")!;
    expect(first).toMatchObject({ leftAt: null, supersededAt: 1_000 });
    expect(agentMarkPhase(first, 1_000)).toBe("past");
    expect(keys(currentAgentSteps(paint, 1_000))).toEqual(["s1:read-1:0", "s1:read-2:0"]);

    expect(agentMarkPhase(first, 1_000 + AGENT_LINGER_MS - 1)).toBe("past");
    expect(agentMarkPhase(first, 1_000 + AGENT_LINGER_MS)).toBe("leaving");
    expect(agentMarkPhase(first, 1_000 + AGENT_LINGER_MS + AGENT_FADE_OUT_MS)).toBe("gone");

    const later = 1_000 + AGENT_LINGER_MS + AGENT_FADE_OUT_MS;
    paint = nextAgentPaint(paint, [match(1), match(2)], later);
    expect(keys(paint)).toEqual(["s1:read-1:0", "s1:read-2:0"]);
    expect(keys(currentAgentSteps(paint, later))).toEqual(["s1:read-2:0"]);
  });

  it("keeps the time a match was superseded across later updates", () => {
    let paint = nextAgentPaint([], [match(1), match(2)], 0);
    paint = nextAgentPaint(paint, [match(1), match(2), match(3)], 5_000);
    expect(paint.map((painted) => painted.supersededAt)).toEqual([Number.NEGATIVE_INFINITY, 5_000, null]);
  });

  it("treats a match first seen behind its chat as already ended", () => {
    const paint = nextAgentPaint([], [match(1), match(2)], 0);
    expect(agentMarkPhase(paint[0]!, 0)).toBe("gone");
    expect(keys(currentAgentSteps(paint, 0))).toEqual(["s1:read-2:0"]);
    expect(nextAgentPaintChange(paint, AGENT_FADE_IN_MS)).toBeNull();
  });

  it("keeps a chat's matches past when its turn ends, then removes them", () => {
    let paint = nextAgentPaint([], [match(1)], 0);
    paint = nextAgentPaint(paint, [], 2_000);
    expect(paint[0]).toMatchObject({ leftAt: 2_000 });
    expect(agentMarkPhase(paint[0]!, 2_000)).toBe("past");
    expect(nextAgentPaint(paint, [], 2_000 + AGENT_LINGER_MS + AGENT_FADE_OUT_MS - 1)).toHaveLength(1);
    expect(nextAgentPaint(paint, [], 2_000 + AGENT_LINGER_MS + AGENT_FADE_OUT_MS)).toHaveLength(0);
  });

  it("fades other marks as soon as they end, and draws the fade even beside a live step", () => {
    let paint = nextAgentPaint([], [intent("c1"), read(1)], 0);
    expect(keys(currentAgentSteps(paint, 0))).toEqual(["c1:0"]);
    paint = nextAgentPaint(paint, [read(1)], 1_000);
    const ended = paint.find((painted) => painted.mark.key === "c1:0")!;
    expect(agentMarkPhase(ended, 1_000)).toBe("leaving");
    expect(keys(currentAgentSteps(paint, 1_000))).toEqual(["read-1:0", "c1:0"]);
    expect(nextAgentPaint(paint, [read(1)], 1_000 + AGENT_FADE_OUT_MS).map((painted) => painted.mark.key)).toEqual(["read-1:0"]);
  });

  it("draws at most the newest past matches per chat", () => {
    let paint: PaintedAgentMark[] = [];
    const reported: ReportedAgentMark[] = [];
    for (let sequence = 1; sequence <= 12; sequence++) {
      reported.push(match(sequence), match(sequence, "s2"));
      paint = nextAgentPaint(paint, [...reported], sequence * 10);
    }
    const drawn = currentAgentSteps(paint, 120);
    for (const sessionId of ["s1", "s2"]) {
      const mine = drawn.filter((painted) => painted.mark.chat.sessionId === sessionId);
      expect(mine).toHaveLength(AGENT_PAST_MARKS_MAX + 1);
      expect(mine.map((painted) => painted.from)).toEqual([4, 5, 6, 7, 8, 9, 10, 11, 12]);
    }
  });

  it("wakes for the next fade-in end, linger end, or fade end", () => {
    let paint = nextAgentPaint([], [match(1)], 0);
    expect(nextAgentPaintChange(paint, 0)).toBe(AGENT_FADE_IN_MS);
    paint = nextAgentPaint(paint, [], 1_000);
    expect(nextAgentPaintChange(paint, 1_000)).toBe(1_000 + AGENT_LINGER_MS);
    expect(nextAgentPaintChange(paint, 1_000 + AGENT_LINGER_MS)).toBe(1_000 + AGENT_LINGER_MS + AGENT_FADE_OUT_MS);
    expect(nextAgentPaintChange(paint, 1_000 + AGENT_LINGER_MS + AGENT_FADE_OUT_MS)).toBeNull();
  });
});
