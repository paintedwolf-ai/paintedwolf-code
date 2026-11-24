import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { stubClient } from "../../test/client-fixture.ts";
import { gitWorkspaceStatus } from "../../test/git-status-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { loaded, valueOf } from "../../store/load-state.ts";
import { refreshGitStatus } from "./git-status-reads.ts";
import {
  checkoutBranch,
  commitChanges,
  discardAll,
  draftCommitMessage,
  getCheckoutDiff,
  listBranches,
  listStashes,
  listWorktrees,
  pullChanges,
  pushChanges,
  stashChanges,
} from "./git-actions.ts";
import type { GitStatusSummary } from "../../api/types.ts";
import { loadGitWorkspaceStatus, type GitWorkspaceStatus } from "./git-workspace-status.ts";
import { GitStrip } from "../../components/GitStrip.tsx";

describe("git checkout targets", () => {
  it("scopes status and changes reads to the selected checkout and drops late responses from superseded checkouts", async () => {
    const store = createAppStore();
    store.actions.setGitWorktreeTarget("proj-1", "repo-1", "wt-a");

    let finishOldStatus!: (summary: GitStatusSummary) => void;
    const oldStatusPromise = new Promise<GitStatusSummary>((resolve) => {
      finishOldStatus = resolve;
    });

    const getGitStatus = vi.fn((_p: string, _r: string, _s?: string, worktreeId?: string) => {
      if (worktreeId === "wt-a") return oldStatusPromise;
      return Promise.resolve({
        available: true,
        repo_id: "repo-1",
        worktree_id: worktreeId,
        root_ids: ["root-1"],
        revision: 2,
        refreshing: false,
        branch: "feat/b",
        head_short: "bbbbbbbb",
        ahead: 0,
        behind: 0,
        dirty: true,
        staged_count: 0,
        unstaged_count: 1,
        changed_count: 1,
      });
    });

    const listGitChanges = vi.fn(async (_p: string, _r: string, query?: { worktree_id?: string; revision?: number }) => ({
      repo_id: "repo-1",
      worktree_id: query?.worktree_id,
      revision: query?.revision ?? 2,
      refreshing: false,
      files: [{
        path: query?.worktree_id === "wt-a" ? "a.txt" : "b.txt",
        status: " M",
        root_id: "root-1",
        root_relative_path: query?.worktree_id === "wt-a" ? "a.txt" : "b.txt",
      }],
    }));

    const client = stubClient({ getGitStatus, listGitChanges });

    const flightA = refreshGitStatus(store, client, "proj-1", "sess-1", { repoId: "repo-1", refreshSet: false });

    // Switch checkout target to wt-b before wt-a settles.
    store.actions.setGitWorktreeTarget("proj-1", "repo-1", "wt-b");
    const statusB = await refreshGitStatus(store, client, "proj-1", "sess-1", { repoId: "repo-1", refreshSet: false });
    expect(statusB?.worktree_id).toBe("wt-b");
    expect(statusB?.files[0]?.path).toBe("b.txt");

    // Settle wt-a late; it must not overwrite wt-b in store state.
    finishOldStatus({
      available: true,
      repo_id: "repo-1",
      worktree_id: "wt-a",
      root_ids: ["root-1"],
      revision: 1,
      refreshing: false,
      branch: "feat/a",
      head_short: "aaaaaaaa",
      ahead: 0,
      behind: 0,
      dirty: true,
      staged_count: 1,
      unstaged_count: 0,
      changed_count: 1,
    });
    await flightA;

    expect(valueOf(store.state.gitStatus)?.worktree_id).toBe("wt-b");
    expect(valueOf(store.state.gitStatus)?.branch).toBe("feat/b");
    expect(listGitChanges).toHaveBeenCalledWith("proj-1", "repo-1", expect.objectContaining({ worktree_id: "wt-b" }));
  });

  it("threads worktree_id, preview guards, and remote/ref options across git reads and mutations", async () => {
    const store = createAppStore();
    store.actions.setGitWorktreeTarget("proj-1", "repo-1", "wt-selected");

    const client = stubClient({
      listGitRepos: vi.fn(async () => ({ repos: [], active_repo_id: "repo-1" })),
      listGitWorktrees: vi.fn(async () => ({ repo_id: "repo-1", project_checkout_id: "wt-prim", worktrees: [] })),
      getGitDiff: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", mode: "staged" as const, patch: "+ok", files: [] })),
      listGitStashes: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", stashes: [{ ref: "stash@{0}", oid: "1234", subject: "wip" }] })),
      listGitBranches: vi.fn(async () => ({ worktree_id: "wt-selected", branches: [{ name: "feat/sel", current: true }] })),
      draftGitCommitMessage: vi.fn(async () => ({ worktree_id: "wt-selected", message: "feat: drafted" })),
      commitGit: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", revision: 3, refreshing: false })),
      discardGit: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", revision: 4, refreshing: false })),
      checkoutGit: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", revision: 5, refreshing: false })),
      stashGit: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", revision: 6, refreshing: false })),
      pushGit: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", revision: 7, refreshing: false })),
      pullGit: vi.fn(async () => ({ repo_id: "repo-1", worktree_id: "wt-selected", revision: 8, refreshing: false })),
      getGitStatus: vi.fn(async (_p, _r, _s, wt) => ({
        available: true,
        repo_id: "repo-1",
        worktree_id: wt,
        root_ids: ["root-1"],
        revision: 9,
        refreshing: false,
        ahead: 0,
        behind: 0,
        dirty: false,
        staged_count: 0,
        unstaged_count: 0,
        changed_count: 0,
      })),
      listGitChanges: vi.fn(async (_p, _r, q) => ({
        repo_id: "repo-1",
        worktree_id: q?.worktree_id,
        revision: q?.revision ?? 9,
        refreshing: false,
        files: [],
      })),
    });

    await listWorktrees(client, "proj-1", "repo-1", "sess-1");
    expect(client.listGitWorktrees).toHaveBeenCalledWith("proj-1", "repo-1", "sess-1");

    await getCheckoutDiff(client, "proj-1", "repo-1", { worktreeId: "wt-selected", mode: "staged" });
    expect(client.getGitDiff).toHaveBeenCalledWith("proj-1", "repo-1", expect.objectContaining({ worktree_id: "wt-selected", mode: "staged" }));

    const stashes = await listStashes(client, "proj-1", "repo-1", "sess-1", "wt-selected");
    expect(stashes).toHaveLength(1);

    await listBranches(client, "proj-1", "repo-1", "sess-1", "wt-selected");
    expect(client.listGitBranches).toHaveBeenCalledWith("proj-1", "repo-1", "sess-1", "wt-selected");

    await draftCommitMessage(client, "proj-1", "repo-1", "sess-1", "wt-selected");
    expect(client.draftGitCommitMessage).toHaveBeenCalledWith("proj-1", "repo-1", "sess-1", "wt-selected");

    await commitChanges(store, client, "proj-1", "repo-1", "feat: msg", "sess-1", undefined, { staged_only: true, expected_head: "abc" });
    expect(client.commitGit).toHaveBeenCalledWith(
      "proj-1",
      "repo-1",
      { message: "feat: msg", staged_only: true, expected_head: "abc" },
      "sess-1",
      "wt-selected",
    );

    await discardAll(store, client, "proj-1", "repo-1", "sess-1", undefined, { expectedHead: "abc", expectedRevision: 3 });
    expect(client.discardGit).toHaveBeenCalledWith("proj-1", "repo-1", "sess-1", "wt-selected", { expectedHead: "abc", expectedRevision: 3 });

    await checkoutBranch(store, client, "proj-1", "repo-1", "feat/next", false, "sess-1", undefined, "abc");
    expect(client.checkoutGit).toHaveBeenCalledWith("proj-1", "repo-1", { branch: "feat/next", create: false, expected_head: "abc" }, "sess-1", "wt-selected");

    await stashChanges(store, client, "proj-1", "repo-1", "sess-1", undefined, { action: "pop", ref: "stash@{0}" });
    expect(client.stashGit).toHaveBeenCalledWith("proj-1", "repo-1", { action: "pop", ref: "stash@{0}" }, "sess-1", "wt-selected");

    await pushChanges(store, client, "proj-1", "repo-1", "sess-1", undefined, { remote: "origin", ref: "HEAD:refs/heads/main" });
    expect(client.pushGit).toHaveBeenCalledWith("proj-1", "repo-1", "sess-1", "wt-selected", { remote: "origin", ref: "HEAD:refs/heads/main" });

    await pullChanges(store, client, "proj-1", "repo-1", "sess-1", undefined, { remote: "origin", ref: "main" });
    expect(client.pullGit).toHaveBeenCalledWith("proj-1", "repo-1", "sess-1", "wt-selected", { remote: "origin", ref: "main" });
  });

  it("closes an open discard confirmation dialog if the checkout target changes", async () => {
    const [status, setStatus] = createSignal<GitWorkspaceStatus>(
      gitWorkspaceStatus({
        repo_id: "repo-1",
        worktree_id: "wt-a",
        head_sha: "11111111",
        dirty: true,
        unstaged_count: 1,
        changed_count: 1,
        files: [{ path: "a.txt", status: " M", root_id: "root-1", root_relative_path: "a.txt" }],
      }),
    );
    const onDiscard = vi.fn();

    const view = render(() => (
      <GitStrip
        projectId="proj-1"
        sessionId="sess-1"
        status={loaded(status())}
        onScopePin={vi.fn()}
        onSelectRepo={vi.fn()}
        onInit={vi.fn()}
        onCommit={async () => true}
        onStash={vi.fn()}
        onDraftMessage={async () => ""}
        onDiscard={onDiscard}
        onPush={vi.fn()}
        onPull={vi.fn()}
        onListBranches={async () => []}
        onCheckout={async () => undefined}
      />
    ));

    fireEvent.click(view.getByTestId("git-discard"));
    expect(screen.queryByTestId("git-discard-modal")).not.toBeNull();

    // Switching checkout target closes the modal so confirming cannot discard another checkout.
    setStatus(
      gitWorkspaceStatus({
        repo_id: "repo-1",
        worktree_id: "wt-b",
        head_sha: "22222222",
        dirty: true,
        unstaged_count: 1,
        changed_count: 1,
        files: [{ path: "b.txt", status: " M", root_id: "root-1", root_relative_path: "b.txt" }],
      }),
    );

    expect(screen.queryByTestId("git-discard-modal")).toBeNull();
    expect(onDiscard).not.toHaveBeenCalled();
  });

  it("waits for lazy sibling checkout first-read refresh beyond initial primary probes", async () => {
    let calls = 0;
    const getGitStatus = vi.fn(async () => {
      calls += 1;
      if (calls < 5) {
        return {
          available: true,
          repo_id: "repo-1",
          worktree_id: "wt-cold",
          root_ids: ["root-1"],
          revision: 0,
          refreshing: true,
          ahead: 0,
          behind: 0,
          dirty: false,
          staged_count: 0,
          unstaged_count: 0,
          changed_count: 0,
        };
      }
      return {
        available: true,
        repo_id: "repo-1",
        worktree_id: "wt-cold",
        root_ids: ["root-1"],
        revision: 1,
        refreshing: false,
        branch: "feat/cold",
        ahead: 0,
        behind: 0,
        dirty: true,
        staged_count: 0,
        unstaged_count: 1,
        changed_count: 1,
      };
    });
    const listGitChanges = vi.fn(async () => ({
      repo_id: "repo-1",
      worktree_id: "wt-cold",
      revision: 1,
      refreshing: false,
      files: [{ path: "cold.ts", status: " M", root_id: "root-1", root_relative_path: "cold.ts" }],
    }));

    const status = await loadGitWorkspaceStatus({ getGitStatus, listGitChanges }, "proj-1", "repo-1", "sess-1", "wt-cold");
    expect(calls).toBe(5);
    expect(status.branch).toBe("feat/cold");
    expect(status.files[0]?.path).toBe("cold.ts");
  });
});
