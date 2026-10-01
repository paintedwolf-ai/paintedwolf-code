import { describe, expect, it } from "vitest";
import type { GitRepoEntry } from "../api/types.ts";
import {
  gitReposChangeCount,
  pinMatchesSet,
  resolveScope,
} from "./git-repo-scope.ts";

function repo(
  overrides: Partial<GitRepoEntry> & Pick<GitRepoEntry, "repo_id" | "label">,
): GitRepoEntry {
  return {
    available: true,
    root_ids: ["r1"],
    ahead: 0,
    behind: 0,
    dirty: false,
    staged_count: 0,
    unstaged_count: 0,
    changed_count: 0,
    ...overrides,
  };
}

const alpha = repo({
  repo_id: "ra",
  label: "alpha",
  branch: "main",
  staged_count: 1,
  unstaged_count: 2,
  dirty: true,
});
const beta = repo({
  repo_id: "rb",
  label: "beta",
  branch: "feat",
  root_ids: ["r2"],
  staged_count: 4,
  dirty: true,
});
const bare: GitRepoEntry = {
  repo_id: "",
  label: "scratch",
  root_ids: ["root-x"],
  available: false,
  ahead: 0,
  behind: 0,
  dirty: false,
  staged_count: 0,
  unstaged_count: 0,
  changed_count: 0,
};

describe("resolveScope", () => {
  it("defaults to the active repository", () => {
    expect(resolveScope([alpha, beta], "rb", null)).toEqual({
      kind: "repo",
      repoId: "rb",
    });
  });

  it("falls back to the first available when active is missing", () => {
    expect(resolveScope([alpha, beta], "", null)).toEqual({
      kind: "repo",
      repoId: "ra",
    });
  });

  it("returns all when the set is empty", () => {
    expect(resolveScope([], "", null)).toEqual({ kind: "all" });
  });

  it("returns a non-repo root when nothing is available", () => {
    expect(resolveScope([bare], "", null)).toEqual({
      kind: "root",
      rootId: "root-x",
    });
  });

  it("lets a pin win over the active repository", () => {
    expect(
      resolveScope([alpha, beta], "ra", { kind: "repo", repoId: "rb" }),
    ).toEqual({ kind: "repo", repoId: "rb" });
  });

  it("drops a stale pin silently", () => {
    expect(
      resolveScope([alpha, beta], "ra", { kind: "repo", repoId: "gone" }),
    ).toEqual({ kind: "repo", repoId: "ra" });
  });

  it("keeps an all pin", () => {
    expect(resolveScope([alpha, beta], "ra", { kind: "all" })).toEqual({
      kind: "all",
    });
  });
});

describe("pinMatchesSet", () => {
  it("matches non-repo roots by root id", () => {
    expect(
      pinMatchesSet([bare], { kind: "root", rootId: "root-x" }),
    ).toBe(true);
    expect(
      pinMatchesSet([bare], { kind: "root", rootId: "other" }),
    ).toBe(false);
  });
});

describe("gitReposChangeCount", () => {
  it("sums available repositories only", () => {
    expect(gitReposChangeCount([alpha, beta, bare])).toBe(7);
  });
});
