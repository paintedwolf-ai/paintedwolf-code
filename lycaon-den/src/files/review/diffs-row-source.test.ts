import { afterEach, describe, expect, it } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceWalkEffect, SourceWalkFile } from "../../api/types.ts";
import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { diffsFileChange, diffsFileWrites, diffsRowSource, gitDiffsRowSource, type DiffsRowContext } from "./diffs-row-source.ts";
import type { DiffsAddress, GitDiffsAddress } from "./diffs-address.ts";
import { resetScopeResolutionForTests } from "../tree/scope-resolution.ts";

const TURN: DiffsAddress = { kind: "turn", sessionId: "s1", turn: 4, messageId: "s1-user-4" };
const LENS: DiffsAddress = { kind: "lens" };

function effect(id: string, ordinal: number, op: SourceWalkEffect["op"]): SourceWalkEffect {
  return sourceEffectFixture({
    id, project_id: "p1", operation_id: `op-${id}`, file_id: "file-a",
    before_version_id: `before-${id}`, after_version_id: `after-${id}`,
    workspace_kind: "project", root_id: "r1", path: "internal/load.go", entry_kind: "file",
    op, origin: "agent", session_id: "s1", turn: 4, ordinal,
    tool_call_id: `call-${id}`, tool_name: "replace_lines", cause: "tool",
    capture_quality: "exact", observed_at: "2026-09-17T00:00:00Z",
  });
}

function file(effects: SourceWalkEffect[], extra: Partial<SourceWalkFile> = {}): SourceWalkFile {
  return {
    file_id: "file-a", root_id: "r1", path: "internal/load.go",
    changed_since_presented: false, unpresented_agent_effects: 0,
    tip: { state: "content", sha256: "tip" }, head_match: "unknown", effects, ...extra,
  };
}

function context(address: DiffsAddress): DiffsRowContext {
  return {
    client: () => null,
    projectId: () => "p1",
    address: () => address,
    markUserEdits: () => true,
    rootRefs: () => undefined,
  };
}

afterEach(() => resetScopeResolutionForTests());

describe("a changed file presented as a diff row", () => {
  it("reads the comparison's writes in the order they were applied", () => {
    // The lens answers newest first; the strip counts forward.
    const writes = diffsFileWrites(file([effect("third", 9, "write"), effect("first", 3, "write")]));
    expect(writes.map((write) => write.id)).toEqual(["first", "third"]);
  });

  it("classifies the whole range, so a file created and rewritten still reads as added", () => {
    expect(diffsFileChange(file([effect("b", 9, "write"), effect("a", 3, "create")]))).toBe("added");
    expect(diffsFileChange(file([effect("b", 9, "delete"), effect("a", 3, "write")]))).toBe("deleted");
    expect(diffsFileChange(file([effect("b", 9, "write"), effect("a", 3, "write")]))).toBe("changed");
  });

  it("reads a Git row's presence from its tip, because it has no recorded history", () => {
    const untracked = file([], {
      commit: { head: "abc", status: "??", op: "create", availability: "available", history_truncated: false },
    });
    expect(diffsFileChange(untracked)).toBe("added");
    expect(diffsRowSource(context(LENS), untracked).revisions).toHaveLength(0);
  });

  it("keys a row by its comparison, so two pages never share disclosure state", () => {
    const rows = file([effect("a", 3, "write")]);
    expect(diffsRowSource(context(LENS), rows).key)
      .not.toBe(diffsRowSource(context(TURN), rows).key);
    expect(diffsRowSource(context(TURN), rows).key)
      .not.toBe(diffsRowSource(context({ ...TURN, turn: 5 }), rows).key);
  });

  it("carries a chat destination only for a turn, which is the only address with a chat", () => {
    const rows = file([effect("a", 3, "write")]);
    expect(diffsRowSource(context(TURN), rows).chatDestination).toEqual({ projectId: "p1", sessionId: "s1" });
    expect(diffsRowSource(context(LENS), rows).chatDestination).toBeNull();
  });

  it("leaves a removed file unopenable while its writes stay openable", () => {
    const row = diffsRowSource(context(TURN), file([effect("b", 9, "delete"), effect("a", 3, "write")]));
    expect(row.openable(null)).toBe(false);
    expect(row.openable(0)).toBeUndefined();
    expect(row.change(0)).toBe("changed");
    expect(row.change(1)).toBe("deleted");
  });

  it("leaves a file absent at tip unopenable across all revisions", () => {
    const row = diffsRowSource(context(TURN), file(
      [effect("b", 9, "delete"), effect("a", 3, "create")],
      { tip: { state: "absent" } },
    ));
    expect(row.openable(null)).toBe(false);
    expect(row.openable(0)).toBe(false);
    expect(row.openable(1)).toBe(false);
  });

  it("asks the comparison for its line counts rather than claiming any", () => {
    const row = diffsRowSource(context(LENS), file([effect("a", 3, "write")]));
    expect(row.stat(null)).toBeNull();
    expect(row.access(null)).toBeUndefined();
  });

  it("hands a remounted row the comparison it already opened", () => {
    // Working-tree selectors never share a host view across requests.
    const client = {} as LycaonClient;
    const row = diffsRowSource({ ...context(LENS), client: () => client }, file([effect("a", 3, "write")]));
    const first = row.access(null);
    expect(first).toBeDefined();
    expect(row.access(null)).toBe(first);
    expect(row.access(0)).not.toBe(first);
    expect(diffsRowSource({ ...context(LENS), client: () => client }, file([effect("a", 3, "write")])).access(null))
      .not.toBe(first);
  });

  it("recognizes binary files and explains that their contents cannot be shown as a text diff", () => {
    const binaryDigest = {
      in_range: true,
      changes_rows: 0,
      summary: {
        added: 0,
        removed: 0,
        rows: 0,
        change_areas: [],
        change_area_count: 0,
        before: { path: "img.png", sha256: "b1", lines: 0, availability: "binary" },
        after: { path: "img.png", sha256: "b2", lines: 0, availability: "binary" },
      },
    };
    const row = diffsRowSource(context(TURN), file([effect("a", 3, "write")]), () => binaryDigest);
    expect(row.isBinary?.(null)).toBe(true);
    expect(row.isNoop(null)).toBe(false);
    expect(row.noopNote).toBe("This file is binary and cannot be shown as a text diff.");
  });

  it("recognizes binary Git review files and provides the binary note", () => {
    const gitAddress: GitDiffsAddress = {
      kind: "git", rootId: "r1", spec: "c1..c2", beforeCommit: "c1", afterCommit: "c2", label: "c1..c2",
    };
    const gitRow = gitDiffsRowSource(
      context(gitAddress),
      gitAddress,
      {
        path: "logo.png", before_path: "logo.png", op: "write",
        before_mode: "100644", after_mode: "100644",
        before_oid: "1".repeat(40), after_oid: "2".repeat(40),
        insertions: 0, deletions: 0, binary: true,
      },
    );
    expect(gitRow.isBinary?.(null)).toBe(true);
    expect(gitRow.noopNote).toBe("This file is binary and cannot be shown as a text diff.");
  });
});
