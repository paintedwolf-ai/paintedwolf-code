import { afterEach, describe, expect, it, vi } from "vitest";
import { createRoot } from "solid-js";
import { valueOf } from "../../store/load-state.ts";
import { createChatGitBinding } from "./chat-git-binding.ts";
import { gitStatusRefreshPending } from "../../chat/actions/git-status-reads.ts";
import { maybeRefreshGitAfterBoard } from "../../chat/actions/board-actions.ts";
import { createAppStore } from "../../store/app-state.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { BoardView, GitMutationResult, GitRepoEntry, GitReposView, GitStatusSummary, Project } from "../../api/types.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";

const REPO_ID = "repo-1";
const repo: GitRepoEntry = {
  repo_id: REPO_ID,
  label: "app",
  root_ids: ["root-1"],
  available: true,
  ahead: 1,
  behind: 0,
  dirty: true,
  staged_count: 0,
  unstaged_count: 1,
  changed_count: 1,
};

const project = { id: "project-1", roots: [{ id: "root-1", path: "/work/app" }] } as unknown as Project;
const projects = {
  state: { projects: [project] },
  byId: (id: string) => (id === project.id ? project : undefined),
} as unknown as ReturnType<typeof createProjectsStore>;

afterEach(() => setLycaonClientForTest(null));

function summaryOf(overrides: Partial<GitStatusSummary>): GitStatusSummary {
  return {
    available: true,
    repo_id: REPO_ID,
    root_ids: ["root-1"],
    upstream: "origin/main",
    ahead: 1,
    behind: 0,
    dirty: true,
    staged_count: 0,
    unstaged_count: 1,
    changed_count: 1,
    revision: 1,
    refreshing: false,
    ...overrides,
  };
}

function repoSet(overrides: Partial<GitRepoEntry>): GitReposView {
  return { repos: [{ ...repo, ...overrides }], active_repo_id: REPO_ID };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

function bindingFor(client: ReturnType<typeof stubClient>, appStore: ReturnType<typeof createAppStore>, reportError: () => void) {
  return createChatGitBinding({
    appStore: () => appStore,
    projects: () => projects,
    projectDir: () => "/work/app",
    projectId: () => project.id,
    sessionId: () => "session-1",
    clientOrThrow: () => client,
    chatNotices: () => ({ reportError }),
    sendWithStreamFollow: vi.fn(),
  })();
}

describe("chat Git binding", () => {
  it("sends a user's pull while a background status read is still in flight", async () => {
    const pullError = Object.assign(new Error("Fixture pull refusal"), { code: "git_pull_failed" });
    const client = stubClient({
      listGitRepos: vi.fn(() => new Promise<never>(() => {})),
      pullGit: vi.fn(async () => {
        throw pullError;
      }),
    });
    setLycaonClientForTest(client);
    const appStore = createAppStore();
    appStore.actions.setGitRepos([repo], REPO_ID);
    const reportError = vi.fn();

    await createRoot(async (dispose) => {
      const binding = bindingFor(client, appStore, reportError);

      void binding.onRefreshRepos();
      await vi.waitFor(() => expect(client.listGitRepos).toHaveBeenCalledTimes(1));
      expect(binding.busy()).toBe(false);

      binding.onPull();
      expect(binding.busy()).toBe(true);
      await vi.waitFor(() => expect(reportError).toHaveBeenCalledWith(pullError));
      expect(client.pullGit).toHaveBeenCalledWith(project.id, REPO_ID, "session-1");
      expect(binding.busy()).toBe(false);
      dispose();
    });
  });

  it("pulls the repository whose status is shown before the repository set loads", async () => {
    const client = stubClient({
      pullGit: vi.fn(async () => {
        throw new Error("Fixture pull refusal");
      }),
    });
    setLycaonClientForTest(client);
    const appStore = createAppStore();
    appStore.actions.setGitStatus({
      available: true,
      repo_id: REPO_ID,
      root_ids: repo.root_ids,
      upstream: "origin/main",
      ahead: 1,
      behind: 0,
      dirty: true,
      staged_count: 0,
      unstaged_count: 1,
      changed_count: 1,
      files: [],
    });
    const reportError = vi.fn();

    await createRoot(async (dispose) => {
      bindingFor(client, appStore, reportError).onPull();
      await vi.waitFor(() => expect(reportError).toHaveBeenCalledTimes(1));
      expect(client.pullGit).toHaveBeenCalledWith(project.id, REPO_ID, "session-1");
      dispose();
    });
  });

  it("keeps a push's fresh status when a background read is requested mid-write", async () => {
    let pushed = false;
    let finishPush: (result: GitMutationResult) => void = () => {};
    const summary = (ahead: number): GitStatusSummary => ({
      available: true,
      repo_id: REPO_ID,
      root_ids: ["root-1"],
      upstream: "origin/main",
      ahead,
      behind: 0,
      dirty: true,
      staged_count: 0,
      unstaged_count: 1,
      changed_count: 1,
      revision: 1,
      refreshing: false,
    });
    const client = stubClient({
      pushGit: vi.fn(() => new Promise<GitMutationResult>((resolve) => {
        finishPush = (result) => {
          pushed = true;
          resolve(result);
        };
      })),
      getGitStatus: vi.fn(async () => summary(pushed ? 0 : 1)),
      listGitChanges: vi.fn(async () => ({ repo_id: REPO_ID, revision: 1, refreshing: false, files: [] })),
      listGitRepos: vi.fn(async () => ({ repos: [{ ...repo, ahead: pushed ? 0 : 1 }], active_repo_id: REPO_ID })),
    });
    setLycaonClientForTest(client);
    const appStore = createAppStore();
    appStore.actions.setGitRepos([repo], REPO_ID);
    const reportError = vi.fn();

    await createRoot(async (dispose) => {
      const binding = bindingFor(client, appStore, reportError);
      binding.onPush();
      expect(binding.busy()).toBe(true);

      await binding.onRefreshRepos();
      finishPush({ repo_id: REPO_ID, revision: 2, refreshing: false });
      await vi.waitFor(() => expect(binding.busy()).toBe(false));

      expect(valueOf(appStore.state.gitStatus)?.ahead).toBe(0);
      expect(reportError).not.toHaveBeenCalled();
      dispose();
    });
  });

  it("loads the repository set and host status when a pull fails during the first read", async () => {
    const pullError = Object.assign(new Error("Fixture pull refusal"), { code: "git_pull_failed" });
    let pullFailed = false;
    const firstSet = deferred<GitReposView>();
    const client = stubClient({
      listGitRepos: vi.fn()
        .mockImplementationOnce(() => firstSet.promise)
        .mockImplementation(async () => repoSet({ behind: 2 })),
      pullGit: vi.fn(async () => {
        pullFailed = true;
        throw pullError;
      }),
      getGitStatus: vi.fn(async () => summaryOf({ behind: pullFailed ? 2 : 0 })),
      listGitChanges: vi.fn(async () => ({ repo_id: REPO_ID, revision: 1, refreshing: false, files: [] })),
    });
    setLycaonClientForTest(client);
    const appStore = createAppStore();
    const { revision: _revision, refreshing: _refreshing, ...shown } = summaryOf({});
    appStore.actions.setGitStatus({ ...shown, files: [] });
    const reportError = vi.fn();

    await createRoot(async (dispose) => {
      const binding = bindingFor(client, appStore, reportError);
      const firstRead = binding.onRefreshRepos();
      await vi.waitFor(() => expect(client.listGitRepos).toHaveBeenCalledTimes(1));

      binding.onPull();
      await vi.waitFor(() => expect(reportError).toHaveBeenCalledWith(pullError));
      expect(binding.busy()).toBe(false);
      firstSet.resolve(repoSet({ behind: 0 }));
      await firstRead;
      await vi.waitFor(() => expect(gitStatusRefreshPending(appStore)).toBe(false));

      expect(appStore.state.gitRepos).toEqual(repoSet({ behind: 2 }).repos);
      expect(valueOf(appStore.state.gitStatus)?.behind).toBe(2);
      expect(reportError).toHaveBeenCalledTimes(1);
      dispose();
    });
  });

  it.each([
    { settles: "the push's repository set first", writeFirst: true },
    { settles: "the first read's repository set first", writeFirst: false },
  ])("loads the pushed repository set after a board event during the first read, settling $settles", async ({ writeFirst }) => {
    let pushed = false;
    const firstSet = deferred<GitReposView>();
    const pushSet = deferred<GitReposView>();
    const client = stubClient({
      listGitRepos: vi.fn()
        .mockImplementationOnce(() => firstSet.promise)
        .mockImplementationOnce(() => pushSet.promise),
      pushGit: vi.fn(async () => {
        pushed = true;
        return { repo_id: REPO_ID, revision: 2, refreshing: false };
      }),
      getGitStatus: vi.fn(async () => summaryOf({ ahead: pushed ? 0 : 1 })),
      listGitChanges: vi.fn(async () => ({ repo_id: REPO_ID, revision: 1, refreshing: false, files: [] })),
    });
    setLycaonClientForTest(client);
    const appStore = createAppStore();
    const { revision: _revision, refreshing: _refreshing, ...shown } = summaryOf({});
    appStore.actions.setGitStatus({ ...shown, files: [] });
    const reportError = vi.fn();

    await createRoot(async (dispose) => {
      const binding = bindingFor(client, appStore, reportError);
      const firstRead = binding.onRefreshRepos();
      await vi.waitFor(() => expect(client.listGitRepos).toHaveBeenCalledTimes(1));

      binding.onPush();
      await vi.waitFor(() => expect(client.listGitRepos).toHaveBeenCalledTimes(2));
      expect(valueOf(appStore.state.gitStatus)?.ahead).toBe(0);
      // The host publishes a board snapshot after the push lands.
      appStore.actions.setBoard(undefined);
      if (writeFirst) {
        pushSet.resolve(repoSet({ ahead: 0 }));
        await vi.waitFor(() => expect(binding.busy()).toBe(false));
        firstSet.resolve(repoSet({ ahead: 1 }));
        await firstRead;
      } else {
        firstSet.resolve(repoSet({ ahead: 1 }));
        await firstRead;
        pushSet.resolve(repoSet({ ahead: 0 }));
        await vi.waitFor(() => expect(binding.busy()).toBe(false));
      }

      expect(appStore.state.gitRepos).toEqual(repoSet({ ahead: 0 }).repos);
      expect(valueOf(appStore.state.gitStatus)?.ahead).toBe(0);
      expect(reportError).not.toHaveBeenCalled();
      dispose();
    });
  });

  it("keeps a push's status when a board snapshot and its status read arrive mid-push", async () => {
    let pushed = false;
    let finishPush: (result: GitMutationResult) => void = () => {};
    const client = stubClient({
      pushGit: vi.fn(() => new Promise<GitMutationResult>((resolve) => {
        finishPush = (result) => {
          pushed = true;
          resolve(result);
        };
      })),
      getGitStatus: vi.fn(async () => {
        const ahead = pushed ? 0 : 1;
        await Promise.resolve();
        return summaryOf({ ahead, dirty: !pushed });
      }),
      listGitChanges: vi.fn(async () => ({ repo_id: REPO_ID, revision: 1, refreshing: false, files: [] })),
      listGitRepos: vi.fn(async () => repoSet({ ahead: pushed ? 0 : 1 })),
    });
    setLycaonClientForTest(client);
    const appStore = createAppStore();
    appStore.actions.setGitRepos([repo], REPO_ID);
    const { revision: _revision, refreshing: _refreshing, ...shown } = summaryOf({});
    appStore.actions.setGitStatus({ ...shown, files: [] });
    const published = vi.spyOn(appStore.actions, "setGitStatus");
    const reportError = vi.fn();
    // A pre-push pulse: the push itself leaves the tree clean.
    const board = { git: { available: true, repo_id: REPO_ID, label: "app", branch: "main", dirty: true,
      staged_count: 0, unstaged_count: 1, others: [], others_truncated: 0 } } as unknown as BoardView;

    await createRoot(async (dispose) => {
      const binding = bindingFor(client, appStore, reportError);
      binding.onPush();
      appStore.actions.setBoard(board);
      const boardRefresh = maybeRefreshGitAfterBoard(appStore, client, "/work/app", [project]);
      await new Promise((resolve) => setTimeout(resolve, 0));
      finishPush({ repo_id: REPO_ID, revision: 2, refreshing: false });
      await boardRefresh;
      await vi.waitFor(() => expect(binding.busy()).toBe(false));

      const statuses = published.mock.calls.map(([status]) => status);
      expect(statuses.length).toBeGreaterThan(0);
      expect(statuses.every((status) => status.ahead === 0 && !status.dirty)).toBe(true);
      expect(valueOf(appStore.state.gitStatus)?.ahead).toBe(0);
      expect(reportError).not.toHaveBeenCalled();
      dispose();
    });
  });
});
