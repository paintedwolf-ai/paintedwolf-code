import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { describe, expect, it } from "vitest";
import type {
  SourceCommandWindow,
  SourceGitChange,
  SourceWalkEffect,
  SourceWalkFile,
} from "../../api/types.ts";
import {
  buildWalk,
  clampWalkIndex,
  walkStepAt,
  walkStepAction,
  walkStepActor,
  walkStepEffectForVersion,
  walkStepFiles,
  walkGroupFileSummary,
  walkStepGitChange,
  walkStepIndexForFile,
  walkStepKind,
  walkStepOperation,
  type WalkStep,
} from "./walk-model.ts";

function change(
  overrides: Partial<SourceWalkEffect> & { tool_call_id?: string } = {},
): SourceWalkEffect {
  const id = overrides.id ?? "c1";
  const path = overrides.path ?? "src/a.ts";
  return sourceEffectFixture({
    id,
    project_id: "p1",
    operation_id: overrides.operation_id ?? `operation-${id}`,
    file_id: overrides.file_id ?? `file-${path}`,
    after_version_id: overrides.after_version_id ?? `version-${id}`,
    workspace_kind: overrides.workspace_kind ?? "project",
    root_id: overrides.root_id ?? "r1",
    path,
    entry_kind: overrides.entry_kind ?? "file",
    op: overrides.op ?? "write",
    origin: overrides.origin ?? "agent",
    turn: overrides.turn ?? 1,
    ordinal: overrides.ordinal ?? 1,
    cause: overrides.cause ?? "tool",
    capture_quality: overrides.capture_quality ?? "exact",
    observed_at: overrides.observed_at ?? "2026-08-09T10:00:00Z",
    tool_call_id: overrides.tool_call_id,
    tool_name: overrides.tool_name,
    session_id: overrides.session_id,
    actor_label: overrides.actor_label,
    git_change_id: overrides.git_change_id,
    command_id: overrides.command_id,
  });
}

function file(path: string, effects: SourceWalkEffect[]): SourceWalkFile {
  return {
    file_id: `file-${path}`,
    root_id: "r1",
    path,
    changed_since_presented: false,
    unpresented_agent_effects: 0,
    tip: { state: "content", sha256: "sha-a" },
    head_match: "unknown",
    effects,
  };
}

function movement(
  overrides: Partial<SourceGitChange> = {},
): SourceGitChange {
  return {
    session_id: overrides.session_id ?? "", turn: overrides.turn ?? 0,
    tool_call_id: overrides.tool_call_id ?? "", tool_name: overrides.tool_name ?? "",
    id: overrides.id ?? "t1",
    root_id: overrides.root_id ?? "r1",
    kind: overrides.kind ?? "checkout",
    from_ref: overrides.from_ref,
    to_ref: overrides.to_ref,
    from_commit: overrides.from_commit,
    to_commit: overrides.to_commit,
    detail: overrides.detail,
    ordinal: overrides.ordinal ?? 4,
    observed_at: overrides.observed_at ?? "2026-08-09T10:00:00Z",
  };
}

function effectId(step: WalkStep): string {
  if (step.kind !== "effect") throw new Error(`invariant: git step in effect position: ${step.key}`);
  return step.effect.id;
}

describe("buildWalk", () => {
  it("orders by ledger ordinal", () => {
    const first = change({ id: "c1", path: "first.ts", tool_call_id: "t1", ordinal: 1 });
    const second = change({ id: "c2", path: "second.ts", tool_call_id: "t2", ordinal: 2 });
    const third = change({ id: "c3", path: "third.ts", tool_call_id: "t3", ordinal: 3 });
    const walk = buildWalk(
      "s1",
      [
        file("first.ts", [first]),
        file("second.ts", [second]),
        file("third.ts", [third]),
      ],
    );
    expect(walk.steps.map((s) => s.ordinal)).toEqual([1, 2, 3]);
    expect(walk.steps.map(effectId)).toEqual(["c1", "c2", "c3"]);
    expect(walk.steps.map((s) => s.toolCallId)).toEqual(["t1", "t2", "t3"]);
  });

  it("finds the logical file's first step from its current tree path", () => {
    const walk = buildWalk("s1", [
      file("other.ts", [change({ id: "other", path: "other.ts", ordinal: 1 })]),
      file("new.ts", [
        change({
          id: "before-rename",
          file_id: "logical-file",
          path: "old.ts",
          ordinal: 2,
        }),
        change({
          id: "rename",
          file_id: "logical-file",
          path: "new.ts",
          from_root_id: "r1",
          from_path: "old.ts",
          op: "rename",
          ordinal: 3,
        }),
      ]),
    ]);

    expect(walkStepIndexForFile(walk, { rootId: "r1", path: "new.ts" })).toBe(1);
    expect(walkStepIndexForFile(walk, { rootId: "r1", path: "missing.ts" })).toBe(-1);
  });

  it("expands a multi-path call into one step per effect, ordered by ordinal", () => {
    const rows = [
      change({ id: "c1", path: "a.ts", tool_call_id: "t1", ordinal: 10 }),
      change({ id: "c2", path: "b.ts", tool_call_id: "t1", ordinal: 11 }),
      change({ id: "c3", path: "c.ts", tool_call_id: "t1", ordinal: 12 }),
      change({ id: "c4", path: "d.ts", tool_call_id: "t1", ordinal: 13 }),
      change({ id: "c5", path: "e.ts", tool_call_id: "t1", ordinal: 14 }),
    ];
    const walk = buildWalk(
      "s1",
      [
        file("a.ts", [rows[0]!]),
        file("b.ts", [rows[1]!]),
        file("c.ts", [rows[2]!]),
        file("d.ts", [rows[3]!]),
        file("e.ts", [rows[4]!]),
      ],
    );
    expect(walk.steps).toHaveLength(5);
    expect(walk.steps.map(effectId)).toEqual(["c1", "c2", "c3", "c4", "c5"]);
    expect(walk.steps.every((s) => s.toolCallId === "t1")).toBe(true);
  });

  it("keeps every effect when a call wrote the same path twice", () => {
    const walk = buildWalk(
      "s1",
      [
        file("a.ts", [
          change({
            id: "old",
            path: "a.ts",
            observed_at: "2026-08-09T10:00:00Z",
            tool_call_id: "t1",
            ordinal: 1,
          }),
          change({
            id: "new",
            path: "a.ts",
            observed_at: "2026-08-09T10:05:00Z",
            tool_call_id: "t1",
            ordinal: 2,
          }),
        ]),
      ],
    );
    expect(walk.steps).toHaveLength(2);
    expect(walk.steps.map(effectId)).toEqual(["old", "new"]);
  });

  it("uses the ledger tool name", () => {
    const walk = buildWalk(
      "s1",
      [file("a.ts", [change({ id: "c9", path: "a.ts", tool_call_id: "t9", tool_name: "edit" })])],
    );
    expect(walk.steps.map(effectId)).toEqual(["c9"]);
    expect(walk.steps.map((s) => s.toolCallId)).toEqual(["t9"]);
    expect(walk.steps[0]!.label).toBe("edit");
  });

  it("returns no steps when the ledger has no effects", () => {
    const walk = buildWalk("s1", []);
    expect(walk.steps).toEqual([]);
  });

  it("includes affiliated human edits as steps interleaved by ordinal", () => {
    const walk = buildWalk(
      "s1",
      [
        file("a.ts", [
          change({
            id: "c-agent",
            path: "a.ts",
            tool_call_id: "t1",
            ordinal: 1,
            origin: "agent",
          }),
        ]),
        file("b.ts", [
          change({
            id: "c-you",
            path: "b.ts",
            ordinal: 2,
            origin: "user",
            session_id: "s1",
            cause: "human_edit",
          }),
        ]),
      ],
    );
    expect(walk.steps.map(effectId)).toEqual(["c-agent", "c-you"]);
    expect(walk.steps[1]!.toolCallId).toBeNull();
    expect(walk.steps[1]!.label).toBe("edit");
  });

  it("keeps external rows independently addressable", () => {
    const walk = buildWalk(
      "s1",
      [
        file("external.ts", [
          change({ id: "cx", path: "external.ts", origin: "external", cause: "" }),
        ]),
      ],
    );
    expect(walk.steps).toHaveLength(1);
    expect(effectId(walk.steps[0]!)).toBe("cx");
    expect(walk.steps[0]!.toolCallId).toBeNull();
    expect(walk.steps[0]!.label).toBe("write");
    expect(walkStepGitChange(walk.steps[0]!)).toBeNull();
  });

  it("labels an unanchored agent tip from the ledger, never a fake tool name", () => {
    const walk = buildWalk(
      "s1",
      [
        file("a.ts", [
          change({
            id: "c1",
            path: "a.ts",
            tool_call_id: "t1",
            ordinal: 1,
            cause: "overlay_promote",
          }),
        ]),
      ],
    );
    expect(walk.steps).toHaveLength(1);
    expect(walk.steps[0]!.label).toBe("overlay promote");
    expect(walk.steps[0]!.toolCallId).toBe("t1");
  });
});

describe("git steps", () => {
  const checkout = movement({
    id: "t1",
    from_ref: "main",
    to_ref: "feature-x",
    detail: "moving from main to feature-x",
    ordinal: 4,
  });
  const rewritten = [
    change({
      id: "cg1",
      path: "swapped.ts",
      origin: "external",
      cause: "filesystem_reconcile",
      git_change_id: "t1",
      ordinal: 5,
    }),
    change({
      id: "cg2",
      path: "other.ts",
      origin: "external",
      cause: "filesystem_reconcile",
      git_change_id: "t1",
      ordinal: 6,
    }),
  ];

  it("collapses a movement's effects into one step at its own ordinal", () => {
    const walk = buildWalk(
      "s1",
      [
        file("before.ts", [change({ id: "c1", path: "before.ts", ordinal: 2 })]),
        file("swapped.ts", [rewritten[0]!]),
        file("other.ts", [rewritten[1]!]),
        file("after.ts", [change({ id: "c2", path: "after.ts", ordinal: 9 })]),
      ],
      [checkout],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["c1", "git:t1", "c2"]);
    const git = walk.steps[1]!;
    if (git.kind !== "git") throw new Error("invariant: expected a git step");
    expect(git.label).toBe("git checkout");
    expect(git.ordinal).toBe(4);
    expect(git.effects.map((e) => e.id)).toEqual(["cg1", "cg2"]);
    expect(git.toolCallId).toBeNull();
    expect(walkGroupFileSummary(git)).toBe("2 observed files");
    expect(walkGroupFileSummary({ ...git, effects: [] })).toBe("Git review");
    expect(walkStepKind(git)).toBe("git");
    expect(walkStepOperation(git)).toBe("Git checkout");
    expect(walkStepActor(git)).toBe("Git");
    expect(walkStepGitChange(git)).toBe(
      "checkout main → feature-x — moving from main to feature-x",
    );
  });

  it("stands a bare movement alone with no effects", () => {
    const commit = movement({ id: "t2", kind: "commit", to_ref: "main", ordinal: 7, detail: "Save the draft" });
    const walk = buildWalk(
      "s1",
      [file("a.ts", [change({ id: "c1", path: "a.ts", ordinal: 2 })])],
      [commit],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["c1", "git:t2"]);
    const bare = walk.steps[1]!;
    if (bare.kind !== "git") throw new Error("invariant: expected a git step");
    expect(bare.effects).toEqual([]);
  });

  it("keeps an effect naming an unresolved movement as a plain step", () => {
    const walk = buildWalk(
      "s1",
      [
        file("swapped.ts", [
          change({
            id: "cg1",
            path: "swapped.ts",
            origin: "external",
            cause: "filesystem_reconcile",
            git_change_id: "missing",
            ordinal: 5,
          }),
        ]),
      ],
      [],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["cg1"]);
    expect(walk.steps[0]!.kind).toBe("effect");
  });

  it("locates a file inside a git step", () => {
    const walk = buildWalk(
      "s1",
      [file("swapped.ts", [rewritten[0]!]), file("other.ts", [rewritten[1]!])],
      [checkout],
    );
    expect(walkStepIndexForFile(walk, { rootId: "r1", path: "other.ts" })).toBe(0);
    expect(walkStepIndexForFile(walk, { rootId: "r1", path: "absent.ts" })).toBe(-1);
  });

  it("answers which effect step produced a version; a group answers with its page", () => {
    const walk = buildWalk(
      "s1",
      [
        file("before.ts", [change({ id: "c1", path: "before.ts", ordinal: 2 })]),
        file("swapped.ts", [rewritten[0]!]),
      ],
      [checkout],
    );
    expect(walkStepEffectForVersion(walk.steps[0]!, "version-c1")?.id)
      .toBe("c1");
    expect(walkStepEffectForVersion(walk.steps[1]!, "version-cg1")).toBeNull();
    expect(walkStepEffectForVersion(walk.steps[0]!, "version-none")).toBeNull();
  });
});

describe("outside steps", () => {
  const outside = (id: string, path: string, ordinal: number, observed_at = "2026-08-09T10:00:00Z") =>
    change({
      id, path, ordinal, observed_at,
      origin: "external", cause: "filesystem_reconcile", session_id: undefined, turn: 0,
    });

  it("collapses a run of outside changes into one step keyed by its first effect", () => {
    const walk = buildWalk(
      "s1",
      [
        file("a.ts", [change({ id: "c1", path: "a.ts", ordinal: 1 })]),
        file("gen/x.ts", [outside("o1", "gen/x.ts", 2)]),
        file("gen/y.ts", [outside("o2", "gen/y.ts", 3)]),
        file("gen/z.ts", [outside("o3", "gen/z.ts", 4)]),
        file("b.ts", [change({ id: "c2", path: "b.ts", ordinal: 5 })]),
      ],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["c1", "outside:o1", "c2"]);
    const run = walk.steps[1]!;
    if (run.kind !== "outside") throw new Error("invariant: expected an outside step");
    expect(run.ordinal).toBe(2);
    expect(run.effects.map((e) => e.id)).toEqual(["o1", "o2", "o3"]);
    expect(run.toolCallId).toBeNull();
    expect(walkStepKind(run)).toBe("outside");
    expect(walkStepOperation(run)).toBe("Outside the app");
    expect(walkStepActor(run)).toBe("Outside the app");
  });

  it("leaves a single outside change as the edit it is", () => {
    const walk = buildWalk(
      "s1",
      [
        file("a.ts", [change({ id: "c1", path: "a.ts", ordinal: 1 })]),
        file("notes.md", [outside("o1", "notes.md", 2)]),
        file("b.ts", [change({ id: "c2", path: "b.ts", ordinal: 3 })]),
      ],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["c1", "o1", "c2"]);
    expect(walk.steps[1]!.kind).toBe("effect");
    expect(walkStepActor(walk.steps[1]!)).toBe("Outside app");
  });

  it("breaks a run at a git movement or a chat step", () => {
    const checkout = movement({ id: "t1", ordinal: 3 });
    const walk = buildWalk(
      "s1",
      [
        file("gen/x.ts", [outside("o1", "gen/x.ts", 1)]),
        file("gen/y.ts", [outside("o2", "gen/y.ts", 2)]),
        file("gen/z.ts", [outside("o3", "gen/z.ts", 4)]),
        file("gen/w.ts", [outside("o4", "gen/w.ts", 5)]),
      ],
      [checkout],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["outside:o1", "git:t1", "outside:o3"]);
  });

  it("keeps a session-affiliated change out of an outside run", () => {
    const walk = buildWalk(
      "s1",
      [
        file("gen/x.ts", [outside("o1", "gen/x.ts", 1)]),
        file("saved.ts", [change({ id: "u1", path: "saved.ts", ordinal: 2, origin: "user", session_id: "s1" })]),
        file("gen/y.ts", [outside("o2", "gen/y.ts", 3)]),
      ],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["o1", "u1", "o2"]);
  });

  it("counts a file written twice in one run as one file at its latest version", () => {
    const walk = buildWalk(
      "s1",
      [
        file("README.md", [outside("o1", "README.md", 1), outside("o2", "README.md", 2)]),
        file("docs/guide.md", [outside("o3", "docs/guide.md", 3)]),
      ],
    );
    const run = walk.steps[0]!;
    expect(run.kind).toBe("outside");
    expect(walkStepFiles(run).map((effect) => [effect.path, effect.id]))
      .toEqual([["docs/guide.md", "o3"], ["README.md", "o2"]]);
  });

  it("locates a file inside an outside run", () => {
    const walk = buildWalk(
      "s1",
      [
        file("gen/x.ts", [outside("o1", "gen/x.ts", 1)]),
        file("gen/y.ts", [outside("o2", "gen/y.ts", 2)]),
      ],
    );
    expect(walkStepIndexForFile(walk, { rootId: "r1", path: "gen/y.ts" })).toBe(0);
  });
});

describe("command steps", () => {
  const build = (overrides: Partial<SourceCommandWindow> = {}): SourceCommandWindow => ({
    id: "w1",
    session_id: "s1",
    turn: 3,
    tool_call_id: "call-1",
    tool_name: "command",
    command_line: "cargo build --release",
    state: "ended",
    admission_mode: "scope",
    ordinal: 4,
    started_at: "2026-08-09T10:00:00Z",
    ended_at: "2026-08-09T10:00:30Z",
    ...overrides,
  });
  const observed = [
    change({
      id: "cw1",
      path: "Cargo.lock",
      op: "create",
      origin: "external",
      cause: "command_window",
      command_id: "w1",
      session_id: "s1",
      turn: 3,
      ordinal: 5,
    }),
    change({
      id: "cw2",
      path: "src/gen.rs",
      origin: "external",
      cause: "command_window",
      command_id: "w1",
      session_id: "s1",
      turn: 3,
      ordinal: 6,
    }),
  ];

  it("collapses a window's effects into one step at its own ordinal", () => {
    const walk = buildWalk(
      "s1",
      [
        file("before.ts", [change({ id: "c1", path: "before.ts", ordinal: 2 })]),
        file("Cargo.lock", [observed[0]!]),
        file("src/gen.rs", [observed[1]!]),
        file("after.ts", [change({ id: "c2", path: "after.ts", ordinal: 9 })]),
      ],
      [],
      [build()],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["c1", "command:w1", "c2"]);
    const command = walk.steps[1]!;
    if (command.kind !== "command") throw new Error("invariant: expected a command step");
    expect(command.label).toBe("command");
    expect(command.ordinal).toBe(4);
    expect(command.effects.map((e) => e.id)).toEqual(["cw1", "cw2"]);
    expect(command.toolCallId).toBe("call-1");
    expect(walkStepKind(command)).toBe("command");
    expect(walkStepOperation(command)).toBe("Command");
    expect(walkStepActor(command)).toBe("Command");
    expect(walkStepGitChange(command)).toBeNull();
  });

  it("never stands a window alone: an unreferenced command is no step", () => {
    const walk = buildWalk(
      "s1",
      [file("a.ts", [change({ id: "c1", path: "a.ts", ordinal: 2 })])],
      [],
      [build({ id: "quiet" })],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["c1"]);
  });

  it("keeps an effect naming an unresolved window as a plain command-observed step", () => {
    const walk = buildWalk(
      "s1",
      [file("Cargo.lock", [{ ...observed[0]!, command_id: "missing" }])],
      [],
      [],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["cw1"]);
    expect(walk.steps[0]!.kind).toBe("effect");
    expect(walkStepActor(walk.steps[0]!)).toBe("Command");
  });

  it("lets a movement outrank the window when an effect names both", () => {
    const checkout = movement({ id: "t1", ordinal: 3 });
    const walk = buildWalk(
      "s1",
      [file("Cargo.lock", [{ ...observed[0]!, git_change_id: "t1" }])],
      [checkout],
      [build()],
    );
    expect(walk.steps.map((s) => s.key)).toEqual(["git:t1"]);
  });

  it("locates a file inside a command step", () => {
    const walk = buildWalk(
      "s1",
      [file("Cargo.lock", [observed[0]!]), file("src/gen.rs", [observed[1]!])],
      [],
      [build()],
    );
    expect(walkStepIndexForFile(walk, { rootId: "r1", path: "src/gen.rs" })).toBe(0);
  });
});

describe("step facts", () => {
  const build = (op: SourceWalkEffect["op"]) =>
    buildWalk(
      "s1",
      [file("a.ts", [change({ id: "c1", path: "a.ts", op, tool_call_id: "t1" })])],
    ).steps[0]!;

  it("maps operations to their presentation kind", () => {
    expect(walkStepKind(build("delete"))).toBe("delete");
    expect(walkStepKind(build("create"))).toBe("create");
    expect(walkStepKind(build("write"))).toBe("write");
    expect(walkStepKind(build("rename"))).toBe("write");
  });
});

describe("walk navigation", () => {
  const walk = buildWalk(
    "s1",
    [file("a.ts", [change({ id: "c1", path: "a.ts", tool_call_id: "t1", tool_name: "edit" })])],
  );

  it("clamps an index onto the walk", () => {
    expect(clampWalkIndex(walk, -5)).toBe(0);
    expect(clampWalkIndex(walk, 99)).toBe(0);
    expect(clampWalkIndex(walk, Number.NaN)).toBe(0);
    expect(clampWalkIndex({ baseline: "session:s1", steps: [], chapters: [] }, 0)).toBe(-1);
  });

  it("labels a step by its action", () => {
    expect(walkStepAction(walkStepAt(walk, 0)!)).toBe("edit");
  });

  it("shares an action label across a multi-path call", () => {
    const many = buildWalk(
      "s1",
      [
        file("a.ts", [change({ id: "c1", path: "a.ts", tool_call_id: "t1", tool_name: "promote_overlay", ordinal: 1 })]),
        file("b.ts", [change({ id: "c2", path: "b.ts", tool_call_id: "t1", tool_name: "promote_overlay", ordinal: 2 })]),
      ],
    );
    expect(walkStepAction(many.steps[0]!)).toBe("promote overlay");
    expect(walkStepAction(many.steps[1]!)).toBe("promote overlay");
  });

  it("labels a human tip as edit", () => {
    const human = buildWalk(
      "s1",
      [
        file("note.md", [
          change({ id: "h1", path: "note.md", origin: "user", ordinal: 1 }),
        ]),
      ],
    );
    expect(walkStepAction(human.steps[0]!)).toBe("edit");
  });

  it("derives preview context from ledger fields", () => {
    const contextual = buildWalk(
      "s1",
      [
        file("src/a.ts", [
          change({
            path: "src/a.ts",
            op: "rename",
            tool_call_id: "t1",
            tool_name: "promote_overlay",
            actor_label: "Builder",
          }),
        ]),
      ],
    ).steps[0]!;

    expect(walkStepOperation(contextual)).toBe("Renamed");
    expect(walkStepKind(contextual)).toBe("write");
    expect(walkStepActor(contextual)).toBe("Builder");
    expect(walkStepAction(contextual)).toBe("promote overlay");
  });
});
