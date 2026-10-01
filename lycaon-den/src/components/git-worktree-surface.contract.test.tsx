import { readFileSync } from "node:fs";
import { loaded } from "../store/load-state.ts";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import {
  GitStrip,
} from "./GitStrip.tsx";
import type { GitScope } from "./git-repo-scope.ts";
import type {
  GitBranchEntry,
  GitRepoEntry,
  GitWorktreeView,
} from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(here, "..", "..", "..");

const sessionId = "11111111-1111-4111-8111-111111111111";

const boundWorktree: GitWorktreeView = {
  bound: true,
  session_id: sessionId,
  repo_id: "rtest",
  label: "app",
  branch: `session/${sessionId}`,
  base_branch: "main",
  state: "ready",
  dirty: false,
  ahead_of_base: 2,
  behind_base: 0,
};

const dirty: GitWorkspaceStatus = {
  available: true,
  repo_id: "rtest",
  root_ids: ["root-1"],
  branch: "main",
  head_short: "abc12345",
  ahead: 0,
  behind: 0,
  dirty: true,
  staged_count: 1,
  unstaged_count: 1,
  changed_count: 2,
  files: [],
};

const noop = () => {};
const noDraft = () => Promise.resolve("");
const noBranches = (): Promise<GitBranchEntry[]> => Promise.resolve([]);
const onInit = (_rootId: string) => {};
const onScopePin = (_pin: GitScope | null) => {};
const onSelectRepo = (_repoId: string) => {};

function repo(
  overrides: Partial<GitRepoEntry> & Pick<GitRepoEntry, "repo_id" | "label">,
): GitRepoEntry {
  return {
    available: true,
    root_ids: ["root-1"],
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
  branch: "develop",
  staged_count: 0,
  unstaged_count: 1,
  dirty: true,
});

describe("git worktree surface contract", () => {
  it("pin-free client — no worktree key in app-state surfaces", () => {
    const paths = [
      "lycaon-den/shared/app-state-types.ts",
      "lycaon-den/src-tauri/src/lib.rs",
      "lycaon-den/src/platform/persistence/app-state-parse.ts",
    ];
    for (const rel of paths) {
      const body = readFileSync(join(repoRoot, rel), "utf8");
      expect(body.toLowerCase()).not.toMatch(/worktree/);
    }
  });

  it("aggregate has no worktree control — present or disabled", async () => {
    render(() => (
      <GitStrip
        onInit={onInit}
        onCommit={async () => true}
        onStash={noop}
        onDraftMessage={noDraft}
        onListBranches={noBranches}
        onCheckout={async () => {}}
        onDiscard={noop}
        onPush={noop}
        onPull={noop}
        onGroupCommit={noop}
        onScopePin={onScopePin}
        onSelectRepo={onSelectRepo}
        status={loaded(dirty)}
        repos={[alpha, beta]}
        scopePin={{ kind: "all" }}
        sessionId={sessionId}
        onRefreshWorktree={async () => boundWorktree}
        onBindWorktree={async () => boundWorktree}
        onLandWorktree={async () => ({
          landed: true,
          base_branch: "main",
          commits: 1,
          reason: "",
          conflicts: [],
        })}
        onUnbindWorktree={async () => {}}
      />
    ));
    await waitFor(() => screen.getByTestId("git-repos"));
    for (const id of [
      "git-worktree",
      "git-worktree-bind-open",
      "git-worktree-land",
      "git-worktree-unbind",
    ]) {
      expect(screen.queryByTestId(id)).toBeNull();
    }
  });

});
