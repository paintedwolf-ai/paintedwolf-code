import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { GitStrip } from "./GitStrip.tsx";
import { CHAT_COL_MIN } from "../shell/stage-placement.ts";
import { loaded } from "../store/load-state.ts";
import type { GitBranchEntry, GitRepoEntry } from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";

const noop = () => {};
const noDraft = () => Promise.resolve("");
const noBranches = (): Promise<GitBranchEntry[]> => Promise.resolve([]);

const baseProps = {
  onInit: noop as (rootId: string) => void,
  onCommit: async () => true,
  onStash: noop,
  onDraftMessage: noDraft,
  onListBranches: noBranches,
  onCheckout: async () => {},
  onDiscard: noop,
  onPush: noop,
  onPull: noop,
  onScopePin: noop as (pin: import("./git-repo-scope.ts").GitScope | null) => void,
  onSelectRepo: noop as (repoId: string) => void,
};

const dirtyStatus: GitWorkspaceStatus = {
  available: true,
  repo_id: "ra",
  root_ids: ["root-1"],
  branch: "main",
  head_short: "abc12345",
  ahead: 0,
  behind: 0,
  dirty: true,
  staged_count: 1,
  unstaged_count: 1,
  changed_count: 2,
  files: [
    {
      path: "src/api/auth.go",
      status: " M",
      root_id: "root-1",
      root_relative_path: "src/api/auth.go",
    },
  ],
};

const repoAlpha: GitRepoEntry = {
  repo_id: "ra",
  label: "alpha",
  available: true,
  root_ids: ["root-1"],
  branch: "main",
  ahead: 0,
  behind: 0,
  dirty: true,
  staged_count: 1,
  unstaged_count: 1,
  changed_count: 2,
};

const repoBeta: GitRepoEntry = {
  ...repoAlpha,
  repo_id: "rb",
  label: "beta",
  root_ids: ["root-2"],
};

describe("GitStrip narrow chat compose layout", () => {
  it("renders both AI actions inside the wrapping compose footer at the minimum chat column width", () => {
    const onGroupCommit = vi.fn();
    const view = render(() => (
      <div style={{ width: `${CHAT_COL_MIN}px` }}>
        <GitStrip
          {...baseProps}
          status={loaded(dirtyStatus)}
          repos={[repoAlpha, repoBeta]}
          activeRepoId="ra"
          onGroupCommit={onGroupCommit}
        />
      </div>
    ));

    const foot = view.container.querySelector(".git-strip__compose-foot");
    const aiGroup = view.container.querySelector(".git-strip__ai-group");
    expect(foot).toBeTruthy();
    expect(aiGroup).toBeTruthy();
    expect(foot?.contains(aiGroup!)).toBe(true);
    expect(aiGroup?.querySelectorAll(".git-strip__ai")).toHaveLength(2);
    expect(screen.getByTestId("git-ai").textContent).toBe("✨ AI summary");
    expect(screen.getByTestId("git-group-commit").textContent).toBe(
      "✨ AI auto-commit",
    );
    expect(screen.getByTestId("git-commit").textContent).toBe("Stage & commit");

    fireEvent.click(screen.getByTestId("git-group-commit"));
    expect(onGroupCommit).toHaveBeenCalledOnce();
    view.unmount();
  });

  it("renders a single AI action inside the compose footer when grouped commit is omitted", () => {
    const view = render(() => (
      <div style={{ width: `${CHAT_COL_MIN}px` }}>
        <GitStrip {...baseProps} status={loaded(dirtyStatus)} />
      </div>
    ));

    const aiGroup = view.container.querySelector(".git-strip__ai-group");
    expect(aiGroup).toBeTruthy();
    expect(aiGroup?.querySelectorAll(".git-strip__ai")).toHaveLength(1);
    expect(screen.getByTestId("git-ai").textContent).toBe("✨ AI summary");
    expect(screen.queryByTestId("git-group-commit")).toBeNull();
    expect(screen.getByTestId("git-commit").textContent).toBe("Stage & commit");
    view.unmount();
  });
});
