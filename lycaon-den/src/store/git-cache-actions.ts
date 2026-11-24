import { produce } from "solid-js/store";
import { loadFailed, loaded } from "./load-state.ts";
import type { SetStoreFunction } from "solid-js/store";
import type { AppState, AppStoreActions } from "./app-state-model.ts";

function gitWorktreeTargetKey(projectId: string, repoId: string): string {
  return `${projectId.trim()}\u0000${repoId.trim()}`;
}

export function resolveGitWorktreeTarget(
  state: Pick<AppState, "gitWorktreeTargets">,
  projectId: string,
  repoId: string,
): string | undefined {
  const pid = projectId.trim();
  const rid = repoId.trim();
  if (!pid || !rid) return undefined;
  const target = state.gitWorktreeTargets?.[gitWorktreeTargetKey(pid, rid)]?.trim();
  return target || undefined;
}

export function createGitCacheActions(setState: SetStoreFunction<AppState>): Pick<AppStoreActions,
  | "setGitStatus"
  | "setGitStatusLoadFailed"
  | "setGitRepos"
  | "setGitScopePin"
  | "clearGitScopePin"
  | "setGitWorktreeTarget"
  | "clearGitWorktreeTargets"> {
  return {
    setGitStatus(status) {
      setState(
        produce((s) => {
          s.gitStatus = loaded(status);
        }),
      );
    },
    setGitStatusLoadFailed(err) {
      setState(
        produce((s) => {
          // A failed refresh preserves the displayed repository status.
          s.gitStatus = loadFailed(err, s.gitStatus);
        }),
      );
    },
    setGitRepos(repos, activeRepoId) {
      setState(
        produce((s) => {
          s.gitRepos = repos;
          s.gitActiveRepoId = activeRepoId;
        }),
      );
    },
    setGitScopePin(pin) {
      setState(
        produce((s) => {
          s.gitScopePin = pin;
        }),
      );
    },
    clearGitScopePin() {
      setState(
        produce((s) => {
          s.gitScopePin = null;
        }),
      );
    },
    setGitWorktreeTarget(projectId, repoId, worktreeId) {
      const key = gitWorktreeTargetKey(projectId, repoId);
      const next = worktreeId?.trim() ?? "";
      setState(
        produce((s) => {
          if (!s.gitWorktreeTargets) s.gitWorktreeTargets = {};
          const prev = s.gitWorktreeTargets[key] ?? "";
          if (prev === next) return;
          if (next) {
            s.gitWorktreeTargets[key] = next;
          } else {
            delete s.gitWorktreeTargets[key];
          }
          s.gitWorktreeEpoch = (s.gitWorktreeEpoch ?? 0) + 1;
        }),
      );
    },
    clearGitWorktreeTargets(projectId) {
      const prefix = projectId?.trim() ? `${projectId.trim()}\u0000` : "";
      setState(
        produce((s) => {
          if (!s.gitWorktreeTargets) return;
          let changed = false;
          for (const key of Object.keys(s.gitWorktreeTargets)) {
            if (!prefix || key.startsWith(prefix)) {
              delete s.gitWorktreeTargets[key];
              changed = true;
            }
          }
          if (changed) {
            s.gitWorktreeEpoch = (s.gitWorktreeEpoch ?? 0) + 1;
          }
        }),
      );
    },
  };
}
