import { describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import { gitStatusReads, gitWorkspaceStatus } from "../../test/git-status-fixture.ts";
import { observeWorkspaceInvalidation } from "../../files/source/workspace-invalidation.ts";
import { createAppStore } from "../../store/app-state.ts";
import { type AppStore } from "../../store/app-state-model.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { checkoutBranch, commitChanges, discardAll, initRepo, landWorktree, pullChanges, pushChanges, stashChanges } from "./git-actions.ts";

const mutations: Record<string, (store: AppStore, client: LycaonClient) => Promise<unknown>> = {
  checkout: (s, c) => checkoutBranch(s, c, "project", "repo", "branch", false, "chat"),
  commit: (s, c) => commitChanges(s, c, "project", "repo", "message", "chat"),
  discard: (s, c) => discardAll(s, c, "project", "repo", "chat"),
  initialize: (s, c) => initRepo(s, c, "project", "root", "chat"),
  merge: (s, c) => landWorktree(s, c, "project", "chat"),
  pull: (s, c) => pullChanges(s, c, "project", "repo", "chat"),
  push: (s, c) => pushChanges(s, c, "project", "repo", "chat"),
  stash: (s, c) => stashChanges(s, c, "project", "repo", "chat"),
};

describe("Git mutation workspace projections", () => {
  for (const [name, mutate] of Object.entries(mutations)) {
    it(`${name} refreshes every resident view of the project, even after navigation`, async () => {
      const store = createAppStore();
      const status = gitWorkspaceStatus({ repo_id: "repo", root_ids: ["root"] });
      let finish!: () => void;
      const gate = new Promise<void>((resolve) => { finish = resolve; });
      const result = async () => { await gate; return { repo_id: "repo", revision: 1, refreshing: false }; };
      const createGitRepo = async () => {
        await gate;
        return { repo_id: "repo", label: "repo", root_ids: ["root"], available: true, ahead: 0, behind: 0,
          dirty: false, staged_count: 0, unstaged_count: 0, changed_count: 0 };
      };
      const client = stubClient({ checkoutGit: result, commitGit: result, discardGit: result, createGitRepo,
        pullGit: result, pushGit: result, stashGit: result, ...gitStatusReads(() => status),
        listGitRepos: async () => ({ repos: [], active_repo_id: "repo" }),
        landGitWorktree: async () => { await gate; return { landed: true, base_branch: "main", commits: 1, reason: "", conflicts: [] }; },
      });
      const current = vi.fn(), sibling = vi.fn(), otherProject = vi.fn(), otherClient = vi.fn();
      const cleanups = [
        observeWorkspaceInvalidation(client, "project", () => "chat", current),
        observeWorkspaceInvalidation(client, "project", () => "sibling", sibling),
        observeWorkspaceInvalidation(client, "other", () => "chat", otherProject),
        observeWorkspaceInvalidation(stubClient({}), "project", () => "chat", otherClient),
      ];
      try {
        const pending = mutate(store, client);
        expect(current).not.toHaveBeenCalled();
        store.actions.setGitScopePin({ kind: "repo", repoId: "elsewhere" });
        finish();
        await pending;
        expect(current).toHaveBeenCalledOnce();
        expect(sibling).toHaveBeenCalledOnce();
        expect(otherProject).not.toHaveBeenCalled();
        expect(otherClient).not.toHaveBeenCalled();
      } finally {
        cleanups.forEach((cleanup) => cleanup());
      }
    });
  }
});
