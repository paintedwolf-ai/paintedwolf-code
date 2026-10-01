import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { TEST_OWNER_PERSON_ID } from "../../platform/connection/host-identity-test.ts";
import { describe, expect, it } from "vitest";
import type {
  SourceWalkEffect,
  SourceWalkResponse,
} from "../../api/types.ts";
import {
  baselineTurn,
  buildReviewFileRows,
  buildReviewRows,
  encodeEyeChoice,
  lensEmptyCopy,
  lensPickerLabel,
  lensScopeHeader,
  liveRowsInScope,
  parseEyeChoice,
  previewDiffBufferJobId,
  requestBaseline,
  reviewRowKey,
  turnBaseline,
  unpresentedAgentFileCount,
  type LiveRowScope,
  type ReviewLiveRow,
  type ReviewLiveState,
} from "./review-model.ts";

function row(
  overrides: Partial<{
    id: string;
    path: string;
    root_id: string;
    session_id: string;
    origin: "agent" | "user" | "external";
    batch_id: string;
    op: "write" | "create" | "delete" | "rename";
  }> = {},
): SourceWalkEffect {
  return sourceEffectFixture({
    id: overrides.id ?? "c1",
    project_id: "p1",
    operation_id: `operation-${overrides.id ?? "c1"}`,
    file_id: `file-${overrides.path ?? "src/a.ts"}`,
    after_version_id: `version-${overrides.id ?? "c1"}`,
    workspace_kind: "project",
    root_id: overrides.root_id ?? "r1",
    path: overrides.path ?? "src/a.ts",
    entry_kind: "file",
    op: overrides.op ?? ("write" as const),
    origin: overrides.origin ?? ("agent" as const),
    session_id: overrides.session_id,
    turn: 1,
    ordinal: 1,
    cause: "tool",
    capture_quality: "exact",
    batch_id: overrides.batch_id,
    observed_at: "2026-07-30T12:00:00Z",
  });
}

describe("review-model comparisons", () => {
  it("remembers the eye's choice without naming a chat", () => {
    for (const scope of [
      { kind: "new" as const },
      { kind: "turn" as const },
      { kind: "session" as const },
      { kind: "commit" as const },
      { kind: "pin" as const, pinId: "pin-1" },
    ]) {
      expect(parseEyeChoice(encodeEyeChoice(scope))).toEqual(scope);
    }
    expect(parseEyeChoice("turn:s1,3")).toBeNull();
    expect(parseEyeChoice("presentation")).toBeNull();
    expect(parseEyeChoice("bogus")).toBeNull();
  });

  it("asks the host for the selected chat's current turn, and names no chat without one", () => {
    const subject = { sessionId: "s1", title: "Focus", sessionScoped: true };
    expect(requestBaseline({ kind: "new" }, subject)).toBe("presentation");
    expect(requestBaseline({ kind: "turn" }, subject)).toBe("turn:s1");
    expect(requestBaseline({ kind: "session" }, subject)).toBe("session:s1");
    expect(requestBaseline({ kind: "commit" }, null)).toBe("commit");
    expect(requestBaseline({ kind: "pin", pinId: "pin-1" }, null)).toBe("pin:pin-1");
    expect(requestBaseline({ kind: "turn" }, null)).toBeNull();
    expect(requestBaseline({ kind: "session" }, null)).toBeNull();
  });

  it("reads the turn a resolved baseline names", () => {
    expect(turnBaseline("s1", 4)).toBe("turn:s1,4");
    expect(baselineTurn("turn:s1,4")).toBe(4);
    expect(baselineTurn("turn:s1,0")).toBe(0);
    expect(baselineTurn("turn:s1")).toBeNull();
    expect(baselineTurn("session:s1")).toBeNull();
  });

  it("provides per-lens empty and header copy", () => {
    expect(lensEmptyCopy({ kind: "new" })).toBe("Nothing new since you last looked.");
    expect(lensEmptyCopy({ kind: "turn" })).toBe("Nothing changed yet in this turn.");
    expect(lensEmptyCopy({ kind: "pin", pinId: "p" })).toBe("No changes since this pin.");
    expect(lensScopeHeader({ kind: "new" })).toBe("New since you looked");
    expect(lensScopeHeader({ kind: "turn" }, { turnRunning: true })).toBe("Changes in this turn, as they land");
    expect(lensScopeHeader({ kind: "turn" }, { turnRunning: false })).toBe("Changes in this turn");
    expect(lensScopeHeader({ kind: "session" }, { subjectTitle: "Fix auth" })).toBe("Changes in “Fix auth”");
    expect(lensScopeHeader({ kind: "session" })).toBe("Changes in this chat");
    expect(lensScopeHeader({ kind: "pin", pinId: "p", label: "Before the big refactor" }))
      .toContain("Before the big refactor");
    expect(lensPickerLabel({ kind: "new" })).toBe("New since you looked");
    expect(lensPickerLabel({ kind: "turn" })).toBe("This turn");
  });
});

describe("review-model list", () => {
  it("keeps every contributor in one file list", () => {
    const response: SourceWalkResponse = {
      baseline: "presentation",
      commit_available: true,
      next_cursor: undefined,
      git_changes: [], commands: [], turns: [],
      files: [
        {
          file_id: "file-fixture",
          root_id: "r1",
          path: "src/a.ts",
          changed_since_presented: true,
          unpresented_agent_effects: 1,
          tip: { state: "content", sha256: "tip" },
          head_match: "unknown",
          last_at: "2026-07-30T12:00:00Z",
          effects: [
            row({ id: "1", session_id: "s1" }),
            row({ id: "2", session_id: "s2", origin: "user" }),
          ],
        },
        {
          file_id: "file-fixture",
          root_id: "r1",
          path: "tmp/out.log",
          changed_since_presented: false,
          unpresented_agent_effects: 0,
          tip: { state: "content", sha256: "tip" },
          head_match: "unknown",
          effects: [row({ id: "3", path: "tmp/out.log", origin: "external" })],
        },
      ],
    };
    const files = buildReviewFileRows(response.files);
    expect(files).toHaveLength(2);
    expect(files[0]!.attribution).toBe("You + Agent");
    expect(files[0]!.unpresentedAgentEffects).toBe(1);
    expect(files[1]!.attribution).toBe("Outside app");
    expect(files[1]!.contributors.map((c) => c.kind)).toEqual(["external"]);
  });

  it("names another person's edit without claiming it as yours", () => {
    const mine = row({ id: "6", session_id: "s1", origin: "user" });
    const theirs = row({ id: "7", session_id: "s1", origin: "user" });
    theirs.contributors = theirs.contributors.map((author) => ({
      ...author,
      person_id: "00000000-0000-4000-8000-0000000000b2",
    }));
    const model = buildReviewFileRows([
      {
        file_id: "file-shared",
        root_id: "r1",
        path: "src/shared.ts",
        changed_since_presented: false,
        unpresented_agent_effects: 0,
        tip: { state: "content", sha256: "tip" },
        head_match: "unknown",
        effects: [theirs, mine],
      },
    ]);
    expect(model[0]!.attribution).toBe("Another person + You");
    expect(model[0]!.contributors.map((c) => c.personId)).toEqual([
      "00000000-0000-4000-8000-0000000000b2",
      TEST_OWNER_PERSON_ID,
    ]);
  });

  it("tells a change a command window covered apart from one outside the app", () => {
    const model = buildReviewFileRows([
      {
        file_id: "file-lock",
        root_id: "r1",
        path: "Cargo.lock",
        changed_since_presented: false,
        unpresented_agent_effects: 0,
        tip: { state: "content", sha256: "tip" },
        head_match: "unknown",
        effects: [
          {
            ...row({ id: "4", path: "Cargo.lock", origin: "external" }),
            cause: "command_window",
            command_id: "w1",
          },
          row({ id: "5", path: "Cargo.lock", origin: "agent" }),
        ],
      },
    ]);
    expect(model[0]!.attribution).toBe("Agent + Command");
    expect(model[0]!.contributors.map((c) => c.kind)).toEqual(["agent", "command"]);
  });

  it("counts files with unpresented agent changes", () => {
    const response: SourceWalkResponse = {
      baseline: "presentation",
      commit_available: true,
      next_cursor: undefined,
      git_changes: [], commands: [], turns: [],
      files: [
        {
          file_id: "file-fixture",
          root_id: "r1",
          path: "src/a.ts",
          changed_since_presented: true,
          unpresented_agent_effects: 1,
          tip: { state: "content", sha256: "tip" },
          head_match: "unknown",
          effects: [row({ id: "1" })],
        },
        {
          file_id: "file-fixture",
          root_id: "r1",
          path: "src/b.ts",
          changed_since_presented: false,
          unpresented_agent_effects: 0,
          tip: { state: "content", sha256: "tip" },
          head_match: "unknown",
          effects: [row({ id: "2", path: "src/b.ts" })],
        },
        {
          file_id: "file-fixture",
          root_id: "r1",
          path: "tmp/out.log",
          changed_since_presented: true,
          unpresented_agent_effects: 0,
          tip: { state: "content", sha256: "tip" },
          head_match: "unknown",
          effects: [row({ id: "3", path: "tmp/out.log", origin: "external" })],
        },
      ],
    };
    expect(unpresentedAgentFileCount(response.files)).toBe(1);
  });

  it("gives preview diffs their own per-path identity", () => {
    expect(previewDiffBufferJobId("src/a.ts")).toBe("diff:preview:src/a.ts");
    expect(previewDiffBufferJobId("src/b.ts")).not.toBe(
      previewDiffBufferJobId("src/a.ts"),
    );
  });
});

describe("dominantOp via buildReviewListModel", () => {
  it("labels a file created and then edited again as created, not modified", () => {
    const model = buildReviewFileRows([
      {
        file_id: "file-fixture",
        root_id: "r1",
        path: "src/new.ts",
        changed_since_presented: true,
        unpresented_agent_effects: 1,
        tip: { state: "content", sha256: "tip" },
        head_match: "unknown",
        // Newest-first: the introducing create is the last entry.
        effects: [
          row({ id: "2", path: "src/new.ts", op: "write" }),
          row({ id: "1", path: "src/new.ts", op: "create" }),
        ],
      },
    ]);
    expect(model[0]!.op).toBe("create");
  });

  it("still labels a plain edit as write", () => {
    const model = buildReviewFileRows([
      {
        file_id: "file-fixture",
        root_id: "r1",
        path: "src/a.ts",
        changed_since_presented: true,
        unpresented_agent_effects: 0,
        tip: { state: "content", sha256: "tip" },
        head_match: "unknown",
        effects: [row({ id: "1", op: "write" })],
      },
    ]);
    expect(model[0]!.op).toBe("write");
  });

  it("labels a file created and then deleted as deleted", () => {
    const model = buildReviewFileRows([
      {
        file_id: "file-fixture",
        root_id: "r1",
        path: "src/new.ts",
        changed_since_presented: true,
        unpresented_agent_effects: 0,
        tip: { state: "absent" },
        head_match: "unknown",
        effects: [
          row({ id: "2", path: "src/new.ts", op: "delete" }),
          row({ id: "1", path: "src/new.ts", op: "create" }),
        ],
      },
    ]);
    expect(model[0]!.op).toBe("delete");
  });
});

describe("buildReviewRows", () => {
  const files = buildReviewFileRows([
    {
      file_id: "file-fixture",
      root_id: "r1",
      path: "src/a.ts",
      changed_since_presented: true,
      unpresented_agent_effects: 1,
      tip: { state: "content", sha256: "tip" },
      head_match: "unknown",
      effects: [row()],
    },
    {
      file_id: "file-fixture",
      root_id: "r1",
      path: "src/b.ts",
      changed_since_presented: false,
      unpresented_agent_effects: 0,
      tip: { state: "content", sha256: "tip" },
      head_match: "unknown",
      effects: [row({ id: "c2", path: "src/b.ts" })],
    },
  ]);

  const live = (path: string, state: ReviewLiveState, overrides: Partial<ReviewLiveRow> = {}): ReviewLiveRow => ({
    rootId: "r1",
    path,
    state,
    chatId: "s1",
    chatTitle: "Work",
    turn: 3,
    toolCallIds: [`call-${path}`],
    jobIds: [],
    ...overrides,
  });

  it("keeps a live file as one row, not a second entry", () => {
    const rows = buildReviewRows(files, [live("src/a.ts", "editing")]);
    expect(rows).toHaveLength(2);
    const a = rows.find((r) => r.path === "src/a.ts");
    expect(a?.liveVerb).toBe("editing");
    expect(a?.steps).toHaveLength(1);
    expect(a?.unpresentedAgentEffects).toBe(1);
  });

  it("orders in-flight rows ahead of settled ones", () => {
    const rows = buildReviewRows(files, [live("src/b.ts", "editing")]);
    expect(rows.map((r) => r.path)).toEqual(["src/b.ts", "src/a.ts"]);
  });

  it("adds a row for a path being changed that has not landed yet", () => {
    const rows = buildReviewRows(files, [live("src/new.ts", "editing")]);
    expect(rows[0]?.path).toBe("src/new.ts");
    expect(rows[0]?.basename).toBe("new.ts");
    expect(rows[0]?.attribution).toBe("Work");
    expect(rows[0]?.steps).toEqual([]);
  });

  it("labels worker drafts and marks them read-only", () => {
    expect(buildReviewRows(files, [live("w.go", "sandbox")])[0]).toMatchObject({ liveVerb: "drafting", sandbox: true });
    expect(buildReviewRows(files, [live("w.go", "ready")])[0]).toMatchObject({ liveVerb: "ready to land", sandbox: true });
    expect(buildReviewRows(files, [live("w.go", "landing")])[0]).toMatchObject({ liveVerb: "landing", sandbox: false });
  });

  it("keeps the same path in two roots as two files", () => {
    const rows = buildReviewRows(files, [live("src/a.ts", "editing", { rootId: "r2" })]);
    expect(rows).toHaveLength(3);
    expect(rows[0]).toMatchObject({ rootId: "r2", path: "src/a.ts", liveVerb: "editing" });
    expect(rows.find((r) => r.rootId === "r1" && r.path === "src/a.ts")?.liveVerb).toBeNull();
  });

  it("carries a landing's line counts onto its file, never a Git row", () => {
    const rows = buildReviewRows(files, [], new Map([[reviewRowKey("r1", "src/a.ts"), { added: 54, removed: 18 }]]));
    expect(rows.find((r) => r.path === "src/a.ts")).toMatchObject({ added: 54, removed: 18 });
    expect(rows.find((r) => r.path === "src/b.ts")).toMatchObject({ added: null, removed: null });
  });
});

describe("liveRowsInScope", () => {
  const rows = (): ReviewLiveRow[] => [
    { rootId: "r1", path: "a.ts", state: "editing", chatId: "s1", chatTitle: "One", turn: 3, toolCallIds: ["c1"], jobIds: [] },
    { rootId: "r1", path: "b.ts", state: "editing", chatId: "s1", chatTitle: "One", turn: 4, toolCallIds: ["c2"], jobIds: [] },
    { rootId: "r1", path: "c.ts", state: "editing", chatId: "s2", chatTitle: "Two", turn: 3, toolCallIds: ["c3"], jobIds: [] },
  ];
  const view = (overrides: Partial<LiveRowScope>): LiveRowScope => ({
    scope: { kind: "new" },
    subject: { sessionId: "s1", title: "One", sessionScoped: true },
    turn: null,
    comparisonOff: false,
    ...overrides,
  });
  const paths = (list: ReviewLiveRow[]) => list.map((row) => row.path);

  it("lists only what the answered turn will list once it lands", () => {
    expect(paths(liveRowsInScope(rows(), view({ scope: { kind: "turn" }, turn: 3 })))).toEqual(["a.ts"]);
  });

  it("lists the chat's own work for the whole chat", () => {
    expect(paths(liveRowsInScope(rows(), view({ scope: { kind: "session" } })))).toEqual(["a.ts", "b.ts"]);
  });

  it("lists nothing for a chat comparison with no chat", () => {
    expect(liveRowsInScope(rows(), view({ scope: { kind: "turn" }, subject: null, turn: null }))).toEqual([]);
  });

  it("lists every chat's work for comparisons that include it, and while marking is off", () => {
    expect(paths(liveRowsInScope(rows(), view({})))).toEqual(["a.ts", "b.ts", "c.ts"]);
    expect(paths(liveRowsInScope(rows(), view({ scope: { kind: "turn" }, turn: 3, comparisonOff: true }))))
      .toEqual(["a.ts", "b.ts", "c.ts"]);
  });
});
