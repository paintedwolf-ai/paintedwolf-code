import { createSignal, type Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import { projectIdForPath } from "../../store/app-state.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { resolveScope, type GitScope } from "../git-repo-scope.ts";
import type { ChatTabGitBinding } from "../../chat/composer/chat-tab-rail-bindings.ts";
import {
  bindWorktree,
  checkoutBranch,
  commitChanges,
  discardAll,
  draftCommitMessage,
  initRepo,
  landWorktree,
  listBranches,
  pullChanges,
  pushChanges,
  refreshGitStatus,
  refreshWorktree,
  stashChanges,
  unbindWorktree,
} from "../../chat/actions/git-actions.ts";

type ChatGitOptions = {
  appStore: Accessor<AppStore>;
  projects: Accessor<ReturnType<typeof createProjectsStore>>;
  projectDir: Accessor<string>;
  projectId: Accessor<string>;
  sessionId: Accessor<string>;
  clientOrThrow: () => LycaonClient;
  chatNotices: () => { reportError: (error: unknown) => unknown };
  sendWithStreamFollow: (payload: { text: string }) => unknown;
};

function groupCommitPrompt(rootPath: string): string {
  const path = rootPath.trim();
  if (!path) {
    return "please check in all uncommitted work locally in logical groups";
  }
  return `please check in all uncommitted work locally in logical groups under ${path}`;
}

export function createChatGitBinding(options: ChatGitOptions) {
  const { appStore, projects, projectDir, projectId, sessionId,
    clientOrThrow, chatNotices, sendWithStreamFollow } = options;
  const gitProjectId = () =>
    projectIdForPath(projects().state.projects, projectDir()) ??
    appStore().state.currentSession?.project_id ??
    "";

  const gitSessionId = () => sessionId()?.trim() || undefined;

  const gitResolvedScope = (): GitScope =>
    resolveScope(
      appStore().state.gitRepos ?? [],
      appStore().state.gitActiveRepoId ?? "",
      appStore().state.gitScopePin ?? null,
    );

  const gitScopedRepoId = (): string | undefined => {
    const scope = gitResolvedScope();
    return scope.kind === "repo" ? scope.repoId : undefined;
  };

  const gitScopedRootPath = (): string => {
    const scope = gitResolvedScope();
    if (scope.kind !== "repo") return "";
    const entry = (appStore().state.gitRepos ?? []).find(
      (r) => r.available && r.repo_id === scope.repoId,
    );
    const rootId = entry?.root_ids[0]?.trim() ?? "";
    if (!rootId) return "";
    const roots =
      (
        projects().byId(projectId()) ??
        projects().state.projects.find((p) => p.id === projectId())
      )?.roots ?? [];
    return roots.find((r) => r.id === rootId)?.path?.trim() ?? "";
  };

  const [gitBusy, setGitBusy] = createSignal(false);
  const runGitAction = async (action: (projectId: string) => Promise<unknown>): Promise<boolean> => {
    const client = getLycaonClient();
    const projectId = gitProjectId();
    if (!client || !projectId || gitBusy()) return false;
    const notices = chatNotices();
    setGitBusy(true);
    try {
      await action(projectId);
      return true;
    } catch (err) {
      notices.reportError(err);
      return false;
    } finally {
      setGitBusy(false);
    }
  };

  const binding = (): ChatTabGitBinding => ({
  projectId: projectId(),
  rootRefs:
    (
      projects().byId(projectId()) ??
      projects().state.projects.find(
        (p) => p.id === projectId(),
      )
    )?.roots ?? [],
  busy: gitBusy,
  onRefreshRepos: () =>
    void runGitAction((pid) =>
      refreshGitStatus(
        appStore(),
        clientOrThrow(),
        pid,
        gitSessionId(),
      ),
    ),
  onScopePin: (pin) => {
    appStore().actions.setGitScopePin(pin);
  },
  onSelectRepo: (repoId) =>
    void runGitAction((pid) =>
      refreshGitStatus(
        appStore(),
        clientOrThrow(),
        pid,
        gitSessionId(),
        { repoId, refreshSet: false },
      ),
    ),
  onInit: (rootId) => {
    if (!rootId.trim()) return;
    void runGitAction((pid) =>
      initRepo(
        appStore(),
        clientOrThrow(),
        pid,
        rootId,
        gitSessionId(),
      ),
    );
  },
  onCommit: async (message) => {
    const repoId = gitScopedRepoId();
    if (!repoId) return false;
    return runGitAction((pid) =>
      commitChanges(
        appStore(),
        clientOrThrow(),
        pid,
        repoId,
        message,
        gitSessionId(),
      ),
    );
  },
  onStash: () => {
    const repoId = gitScopedRepoId();
    if (!repoId) return;
    void runGitAction((pid) =>
      stashChanges(
        appStore(),
        clientOrThrow(),
        pid,
        repoId,
        gitSessionId(),
      ),
    );
  },
  onDraftMessage: async () => {
    const projectId = gitProjectId();
    const repoId = gitScopedRepoId();
    if (!projectId || !repoId) return "";
    try {
      return await draftCommitMessage(
        clientOrThrow(),
        projectId,
        repoId,
        gitSessionId(),
      );
    } catch (err) {
      chatNotices().reportError(err);
      return "";
    }
  },
  onListBranches: async () => {
    const projectId = gitProjectId();
    const repoId = gitScopedRepoId();
    if (!projectId || !repoId) return [];
    return listBranches(clientOrThrow(), projectId, repoId, gitSessionId());
  },
  onCheckout: async (branch, create) => {
    const projectId = gitProjectId();
    const repoId = gitScopedRepoId();
    if (!projectId || !repoId) throw new Error("Choose a repository first.");
    if (gitBusy()) throw new Error("A Git action is already running.");
    setGitBusy(true);
    try {
      await checkoutBranch(
        appStore(), clientOrThrow(), projectId, repoId,
        branch, create, gitSessionId(),
      );
    } finally {
      setGitBusy(false);
    }
  },
  onDiscard: () => {
    const repoId = gitScopedRepoId();
    if (!repoId) return;
    void runGitAction((pid) =>
      discardAll(
        appStore(),
        clientOrThrow(),
        pid,
        repoId,
        gitSessionId(),
      ),
    );
  },
  onPush: () => {
    const repoId = gitScopedRepoId();
    if (!repoId) return;
    void runGitAction((pid) =>
      pushChanges(
        appStore(),
        clientOrThrow(),
        pid,
        repoId,
        gitSessionId(),
      ),
    );
  },
  onPull: () => {
    const repoId = gitScopedRepoId();
    if (!repoId) return;
    void runGitAction((pid) =>
      pullChanges(
        appStore(),
        clientOrThrow(),
        pid,
        repoId,
        gitSessionId(),
      ),
    );
  },
  onGroupCommit: () => {
    if (gitResolvedScope().kind !== "repo" && (appStore().state.gitRepos?.length ?? 0) > 1) {
      return;
    }
    void sendWithStreamFollow({
      text: groupCommitPrompt(gitScopedRootPath()),
    });
  },
  onRefreshWorktree: async () => {
    const projectId = gitProjectId();
    const sessionId = gitSessionId();
    if (!projectId || !sessionId) return undefined;
    return refreshWorktree(
      clientOrThrow(),
      projectId,
      sessionId,
    );
  },
  onBindWorktree: async (repoId, branch) => {
    const projectId = gitProjectId();
    const sessionId = gitSessionId();
    if (!projectId || !sessionId) {
      throw new Error("project and session required");
    }
    if (gitBusy()) throw new Error("git busy");
    setGitBusy(true);
    try {
      return await bindWorktree(
        appStore(),
        clientOrThrow(),
        projectId,
        sessionId,
        repoId,
        branch,
      );
    } finally {
      setGitBusy(false);
    }
  },
  onLandWorktree: async () => {
    const projectId = gitProjectId();
    const sessionId = gitSessionId();
    if (!projectId || !sessionId) {
      throw new Error("project and session required");
    }
    if (gitBusy()) throw new Error("git busy");
    setGitBusy(true);
    try {
      return await landWorktree(
        appStore(),
        clientOrThrow(),
        projectId,
        sessionId,
      );
    } finally {
      setGitBusy(false);
    }
  },
  onUnbindWorktree: async () => {
    const projectId = gitProjectId();
    const sessionId = gitSessionId();
    if (!projectId || !sessionId) {
      throw new Error("project and session required");
    }
    if (gitBusy()) throw new Error("git busy");
    setGitBusy(true);
    try {
      return await unbindWorktree(
        appStore(),
        clientOrThrow(),
        projectId,
        sessionId,
      );
    } finally {
      setGitBusy(false);
    }
  },
});
  return binding;
}
