import { produce } from "solid-js/store";
import { loadFailed, loaded } from "./load-state.ts";
import type { SetStoreFunction } from "solid-js/store";
import type { AppState, AppStoreActions } from "./app-state-model.ts";

export function createGitCacheActions(setState: SetStoreFunction<AppState>): Pick<AppStoreActions,
  "setGitStatus" | "setGitStatusLoadFailed" | "setGitRepos" | "setGitScopePin" | "clearGitScopePin"> {
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
  };
}
