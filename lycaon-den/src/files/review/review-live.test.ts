import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSessionPresence, SourceChange, SourceWalkResponse } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { MIN_VISIBLE_MS } from "../../ui/min-visible-hold.ts";
import {
  noteReviewChangeStats,
  noteReviewLanding,
  projectLiveRows,
  resetReviewLiveForTests,
  reviewLiveSnapshot,
  syncReviewPresence,
} from "./review-live.ts";
import { reviewRowKey } from "./review-model.ts";
import { resetFilesStagePaneForTests } from "./review-pane.ts";
import { resetScopeResolutionForTests, resolveScope } from "../tree/scope-resolution.ts";

const PROJECT = "p1";

function chat(sessionId: string, over: Partial<AgentSessionPresence> = {}): AgentSessionPresence {
  return { session_id: sessionId, title: `Chat ${sessionId}`, turn: 3, activities: [], reads: [], intents: [], worker_drafts: [], ...over };
}

function sessions(...chats: AgentSessionPresence[]): Map<string, AgentSessionPresence> {
  return new Map(chats.map((c) => [c.session_id, c]));
}

function editing(sessionId: string, path: string, toolCallId: string): AgentSessionPresence {
  return chat(sessionId, {
    intents: [{ id: `i-${toolCallId}`, tool_call_id: toolCallId, tool: "edit", operation: "edit", state: "pending",
      root_id: "r1", path, extent: "range", ranges: [{ start_line: 1, end_line: 1 }] }],
  });
}

function landing(path: string, over: Partial<SourceChange> = {}): SourceChange {
  return { root_id: "r1", path, op: "write", origin: "agent", ts: "2026-09-18T00:00:00Z", ...over } as SourceChange;
}

function paths(): string[] {
  return reviewLiveSnapshot(PROJECT).rows.map((row) => `${row.path}:${row.state}`);
}

/** One comparison answer; its claim is newer than every landing noted before it started. */
function answer() {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const client = stubClient({
    listProjectSourceWalk: vi.fn(async (): Promise<SourceWalkResponse> => {
      await gate;
      return { baseline: "presentation", files: [], git_changes: [], commands: [], turns: [], commit_available: false, next_cursor: undefined };
    }),
    listProjectSourceSeen: vi.fn(async () => ({ files: [], next_cursor: undefined })),
  });
  const flight = resolveScope(PROJECT, client, null);
  return { land: async () => { release(); await flight; } };
}

describe("projectLiveRows", () => {
  it("lists what agents are about to change, never what they read", () => {
    const rows = projectLiveRows(sessions(chat("s1", {
      activities: [
        { tool_call_id: "r", tool: "read", root_id: "r1", path: "read.ts", kind: "reading" },
        { tool_call_id: "g", tool: "grep", root_id: "r1", path: "src", kind: "reading" },
        { tool_call_id: "w", tool: "write", root_id: "r1", path: "write.ts", kind: "editing" },
      ],
      reads: [{ id: "x", tool_call_id: "r", tool: "read", root_id: "r1", path: "read.ts", extent: "whole_file", ranges: [], sequence: 1, stale: false } as never],
    })));
    expect([...rows.values()].map((row) => row.path)).toEqual(["write.ts"]);
  });

  it("keeps the strongest state per file and every call and job behind it", () => {
    const rows = projectLiveRows(sessions(
      chat("s1", {
        worker_drafts: [{ worker_id: "j1", state: "ready", root_id: "r1", path: "a.ts", extent: "whole_file", ranges: [] }],
      }),
      chat("s2", {
        turn: 7,
        intents: [{ id: "i", tool_call_id: "c2", tool: "edit", operation: "edit", state: "awaiting_approval",
          root_id: "r1", path: "a.ts", extent: "range", ranges: [] }],
      }),
    ));
    expect(rows.get(reviewRowKey("r1", "a.ts"))).toEqual({
      rootId: "r1", path: "a.ts", state: "waiting", chatId: "s2", chatTitle: "Chat s2", turn: 7,
      toolCallIds: ["c2"], jobIds: ["j1"],
    });
  });

  it("names worker draft states the way Review reads them", () => {
    const rows = projectLiveRows(sessions(chat("s1", {
      worker_drafts: (["reserved", "drafting", "ready", "landing"] as const).map((state) => ({
        worker_id: "j", state, root_id: "r1", path: `${state}.ts`, extent: "whole_file" as const, ranges: [],
      })),
    })));
    expect([...rows.values()].map((row) => row.state)).toEqual(["reserved", "sandbox", "ready", "landing"]);
  });
});

describe("review live rows", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    resetReviewLiveForTests();
    resetScopeResolutionForTests();
    resetFilesStagePaneForTests();
  });
  afterEach(() => {
    resetReviewLiveForTests();
    vi.useRealTimers();
  });

  it("holds a landed edit until an answer read after the landing arrives", async () => {
    syncReviewPresence(PROJECT, sessions(editing("s1", "a.ts", "c1")));
    expect(paths()).toEqual(["a.ts:editing"]);

    // A read already in flight when the file lands may not include it.
    const inFlight = answer();
    noteReviewLanding(PROJECT, landing("a.ts", { session_id: "s1", tool_call_id: "c1" }));
    syncReviewPresence(PROJECT, sessions());
    vi.advanceTimersByTime(MIN_VISIBLE_MS * 4);
    expect(paths()).toEqual(["a.ts:landing"]);

    await inFlight.land();
    expect(paths()).toEqual(["a.ts:landing"]);

    await answer().land();
    vi.advanceTimersByTime(MIN_VISIBLE_MS);
    expect(paths()).toEqual([]);
  });

  it("holds a landing that arrives just after its presence ended", async () => {
    syncReviewPresence(PROJECT, sessions(editing("s1", "a.ts", "c1")));
    syncReviewPresence(PROJECT, sessions());
    noteReviewLanding(PROJECT, landing("a.ts", { session_id: "s1", tool_call_id: "c1" }));
    vi.advanceTimersByTime(MIN_VISIBLE_MS * 4);
    expect(paths()).toEqual(["a.ts:landing"]);
    await answer().land();
    vi.advanceTimersByTime(MIN_VISIBLE_MS);
    expect(paths()).toEqual([]);
  });

  it("shows work that never landed for the minimum readable time, then drops it", () => {
    syncReviewPresence(PROJECT, sessions(editing("s1", "a.ts", "c1")));
    syncReviewPresence(PROJECT, sessions());
    vi.advanceTimersByTime(MIN_VISIBLE_MS - 1);
    expect(paths()).toEqual(["a.ts:editing"]);
    vi.advanceTimersByTime(1);
    expect(paths()).toEqual([]);
  });

  it("hands a worker's drafts to its promotion by job", async () => {
    syncReviewPresence(PROJECT, sessions(chat("s1", {
      worker_drafts: [{ worker_id: "j1", state: "landing", root_id: "r1", path: "w.ts", extent: "whole_file", ranges: [] }],
    })));
    noteReviewLanding(PROJECT, landing("w.ts", { session_id: "s1", worker_id: "j1", tool_call_id: "promote-1" }));
    syncReviewPresence(PROJECT, sessions());
    vi.advanceTimersByTime(MIN_VISIBLE_MS * 4);
    expect(paths()).toEqual(["w.ts:landing"]);
    await answer().land();
    vi.advanceTimersByTime(MIN_VISIBLE_MS);
    expect(paths()).toEqual([]);
  });

  it("does not hold a row for someone else's write to the same file", () => {
    syncReviewPresence(PROJECT, sessions(editing("s1", "a.ts", "c1")));
    noteReviewLanding(PROJECT, landing("a.ts", { origin: "user", session_id: "s1" }));
    syncReviewPresence(PROJECT, sessions());
    vi.advanceTimersByTime(MIN_VISIBLE_MS);
    expect(paths()).toEqual([]);
  });

  it("attaches line counts to a file's newest landing once", () => {
    noteReviewLanding(PROJECT, landing("a.ts"));
    noteReviewChangeStats(PROJECT, "r1", "a.ts", { added: 3, removed: 1 });
    noteReviewChangeStats(PROJECT, "r1", "a.ts", { added: 9, removed: 9 });
    expect(reviewLiveSnapshot(PROJECT).stats.get(reviewRowKey("r1", "a.ts"))).toEqual({ added: 3, removed: 1 });
    noteReviewLanding(PROJECT, landing("a.ts"));
    expect(reviewLiveSnapshot(PROJECT).stats.has(reviewRowKey("r1", "a.ts"))).toBe(false);
  });
});
