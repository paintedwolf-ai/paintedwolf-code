import { describe, expect, it } from "vitest";
import type { Message, Session, TurnClock } from "../api/types.ts";
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
  content: id, created_at: "2026-09-12T10:00:00Z",
});

const clock = (sessionId: string, opening: string, patch: Partial<TurnClock> = {}): TurnClock => ({
  session_id: sessionId,
  opening_message_id: opening,
  active_ms: 5_000,
  work_ms: 4_000,
  running: false,
  settled_at: "2026-09-12T10:05:00Z",
  ...patch,
});

describe("app store turn clocks", () => {
  it("installs page clocks with the transcript and folds in older pages", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("s1"));
    store.actions.installTranscriptBaseline("s1", [prompt("u2", 2)], 2, {
      hasMoreBefore: true,
      turnClocks: [clock("s1", "u2")],
      turnLoads: [],
    });
    expect(Object.keys(store.state.turnClocks)).toEqual(["u2"]);

    store.actions.loadTranscriptPage({ edge: "older", beforeMessageId: "u2" }, {
      messages: [prompt("u1", 1)],
      after_cursor: "newer",
      watermark: 2,
      turn_clocks: { u1: clock("s1", "u1") },
      turn_loads: {},
    });
    expect(Object.keys(store.state.turnClocks).sort()).toEqual(["u1", "u2"]);
  });

  it("applies live edges for the transcript's session and ignores stale ones", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("s1"));
    store.actions.installTranscriptBaseline("s1", [prompt("u1", 1)], 1, {
      turnClocks: [clock("s1", "u1", { running: true, running_at: "2026-09-12T10:00:00Z", settled_at: undefined })],
      turnLoads: [],
    });
    store.actions.setTurnClock(clock("s1", "u1", { active_ms: 9_000, settled_at: "2026-09-12T10:09:00Z" }));
    expect(store.state.turnClocks.u1?.active_ms).toBe(9_000);
    expect(store.state.turnClock?.opening_message_id).toBe("u1");

    // A running edge delivered late does not reopen a settled turn.
    store.actions.setTurnClock(clock("s1", "u1", { running: true, settled_at: undefined }));
    expect(store.state.turnClocks.u1?.running).toBe(false);

    store.actions.setTurnClock(clock("other", "x1"));
    expect(store.state.turnClocks.x1).toBeUndefined();
  });

  it("starts empty for the next session's transcript", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("s1"));
    store.actions.installTranscriptBaseline("s1", [prompt("u1", 1)], 1, { turnClocks: [clock("s1", "u1")] });
    store.actions.setCurrentSession(session("s2"));
    store.actions.installTranscriptBaseline("s2", [prompt("v1", 1)], 1, { turnClocks: [] });
    expect(store.state.turnClocks).toEqual({});
  });

  it("records the host's seen stamp on the current session only", () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session("s1"));
    store.actions.noteSessionSeen({ ...session("s1"), seen_at: "2026-09-12T11:00:00Z" });
    store.actions.noteSessionSeen({ ...session("s2"), seen_at: "2026-09-12T12:00:00Z" });
    expect(store.state.currentSession?.seen_at).toBe("2026-09-12T11:00:00Z");
  });
});
