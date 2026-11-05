import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import axe from "axe-core";
import { GitStrip } from "./GitStrip.tsx";
import { loadFailed, loaded } from "../store/load-state.ts";
import type {
  GitBranchEntry,
  GitRepoEntry,
  GitWorktreeView,
} from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";

const axeRunOnly = {
  type: "tag" as const,
  values: ["wcag2a", "wcag2aa"],
};

async function assertNoAxeViolations(root: HTMLElement): Promise<void> {
  const results = await axe.run(root, { runOnly: axeRunOnly });
  const summary = results.violations
    .map(
      (v) =>
        `${v.id} (${v.impact ?? "n/a"}): ${v.help} — ${v.nodes
          .map((n) => n.target.join(" "))
          .join("; ")}`,
    )
    .join("\n");
  expect(results.violations, summary || "axe violations").toEqual([]);
}

const unboundWorktree: GitWorktreeView = {
  bound: false,
  session_id: "11111111-1111-4111-8111-111111111111",
  dirty: false,
  ahead_of_base: 0,
  behind_base: 0,
};

const boundWorktree: GitWorktreeView = {
  bound: true,
  session_id: unboundWorktree.session_id,
  repo_id: "rtest",
  label: "app",
  branch: "session/11111111-1111-4111-8111-111111111111",
  base_branch: "main",
  state: "ready",
  dirty: false,
  ahead_of_base: 2,
  behind_base: 0,
};

const staleWorktree: GitWorktreeView = {
  ...boundWorktree,
  state: "stale",
  path: "/tmp/missing-wt",
  ahead_of_base: 0,
};

const noop = () => {};
const noDraft = () => Promise.resolve("");
const noBranches = (): Promise<GitBranchEntry[]> => Promise.resolve([]);

const base = {
  onInit: noop as (rootId: string) => void,
  onCommit: async () => true,
  onStash: noop,
  onDraftMessage: noDraft,
  onListBranches: noBranches,
  onCheckout: async () => {},
  onDiscard: noop,
  onPush: noop,
  onPull: noop,
  onGroupCommit: noop,
  onScopePin: noop as (
    pin: import("./git-repo-scope.ts").GitScope | null,
  ) => void,
  onSelectRepo: noop as (repoId: string) => void,
};

function worktreeBase(view: GitWorktreeView = unboundWorktree) {
  return {
    ...base,
    sessionId: unboundWorktree.session_id,
    onRefreshWorktree: async () => view,
    onBindWorktree: async () => boundWorktree,
    onLandWorktree: async () => ({
      landed: true,
      base_branch: "main",
      commits: 2,
      reason: "",
      conflicts: [],
    }),
    onUnbindWorktree: async () => {},
  };
}

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
  files: [
    {
      path: "src/api/auth.go",
      status: " M",
      root_id: "root-1",
      root_relative_path: "src/api/auth.go",
    },
    {
      path: "src/ui/login.tsx",
      status: "??",
      root_id: "root-1",
      root_relative_path: "src/ui/login.tsx",
    },
  ],
};

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
  branch: "feat",
  root_ids: ["root-2"],
  staged_count: 4,
  dirty: true,
});

describe("GitStrip checkout flow", () => {
  afterEach(() => vi.useRealTimers());

  async function openBranchMenu() {
    const button = screen.getByTestId("git-branch");
    await waitFor(() =>
      expect(button.getAttribute("aria-disabled")).toBeNull(),
    );
    fireEvent.click(button);
    await waitFor(() => screen.getByTestId("git-branches"));
  }
  async function openWorktreeForm() {
    await openBranchMenu();
    fireEvent.click(screen.getByTestId("git-worktree-bind-open"));
    await waitFor(() =>
      screen.getByRole("dialog", { name: "Create worktree" }),
    );
  }

  it("shows one entry point and distinguishes current location from creation", async () => {
    render(() => <GitStrip {...worktreeBase()} status={loaded(dirty)} />);
    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-location").textContent).toBe(
        "Project folder",
      ),
    );
    expect(screen.queryByTestId("git-worktree-bind-open")).toBeNull();
    await openWorktreeForm();
    const dialog = screen.getByRole("dialog", { name: "Create worktree" });
    expect(dialog.textContent).toContain("Starting pointmain");
    expect(dialog.textContent).toContain(
      "2 changed files stay in the project folder and are not copied.",
    );
    expect(dialog.textContent).not.toContain("Other attached repositories");
    expect(
      (screen.getByLabelText("New branch name") as HTMLInputElement).value,
    ).toBe("");
    expect(
      (screen.getByTestId("git-worktree-create") as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    expect(screen.getByTestId("git-branch").textContent).toContain("main");
  });

  it("Cancel and Escape close creation without mutation and return focus", async () => {
    const bind = vi.fn(async () => boundWorktree);
    render(() => (
      <GitStrip
        {...worktreeBase()}
        status={loaded(dirty)}
        onBindWorktree={bind}
      />
    ));
    await openWorktreeForm();
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByLabelText("New branch name"),
      ),
    );
    fireEvent.input(screen.getByLabelText("New branch name"), {
      target: { value: "feature/hello" },
    });
    fireEvent.click(screen.getByTestId("git-checkout-cancel"));
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByTestId("git-branch")),
    );
    await openWorktreeForm();
    expect(
      (screen.getByLabelText("New branch name") as HTMLInputElement).value,
    ).toBe("");
    fireEvent.keyDown(screen.getByLabelText("New branch name"), {
      key: "Escape",
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(bind).not.toHaveBeenCalled();
  });

  it("returns focus to the branch trigger while a background refresh holds it", async () => {
    const [busy, setBusy] = createSignal(false);
    const onListBranches = vi.fn(noBranches);
    render(() => (
      <GitStrip
        {...worktreeBase()}
        status={loaded(dirty)}
        busy={busy}
        onListBranches={onListBranches}
      />
    ));
    await openWorktreeForm();
    setBusy(true);
    fireEvent.keyDown(screen.getByLabelText("New branch name"), {
      key: "Escape",
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    const trigger = screen.getByTestId("git-branch");
    expect(trigger.getAttribute("aria-disabled")).toBe("true");
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    fireEvent.click(trigger);
    expect(screen.queryByTestId("git-branches")).toBeNull();
    expect(onListBranches).toHaveBeenCalledOnce();
    setBusy(false);
    expect(trigger.getAttribute("aria-disabled")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps a failed worktree form and retries the exact named branch", async () => {
    let view = unboundWorktree;
    const bind = vi
      .fn()
      .mockRejectedValueOnce(
        new Error("A branch with that name already exists."),
      )
      .mockImplementationOnce(async () => (view = boundWorktree));
    render(() => (
      <GitStrip
        {...worktreeBase()}
        status={loaded(dirty)}
        onRefreshWorktree={async () => view}
        onBindWorktree={bind}
      />
    ));
    await openWorktreeForm();
    fireEvent.input(screen.getByLabelText("New branch name"), {
      target: { value: " feature/login " },
    });
    fireEvent.submit(
      screen.getByLabelText("New branch name").closest("form")!,
    );
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "already exists",
      ),
    );
    expect(
      (screen.getByLabelText("New branch name") as HTMLInputElement).value,
    ).toBe(" feature/login ");
    fireEvent.input(screen.getByLabelText("New branch name"), {
      target: { value: "feature/login-2" },
    });
    fireEvent.submit(
      screen.getByLabelText("New branch name").closest("form")!,
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(bind.mock.calls).toEqual([
      ["rtest", "feature/login"],
      ["rtest", "feature/login-2"],
    ]);
    expect(screen.getByTestId("git-checkout-location").textContent).toBe(
      "Chat worktree",
    );
  });

  it("keeps branch creation open after checkout failure", async () => {
    const checkout = vi
      .fn()
      .mockRejectedValue(new Error("Invalid branch name"));
    render(() => (
      <GitStrip {...base} status={loaded(dirty)} onCheckout={checkout} />
    ));
    await openBranchMenu();
    fireEvent.click(screen.getByTestId("git-new-branch-open"));
    fireEvent.input(screen.getByLabelText("New branch name"), {
      target: { value: "bad..name" },
    });
    fireEvent.submit(
      screen.getByLabelText("New branch name").closest("form")!,
    );
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "Invalid branch name",
      ),
    );
    expect(
      screen.getByRole("dialog", { name: "Create branch" }).textContent,
    ).toContain(
      "Your uncommitted changes stay in this folder on the new branch.",
    );
    expect(checkout).toHaveBeenCalledWith("bad..name", true);
  });

  it("loads, filters, and retries branches without losing the creation choices", async () => {
    const list = vi
      .fn()
      .mockRejectedValueOnce(new Error("Repository unavailable"))
      .mockResolvedValueOnce([
        { name: "main", current: true },
        { name: "feature/login", current: false },
      ]);
    render(() => (
      <GitStrip
        {...worktreeBase()}
        status={loaded(dirty)}
        onListBranches={list}
      />
    ));
    await openBranchMenu();
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "Repository unavailable",
      ),
    );
    expect(screen.getByTestId("git-worktree-bind-open")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry branches" }));
    await waitFor(() =>
      expect(screen.getAllByTestId("git-branch-item")).toHaveLength(2),
    );
    fireEvent.input(screen.getByLabelText("Switch branch"), {
      target: { value: "LOGIN" },
    });
    expect(screen.getAllByTestId("git-branch-item")).toHaveLength(1);
    fireEvent.input(screen.getByLabelText("Switch branch"), {
      target: { value: "missing" },
    });
    expect(screen.getByText("No matching branches.")).toBeTruthy();
  });

  it("does not submit twice while a checkout is pending", async () => {
    let finish!: () => void;
    const checkout = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    render(() => (
      <GitStrip {...base} status={loaded(dirty)} onCheckout={checkout} />
    ));
    await openBranchMenu();
    fireEvent.click(screen.getByTestId("git-new-branch-open"));
    fireEvent.input(screen.getByLabelText("New branch name"), {
      target: { value: "feature/new" },
    });
    const form = screen.getByLabelText("New branch name").closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    expect(checkout).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(screen.getByLabelText("New branch name"), {
      key: "Escape",
    });
    expect(screen.getByRole("dialog")).toBeTruthy();
    finish();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId("git-branch")));
  });

  it("ignores an old chat's delayed worktree response", async () => {
    let resolveOld!: (view: GitWorktreeView) => void;
    const [session, setSession] = createSignal(unboundWorktree.session_id);
    const refresh = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<GitWorktreeView>((resolve) => {
            resolveOld = resolve;
          }),
      )
      .mockResolvedValue(unboundWorktree);
    render(() => (
      <GitStrip
        {...worktreeBase()}
        sessionId={session()}
        onRefreshWorktree={refresh}
        status={loaded(dirty)}
      />
    ));
    setSession("another-chat");
    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-location").textContent).toBe(
        "Project folder",
      ),
    );
    resolveOld(boundWorktree);
    await Promise.resolve();
    expect(screen.getByTestId("git-checkout-location").textContent).toBe(
      "Project folder",
    );
  });

  it("closes drafts when the chat changes and ignores callback identity changes", async () => {
    const first = vi.fn(async () => unboundWorktree);
    const second = vi.fn(async () => unboundWorktree);
    const [callback, setCallback] = createSignal(first);
    const [session, setSession] = createSignal(unboundWorktree.session_id);
    render(() => (
      <GitStrip
        {...worktreeBase()}
        sessionId={session()}
        onRefreshWorktree={callback()}
        status={loaded(dirty)}
      />
    ));
    await openWorktreeForm();
    setCallback(() => second);
    await Promise.resolve();
    expect(second).not.toHaveBeenCalled();
    setSession("another-chat");
    await waitFor(() => expect(second).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("keeps an entered branch name across status refreshes for the same repository", async () => {
    const [status, setStatus] = createSignal(dirty);
    render(() => <GitStrip {...worktreeBase()} status={loaded(status())} />);
    await openWorktreeForm();
    fireEvent.input(screen.getByLabelText("New branch name"), {
      target: { value: "feature/in-progress" },
    });
    setStatus({ ...dirty, changed_count: 3 });
    await waitFor(() =>
      expect(screen.getByRole("dialog").textContent).toContain(
        "3 changed files stay",
      ),
    );
    expect(
      (screen.getByLabelText("New branch name") as HTMLInputElement).value,
    ).toBe("feature/in-progress");
  });

  it("refreshes merge counts after git status changes", async () => {
    const refresh = vi
      .fn()
      .mockResolvedValueOnce({ ...boundWorktree, ahead_of_base: 0 })
      .mockResolvedValue(boundWorktree);
    const [status, setStatus] = createSignal(dirty);
    render(() => (
      <GitStrip
        {...worktreeBase()}
        status={loaded(status())}
        onRefreshWorktree={refresh}
      />
    ));
    await waitFor(() =>
      expect(
        (screen.getByTestId("git-worktree-land") as HTMLButtonElement)
          .disabled,
      ).toBe(true),
    );
    setStatus({ ...dirty, head_short: "new-commit" });
    await waitFor(() =>
      expect(
        (screen.getByTestId("git-worktree-land") as HTMLButtonElement)
          .disabled,
      ).toBe(false),
    );
  });

  it("names the merge destination, preserves the worktree, and renders conflict paths as text", async () => {
    const merge = vi.fn(async () => ({
      landed: false,
      base_branch: "main",
      commits: 0,
      reason: "conflict",
      conflicts: ["pkg/a.go"],
    }));
    render(() => (
      <GitStrip
        {...worktreeBase(boundWorktree)}
        status={loaded(dirty)}
        onLandWorktree={merge}
      />
    ));
    await waitFor(() => screen.getByTestId("git-worktree-land"));
    fireEvent.click(screen.getByTestId("git-worktree-land"));
    expect(screen.getByRole("dialog").textContent).toContain(
      "This chat will stay in its worktree.",
    );
    fireEvent.click(screen.getByTestId("git-worktree-land-confirm"));
    await waitFor(() => screen.getByTestId("git-worktree-result"));
    expect(screen.getByTestId("git-worktree-result").textContent).toContain(
      "pkg/a.go",
    );
    expect(
      screen.getByTestId("git-worktree-result").querySelector("a"),
    ).toBeNull();
    expect(screen.getByTestId("git-checkout-location").textContent).toBe(
      "Chat worktree",
    );
  });

  it("returns only after confirmation and says unmerged commits remain on the branch", async () => {
    let view = boundWorktree;
    const remove = vi.fn(async () => { view = unboundWorktree; });
    render(() => (
      <GitStrip
        {...worktreeBase(boundWorktree)}
        onRefreshWorktree={async () => view}
        onUnbindWorktree={remove}
        status={loaded(dirty)}
      />
    ));
    await waitFor(() => screen.getByTestId("git-worktree-unbind"));
    fireEvent.click(screen.getByTestId("git-worktree-unbind"));
    expect(remove).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog").textContent).toContain(
      "Unmerged commits remain on the branch.",
    );
    fireEvent.click(screen.getByTestId("git-worktree-unbind-confirm"));
    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-location").textContent).toBe(
        "Project folder",
      ),
    );
    expect(remove).toHaveBeenCalledOnce();
  });

  it("keeps missing-worktree recovery available when git status failed", async () => {
    render(() => (
      <GitStrip
        {...worktreeBase(staleWorktree)}
        activeRepoId="rtest"
        status={loadFailed(new Error("Missing checkout"))}
      />
    ));
    await waitFor(() => screen.getByTestId("git-worktree-unbind"));
    expect(screen.getByTestId("git-worktree").textContent).toContain(
      "worktree is missing",
    );
    expect(screen.queryByTestId("git-worktree-land")).toBeNull();
  });

  it("does not label another repository as the chat worktree", async () => {
    render(() => (
      <GitStrip
        {...worktreeBase({ ...boundWorktree, repo_id: "ra" })}
        status={loaded({ ...dirty, repo_id: "rb" })}
        repos={[alpha, beta]}
        scopePin={{ kind: "repo", repoId: "rb" }}
      />
    ));
    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-location").textContent).toBe(
        "Project folderbeta",
      ),
    );
    expect(screen.queryByTestId("git-worktree-land")).toBeNull();
    await openBranchMenu();
    expect(screen.getByRole("dialog").textContent).toContain(
      "worktree for app",
    );
    expect(screen.queryByTestId("git-worktree-bind-open")).toBeNull();
  });

  it("reports checkout load failure with retry instead of claiming project-folder mode", async () => {
    const refresh = vi
      .fn()
      .mockRejectedValueOnce(new Error("Checkout unavailable"))
      .mockResolvedValue(unboundWorktree);
    render(() => (
      <GitStrip
        {...worktreeBase()}
        onRefreshWorktree={refresh}
        status={loaded(dirty)}
      />
    ));
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "Checkout unavailable",
      ),
    );
    expect(
      screen.getByTestId("git-checkout-location").textContent,
    ).not.toContain("Project folder");
    fireEvent.click(screen.getByTestId("git-worktree-retry"));
    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-location").textContent).toBe(
        "Project folder",
      ),
    );
  });

  it.each([
    ["project", unboundWorktree],
    ["worktree", boundWorktree],
    ["missing", staleWorktree],
  ] as const)("is accessible in %s mode", async (_label, view) => {
    const { container } = render(() => (
      <GitStrip {...worktreeBase(view)} status={loaded(dirty)} />
    ));
    await waitFor(() =>
      expect(
        screen.getByTestId("git-checkout-location").textContent,
      ).not.toBe("Checking checkout…"),
    );
    await assertNoAxeViolations(container);
  });

  it("has an accessible creation dialog and traps keyboard focus", async () => {
    render(() => <GitStrip {...worktreeBase()} status={loaded(dirty)} />);
    await openWorktreeForm();
    await assertNoAxeViolations(screen.getByRole("dialog"));
    const cancel = screen.getByTestId("git-checkout-cancel");
    cancel.focus();
    fireEvent.keyDown(cancel, { key: "Tab" });
    expect(document.activeElement).toBe(
      screen.getByLabelText("New branch name"),
    );
  });
});
