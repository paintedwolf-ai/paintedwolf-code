import { describe, expect, it } from "vitest";
import type { Message, Session, TurnLoad } from "../api/types.ts";
import { createAppStore } from "./app-state.ts";

const session = (id: string): Session => ({
  id,
  owner_person_id: "00000000-0000-4000-8000-000000000002",
  project_id: "00000000-0000-4000-8000-000000000001",
  workspace_path: "/tmp/p",
  posture: "build",
  status: "idle",
  created_at: "t",
  activity_at: "t",
  updated_at: "t",
});

const prompt = (id: string, ord: number): Message => ({
  id, ord, seq: ord, role: "user", origin: "user", authority: "user", trust_tier: "trusted",
  content: id, created_at: "2026-09-25T10:00:00Z",
});

const load = (sessionId: string, opening: string, patch: Partial<TurnLoad> = {}): TurnLoad => ({
  session_id: sessionId,
  opening_message_id: opening,
  trigger: "turn",
  abstained: false,
  elapsed_ms: 210,
  floor: [],
  tools: [],
  ...patch,
});

describe("app store turn loads", () => {
  it("installs page receipts with the transcript and folds in older pages", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("s1"));
    store.actions.installTranscriptBaseline("s1", [prompt("u2", 2)], 2, {
      hasMoreBefore: true,
      turnLoads: [load("s1", "u2")],
    });
    expect(Object.keys(store.state.turnLoads)).toEqual(["u2"]);

    store.actions.loadTranscriptPage({ edge: "older", beforeMessageId: "u2" }, {
      messages: [prompt("u1", 1)],
      after_cursor: "newer",
      watermark: 2,
      turn_clocks: {},
      turn_loads: { u1: [load("s1", "u1"), load("s1", "u1", { trigger: "request", tool_call_id: "c1" })] },
    });
    expect(Object.keys(store.state.turnLoads).sort()).toEqual(["u1", "u2"]);
    expect(store.state.turnLoads.u1?.map((r) => r.trigger)).toEqual(["turn", "request"]);
  });

  it("applies live receipts for the transcript's session, replacing a repeat, and ignores other sessions", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("s1"));
    store.actions.installTranscriptBaseline("s1", [prompt("u1", 1)], 1, {
      turnLoads: [load("s1", "u1", { elapsed_ms: 100 })],
    });
    store.actions.setTurnLoad(load("s1", "u1", { elapsed_ms: 300 }));
    expect(store.state.turnLoads.u1).toHaveLength(1);
    expect(store.state.turnLoads.u1?.[0]?.elapsed_ms).toBe(300);

    store.actions.setTurnLoad(load("s2", "u9"));
    expect(store.state.turnLoads.u9).toBeUndefined();
  });

  it("clears receipts with the transcript", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("s1"));
    store.actions.installTranscriptBaseline("s1", [prompt("u1", 1)], 1, { turnLoads: [load("s1", "u1")] });
    store.actions.installTranscriptBaseline("s2", [prompt("v1", 1)], 1, {});
    expect(store.state.turnLoads).toEqual({});
  });
});
