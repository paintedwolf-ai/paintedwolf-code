import { describe, it, expect } from "vitest";
import { formatWorktreeLandResult } from "./git-worktree-result.ts";

describe("worktree merge outcomes", () => {
  it("renders each land reason with its locked sentence", () => {
    const branch = "session/x";
    expect(
      formatWorktreeLandResult(
        {
          landed: true,
          base_branch: "main",
          commits: 3,
          reason: "",
          conflicts: [],
        },
        branch,
      ).sentence,
    ).toBe("Merged 3 commits into main.");
    expect(
      formatWorktreeLandResult(
        {
          landed: false,
          base_branch: "main",
          commits: 0,
          reason: "nothing_to_land",
          conflicts: [],
        },
        branch,
      ).sentence,
    ).toBe("Nothing to merge — no new commits on session/x.");
    expect(
      formatWorktreeLandResult(
        {
          landed: false,
          base_branch: "main",
          commits: 0,
          reason: "base_dirty",
          conflicts: [],
        },
        branch,
      ).sentence,
    ).toMatch(/uncommitted changes/);
    expect(
      formatWorktreeLandResult(
        {
          landed: false,
          base_branch: "main",
          commits: 0,
          reason: "base_on_other_branch",
          conflicts: [],
          current: "feat",
        },
        branch,
      ).sentence,
    ).toBe(
      "Your project folder is on feat, not main. Switch back, then merge again.",
    );
    expect(
      formatWorktreeLandResult(
        {
          landed: false,
          base_branch: "main",
          commits: 0,
          reason: "base_branch_missing",
          conflicts: [],
        },
        branch,
      ).sentence,
    ).toBe("main no longer exists.");
    const conflict = formatWorktreeLandResult(
      {
        landed: false,
        base_branch: "main",
        commits: 0,
        reason: "conflict",
        conflicts: ["a.txt", "b.txt"],
      },
      branch,
    );
    expect(conflict.sentence).toMatch(/conflicts and was not applied/);
    expect(conflict.conflicts).toEqual(["a.txt", "b.txt"]);
  });
});
