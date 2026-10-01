import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { valueOf } from "../../store/load-state.ts";
import type { GitMutationResult } from "../../api/types.ts";
import type { GitWorkspaceStatus } from "./git-workspace-status.ts";
import { gitStatusReads, gitWorkspaceStatus } from "../../test/git-status-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { checkoutBranch, refreshGitStatus } from "./git-actions.ts";

function statusFor(repoId: string): GitWorkspaceStatus {
  return gitWorkspaceStatus({ repo_id: repoId });
}

describe("refreshGitStatus", () => {
  it("rejects an older read when a different repository becomes current", async () => {
    const store = createAppStore();
    let finish!: (status: GitWorkspaceStatus) => void;
    const response = new Promise<GitWorkspaceStatus>((resolve) => { finish = resolve; });
    const client = stubClient(gitStatusReads((repo) => repo === "old" ? response : statusFor(repo)));
    const old = refreshGitStatus(store, client, "project", undefined, { repoId: "old", refreshSet: false });
    await refreshGitStatus(store, client, "project", undefined, { repoId: "new", refreshSet: false });
    finish(statusFor("old"));
    await old;
    expect(valueOf(store.state.gitStatus)?.repo_id).toBe("new");
  });

  it("keeps mutation results and required reads without repainting another session", async () => {
    const store = createAppStore();
    const session = { id: "s1", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "p1", posture: "build" as const, status: "idle" as const, created_at: "t", activity_at: "t", updated_at: "t" };
    store.actions.setCurrentSession(session);
    let finish!: (result: GitMutationResult) => void;
    const response = new Promise<GitMutationResult>((resolve) => { finish = resolve; });
    const listGitRepos = vi.fn(async () => ({ repos: [], active_repo_id: "old" }));
    const client = stubClient({ checkoutGit: () => response, listGitRepos, ...gitStatusReads(statusFor) });
    const changing = checkoutBranch(store, client, "p1", "old", "branch", false, "s1");
    store.actions.setCurrentSession({ ...session, id: "s2" });
    store.actions.setGitStatus(statusFor("new"));
    finish({ repo_id: "old", revision: 1, refreshing: false });
    expect(await changing).toEqual(statusFor("old"));
    expect(listGitRepos).toHaveBeenCalledOnce();
    expect(valueOf(store.state.gitStatus)?.repo_id).toBe("new");
    expect(store.state.gitActiveRepoId).not.toBe("old");
  });

  it("joins identical repository and status hydration", async () => {
    const appStore = createAppStore();
    const listGitRepos = vi.fn().mockResolvedValue({
      active_repo_id: "repo-1",
      repos: [{ available: true, repo_id: "repo-1", root_ids: ["root-1"] }],
    });
    const status = gitWorkspaceStatus({ repo_id: "repo-1", root_ids: ["root-1"] });
    const reads = gitStatusReads(() => status);
    const getGitStatus = vi.fn(reads.getGitStatus);
    const client = stubClient({ listGitRepos, getGitStatus, listGitChanges: reads.listGitChanges });

    const [first, second] = await Promise.all([
      refreshGitStatus(appStore, client, "project-1", "session-1"),
      refreshGitStatus(appStore, client, "project-1", "session-1"),
    ]);

    expect(first).toEqual(status);
    expect(second).toEqual(status);
    expect(listGitRepos).toHaveBeenCalledOnce();
    expect(getGitStatus).toHaveBeenCalledOnce();
  });

  it("lets a new client supersede an old status read", async () => {
    const appStore = createAppStore();
    let resolveOldRepos!: (value: {
      active_repo_id: string;
      repos: Array<{ available: boolean; repo_id: string; root_ids: string[] }>;
    }) => void;
    const oldClient = stubClient({
      listGitRepos: vi.fn(
        () =>
          new Promise((resolve) => {
            resolveOldRepos = resolve;
          }),
      ),
      getGitStatus: vi.fn(),
    });
    const freshStatus = gitWorkspaceStatus({ repo_id: "repo-new", root_ids: ["root-new"] });
    const newClient = stubClient({
      listGitRepos: vi.fn().mockResolvedValue({
        active_repo_id: "repo-new",
        repos: [{ available: true, repo_id: "repo-new", root_ids: ["root-new"] }],
      }),
      ...gitStatusReads(() => freshStatus),
    });

    const oldRead = refreshGitStatus(
      appStore,
      oldClient,
      "project-1",
      "session-1",
    );
    await refreshGitStatus(appStore, newClient, "project-1", "session-1");
    resolveOldRepos({
      active_repo_id: "repo-old",
      repos: [{ available: true, repo_id: "repo-old", root_ids: ["root-old"] }],
    });
    await oldRead;

    expect(appStore.state.gitActiveRepoId).toBe("repo-new");
    expect(valueOf(appStore.state.gitStatus)).toEqual(freshStatus);
    expect(oldClient.getGitStatus).not.toHaveBeenCalled();
  });
});
