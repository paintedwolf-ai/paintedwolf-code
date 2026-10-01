import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { commitChanges, landWorktree } from "./git-actions.ts";
import { gitStatusReads, gitWorkspaceStatus } from "../../test/git-status-fixture.ts";

const status = gitWorkspaceStatus({ repo_id: "r1", root_ids: ["root-1"] });

function appStoreStub(): AppStore {
  return {
    state: {
      gitRepos: [],
      gitActiveRepoId: "",
      gitScopePin: null,
      gitStatus: undefined,
    },
    actions: {
      setGitRepos: vi.fn(),
      setGitStatus: vi.fn(),
    },
  } as unknown as AppStore;
}

describe("landWorktree", () => {
  it("normalizes worktree_land_blocked into a landed:false outcome and refreshes", async () => {
    const landGitWorktree = vi.fn(async () => {
      throw new LycaonApiError("blocked", 409, "worktree_land_blocked", {
        details: {
          reason: "base_dirty",
          base_branch: "main",
          landed: false,
          commits: 0,
          conflicts: [],
        },
      });
    });
    const listGitRepos = vi.fn(async () => ({
      repos: [],
      active_repo_id: "",
    }));
    const client = stubClient({
      landGitWorktree,
      listGitRepos,
      ...gitStatusReads(() => status),
    });

    const outcome = await landWorktree(appStoreStub(), client, "p1", "s1");
    expect(outcome).toEqual({
      landed: false,
      base_branch: "main",
      commits: 0,
      reason: "base_dirty",
      conflicts: [],
    });
    await Promise.resolve();
    expect(listGitRepos).toHaveBeenCalled();
  });

  it("returns a successful land result", async () => {
    const client = stubClient({
      landGitWorktree: vi.fn(async () => ({
        landed: true,
        base_branch: "main",
        commits: 3,
        reason: "",
        conflicts: [],
      })),
      listGitRepos: vi.fn(async () => ({ repos: [], active_repo_id: "" })),
      ...gitStatusReads(() => status),
    });

    const outcome = await landWorktree(appStoreStub(), client, "p1", "s1");
    expect(outcome.landed).toBe(true);
    expect(outcome.commits).toBe(3);
  });
});

describe("commitChanges", () => {
  it("commits in the session checkout when a session is active", async () => {
    const commitGit = vi.fn(async () => ({ repo_id: "r1", revision: 1, refreshing: false }));
    const client = stubClient({
      commitGit,
      listGitRepos: vi.fn(async () => ({ repos: [], active_repo_id: "" })),
      ...gitStatusReads(() => status),
    });

    await commitChanges(appStoreStub(), client, "p1", "r1", "feat: isolated", "s1");

    expect(commitGit).toHaveBeenCalledWith("p1", "r1", { message: "feat: isolated" }, "s1");
  });
});
