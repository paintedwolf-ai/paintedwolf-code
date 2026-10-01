import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, beforeEach, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type {
  SourceSeenFile,
  SourceWalkFile,
  SourceChangeOp,
  SourceWalkResponse,
} from "../../api/types.ts";
import { buildReviewFileRows } from "../review/review-model.ts";
import {
  resetFilesStagePaneForTests,
  setComparisonOff,
  setSidebarScope,
} from "../review/review-pane.ts";
import {
  enterWalk,
  isWalking,
  leaveWalk,
  resetWalkForTests,
  setWalkAt,
} from "../walk/walk-store.ts";
import {
  deletedPathsInScope,
  isAddedInScope,
  isDeletedInScope,
  isInScope,
  resetScopeResolutionForTests,
  resolveScope,
  resolvedScope,
  scopeBaseline,
  scopeComparisonTarget,
  scopeEffectFor,
  scopeContentEpoch,
  scopeDiffRevision,
  scopeFileFor,
  scopeMarksEpoch,
  rebindScopeSubject,
  requestScopeResolve,
  resolvedLensView,
  scopeSessionId,
  scopeSettledEpoch,
  setScopeResolver,
  subscribeResolvedScope,
  subscribeScopeSettled,
} from "./scope-resolution.ts";

const PROJECT = "p1";

function file(
  path: string,
  over: {
    changeId?: string;
    sha?: string;
    deleted?: boolean;
    /** Op of the oldest change in range — how the file entered it. */
    entryOp?: SourceChangeOp;
  } = {},
): SourceWalkFile {
  const sha = over.sha ?? `sha-${path}`;
  // Newest first, the order the ledger answers in.
  const newest = sourceEffectFixture({
    id: over.changeId ?? "",
    project_id: PROJECT,
    operation_id: `operation-${over.changeId}`,
    file_id: `file-${path}`,
    after_version_id: `version-${over.changeId}`,
    workspace_kind: "project" as const,
    root_id: "r1",
    path,
    op: (over.deleted ? "delete" : "write") as SourceChangeOp,
    entry_kind: "file",
    origin: "agent" as const,
    turn: 1,
    ordinal: 2,
    cause: "tool",
    capture_quality: "exact" as const,
    observed_at: "2026-08-02T00:00:00Z",
  });
  const entry = sourceEffectFixture({
    id: `${over.changeId}-entry`,
    project_id: PROJECT,
    operation_id: `operation-${over.changeId}-entry`,
    file_id: `file-${path}`,
    after_version_id: `version-${over.changeId}-entry`,
    workspace_kind: "project" as const,
    root_id: "r1",
    path,
    op: over.entryOp ?? "write",
    entry_kind: "file",
    origin: "agent" as const,
    turn: 1,
    ordinal: 1,
    cause: "tool",
    capture_quality: "exact" as const,
    observed_at: "2026-08-01T00:00:00Z",
  });
  return {
    file_id: `file-${path}`,
    root_id: "r1",
    path,
    changed_since_presented: over.changeId !== undefined,
    unpresented_agent_effects: over.changeId === undefined ? 0 : 1,
    // The host answers what the file ends at; a delete ends at absence.
    tip: over.deleted
      ? { state: "absent" }
      : over.changeId === undefined
        ? { state: "content", sha256: "" }
        : { state: "content", sha256: sha },
    head_match: "unknown",
    effects:
      over.changeId === undefined
        ? []
        : over.entryOp
          ? [newest, entry]
          : [newest],
  };
}

/** The chat the eye's comparisons read in these tests, and the turn the host says it is on. */
const SUBJECT = { sessionId: "s1", title: "Chat", sessionScoped: true };
const CURRENT_TURN = 4;

type WalkPage = Omit<SourceWalkResponse, "baseline">;

/** Answers in the host's grammar: a chat's current turn comes back as the turn it read. */
function answered(baseline: string, page: WalkPage): SourceWalkResponse {
  const current = /^turn:([^,]+)$/.exec(baseline);
  return { ...page, baseline: current ? `turn:${current[1]},${CURRENT_TURN}` : baseline };
}

function walk(respond: (baseline: string, cursor?: string) => WalkPage | Promise<WalkPage>) {
  return vi.fn(async (_p: string, opts: { baseline: string; cursor?: string }) =>
    answered(opts.baseline, await respond(opts.baseline, opts.cursor)));
}

function stub(
  respond: (
    baseline: string,
    cursor?: string,
  ) => WalkPage | Promise<WalkPage>,
): LycaonClient {
  return stubClient({
    listProjectSourceWalk: walk(respond),
    listProjectSourceSeen: vi.fn(async () => ({ files: [], next_cursor: undefined })),
  });
}

function ok(files: SourceWalkFile[]): WalkPage {
  return {
    files, git_changes: [], commands: [], turns: [],
    commit_available: false, next_cursor: undefined,
  };
}

describe("scope resolution", () => {
  beforeEach(() => {
    resetScopeResolutionForTests();
    resetFilesStagePaneForTests();
  });

  it("indexes the files the scope addresses, and the baseline they came from", async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    await resolveScope(PROJECT, stub(() => ok([file("a.ts", { changeId: "c1" })])), SUBJECT);

    expect(scopeBaseline(PROJECT)).toBe("commit");
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(true);
    expect(scopeEffectFor(PROJECT, "r1", "a.ts")).toEqual({
      fileId: "file-a.ts",
      tip: { state: "content", sha256: "sha-a.ts" },
      entryOp: "write",
      entryEffectId: "c1",
    });
    expect(scopeFileFor(PROJECT, "r1", "a.ts")?.path).toBe("a.ts");
  });

  it("keeps one editor's diff revision stable across unrelated file activity", async () => {
    await resolveScope(
      PROJECT,
      stub(() =>
        ok([
          file("open.ts", { changeId: "open-1", sha: "open-sha" }),
          file("noise.json", { changeId: "noise-1", sha: "noise-1" }),
        ]),
      ),
      SUBJECT,
    );
    const initial = scopeDiffRevision(PROJECT, "r1", "open.ts");
    const observed: string[] = [];
    const stop = subscribeResolvedScope((projectId) => {
      if (projectId === PROJECT) {
        observed.push(scopeDiffRevision(PROJECT, "r1", "open.ts"));
      }
    });

    await resolveScope(
      PROJECT,
      stub(() =>
        ok([
          file("open.ts", { changeId: "open-1", sha: "open-sha" }),
          file("noise.json", { changeId: "noise-2", sha: "noise-2" }),
        ]),
      ),
      SUBJECT,
    );
    stop();

    expect(observed.length).toBeGreaterThan(0);
    expect(observed.every((revision) => revision === initial)).toBe(true);
  });

  it("changes an editor diff revision when that file's tip moves", async () => {
    await resolveScope(
      PROJECT,
      stub(() => ok([file("open.ts", { changeId: "c1", sha: "sha-1" })])),
      SUBJECT,
    );
    const initial = scopeDiffRevision(PROJECT, "r1", "open.ts");

    await resolveScope(
      PROJECT,
      stub(() => ok([file("open.ts", { changeId: "c2", sha: "sha-2" })])),
      SUBJECT,
    );

    expect(scopeDiffRevision(PROJECT, "r1", "open.ts")).not.toBe(initial);
  });

  it("lists affiliated human edits under a chat-relative scope", async () => {
    setSidebarScope(PROJECT, { kind: "session" });
    const human = {
      file_id: "file-note.md",
      root_id: "r1",
      path: "note.md",
      changed_since_presented: false,
      unpresented_agent_effects: 0,
      tip: { state: "content" as const, sha256: "sha-note" },
      effects: [
        sourceEffectFixture({
          id: "c-you",
          project_id: PROJECT,
          operation_id: "operation-c-you",
          file_id: "file-note.md",
          after_version_id: "version-c-you",
          workspace_kind: "project" as const,
          root_id: "r1",
          path: "note.md",
          op: "write" as const,
          entry_kind: "file",
          origin: "user" as const,
          turn: 2,
          ordinal: 5,
          observed_at: "2026-08-02T00:00:00Z",
          cause: "human_edit",
          capture_quality: "exact" as const,
          session_id: "s1",
        }),
      ],
    };
    await resolveScope(PROJECT, stub(() => ok([human as SourceWalkFile])), SUBJECT);
    expect(isInScope(PROJECT, "r1", "note.md")).toBe(true);
    const rows = buildReviewFileRows(resolvedScope(PROJECT).files);
    expect(rows.map((f) => f.path)).toEqual(["note.md"]);
    expect(rows[0]!.attribution).toBe("You");
  });

  it("continues through empty filtered pages until the host exhausts its cursor", async () => {
    const client = stub((_baseline, cursor) => {
      if (cursor === undefined) {
        return { ...ok([]), next_cursor: "500" };
      }
      return { ...ok([file("older.ts", { changeId: "c2" })]), next_cursor: undefined };
    });
    await resolveScope(PROJECT, client, SUBJECT);
    expect(isInScope(PROJECT, "r1", "older.ts")).toBe(true);
    expect(client.listProjectSourceWalk).toHaveBeenCalledTimes(2);
  });

  it("joins one file split across ledger pages", async () => {
    const newest = file("a.ts", { changeId: "new" });
    const oldest = file("a.ts", { changeId: "old", entryOp: "create" });
    newest.effects = newest.effects.slice(0, 1);
    oldest.effects = oldest.effects.slice(1);
    const client = stub((_baseline, cursor) =>
      cursor === undefined
        ? { ...ok([newest]), next_cursor: "2" }
        : ok([oldest]),
    );

    await resolveScope(PROJECT, client, SUBJECT);

    const files = resolvedScope(PROJECT).files;
    expect(files).toHaveLength(1);
    expect(files[0]!.effects.map((effect) => effect.id)).toEqual([
      "new",
      "old-entry",
    ]);
    expect(isAddedInScope(PROJECT, "r1", "a.ts")).toBe(true);
  });

  it("does not mark a file the scope has no addressable change for", async () => {
    await resolveScope(PROJECT, stub(() => ok([file("a.ts")])), SUBJECT);
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(false);
    expect(scopeEffectFor(PROJECT, "r1", "a.ts")).toBeNull();
  });

  it("addresses a deleted file — absence is an outcome, not a missing one", async () => {
    await resolveScope(
      PROJECT,
      stub(() => ok([file("gone.ts", { changeId: "d1", deleted: true })])),
      SUBJECT,
    );
    expect(isInScope(PROJECT, "r1", "gone.ts")).toBe(true);
    expect(scopeEffectFor(PROJECT, "r1", "gone.ts")).toEqual({
      fileId: "file-gone.ts",
      tip: { state: "absent" },
      entryOp: "delete",
      entryEffectId: "d1",
    });
    expect(isDeletedInScope(PROJECT, "r1", "gone.ts")).toBe(true);
    expect(deletedPathsInScope(PROJECT, "r1")).toEqual(["gone.ts"]);
  });

  it("says a file was added when the range starts before it existed", async () => {
    await resolveScope(
      PROJECT,
      stub(() =>
        ok([
          file("new.ts", { changeId: "c1", entryOp: "create" }),
          file("old.ts", { changeId: "c2" }),
        ]),
      ),
      SUBJECT,
    );
    expect(isAddedInScope(PROJECT, "r1", "new.ts")).toBe(true);
    expect(scopeEffectFor(PROJECT, "r1", "new.ts")?.entryOp).toBe("create");
    expect(isInScope(PROJECT, "r1", "new.ts")).toBe(true);
    expect(isAddedInScope(PROJECT, "r1", "old.ts")).toBe(false);
  });

  it("calls a file created and then deleted in range deleted, not added", async () => {
    await resolveScope(
      PROJECT,
      stub(() =>
        ok([
          file("blip.ts", { changeId: "d1", deleted: true, entryOp: "create" }),
        ]),
      ),
      SUBJECT,
    );
    expect(isDeletedInScope(PROJECT, "r1", "blip.ts")).toBe(true);
    expect(isAddedInScope(PROJECT, "r1", "blip.ts")).toBe(false);
  });

  it("every mark clears together when the eye goes off", async () => {
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    await resolveScope(PROJECT, client, SUBJECT);
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(true);
    expect(resolvedScope(PROJECT).paths.size).toBe(1);

    setComparisonOff(PROJECT, true);
    await resolveScope(PROJECT, client, SUBJECT);
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(false);
    expect(resolvedScope(PROJECT).paths.size).toBe(0);
  });

  it("off is a whole answer, committed without asking the host", async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    await resolveScope(PROJECT, client, SUBJECT);
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(true);

    setComparisonOff(PROJECT, true);
    const record = await resolveScope(PROJECT, client, SUBJECT);
    expect(record.files).toHaveLength(0);
    expect(record.baseline).toBe("");
    expect(record.status).toBe("ready");
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(false);
    // The picked comparison is remembered, so turning the eye back on lands on it.
    expect(record.scope).toEqual({ kind: "commit" });
    expect(client.listProjectSourceWalk).toHaveBeenCalledTimes(1);
  });

  it("drops a superseded flight so the last pick wins, not the last response", async () => {
    // Resolve requests in reverse order.
    const gate: Array<() => void> = [];
    const client = stub(
      (baseline) =>
        new Promise<WalkPage>((resolve) => {
          gate.push(() =>
            resolve(ok([file(`${baseline}.ts`, { changeId: baseline })])),
          );
        }),
    );

    setSidebarScope(PROJECT, { kind: "new" });
    const first = resolveScope(PROJECT, client, SUBJECT);
    setSidebarScope(PROJECT, { kind: "commit" });
    const second = resolveScope(PROJECT, client, SUBJECT);

    gate[1]!();
    await second;
    gate[0]!();
    await first;

    expect(scopeBaseline(PROJECT)).toBe("commit");
    expect(isInScope(PROJECT, "r1", "commit.ts")).toBe(true);
    expect(isInScope(PROJECT, "r1", "seen.ts")).toBe(false);
  });

  it("holds the settled answer through a failure and reports the staleness", async () => {
    let fail = false;
    const client = stub(() => {
      if (fail) throw new Error("host down");
      return ok([file("a.ts", { changeId: "c1" })]);
    });
    await resolveScope(PROJECT, client, SUBJECT);
    fail = true;
    const record = await resolveScope(PROJECT, client, SUBJECT);

    expect(record.status).toBe("error");
    expect(record.error).toBe("host down");
    // Preserve the last settled files.
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(true);
    expect(scopeBaseline(PROJECT)).toBe("presentation");
  });

  it("leaves the epoch alone when there is nothing to resolve with", async () => {
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    const flight = resolveScope(PROJECT, client, SUBJECT);
    // A missing client does not supersede an active request.
    await resolveScope(PROJECT, null, SUBJECT);
    await flight;
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(true);
  });

  it("notifies once per commit, and the record it notifies for is settled", async () => {
    const seen: string[] = [];
    const statuses: string[] = [];
    const unsub = subscribeResolvedScope((id) => {
      seen.push(id);
      statuses.push(resolvedScope(id).status);
    });
    await resolveScope(
      PROJECT,
      stub(() => ok([file("a.ts", { changeId: "c1" })])),
      SUBJECT,
    );
    unsub();
    expect(seen).toEqual([PROJECT, PROJECT]);
    expect(statuses).toEqual(["resolving", "ready"]);
  });
});

describe("a re-resolve that lands the same answer", () => {
  const same = () => ok([file("a.ts", { changeId: "c1" })]);

  beforeEach(() => {
    resetScopeResolutionForTests();
    resetFilesStagePaneForTests();
  });

  it("holds the mark epoch still", async () => {
    await resolveScope(PROJECT, stub(same), SUBJECT);
    const marks = scopeMarksEpoch(PROJECT);

    await resolveScope(PROJECT, stub(same), SUBJECT);
    await resolveScope(PROJECT, stub(same), SUBJECT);

    expect(scopeMarksEpoch(PROJECT)).toBe(marks);
  });

  it("holds the content epoch still", async () => {
    await resolveScope(PROJECT, stub(same), SUBJECT);
    const content = scopeContentEpoch(PROJECT);

    await resolveScope(PROJECT, stub(same), SUBJECT);

    expect(scopeContentEpoch(PROJECT)).toBe(content);
  });

  it("keeps the record's identity, so a memo over it stays put", async () => {
    await resolveScope(PROJECT, stub(same), SUBJECT);
    const held = resolvedScope(PROJECT);

    await resolveScope(PROJECT, stub(same), SUBJECT);

    expect(resolvedScope(PROJECT)).toBe(held);
  });

  it("moves both epochs when a file joins the scope", async () => {
    await resolveScope(PROJECT, stub(same), SUBJECT);
    const marks = scopeMarksEpoch(PROJECT);
    const content = scopeContentEpoch(PROJECT);

    await resolveScope(
      PROJECT,
      stub(() =>
        ok([file("a.ts", { changeId: "c1" }), file("b.ts", { changeId: "c2" })]),
      ),
      SUBJECT,
    );

    expect(scopeMarksEpoch(PROJECT)).toBeGreaterThan(marks);
    expect(scopeContentEpoch(PROJECT)).toBeGreaterThan(content);
    expect(isInScope(PROJECT, "r1", "b.ts")).toBe(true);
  });

  it("moves content but not marks when only the review count changed", async () => {
    await resolveScope(PROJECT, stub(same), SUBJECT);
    const marks = scopeMarksEpoch(PROJECT);
    const content = scopeContentEpoch(PROJECT);

    await resolveScope(
      PROJECT,
      stub(() => {
        const [f] = same().files as SourceWalkFile[];
        return ok([{ ...f!, unpresented_agent_effects: 3 }]);
      }),
      SUBJECT,
    );

    expect(scopeMarksEpoch(PROJECT)).toBe(marks);
    expect(scopeContentEpoch(PROJECT)).toBeGreaterThan(content);
  });

  it("reports a requested scope change while retaining the complete previous snapshot", async () => {
    await resolveScope(PROJECT, stub(same), SUBJECT);
    const held = resolvedScope(PROJECT);
    let release!: (response: WalkPage) => void;
    const response = new Promise<WalkPage>(resolve => { release = resolve; });
    setSidebarScope(PROJECT, { kind: "commit" });
    const loading = resolveScope(PROJECT, stub(() => response), SUBJECT);
    expect(resolvedScope(PROJECT).status).toBe("resolving");
    expect(resolvedScope(PROJECT).files).toBe(held.files);
    expect(resolvedScope(PROJECT).settled).toBe(true);
    release(ok([]));
    await loading;
    expect(resolvedScope(PROJECT).status).toBe("ready");
    expect(resolvedScope(PROJECT).baseline).toBe("commit");
    expect(resolvedScope(PROJECT).files).toEqual([]);
  });

  it("keeps an unchanged refresh silent for subscribers", async () => {
    await resolveScope(PROJECT, stub(same), SUBJECT);
    const statuses: string[] = [];
    const unsub = subscribeResolvedScope((id) =>
      statuses.push(resolvedScope(id).status),
    );

    await resolveScope(PROJECT, stub(same), SUBJECT);
    unsub();

    expect(statuses).toEqual([]);
  });
});

describe("which chat a resolve names", () => {
  const OTHER = { sessionId: "s2", title: "Other", sessionScoped: false };
  const SHARED = { ...SUBJECT, sessionScoped: false };
  const sessionsOf = (client: LycaonClient) => vi.mocked(client.listProjectSourceWalk).mock.calls.map((call) => call[1]?.sessionId);

  beforeEach(() => {
    resetScopeResolutionForTests();
    resetFilesStagePaneForTests();
  });

  it("asks a checkout comparison the same question for every chat sharing the project checkout", async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    await resolveScope(PROJECT, client, SHARED);
    const request = resolvedScope(PROJECT).request;
    await resolveScope(PROJECT, client, OTHER);

    expect(resolvedScope(PROJECT).request).toBe(request);
    expect(sessionsOf(client)).toEqual([undefined, undefined]);
    expect(resolvedScope(PROJECT).status).toBe("ready");
  });

  it("gives per-file reads the chat the answer addressed, so a shared checkout reads one workspace", async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    await resolveScope(PROJECT, client, SHARED);
    expect(scopeSessionId(PROJECT)).toBeUndefined();

    rebindScopeSubject(PROJECT, OTHER);
    expect(scopeSessionId(PROJECT)).toBeUndefined();

    await resolveScope(PROJECT, client, SUBJECT);
    expect(scopeSessionId(PROJECT)).toBe("s1");
  });

  it("names the chat whose worktree answers a checkout comparison", async () => {
    setSidebarScope(PROJECT, { kind: "new" });
    const client = stub(() => ok([]));
    await resolveScope(PROJECT, client, SUBJECT);
    await resolveScope(PROJECT, client, { ...SUBJECT, sessionId: "s3" });

    expect(sessionsOf(client)).toEqual(["s1", "s3"]);
    expect(vi.mocked(client.listProjectSourceSeen).mock.calls.map((call) => call[1]?.sessionId)).toEqual(["s1", "s3"]);
  });

  it("names the chat a chat comparison is about, however the checkout is shared", async () => {
    setSidebarScope(PROJECT, { kind: "turn" });
    const client = stub(() => ok([]));
    await resolveScope(PROJECT, client, SHARED);
    await resolveScope(PROJECT, client, OTHER);

    expect(sessionsOf(client)).toEqual(["s1", "s2"]);
  });

  it("follows a chat's name and checkout on the held answer without asking the host again", async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    await resolveScope(PROJECT, client, SHARED);
    const notified: string[] = [];
    subscribeResolvedScope((id) => notified.push(id));

    rebindScopeSubject(PROJECT, { ...SHARED, title: "Renamed" });

    expect(resolvedLensView(PROJECT).subjectTitle).toBe("Renamed");
    expect(client.listProjectSourceWalk).toHaveBeenCalledTimes(1);
    expect(notified).toEqual([PROJECT]);
    expect(isInScope(PROJECT, "r1", "a.ts")).toBe(true);
  });

  it("refuses to rename a chat comparison after a chat its answer never read", async () => {
    setSidebarScope(PROJECT, { kind: "turn" });
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    await resolveScope(PROJECT, client, SUBJECT);

    rebindScopeSubject(PROJECT, OTHER);

    // The held answer read s1's turn, so its per-file reads stay addressed to s1.
    expect(resolvedLensView(PROJECT).subjectTitle).toBe(SUBJECT.title);
    expect(scopeSessionId(PROJECT)).toBe("s1");
    expect(scopeBaseline(PROJECT)).toBe(`turn:s1,${CURRENT_TURN}`);
  });
});

describe("Walk and comparison scope", () => {
  const row = (
    id: string,
    path: string,
    toolCallId: string | undefined,
    ordinal: number,
    origin: "agent" | "user" = "agent",
  ) => sourceEffectFixture({
    id,
    project_id: PROJECT,
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project" as const,
    root_id: "r1",
    path,
    op: "write" as const,
    entry_kind: "file",
    origin,
    turn: 1,
    ordinal,
    observed_at: "2026-08-02T00:02:00Z",
    tool_call_id: toolCallId,
    tool_name: toolCallId === "t2" ? "merge" : toolCallId ? "edit" : undefined,
    cause: origin === "user" ? "human_edit" : "agent_tool",
    capture_quality: "exact" as const,
  });

  const sessionChanges = {
    files: [
      {
        file_id: "file-one.ts",
        root_id: "r1",
        path: "one.ts",
        changed_since_presented: false,
        tip: { state: "content" as const, sha256: "sha-one.ts" },
        unpresented_agent_effects: 1,
        effects: [row("c1", "one.ts", "t1", 1)],
      },
      {
        file_id: "file-two.ts",
        root_id: "r1",
        path: "two.ts",
        changed_since_presented: false,
        tip: { state: "content" as const, sha256: "sha-two.ts" },
        unpresented_agent_effects: 1,
        effects: [row("c2", "two.ts", "t2", 2)],
      },
      {
        file_id: "file-three.ts",
        root_id: "r1",
        path: "three.ts",
        changed_since_presented: false,
        tip: { state: "content" as const, sha256: "sha-three.ts" },
        unpresented_agent_effects: 1,
        effects: [row("c3", "three.ts", "t2", 3)],
      },
      {
        file_id: "file-note.md",
        root_id: "r1",
        path: "note.md",
        changed_since_presented: false,
        tip: { state: "content" as const, sha256: "sha-note.md" },
        unpresented_agent_effects: 0,
        effects: [row("c-you", "note.md", undefined, 4, "user")],
      },
    ],
    git_changes: [], commands: [], turns: [],
    commit_available: false,
  };

  function walkClient() {
    return stubClient({
      listProjectSourceWalk: walk(() => sessionChanges as unknown as WalkPage),
    });
  }

  beforeEach(() => {
    resetWalkForTests();
    resetScopeResolutionForTests();
    resetFilesStagePaneForTests();
  });

  it("keeps the comparison scope while the playhead moves", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setWalkAt(PROJECT, 1);
    await resolveScope(PROJECT, client, SUBJECT);
    expect(isInScope(PROJECT, "r1", "two.ts")).toBe(true);
    expect(isInScope(PROJECT, "r1", "three.ts")).toBe(true);
    expect(isInScope(PROJECT, "r1", "one.ts")).toBe(true);
  });

  it("does not repoint the comparison when the playhead moves", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setWalkAt(PROJECT, 0);
    await resolveScope(PROJECT, client, SUBJECT);
    expect(isInScope(PROJECT, "r1", "one.ts")).toBe(true);
    expect(isInScope(PROJECT, "r1", "two.ts")).toBe(true);
  });

  it("resolves the comparison independently from Walk", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    const before = (client.listProjectSourceWalk as ReturnType<typeof vi.fn>).mock
      .calls.length;
    setWalkAt(PROJECT, 1);
    await resolveScope(PROJECT, client, SUBJECT);
    await resolveScope(PROJECT, client, SUBJECT);
    expect(
      (client.listProjectSourceWalk as ReturnType<typeof vi.fn>).mock.calls.length,
    ).toBeGreaterThan(before);
    expect(scopeBaseline(PROJECT)).toBe("presentation");
  });

  it("keeps addressed comparison rows stable across playhead changes", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setWalkAt(PROJECT, 1);
    await resolveScope(PROJECT, client, SUBJECT);
    expect(scopeFileFor(PROJECT, "r1", "two.ts")?.effects.map((c) => c.id)).toEqual([
      "c2",
    ]);
    expect(scopeEffectFor(PROJECT, "r1", "two.ts")?.tip).toEqual({
      state: "content",
      sha256: "sha-two.ts",
    });
    setWalkAt(PROJECT, 2);
    await resolveScope(PROJECT, client, SUBJECT);
    expect(scopeFileFor(PROJECT, "r1", "three.ts")?.effects).toHaveLength(1);
  });

  it("feeds the review list from the comparison scope", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setWalkAt(PROJECT, 1);
    const record = await resolveScope(PROJECT, client, SUBJECT);
    expect(buildReviewFileRows(record.files).map((f) => f.path)).toEqual([
      "one.ts",
      "two.ts",
      "three.ts",
      "note.md",
    ]);
  });

  it("hands the stage back to the baseline when the walk ends", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setWalkAt(PROJECT, 0);
    await resolveScope(PROJECT, client, SUBJECT);
    leaveWalk(PROJECT);
    await resolveScope(PROJECT, client, SUBJECT);
    expect(isWalking(PROJECT)).toBe(false);
    expect(scopeBaseline(PROJECT)).not.toBe("");
    expect(isInScope(PROJECT, "r1", "two.ts")).toBe(true);
  });

  it("keeps human changes in the comparison scope", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setWalkAt(PROJECT, 3);
    const record = await resolveScope(PROJECT, client, SUBJECT);
    const note = buildReviewFileRows(record.files).find((file) => file.path === "note.md");
    expect(note?.attribution).toBe("You");
    expect(note?.contributors.map((c) => c.kind)).toEqual(["user"]);
  });

  it("keeps Walk active when a comparison is picked", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setSidebarScope(PROJECT, { kind: "commit" });
    expect(isWalking(PROJECT)).toBe(true);
  });

  it("keeps Walk active when comparison marking is off", async () => {
    const client = walkClient();
    await enterWalk(PROJECT, client, "s1");
    setComparisonOff(PROJECT, true);
    expect(isWalking(PROJECT)).toBe(true);
  });
});

function seen(path: string, through: number): SourceSeenFile {
  return {
    file_id: `file-${path}`,
    root_id: "r1",
    path,
    tip: { state: "content", sha256: `sha-${path}` },
    seen_at: "2026-09-11T00:00:00Z",
    through_ordinal: through,
    effects: [],
    effects_truncated: false,
  };
}

describe("seen files", () => {
  beforeEach(() => {
    resetScopeResolutionForTests();
    resetFilesStagePaneForTests();
  });

  it("load beside what is new, and a file is never both", async () => {
    setSidebarScope(PROJECT, { kind: "new" });
    const listProjectSourceSeen = vi.fn(async () => ({
      files: [seen("a.ts", 3), seen("b.ts", 5)],
      next_cursor: "cursor-1",
    }));
    const client = stubClient({
      listProjectSourceWalk: walk(() => ok([file("a.ts", { changeId: "c1" })])),
      listProjectSourceSeen,
    });

    const record = await resolveScope(PROJECT, client, SUBJECT);

    expect(listProjectSourceSeen).toHaveBeenCalledWith(
      PROJECT,
      expect.objectContaining({ limit: 25, markUserEdits: false }),
    );
    expect(record.seen.map((f) => f.path)).toEqual(["b.ts"]);
    expect(record.seenMore).toBe(true);
    expect(record.seen[0]?.through_ordinal).toBe(5);
    expect(isInScope(PROJECT, "r1", "b.ts")).toBe(false);
    expect(scopeEffectFor(PROJECT, "r1", "b.ts")).toBeNull();
    expect(scopeComparisonTarget(PROJECT, "r1", "b.ts")).toEqual({
      fileId: "",
      baseline: "presentation",
      markUserEdits: false,
    });
  });

  it("stay listed when reading them fails", async () => {
    setSidebarScope(PROJECT, { kind: "new" });
    const listProjectSourceSeen = vi
      .fn()
      .mockResolvedValueOnce({ files: [seen("b.ts", 5)], next_cursor: undefined })
      .mockRejectedValueOnce(new Error("offline"));
    const client = stubClient({
      listProjectSourceWalk: walk(() => ok([])),
      listProjectSourceSeen,
    });

    await resolveScope(PROJECT, client, SUBJECT);
    const record = await resolveScope(PROJECT, client, SUBJECT);

    expect(record.status).toBe("ready");
    expect(record.seen.map((f) => f.path)).toEqual(["b.ts"]);
  });

  it("are not asked for outside the new scope", async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    const listProjectSourceSeen = vi.fn(async () => ({ files: [seen("b.ts", 5)], next_cursor: undefined }));
    const client = stubClient({
      listProjectSourceWalk: walk(() => ok([])),
      listProjectSourceSeen,
    });

    const record = await resolveScope(PROJECT, client, SUBJECT);

    expect(listProjectSourceSeen).not.toHaveBeenCalled();
    expect(record.seen).toEqual([]);
  });

  it("keep reviewed files outside the automatic comparison when their look changes", async () => {
    setSidebarScope(PROJECT, { kind: "new" });
    const listProjectSourceSeen = vi
      .fn()
      .mockResolvedValueOnce({ files: [seen("b.ts", 5)], next_cursor: undefined })
      .mockResolvedValueOnce({ files: [seen("b.ts", 6)], next_cursor: undefined });
    const client = stubClient({
      listProjectSourceWalk: walk(() => ok([])),
      listProjectSourceSeen,
    });

    await resolveScope(PROJECT, client, SUBJECT);
    const before = scopeDiffRevision(PROJECT, "r1", "b.ts");
    await resolveScope(PROJECT, client, SUBJECT);

    expect(scopeDiffRevision(PROJECT, "r1", "b.ts")).toBe(before);
  });
});

describe("the chat a comparison reads", () => {
  beforeEach(() => {
    resetScopeResolutionForTests();
    resetFilesStagePaneForTests();
  });

  it("asks the host for the chat's current turn and keeps the turn it answered", async () => {
    setSidebarScope(PROJECT, { kind: "turn" });
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));

    const record = await resolveScope(PROJECT, client, SUBJECT);

    expect(client.listProjectSourceWalk).toHaveBeenCalledWith(PROJECT, expect.objectContaining({
      baseline: "turn:s1",
      sessionId: "s1",
    }));
    expect(record.baseline).toBe(`turn:s1,${CURRENT_TURN}`);
    expect(record.turn).toBe(CURRENT_TURN);
    expect(record.subject).toEqual(SUBJECT);
    // A file's comparison addresses the turn the list came from, not whatever turn is current later.
    expect(scopeComparisonTarget(PROJECT, "r1", "a.ts")).toMatchObject({ baseline: `turn:s1,${CURRENT_TURN}` });
    expect(scopeSessionId(PROJECT)).toBe("s1");
  });

  it("reads later pages with the baseline the first page resolved, so every page is one turn", async () => {
    setSidebarScope(PROJECT, { kind: "turn" });
    const client = stub((_baseline, cursor) =>
      cursor === undefined
        ? { ...ok([file("new.ts", { changeId: "c2" })]), next_cursor: "9" }
        : ok([file("old.ts", { changeId: "c1" })]));

    await resolveScope(PROJECT, client, SUBJECT);

    const calls = (client.listProjectSourceWalk as ReturnType<typeof vi.fn>).mock.calls;
    expect(calls.map((call) => call[1].baseline)).toEqual(["turn:s1", `turn:s1,${CURRENT_TURN}`]);
    expect(resolvedScope(PROJECT).files.map((f) => f.path)).toEqual(["new.ts", "old.ts"]);
  });

  it("sends the chat with every comparison, so each reads that chat's workspace", async () => {
    setSidebarScope(PROJECT, { kind: "new" });
    const listProjectSourceSeen = vi.fn(async () => ({ files: [], next_cursor: undefined }));
    const client = stubClient({ listProjectSourceWalk: walk(() => ok([])), listProjectSourceSeen });

    await resolveScope(PROJECT, client, SUBJECT);

    expect(client.listProjectSourceWalk).toHaveBeenCalledWith(PROJECT, expect.objectContaining({ sessionId: "s1" }));
    expect(listProjectSourceSeen).toHaveBeenCalledWith(PROJECT, expect.objectContaining({ sessionId: "s1" }));
  });

  it("answers a chat comparison with no chat as needing one, without asking the host", async () => {
    setSidebarScope(PROJECT, { kind: "session" });
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));

    const record = await resolveScope(PROJECT, client, null);

    expect(client.listProjectSourceWalk).not.toHaveBeenCalled();
    expect(record).toMatchObject({ status: "ready", settled: true, needsChat: true, baseline: "", files: [] });
  });

  it("keeps the held answer labeled with its own chat while another chat's answer loads", async () => {
    setSidebarScope(PROJECT, { kind: "turn" });
    await resolveScope(PROJECT, stub(() => ok([file("one.ts", { changeId: "c1" })])), SUBJECT);
    let release!: (page: WalkPage) => void;
    const pending = new Promise<WalkPage>((resolve) => { release = resolve; });
    const other = { sessionId: "s2", title: "Other", sessionScoped: true };

    const loading = resolveScope(PROJECT, stub(() => pending), other);

    const held = resolvedScope(PROJECT);
    expect(held.status).toBe("resolving");
    expect(held.subject).toEqual(SUBJECT);
    expect(held.files.map((f) => f.path)).toEqual(["one.ts"]);
    release(ok([file("two.ts", { changeId: "c2" })]));
    await loading;
    expect(resolvedScope(PROJECT).subject).toEqual(other);
    expect(resolvedScope(PROJECT).files.map((f) => f.path)).toEqual(["two.ts"]);
  });

  it("names the answer on screen, not the comparison being picked, until the new one lands", async () => {
    setSidebarScope(PROJECT, { kind: "new" });
    await resolveScope(PROJECT, stub(() => ok([file("a.ts", { changeId: "c1" })])), SUBJECT);
    setSidebarScope(PROJECT, { kind: "turn" });
    const failing = stubClient({ listProjectSourceWalk: vi.fn().mockRejectedValue(new Error("offline")) });

    await resolveScope(PROJECT, failing, SUBJECT);

    expect(resolvedScope(PROJECT).status).toBe("error");
    expect(resolvedLensView(PROJECT)).toMatchObject({ scope: { kind: "new" }, comparisonOff: false });
    expect(resolvedScope(PROJECT).files.map((f) => f.path)).toEqual(["a.ts"]);
  });

  it("reports every answer that lands, even one identical to the last", async () => {
    const client = stub(() => ok([file("a.ts", { changeId: "c1" })]));
    const settled: number[] = [];
    const stop = subscribeScopeSettled((id) => settled.push(scopeSettledEpoch(id)));

    await resolveScope(PROJECT, client, SUBJECT);
    await resolveScope(PROJECT, client, SUBJECT);
    stop();

    expect(settled).toHaveLength(2);
    expect(settled[1]!).toBeGreaterThan(settled[0]!);
  });

  it("does not report a superseded flight as landed", async () => {
    let release!: (page: WalkPage) => void;
    const slow = stub(() => new Promise<WalkPage>((resolve) => { release = resolve; }));
    const first = resolveScope(PROJECT, slow, SUBJECT);
    await resolveScope(PROJECT, stub(() => ok([])), SUBJECT);
    const after = scopeSettledEpoch(PROJECT);
    release(ok([file("stale.ts", { changeId: "c9" })]));
    await first;
    expect(scopeSettledEpoch(PROJECT)).toBe(after);
    expect(isInScope(PROJECT, "r1", "stale.ts")).toBe(false);
  });

  it("routes every surface's request through the one registered resolver", () => {
    const resolve = vi.fn();
    setScopeResolver(PROJECT, resolve);
    requestScopeResolve(PROJECT);
    setSidebarScope(PROJECT, { kind: "commit" });
    expect(resolve).toHaveBeenCalledTimes(2);
  });
});
