import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { sourceTextHash } from "../../test/file-edit-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import type {
  SourceGitChange,
  SourceWalkEffect,
  SourceWalkFile,
  SourceWalkResponse,
} from "../../api/types.ts";
import type { WalkStep } from "./walk-model.ts";
import {
  currentWalkStep,
  enterWalk,
  isWalking,
  leaveWalk,
  refreshWalk,
  requestWalkRefresh,
  resetWalkForTests,
  setWalkAt,
  stepWalk,
  subscribeWalk,
  walkNewStepCount,
  walkState,
  walkToLatest,
  walkToStart,
} from "./walk-store.ts";

const PID = "p1";
const SID = "s1";

function effect(
  id: string,
  path: string,
  ordinal: number,
  overrides: Partial<SourceWalkEffect> = {},
): SourceWalkEffect {
  return sourceEffectFixture({
    id,
    project_id: PID,
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project",
    root_id: "r1",
    path,
    op: "write",
    entry_kind: "file",
    origin: "agent",
    session_id: SID,
    turn: 1,
    ordinal,
    cause: "tool",
    tool_name: "edit",
    capture_quality: "exact",
    observed_at: "2026-08-09T10:00:00Z",
    ...overrides,
  });
}

function file(
  path: string,
  effects: SourceWalkEffect[],
  overrides: Partial<SourceWalkFile> = {},
): SourceWalkFile {
  return {
    file_id: `file-${path}`,
    root_id: "r1",
    path,
    changed_since_presented: false,
    unpresented_agent_effects: 0,
    tip: { state: "content", sha256: `sha-${path}` },
    head_match: "unknown",
    effects,
    ...overrides,
  };
}

function changes(): SourceWalkResponse {
  return {
    baseline: "session:s1",
    files: [
      file("a.ts", [effect("c1", "a.ts", 1, { tool_call_id: "t1" })], {
        tip: { state: "content", sha256: "sha" },
      }),
      file("b.ts", [effect("c2", "b.ts", 2, { tool_call_id: "t2" })], {
        tip: { state: "content", sha256: "sha-b" },
      }),
    ],
    git_changes: [], commands: [], turns: [],
    commit_available: false,
    next_cursor: undefined,
  };
}

function stepEffectId(step: WalkStep): string {
  return step.kind === "effect" ? step.effect.id : step.key;
}

function effectIdOf(step: WalkStep | null): string | undefined {
  return step && step.kind === "effect" ? step.effect.id : undefined;
}

function fakeClient(overrides: Partial<Record<string, unknown>> = {}) {
  const supplied = (overrides.listProjectSourceWalk ?? (async () => changes())) as (
    ...args: unknown[]
  ) => unknown;
  const { listProjectSourceWalk: _ignored, ...rest } = overrides;
  return stubFilesClient({
    readComparison: vi.fn(async () => ({
      in_range: true,
      before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
      after: { state: "content", size_bytes: 0, availability: "available", content: "" },
      location_changed: false,
    })),
    ...rest,
    listProjectSourceWalk: vi.fn(async (...args: unknown[]) => await supplied(...args)),
  });
}

let notices = createNoticeStore();
const noticeRows = () => selectProjectNoticeGroups(notices.index()).find((group) => group.projectId === PID)?.notices ?? [];

beforeEach(() => {
  resetWalkForTests();
  notices = createNoticeStore();
  registerNoticePublisher(notices);
});
afterEach(() => registerNoticePublisher(null));

describe("entering and leaving", () => {
  it("goes active immediately, before the fetch settles", async () => {
    let resolve!: (v: unknown) => void;
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(() => new Promise((r) => (resolve = r))),
    });
    const pending = enterWalk(PID, client, SID);
    expect(isWalking(PID)).toBe(true);
    expect(walkState(PID).status).toBe("loading");
    resolve(changes());
    await pending;
    expect(walkState(PID).status).toBe("ready");
  });

  it("keeps the ready presentation unchanged while opening another chat", async () => {
    await enterWalk(PID, fakeClient(), SID);
    const held = walkState(PID);
    let resolve!: (value: SourceWalkResponse) => void;
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(() => new Promise<SourceWalkResponse>((r) => {
        resolve = r;
      })),
    });
    const seen = vi.fn();
    const unsubscribe = subscribeWalk(seen);

    const pending = enterWalk(PID, client, "s2");
    expect(walkState(PID)).toBe(held);
    expect(seen).not.toHaveBeenCalled();

    resolve(changes());
    await pending;
    unsubscribe();
    expect(walkState(PID).status).toBe("ready");
    expect(walkState(PID).sessionId).toBe("s2");
    expect(seen).toHaveBeenCalledOnce();
  });

  it("keeps the ready presentation when replacement cannot connect", async () => {
    await enterWalk(PID, fakeClient(), SID);
    const held = walkState(PID);

    await enterWalk(PID, null, "s2");

    expect(walkState(PID).walk).toBe(held.walk);
    expect(walkState(PID).sessionId).toBe(held.sessionId);
    expect(walkState(PID).status).toBe("error");
    expect(noticeRows()).toMatchObject([{ code: "walk_unavailable", message: "Not connected." }]);
  });

  it("lands on the earliest step", async () => {
    await enterWalk(PID, fakeClient(), SID);
    const s = walkState(PID);
    expect(s.walk.steps).toHaveLength(2);
    expect(s.at).toBe(0);
  });

  it("lands on the selected tree file without narrowing the walk", async () => {
    await enterWalk(
      PID,
      fakeClient(), SID,
      { rootId: "r1", path: "b.ts" },
    );
    const s = walkState(PID);
    expect(s.walk.steps).toHaveLength(2);
    expect(s.at).toBe(1);
    expect(effectIdOf(currentWalkStep(PID))).toBe("c2");
  });

  it("explains a selected Git-only file without inventing a timeline step", async () => {
    await enterWalk(PID, fakeClient(), SID, { rootId: "r1", path: "unobserved.ts" });
    expect(walkState(PID).walk.steps).toHaveLength(2);
    expect(walkState(PID).notice).toContain("no recorded changes in this chat");
    expect(walkState(PID).walk.steps.some((step) => step.key.includes("unobserved"))).toBe(false);
  });

  it("honors a selected file from an earlier turn", async () => {
    const response = changes();
    response.files[1]!.effects[0]!.turn = 2;
    response.turns = [
      { session_id: SID, turn: 1, message_id: "user-1", prompt: "First", observed_at: "2026-08-09T09:00:00Z" },
      { session_id: SID, turn: 2, message_id: "user-2", prompt: "Second", observed_at: "2026-08-09T10:00:00Z" },
    ];
    await enterWalk(PID, fakeClient({ listProjectSourceWalk: async () => response }), SID, {
      rootId: "r1", path: "a.ts",
    });

    expect(walkState(PID).walk.steps).toHaveLength(2);
    expect(effectIdOf(currentWalkStep(PID))).toBe("c1");
  });

  it("uses ledger tool identity", async () => {
    await enterWalk(PID, fakeClient(), SID);
    setWalkAt(PID, 0);
    expect(effectIdOf(currentWalkStep(PID))).toBe("c1");
    expect(currentWalkStep(PID)?.toolCallId).toBe("t1");
    setWalkAt(PID, 1);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
    expect(effectIdOf(currentWalkStep(PID))).toBe("c2");
    expect(currentWalkStep(PID)?.toolCallId).toBe("t2");
  });

  it("follows the host cursor even when the first page is short", async () => {
    let calls = 0;
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async (_p: string, q: { cursor?: string }) => {
        calls += 1;
        return q.cursor === undefined
          ? { ...changes(), next_cursor: "cursor-2" }
          : { files: [], git_changes: [], commands: [], turns: [], commit_available: false, next_cursor: undefined };
      }),
    });
    await enterWalk(PID, client, SID);
    expect(calls).toBe(2);
    expect(walkState(PID).status).toBe("ready");
    expect(walkState(PID).walk.steps).toHaveLength(2);
  });

  it("rejects an incomplete walk when the host cursor stops moving", async () => {
    let calls = 0;
    const client = fakeClient({
      // A cursor that never advances would page without end.
      listProjectSourceWalk: vi.fn(async () => {
        calls += 1;
        return { ...changes(), next_cursor: "stuck" };
      }),
    });
    await enterWalk(PID, client, SID);
    expect(calls).toBe(2);
    expect(walkState(PID).status).toBe("error");
    expect(walkState(PID).walk.steps).toHaveLength(0);
  });

  it("asks the host for this session's story, outside changes included", async () => {
    const client = fakeClient();
    await enterWalk(PID, client, SID);
    expect(client.listProjectSourceWalk).toHaveBeenCalledWith(
      PID,
      expect.objectContaining({
        baseline: `session:${SID}`, sessionId: SID, includeOutsideChanges: true,
      }),
    );
  });

  it("labels human effects from the ledger", async () => {
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [
          file(
            "note.md",
            [effect("c-human", "note.md", 1, { origin: "user", cause: "human_edit" })],
            { tip: { state: "content", sha256: "sha-h" } },
          ),
        ],
        git_changes: [], commands: [], turns: [],
        commit_available: false,
        next_cursor: undefined,
      })),
    });
    await enterWalk(PID, client, SID);
    expect(walkState(PID).walk.steps).toHaveLength(1);
    expect(currentWalkStep(PID)?.label).toBe("edit");
  });

  it("loads every ledger page before building the walk", async () => {
    const client = fakeClient({
      listProjectSourceWalk: vi
        .fn()
        .mockResolvedValueOnce({
          ...changes(),
          files: changes().files.slice(0, 1),
          // A full scan, so the host reports more.
          next_cursor: "500",
        })
        .mockResolvedValueOnce({
          ...changes(),
          files: changes().files.slice(1),
          next_cursor: undefined,
        }),
    });

    await enterWalk(PID, client, SID);
    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
      "c1",
      "c2",
    ]);
    expect(walkState(PID).walk.steps.map((step) => step.toolCallId)).toEqual([
      "t1",
      "t2",
    ]);
    expect(client.listProjectSourceWalk).toHaveBeenCalledTimes(2);
  });

  it("joins one file split across ledger pages", async () => {
    const newest = effect("c2", "a.ts", 2, { tool_call_id: "t2" });
    const oldest = effect("c1", "a.ts", 1, { tool_call_id: "t1" });
    const client = fakeClient({
      listProjectSourceWalk: vi
        .fn()
        .mockResolvedValueOnce({
          files: [file("a.ts", [newest])],
          git_changes: [], commands: [], turns: [],
          commit_available: false,
          next_cursor: "2",
        })
        .mockResolvedValueOnce({
          files: [file("a.ts", [oldest])],
          git_changes: [], commands: [], turns: [],
          commit_available: false,
          next_cursor: undefined,
        }),
    });

    await enterWalk(PID, client, SID);

    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
      "c1",
      "c2",
    ]);
  });

  it("leaving clears the record entirely", async () => {
    await enterWalk(PID, fakeClient(), SID);
    leaveWalk(PID);
    expect(isWalking(PID)).toBe(false);
    expect(currentWalkStep(PID)).toBeNull();
    expect(walkState(PID).walk.steps).toHaveLength(0);
  });

  it("leaving twice is a no-op", async () => {
    await enterWalk(PID, fakeClient(), SID);
    leaveWalk(PID);
    const seen: string[] = [];
    const off = subscribeWalk((id) => seen.push(id));
    leaveWalk(PID);
    off();
    expect(seen).toEqual([]);
  });

  it("reports a failed load without pretending to be idle", async () => {
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => {
        throw new Error("offline");
      }),
    });
    await enterWalk(PID, client, SID);
    expect(isWalking(PID)).toBe(true);
    expect(walkState(PID).status).toBe("error");
    expect(noticeRows()).toMatchObject([{ code: "walk_unavailable", message: "offline" }]);
  });

  it("ignores a stale flight superseded by a newer entry", async () => {
    let slow!: (v: unknown) => void;
    const client = fakeClient({
      listProjectSourceWalk: vi
        .fn()
        .mockImplementationOnce(() => new Promise((r) => (slow = r)))
        .mockImplementation(async () => changes()),
    });
    const first = enterWalk(PID, client, SID);
    await enterWalk(PID, fakeClient(), "s2");
    slow(changes());
    await first;
    expect(walkState(PID).sessionId).toBe("s2");
    expect(walkState(PID).walk.steps).toHaveLength(2);
  });

  it("refuses to enter without a project", async () => {
    await enterWalk("  ", fakeClient(), "s1");
    expect(isWalking(PID)).toBe(false);
  });
});

describe("moving the playhead", () => {
  beforeEach(async () => {
    resetWalkForTests();
    await enterWalk(PID, fakeClient(), SID);
  });

  it("clamps at both ends", async () => {
    setWalkAt(PID, -10);
    expect(walkState(PID).at).toBe(0);
    setWalkAt(PID, 99);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
  });

  it("steps by one", async () => {
    stepWalk(PID, -1);
    expect(walkState(PID).at).toBe(0);
    stepWalk(PID, 1);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
  });

  it("jumps to the ends", async () => {
    walkToStart(PID);
    expect(walkState(PID).at).toBe(0);
    walkToLatest(PID);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
  });

  it("notifies for the request and settled presentation", async () => {
    const seen: string[] = [];
    const off = subscribeWalk((id) => seen.push(id));
    setWalkAt(PID, 1);
    setWalkAt(PID, 1);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
    off();
    expect(seen).toEqual([PID, PID]);
  });

  it("publishes an explicit reselection of the current step", () => {
    const initial = walkState(PID);
    const seen = vi.fn();
    const off = subscribeWalk(seen);
    setWalkAt(PID, initial.at);
    off();
    expect(walkState(PID).at).toBe(initial.at);
    expect(walkState(PID).comparison).toBe(initial.comparison);
    expect(walkState(PID).selectionRevision).toBeGreaterThan(initial.selectionRevision);
    expect(seen).toHaveBeenCalledOnce();
  });

  it("ignores moves once the walk has been left", () => {
    leaveWalk(PID);
    setWalkAt(PID, 0);
    stepWalk(PID, -1);
    expect(walkState(PID).at).toBe(-1);
  });

  it("keeps the settled playhead and snapshot until the requested step is ready", async () => {
    resetWalkForTests();
    let resolveSecond!: (value: unknown) => void;
    const client = fakeClient({
      readComparison: vi.fn(async (_projectId, target: { effectId: string }) => {
        if (target.effectId === "c1") {
          return {
            in_range: true,
            before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
            after: { state: "content", size_bytes: 3, availability: "available", content: "one" },
            location_changed: false,
          };
        }
        return new Promise((resolve) => {
          resolveSecond = resolve;
        });
      }),
    });
    await enterWalk(PID, client, SID);
    const first = walkState(PID).comparison;
    const revision = walkState(PID).selectionRevision;

    setWalkAt(PID, 1);
    expect(walkState(PID).targetAt).toBe(1);
    expect(walkState(PID).at).toBe(0);
    expect(walkState(PID).comparison).toBe(first);
    expect(walkState(PID).selectionRevision).toBe(revision);

    resolveSecond({
      in_range: true,
      before: { state: "content", size_bytes: 3, availability: "available", content: "one" },
      after: { state: "content", size_bytes: 3, availability: "available", content: "two" },
      location_changed: false,
    });
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
    expect(walkState(PID).comparison?.summary?.after.sha256).toBe(sourceTextHash("two"));
    expect(walkState(PID).selectionRevision).toBeGreaterThan(revision);
  });
});

describe("project isolation", () => {
  it("keeps one project's playhead out of another's", async () => {
    await enterWalk(PID, fakeClient(), SID);
    setWalkAt(PID, 0);
    expect(isWalking("p2")).toBe(false);
    expect(walkState("p2").at).toBe(-1);
    expect(walkState(PID).at).toBe(0);
  });
});

function growingRun() {
  const calls = [
    { call: "t1", change: "c1", ord: 2, path: "a.ts" },
    { call: "t2", change: "c2", ord: 3, path: "b.ts" },
  ];
  const walkFile = (c: (typeof calls)[number]) =>
    file(c.path, [effect(c.change, c.path, c.ord, { tool_call_id: c.call })], {
      tip: { state: "content", sha256: `sha-${c.change}` },
    });
  return {
    append(call: string, change: string, ord: number, path = `${call}.ts`) {
      calls.push({ call, change, ord, path });
    },
    client: stubFilesClient({
      readComparison: vi.fn(async () => ({
        in_range: true,
        before: { state: "content", size_bytes: 1, availability: "available", content: "before" },
        after: { state: "content", size_bytes: 1, availability: "available", content: "after" },
        location_changed: false,
      })),
      listProjectSourceWalk: vi.fn(async () => ({
        files: calls.map(walkFile),
        git_changes: [], commands: [], turns: [],
        commit_available: false,
        next_cursor: undefined,
      })),
    }),
  };
}

describe("a run that is still being written", () => {
  it("grows the rail without moving a reader who is reading something earlier", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    setWalkAt(PID, 0);

    run.append("t3", "c3", 4);
    await refreshWalk(PID);

    expect(walkState(PID).walk.steps).toHaveLength(3);
    expect(walkState(PID).at).toBe(0);
    expect(effectIdOf(currentWalkStep(PID))).toBe("c1");
    expect(walkNewStepCount(PID)).toBe(1);
  });

  it("keeps the last step selected when later work arrives", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    walkToLatest(PID);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
    const comparison = walkState(PID).comparison;
    run.append("t3", "c3", 4);
    await refreshWalk(PID);
    expect(walkState(PID).at).toBe(1);
    expect(effectIdOf(currentWalkStep(PID))).toBe("c2");
    expect(walkState(PID).comparison).toBe(comparison);
    expect(walkNewStepCount(PID)).toBe(1);
  });

  it("holds the reader's step when an earlier one arrives out of order", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    setWalkAt(PID, 1);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
    expect(effectIdOf(currentWalkStep(PID))).toBe("c2");

    run.append("t0", "c0", 1, "zero.ts");
    await refreshWalk(PID);

    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
      "c0",
      "c1",
      "c2",
    ]);
    expect(walkState(PID).at).toBe(2);
    expect(effectIdOf(currentWalkStep(PID))).toBe("c2");
  });

  it("re-anchors a pending step selection when a refresh reorders the rail first", async () => {
    const calls = [
      { call: "t1", change: "c1", ord: 2, path: "a.ts" },
      { call: "t2", change: "c2", ord: 3, path: "b.ts" },
    ];
    const walkFile = (c: (typeof calls)[number]) =>
      file(c.path, [effect(c.change, c.path, c.ord, { tool_call_id: c.call })], {
        tip: { state: "content", sha256: `sha-${c.change}` },
      });
    let resolveTarget!: (value: unknown) => void;
    const comparisonFor = (label: string) => ({
      in_range: true,
      before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
      after: { state: "content", size_bytes: 1, availability: "available", content: label },
      location_changed: false,
    });
    const client = stubFilesClient({
      readComparison: vi.fn(
        async (_projectId: string, target: { effectId: string }) => {
          if (target.effectId === "c2") {
            return new Promise((resolve) => {
              resolveTarget = resolve;
            });
          }
          return comparisonFor("c1");
        },
      ),
      listProjectSourceWalk: vi.fn(async () => ({
        files: calls.map(walkFile),
        git_changes: [], commands: [], turns: [],
        commit_available: false,
        next_cursor: undefined,
      })),
    });

    await enterWalk(PID, client, SID);
    setWalkAt(PID, 1); // targets c2, currently at index 1
    expect(walkState(PID).targetAt).toBe(1);

    // Earlier history shifts the pending c2 selection to index 2.
    calls.push({ call: "t0", change: "c0", ord: 1, path: "zero.ts" });
    await refreshWalk(PID);
    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
      "c0",
      "c1",
      "c2",
    ]);

    resolveTarget(comparisonFor("c2"));
    await vi.waitFor(() => expect(effectIdOf(currentWalkStep(PID))).toBe("c2"));
    expect(walkState(PID).at).toBe(2);
    expect(walkState(PID).targetAt).toBe(2);
    expect(walkState(PID).comparison?.summary?.after.sha256).toBe(sourceTextHash("c2"));
  });

  it("clears the arrival count once the reader catches up", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    setWalkAt(PID, 0);
    run.append("t3", "c3", 4);
    await refreshWalk(PID);
    expect(walkNewStepCount(PID)).toBe(1);

    walkToLatest(PID);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(2));
    expect(walkNewStepCount(PID)).toBe(0);
    expect(walkState(PID).at).toBe(walkState(PID).walk.steps.length - 1);
  });

  it("counts arrivals, not distance — a rewound finished run offers nothing", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    setWalkAt(PID, 0);
    expect(walkState(PID).at).toBe(0);
    expect(walkNewStepCount(PID)).toBe(0);
  });

  it("keeps the walk on screen when a refresh fails", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    const before = walkState(PID).walk.steps.length;
    (run.client.listProjectSourceWalk as ReturnType<typeof vi.fn>)
      .mockRejectedValueOnce(new Error("offline"));

    await refreshWalk(PID);

    const after = walkState(PID);
    expect(after.status).toBe("ready");
    expect(after.walk.steps).toHaveLength(before);

    // A failed refresh releases the in-flight guard.
    run.append("t3", "c3", 4);
    await refreshWalk(PID);
    expect(walkState(PID).walk.steps).toHaveLength(before + 1);
  });

  it("does not refresh a walk that has been left", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    leaveWalk(PID);
    const ledger = run.client.listProjectSourceWalk as ReturnType<typeof vi.fn>;
    const calls = ledger.mock.calls.length;

    await refreshWalk(PID);

    expect(ledger.mock.calls.length).toBe(calls);
    expect(isWalking(PID)).toBe(false);
  });
});

describe("refresh throttling", () => {
  it("spends one round trip on a burst of writes", async () => {
    vi.useFakeTimers();
    try {
      const run = growingRun();
      await enterWalk(PID, run.client, SID);
      const ledger = run.client
        .listProjectSourceWalk as ReturnType<typeof vi.fn>;
      const before = ledger.mock.calls.length;

      run.append("t3", "c3", 4);
      for (let i = 0; i < 25; i += 1) requestWalkRefresh(PID);
      await vi.runOnlyPendingTimersAsync();

      expect(ledger.mock.calls.length).toBe(before + 1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stretches the refresh delay after a slow round trip", async () => {
    vi.useFakeTimers();
    try {
      let delay = 0;
      const client = fakeClient({
        listProjectSourceWalk: vi.fn(
          () =>
            new Promise((resolve) => setTimeout(() => resolve(changes()), delay)),
        ),
      });
      const entered = enterWalk(
        PID,
        client,
        SID,
      );
      await vi.runOnlyPendingTimersAsync();
      await entered;

      delay = 1000;
      const slow = refreshWalk(PID);
      await vi.advanceTimersByTimeAsync(1000);
      await slow;

      delay = 0;
      const ledger = client.listProjectSourceWalk as ReturnType<typeof vi.fn>;
      const before = ledger.mock.calls.length;
      requestWalkRefresh(PID);
      // The delay is three times the last round trip.
      await vi.advanceTimersByTimeAsync(2999);
      expect(ledger.mock.calls.length).toBe(before);
      await vi.advanceTimersByTimeAsync(1);
      expect(ledger.mock.calls.length).toBe(before + 1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("holds the step list's identity when a refresh rebuilds the same walk", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    const held = walkState(PID).walk;

    await refreshWalk(PID);

    // Stable step identities preserve mounted rail items.
    expect(walkState(PID).walk).toBe(held);
  });

  it("does not wake subscribers for a refresh that changed nothing", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    let woken = 0;
    const unsub = subscribeWalk(() => {
      woken += 1;
    });

    await refreshWalk(PID);
    await refreshWalk(PID);
    unsub();

    // Subscriber notifications repaint the file tree.
    expect(woken).toBe(0);
  });

  it("grows the rail for a human save", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);
    walkToLatest(PID);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
    const files = (await run.client.listProjectSourceWalk(PID, {})) as {
      files: Array<Record<string, unknown>>;
      commit_available: boolean;
      next_cursor?: string;
    };
    (run.client.listProjectSourceWalk as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...files,
      files: [
        ...files.files,
        file(
          "note.md",
          [effect("c-human", "note.md", 10, { origin: "user", cause: "human_edit" })],
          { tip: { state: "content", sha256: "sha-h" } },
        ),
      ],
    });

    await refreshWalk(PID);

    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
      "c1",
      "c2",
      "c-human",
    ]);
    expect(currentWalkStep(PID)?.label).toBe("edit");
    expect(currentWalkStep(PID)?.toolCallId).toBe("t2");
  });

  it("grows the rail when a new tool call lands", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);

    run.append("t3", "c3", 4);
    await refreshWalk(PID);

    expect(walkState(PID).walk.steps).toHaveLength(3);
  });

  it("starts a different run", async () => {
    const run = growingRun();
    await enterWalk(PID, run.client, SID);

    await enterWalk(PID, run.client, "s2");

    expect(walkState(PID).sessionId).toBe("s2");
  });

  it("drops a scheduled refresh when the walk is left first", async () => {
    vi.useFakeTimers();
    try {
      const run = growingRun();
      await enterWalk(PID, run.client, SID);
      const ledger = run.client.listProjectSourceWalk as ReturnType<typeof vi.fn>;
      const before = ledger.mock.calls.length;

      requestWalkRefresh(PID);
      leaveWalk(PID);
      await vi.runOnlyPendingTimersAsync();

      expect(ledger.mock.calls.length).toBe(before);
    } finally {
      vi.useRealTimers();
    }
  });

  it("picks up a promote that landed while the walk was still loading", async () => {
    vi.useFakeTimers();
    try {
      let releaseEnter: ((v: unknown) => void) | undefined;
      const gate = new Promise((r) => {
        releaseEnter = r;
      });
      const calls: Array<{
        call: string;
        change: string;
        ord: number;
        path: string;
      }> = [];
      const client = stubFilesClient({
        readComparison: vi.fn(async () => ({
          in_range: true,
          before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
          after: { state: "content", size_bytes: 0, availability: "available", content: "" },
          location_changed: false,
        })),
        listProjectSourceWalk: vi.fn(async () => {
          await gate;
          return {
            files: calls.map((c) =>
              file(c.path, [effect(c.change, c.path, c.ord, {
                op: "create",
                tool_call_id: c.call,
                cause: "overlay_promote",
                tool_name: "promote_overlay",
              })], { tip: { state: "content", sha256: `sha-${c.change}` } }),
            ),
            git_changes: [], commands: [], turns: [],
            commit_available: false,
            next_cursor: undefined,
          };
        }),
      });

      const pending = enterWalk(PID, client, SID);
      expect(walkState(PID).status).toBe("loading");
      // Effects during load schedule a refresh after the snapshot lands.
      requestWalkRefresh(PID);

      releaseEnter?.(undefined);
      await pending;
      expect(walkState(PID).status).toBe("ready");
      expect(walkState(PID).walk.steps).toHaveLength(0);

      // Tips become visible on the host after the in-flight enter snapshot.
      calls.push(
        { call: "t-promote", change: "c-a", ord: 1, path: "a.ts" },
        { call: "t-promote", change: "c-b", ord: 2, path: "b.ts" },
      );
      await vi.runOnlyPendingTimersAsync();

      expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
        "c-a",
        "c-b",
      ]);
      expect(
        walkState(PID).walk.steps.every((s) => s.label === "promote_overlay"),
      ).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("grows an empty ready walk when promote effects arrive later", async () => {
    vi.useFakeTimers();
    try {
      const files: SourceWalkFile[] = [];
      const client = stubFilesClient({
        readComparison: vi.fn(async () => ({
          in_range: true,
          before: { state: "absent", size_bytes: 0, availability: "absent", content: "" },
          after: { state: "content", size_bytes: 0, availability: "available", content: "" },
          location_changed: false,
        })),
        listProjectSourceWalk: vi.fn(async () => ({
          files,
          git_changes: [], commands: [], turns: [],
          commit_available: false,
          next_cursor: undefined,
        })),
      });

      await enterWalk(PID, client, SID);
      expect(walkState(PID).walk.steps).toHaveLength(0);

      files.push(
        file("a.ts", [effect("c1", "a.ts", 1, {
          op: "create",
          tool_call_id: "t1",
          cause: "overlay_promote",
          tool_name: "promote_overlay",
        })], {
          changed_since_presented: true,
          tip: { state: "content", sha256: "sha-a" },
        }),
        file("b.ts", [effect("c2", "b.ts", 2, {
          op: "create",
          tool_call_id: "t1",
          cause: "overlay_promote",
          tool_name: "promote_overlay",
        })], {
          changed_since_presented: true,
          tip: { state: "content", sha256: "sha-b" },
        }),
      );

      requestWalkRefresh(PID);
      await vi.runOnlyPendingTimersAsync();

      expect(walkState(PID).walk.steps).toHaveLength(2);
      expect(walkState(PID).walk.steps.map((s) => (s.kind === "effect" ? s.effect.path : ""))).toEqual([
        "a.ts",
        "b.ts",
      ]);
    } finally {
      vi.useRealTimers();
    }
  });
});

function checkoutMovement(): SourceGitChange {
  return {
    session_id: "", turn: 0, tool_call_id: "", tool_name: "",
    id: "t-git",
    root_id: "r1",
    kind: "checkout",
    from_ref: "main",
    to_ref: "work",
    ordinal: 3,
    observed_at: "2026-08-09T10:00:00Z",
  };
}

function gitWalkResponse(): SourceWalkResponse {
  return {
    baseline: "session:s1",
    files: [
      file("a.ts", [effect("c1", "a.ts", 1, { tool_call_id: "t1" })]),
      file("swapped.ts", [
        effect("cg1", "swapped.ts", 4, {
          origin: "external",
          tool_name: undefined,
          cause: "filesystem_reconcile",
          git_change_id: "t-git",
        }),
      ]),
      file("other.ts", [
        effect("cg2", "other.ts", 5, {
          origin: "external",
          tool_name: undefined,
          cause: "filesystem_reconcile",
          git_change_id: "t-git",
        }),
      ]),
    ],
    git_changes: [checkoutMovement()],
    commands: [],
    turns: [],
    commit_available: false,
    next_cursor: undefined,
  };
}

describe("git steps in the walk", () => {
  it("collapses a movement's effects and settles on its page with no comparison", async () => {
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => gitWalkResponse()),
    });
    await enterWalk(PID, client, SID);
    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
      "c1",
      "git:t-git",
    ]);
    const comparisons = client.readComparison as ReturnType<typeof vi.fn>;
    const before = comparisons.mock.calls.length;

    setWalkAt(PID, 1);
    expect(walkState(PID).at).toBe(1);
    expect(currentWalkStep(PID)?.kind).toBe("git");
    expect(walkState(PID).comparison).toBeNull();
    expect(comparisons.mock.calls.length).toBe(before);
  });

  it("lands on the movement's step when the walk was opened from one of its files", async () => {
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => gitWalkResponse()),
    });
    await enterWalk(
      PID,
      client, SID,
      { rootId: "r1", path: "other.ts" },
    );
    expect(walkState(PID).at).toBe(1);
    expect(currentWalkStep(PID)?.kind).toBe("git");
    expect(walkState(PID).notice).toBeNull();
  });

  it("settles a bare movement synchronously with no comparison", async () => {
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [file("a.ts", [effect("c1", "a.ts", 1, { tool_call_id: "t1" })])],
        git_changes: [{ ...checkoutMovement(), id: "t-bare", kind: "commit" }],
        commands: [],
        turns: [],
        commit_available: false,
        next_cursor: undefined,
      })),
    });
    await enterWalk(PID, client, SID);
    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual([
      "c1",
      "git:t-bare",
    ]);
    const comparisons = client.readComparison as ReturnType<typeof vi.fn>;
    const before = comparisons.mock.calls.length;

    setWalkAt(PID, 1);
    expect(walkState(PID).at).toBe(1);
    expect(walkState(PID).comparison).toBeNull();
    expect(comparisons.mock.calls.length).toBe(before);
  });

  it("keeps the group step across a refresh", async () => {
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => gitWalkResponse()),
    });
    await enterWalk(PID, client, SID);
    setWalkAt(PID, 1);
    expect(walkState(PID).at).toBe(1);

    await refreshWalk(PID);

    expect(walkState(PID).at).toBe(1);
    expect(currentWalkStep(PID)?.key).toBe("git:t-git");
  });

  it("keeps the reader's place when a lone outside edit grows into a run", async () => {
    const outside = (id: string, path: string, ordinal: number) =>
      effect(id, path, ordinal, {
        origin: "external", cause: "filesystem_reconcile", session_id: undefined, turn: 0,
      });
    const pages = [
      { files: [file("a.ts", [effect("c1", "a.ts", 1, { tool_call_id: "t1" })]), file("gen/x.ts", [outside("o1", "gen/x.ts", 2)])] },
      { files: [file("a.ts", [effect("c1", "a.ts", 1, { tool_call_id: "t1" })]), file("gen/x.ts", [outside("o1", "gen/x.ts", 2)]), file("gen/y.ts", [outside("o2", "gen/y.ts", 3)])] },
    ];
    let page = 0;
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => ({
        ...pages[page]!, git_changes: [], commands: [], turns: [], commit_available: false, next_cursor: undefined,
      })),
    });
    await enterWalk(PID, client, SID);
    setWalkAt(PID, 1);
    await vi.waitFor(() => expect(walkState(PID).at).toBe(1));
    expect(currentWalkStep(PID)?.key).toBe("o1");

    page = 1;
    await refreshWalk(PID);
    expect(noticeRows()).toEqual([]);
    expect(walkState(PID).at).toBe(1);
    expect(currentWalkStep(PID)?.key).toBe("outside:o1");
    expect(currentWalkStep(PID)?.kind).toBe("outside");
  });

  it("collapses a run of outside changes into one step with no comparison", async () => {
    const outside = (id: string, path: string, ordinal: number) =>
      effect(id, path, ordinal, {
        origin: "external", cause: "filesystem_reconcile", session_id: undefined, turn: 0,
      });
    const client = fakeClient({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [
          file("a.ts", [effect("c1", "a.ts", 1, { tool_call_id: "t1" })]),
          file("gen/x.ts", [outside("o1", "gen/x.ts", 2)]),
          file("gen/y.ts", [outside("o2", "gen/y.ts", 3)]),
        ],
        git_changes: [], commands: [], turns: [],
        commit_available: false,
        next_cursor: undefined,
      })),
    });
    await enterWalk(PID, client, SID);
    expect(walkState(PID).walk.steps.map(stepEffectId)).toEqual(["c1", "outside:o1"]);
    setWalkAt(PID, 1);
    expect(currentWalkStep(PID)?.kind).toBe("outside");
    expect(walkState(PID).comparison).toBeNull();
  });
});
