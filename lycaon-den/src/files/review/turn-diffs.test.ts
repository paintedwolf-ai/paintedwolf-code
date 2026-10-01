import { afterEach, describe, expect, it, vi } from "vitest";
import type { SourceWalkResponse } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { walkEffectFixture } from "../walk/walk-fixtures.ts";
import { loadTurnDiffs, resetTurnDiffsForTests, turnDiffsBaseline } from "./turn-diffs.ts";
import type { TurnDiffsAddress } from "./diffs-address.ts";

const TARGET: TurnDiffsAddress = { kind: "turn", sessionId: "s1", turn: 4, messageId: "s1-user-4" };

function page(files: SourceWalkResponse["files"], next?: string): SourceWalkResponse {
  return { baseline: "turn:s1,4", files, turns: [], commands: [], git_changes: [], commit_available: true, next_cursor: next };
}

function fileFixture(path: string, ordinals: number[]): SourceWalkResponse["files"][number] {
  return {
    file_id: `file-${path}`, root_id: "r1", path,
    changed_since_presented: false, unpresented_agent_effects: 0,
    tip: { state: "content", sha256: "tip" }, head_match: "unknown",
    effects: ordinals.map((ordinal) => ({ ...walkEffectFixture(`${path}-${ordinal}`, 4, ordinal), file_id: `file-${path}`, path })),
  };
}

afterEach(() => resetTurnDiffsForTests());

describe("the turn's changed files", () => {
  it("asks for the same baseline the review lens resolves for a turn", () => {
    expect(turnDiffsBaseline(TARGET)).toBe("turn:s1,4");
  });

  it("reads every page and groups a file's writes across them", async () => {
    const pages = [
      page([fileFixture("a.go", [9])], "9"),
      page([fileFixture("a.go", [3]), fileFixture("b.go", [2])]),
    ];
    let at = 0;
    const list = vi.fn(async (_projectId: string, _opts?: { baseline?: string; cursor?: string; sessionId?: string; markUserEdits?: boolean }) => pages[at++]!);
    const client = stubClient({ listProjectSourceWalk: list });

    const diffs = await loadTurnDiffs(client, "p1", TARGET, true);

    expect(list).toHaveBeenCalledTimes(2);
    expect(list.mock.calls[0]![1]).toMatchObject({ baseline: "turn:s1,4", sessionId: "s1", markUserEdits: true });
    expect(list.mock.calls[1]![1]).toMatchObject({ cursor: "9" });
    expect(diffs.files.map((file) => file.path)).toEqual(["a.go", "b.go"]);
    expect(diffs.files[0]!.effects).toHaveLength(2);
    expect(diffs.truncated).toBe(false);
  });

  it("shares one read between the tab and its reopens, and drops it when source moves", async () => {
    const list = vi.fn(async () => page([fileFixture("a.go", [3])]));
    const client = stubClient({ listProjectSourceWalk: list });

    await loadTurnDiffs(client, "p1", TARGET, true);
    await loadTurnDiffs(client, "p1", TARGET, true);
    expect(list).toHaveBeenCalledTimes(1);

    const { invalidateTurnDiffs } = await import("./turn-diffs.ts");
    invalidateTurnDiffs("p1");
    await loadTurnDiffs(client, "p1", TARGET, true);
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("marking the person's own writes selects a different read", async () => {
    const list = vi.fn(async () => page([fileFixture("a.go", [3])]));
    const client = stubClient({ listProjectSourceWalk: list });
    await loadTurnDiffs(client, "p1", TARGET, true);
    await loadTurnDiffs(client, "p1", TARGET, false);
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("refuses a cursor that would not advance rather than paging forever", async () => {
    const list = vi.fn(async () => page([fileFixture("a.go", [9])], "9"));
    const client = stubClient({ listProjectSourceWalk: list });
    await expect(loadTurnDiffs(client, "p1", TARGET, true)).rejects.toThrow(/completely/);
  });
});
