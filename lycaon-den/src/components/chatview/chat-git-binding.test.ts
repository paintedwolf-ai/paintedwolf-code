import { afterEach, describe, expect, it, vi } from "vitest";
import { createRoot } from "solid-js";
import { valueOf } from "../../store/load-state.ts";
import { createChatGitBinding } from "./chat-git-binding.ts";
import { createAppStore } from "../../store/app-state.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { GitMutationResult, GitRepoEntry, GitStatusSummary, Project } from "../../api/types.ts";
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
});
