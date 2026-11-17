import { afterEach, describe, expect, it } from "vitest";
import type { SourceCommandWindow } from "../../api/types.ts";
import { commandWindowCoverageNotices, commandWindowHasGaps } from "./source-command-window.ts";
import { recordOutsideRoot, resetOutsideRootsForTests } from "./outside-namespace.ts";

const window = (coverage: SourceCommandWindow["coverage"]): SourceCommandWindow => ({
  id: "w1",
  session_id: "s1",
  turn: 1,
  tool_name: "command",
  command_line: "make install",
  state: "ended",
  ordinal: 3,
  started_at: "2026-11-16T10:00:00Z",
  coverage,
});

describe("command window coverage", () => {
  afterEach(resetOutsideRootsForTests);

  it("says nothing for folders compared in full", () => {
    const complete = window([{ root_id: "r1", path: "", coverage: "complete" }]);
    expect(commandWindowCoverageNotices(complete)).toEqual([]);
    expect(commandWindowHasGaps(complete)).toBe(false);
  });

  it("names a folder that was not observed and why, never as no changes", () => {
    recordOutsideRoot("p1", "outside");
    const notices = commandWindowCoverageNotices(window([
      { root_id: "outside", path: "Users/me/shared", coverage: "not_observed", reason: "time_budget" },
      { root_id: "outside", path: "", coverage: "not_observed", reason: "unconfined" },
    ]));
    expect(notices.map((notice) => notice.text)).toEqual([
      "/Users/me/shared was not observed: the observation ran out of time.",
      "Everything outside the project was not observed: the command ran without confinement, so it could have changed files anywhere.",
    ]);
    expect(notices.every((notice) => notice.skipped.length === 0)).toBe(true);
  });

  it("lists what a partial observation skipped and why", () => {
    const [notice] = commandWindowCoverageNotices(window([{
      root_id: "r1",
      path: "",
      coverage: "partial",
      boundaries: [{ path: "node_modules", reason: "scope" }, { path: "logs", reason: "walk_budget" }],
      omitted_boundaries: 2,
    }]), () => "app");
    expect(notice!.text).toBe("Not every folder in app was compared. Changes in these folders are unknown:");
    expect(notice!.skipped).toEqual([
      { path: "node_modules", reason: "outside the source scope" },
      { path: "logs", reason: "the entry limit was reached" },
    ]);
    expect(notice!.more).toBe(2);
  });
});
