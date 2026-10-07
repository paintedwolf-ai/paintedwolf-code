import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import axe from "axe-core";
import { GitStrip } from "./GitStrip.tsx";
import { applySourceChangesEvent, requestSourceProjectionResync } from "../files/source/source-events.ts";
import { SOURCE_REFRESH_IDLE_MS } from "../files/source/source-refresh.ts";
import { loadFailed, loaded, unloaded } from "../store/load-state.ts";
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

const tracked: GitWorkspaceStatus = {
  available: true,
  repo_id: "rtest",
  root_ids: ["root-1"],
  branch: "main",
  upstream: "origin/main",
  ahead: 2,
  behind: 1,
  dirty: false,
  staged_count: 0,
  unstaged_count: 0,
  changed_count: 0,
  files: [],
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
const bare: GitRepoEntry = {
  repo_id: "",
  label: "scratch",
  root_ids: ["root-x"],
  available: false,
  ahead: 0,
  behind: 0,
  dirty: false,
  staged_count: 0,
  unstaged_count: 0,
  changed_count: 0,
};

describe("GitStrip", () => {
  afterEach(() => vi.useRealTimers());

  it.each(["write", "commit", "resync"] as const)("refreshes a mounted checkout after %s without reopening", async (cause) => {
    vi.useFakeTimers();
    const [status, setStatus] = createSignal(tracked);
    let refreshCount = 0;
    const refresh = vi.fn(() => {
      if (++refreshCount > 1) setStatus(dirty);
    });
    const view = render(() => <GitStrip {...base} projectId="project" sessionId="chat" status={loaded(status())} onRefreshRepos={refresh} />);
    expect(screen.getByTestId("git-clean").textContent).toContain("clean");
    for (let index = 0; index < 3; index++) {
      if (cause === "resync") requestSourceProjectionResync("project");
      else applySourceChangesEvent({
        project_id: "project", workspace_id: "chat-worktree", workspace_kind: "project", resync: false,
        git_changed: cause === "commit",
        changes: cause === "write" ? [{ root_id: "root-1", path: "src/api/auth.go", op: "write", origin: "agent", changed_at: "2026-09-13T00:00:00Z", session_id: "chat" }] : [],
      });
    }
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(screen.queryByTestId("git-clean")).toBeNull();
    expect(screen.getByTestId("git-count").textContent).toContain("2 changed");
    requestSourceProjectionResync("project");
    view.unmount();
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("holds source invalidation while Git is busy and ignores unrelated projects and overlays", async () => {
    vi.useFakeTimers();
    const [busy, setBusy] = createSignal(false);
    const refresh = vi.fn();
    render(() => <GitStrip {...base} projectId="project" status={loaded(tracked)} busy={busy} onRefreshRepos={refresh} />);
    requestSourceProjectionResync("other-project");
    applySourceChangesEvent({ project_id: "project", workspace_id: "worker", workspace_kind: "worker", changes: [], resync: true });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenCalledOnce();
    setBusy(true);
    requestSourceProjectionResync("project");
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(refresh).toHaveBeenCalledOnce();
    setBusy(false);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("reports not-a-repo and offers init when status is unavailable", () => {
    const onInit = vi.fn();
    render(() => (
      <GitStrip
        {...base}
        status={loaded({
          available: false,
          repo_id: "",
          root_ids: ["root-1"],
          ahead: 0,
          behind: 0,
          dirty: false,
          staged_count: 0,
          unstaged_count: 0,
          changed_count: 0,
          files: [],
        })}
        onInit={onInit}
      />
    ));
    expect(screen.getByTestId("git-strip").textContent).toMatch(/not a git/i);
    fireEvent.click(screen.getByTestId("git-init"));
    expect(onInit).toHaveBeenCalledWith("root-1");
  });

  it("an unloaded status never claims not-a-repo or offers git init", () => {
    // Not-a-repository plus Initialize is a settled claim.
    render(() => <GitStrip {...base} status={unloaded()} />);
    expect(screen.getByTestId("git-status-pending")).toBeTruthy();
    expect(screen.getByTestId("git-strip").textContent).not.toMatch(
      /not a git/i,
    );
    expect(screen.queryByTestId("git-init")).toBeNull();
  });

  it("a failed status read renders the failure, not not-a-repo", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loadFailed(new Error("sidecar restarting"))}
      />
    ));
    expect(screen.getByTestId("git-status-pending").textContent).toMatch(
      /couldn.t read git status/i,
    );
    expect(screen.queryByTestId("git-init")).toBeNull();
  });

  it("a failed refresh keeps showing the last known repository", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loadFailed(new Error("sidecar restarting"), loaded(dirty))}
      />
    ));
    // Stale beats blank.
    expect(screen.queryByTestId("git-status-pending")).toBeNull();
    expect(screen.getByTestId("git-strip").textContent).not.toMatch(
      /not a git/i,
    );
  });

  it("does not present a cold repository as clean", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        repos={[
          repo({ repo_id: "ra", label: "alpha", status_pending: true }),
          repo({ repo_id: "rb", label: "beta" }),
        ]}
      />
    ));

    fireEvent.click(screen.getByTestId("git-scope"));
    expect(screen.getByTestId("git-scope-list").textContent).toMatch(
      /checking changes/i,
    );
  });

  it("shows a clean state when the tree is not dirty", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded({
          available: true,
          repo_id: "rtest",
          root_ids: ["root-1"],
          branch: "main",
          ahead: 0,
          behind: 0,
          dirty: false,
          staged_count: 0,
          unstaged_count: 0,
          changed_count: 0,
          files: [],
        })}
      />
    ));
    expect(screen.getByTestId("git-clean").textContent).toMatch(/clean/i);
    expect(screen.queryByTestId("git-commit")).toBeNull();
  });

  it("omits the scope row for a single-repository project", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        repos={[alpha]}
        activeRepoId="ra"
      />
    ));
    expect(screen.queryByTestId("git-scope")).toBeNull();
    expect(screen.getByTestId("git-branch")).toBeTruthy();
  });

  it("expands the changed-file list on demand", () => {
    render(() => <GitStrip {...base} status={loaded(dirty)} />);
    expect(screen.getByTestId("git-count").textContent).toMatch(/2 changed/);
    expect(screen.queryByTestId("git-files")).toBeNull();
    fireEvent.click(screen.getByTestId("git-count"));
    const list = screen.getByTestId("git-files");
    expect(list.textContent).toContain("src/api/auth.go");
    expect(list.textContent).toContain("src/ui/login.tsx");
  });

  it("renders unattributed paths as plain text without a link", () => {
    const status: GitWorkspaceStatus = {
      ...dirty,
      files: [
        {
          path: "outside/file.txt",
          status: " M",
          root_id: "",
          root_relative_path: "",
        },
      ],
    };
    render(() => (
      <GitStrip {...base} status={loaded(status)} projectId="proj-1" />
    ));
    fireEvent.click(screen.getByTestId("git-count"));
    const unlinked = screen.getByTestId("git-file-unlinked");
    expect(unlinked.textContent).toBe("outside/file.txt");
    expect(unlinked.closest("a")).toBeNull();
  });

  it("commits only with a non-empty message and clears the input after success", async () => {
    const onCommit = vi.fn(async () => true);
    render(() => (
      <GitStrip {...base} status={loaded(dirty)} onCommit={onCommit} />
    ));
    const commit = screen.getByTestId("git-commit") as HTMLButtonElement;
    expect(commit.disabled).toBe(true);
    const input = screen.getByTestId("git-message") as HTMLInputElement;
    fireEvent.input(input, { target: { value: "feat: x" } });
    expect(commit.disabled).toBe(false);
    fireEvent.click(commit);
    expect(onCommit).toHaveBeenCalledWith("feat: x");
    await waitFor(() => expect(input.value).toBe(""));
  });

  it("leaves the empty message box at its CSS height", () => {
    render(() => <GitStrip {...base} status={loaded(dirty)} />);
    const ta = screen.getByTestId("git-message") as HTMLTextAreaElement;
    expect(ta.style.height).toBe("");
    fireEvent.input(ta, { target: { value: "feat: x" } });
    expect(ta.style.height).not.toBe("");
    fireEvent.input(ta, { target: { value: "" } });
    expect(ta.style.height).toBe("");
  });

  it.each(["failure", "new message", "new repository", "new session"] as const)(
    "preserves commit text after %s and prevents duplicate pending submissions",
    async (change) => {
      let finish!: (success: boolean) => void;
      let finishDraft!: (message: string) => void;
      const onCommit = vi.fn(() => new Promise<boolean>((resolve) => { finish = resolve; }));
      const onDraftMessage = vi.fn(() => new Promise<string>((resolve) => { finishDraft = resolve; }));
      const [repo, setRepo] = createSignal(dirty);
      const [session, setSession] = createSignal("session-one");
      render(() => <GitStrip {...base} status={loaded(repo())} sessionId={session()} onCommit={onCommit} onDraftMessage={onDraftMessage} />);
      const input = screen.getByTestId("git-message") as HTMLTextAreaElement;
      const button = screen.getByTestId("git-commit") as HTMLButtonElement;
      fireEvent.input(input, { target: { value: "Original commit message" } });
      if (change === "new message") fireEvent.click(screen.getByTestId("git-ai"));
      fireEvent.click(button);
      fireEvent.keyDown(input, { key: "Enter" });
      expect(onCommit).toHaveBeenCalledTimes(1);
      expect(button.disabled).toBe(true);
      expect(input.value).toBe("Original commit message");
      if (change === "new message") {
        finishDraft("Newer message");
        await waitFor(() => expect(input.value).toBe("Newer message"));
      }
      if (change === "new repository") setRepo({ ...dirty, repo_id: "another-repo" });
      if (change === "new session") setSession("session-two");
      finish(change !== "failure");
      await waitFor(() => expect(button.disabled).toBe(false));
      expect(input.value).toBe(change === "new message" ? "Newer message" : "Original commit message");
    },
  );

  it("commits on Enter but inserts a newline on Shift+Enter", () => {
    const onCommit = vi.fn(async () => true);
    render(() => (
      <GitStrip {...base} status={loaded(dirty)} onCommit={onCommit} />
    ));
    const ta = screen.getByTestId("git-message") as HTMLTextAreaElement;
    fireEvent.input(ta, { target: { value: "feat: x" } });
    fireEvent.keyDown(ta, { key: "Enter", shiftKey: true });
    expect(onCommit).not.toHaveBeenCalled();
    fireEvent.keyDown(ta, { key: "Enter" });
    expect(onCommit).toHaveBeenCalledWith("feat: x");
  });

  it("fills the message from the AI draftsman", async () => {
    const onDraftMessage = vi.fn(() =>
      Promise.resolve("fix(ui): prevent double submit"),
    );
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        onDraftMessage={onDraftMessage}
      />
    ));
    fireEvent.click(screen.getByTestId("git-ai"));
    expect(onDraftMessage).toHaveBeenCalledOnce();
    await waitFor(() =>
      expect(
        (screen.getByTestId("git-message") as HTMLInputElement).value,
      ).toBe("fix(ui): prevent double submit"),
    );
  });

  it("asks the agent to commit in logical groups", () => {
    const onGroupCommit = vi.fn();
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        onGroupCommit={onGroupCommit}
      />
    ));
    fireEvent.click(screen.getByTestId("git-group-commit"));
    expect(onGroupCommit).toHaveBeenCalledOnce();
  });

  it("fires onStash", () => {
    const onStash = vi.fn();
    render(() => (
      <GitStrip {...base} status={loaded(dirty)} onStash={onStash} />
    ));
    fireEvent.click(screen.getByTestId("git-stash"));
    expect(onStash).toHaveBeenCalledOnce();
  });

  it("lists branches and switches on click", async () => {
    const onListBranches = vi.fn(
      (): Promise<GitBranchEntry[]> =>
        Promise.resolve([
          { name: "main", current: true },
          { name: "feat/login", current: false },
        ]),
    );
    const onCheckout = vi.fn();
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        onListBranches={onListBranches}
        onCheckout={onCheckout}
      />
    ));
    fireEvent.click(screen.getByTestId("git-branch"));
    expect(onListBranches).toHaveBeenCalledOnce();
    await waitFor(() => screen.getByTestId("git-branches"));
    const items = screen.getAllByTestId("git-branch-item");
    const other = items.find((el) => el.textContent?.includes("feat/login"))!;
    fireEvent.click(other);
    expect(onCheckout).toHaveBeenCalledWith("feat/login", false);
  });

  it("discards only after confirming in the modal", () => {
    const onDiscard = vi.fn();
    render(() => (
      <GitStrip {...base} status={loaded(dirty)} onDiscard={onDiscard} />
    ));
    expect(screen.queryByTestId("git-discard-modal")).toBeNull();
    fireEvent.click(screen.getByTestId("git-discard"));
    expect(onDiscard).not.toHaveBeenCalled();
    expect(screen.getByTestId("git-discard-modal")).toBeTruthy();
    fireEvent.click(screen.getByTestId("git-discard-confirm"));
    expect(onDiscard).toHaveBeenCalledOnce();
  });

  it("cancels discard from the modal without calling onDiscard", () => {
    const onDiscard = vi.fn();
    render(() => (
      <GitStrip {...base} status={loaded(dirty)} onDiscard={onDiscard} />
    ));
    fireEvent.click(screen.getByTestId("git-discard"));
    fireEvent.click(screen.getByTestId("git-discard-cancel"));
    expect(onDiscard).not.toHaveBeenCalled();
    expect(screen.queryByTestId("git-discard-modal")).toBeNull();
  });

  it("shows ahead/behind and offers push and pull", () => {
    const onPush = vi.fn();
    const onPull = vi.fn();
    render(() => (
      <GitStrip
        {...base}
        status={loaded(tracked)}
        onPush={onPush}
        onPull={onPull}
      />
    ));
    expect(screen.getByTestId("git-ahead").textContent).toContain("2");
    expect(screen.getByTestId("git-behind").textContent).toContain("1");
    fireEvent.click(screen.getByTestId("git-push"));
    expect(onPush).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByTestId("git-pull"));
    expect(onPull).toHaveBeenCalledOnce();
  });

  it("hides sync controls without an upstream", () => {
    render(() => <GitStrip {...base} status={loaded(dirty)} />);
    expect(screen.queryByTestId("git-sync")).toBeNull();
  });

  for (const behind of [0, 1]) {
    for (const dirty of [false, true]) {
      for (const busy of [false, true]) {
        it(`allows a pull to discover remote changes: behind=${behind}, dirty=${dirty}, busy=${busy}`, () => {
          const onPull = vi.fn();
          render(() => <GitStrip {...base} busy={busy} status={loaded({ ...tracked, behind, dirty })} onPull={onPull} />);
          const pull = screen.getByTestId("git-pull") as HTMLButtonElement;
          expect(pull.disabled).toBe(busy);
          if (!busy) {
            fireEvent.click(pull);
            expect(onPull).toHaveBeenCalledOnce();
          }
        });
      }
    }
  }

  it("creates a new branch with create=true", async () => {
    const onCheckout = vi.fn();
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        onListBranches={() =>
          Promise.resolve([{ name: "main", current: true }])
        }
        onCheckout={onCheckout}
      />
    ));
    fireEvent.click(screen.getByTestId("git-branch"));
    fireEvent.click(screen.getByTestId("git-new-branch-open"));
    await waitFor(() => screen.getByTestId("git-new-branch"));
    fireEvent.input(screen.getByTestId("git-new-branch"), {
      target: { value: "feat/new" },
    });
    fireEvent.click(screen.getByTestId("git-create-branch"));
    expect(onCheckout).toHaveBeenCalledWith("feat/new", true);
  });

  it("lists aggregate and per-repo rows in the scope picker", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        repos={[alpha, beta, bare]}
        activeRepoId="ra"
      />
    ));
    fireEvent.click(screen.getByTestId("git-scope"));
    expect(screen.getByTestId("git-scope-list")).toBeTruthy();
    expect(screen.getByTestId("git-scope-all").textContent).toMatch(
      /All repositories/,
    );
    const items = screen.getAllByTestId("git-scope-item");
    expect(items).toHaveLength(3);
    expect(items[2]!.textContent).toMatch(/not a repo/);
  });

  it("closes sibling lists when opening the scope picker", async () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        repos={[alpha, beta]}
        activeRepoId="ra"
        onListBranches={() =>
          Promise.resolve([{ name: "main", current: true }])
        }
      />
    ));
    fireEvent.click(screen.getByTestId("git-branch"));
    await waitFor(() => screen.getByTestId("git-branches"));
    fireEvent.click(screen.getByTestId("git-scope"));
    expect(screen.queryByTestId("git-branches")).toBeNull();
    expect(screen.getByTestId("git-scope-list")).toBeTruthy();
  });

  it("pins a repository and refreshes its status", () => {
    const onScopePin = vi.fn();
    const onSelectRepo = vi.fn();
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        repos={[alpha, beta]}
        activeRepoId="ra"
        onScopePin={onScopePin}
        onSelectRepo={onSelectRepo}
      />
    ));
    fireEvent.click(screen.getByTestId("git-scope"));
    const items = screen.getAllByTestId("git-scope-item");
    fireEvent.click(items[1]!);
    expect(onScopePin).toHaveBeenCalledWith({ kind: "repo", repoId: "rb" });
    expect(onSelectRepo).toHaveBeenCalledWith("rb");
  });

  it("renders a read-only aggregate body with no write controls", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        repos={[alpha, beta]}
        activeRepoId="ra"
        scopePin={{ kind: "all" }}
      />
    ));
    expect(screen.getByTestId("git-repos")).toBeTruthy();
    expect(screen.getAllByTestId("git-repo-card")).toHaveLength(2);
    expect(screen.getAllByTestId("git-repo-open")).toHaveLength(2);
    expect(screen.getByTestId("git-repos").textContent).toMatch(
      /Commit one repository at a time/,
    );
    for (const id of [
      "git-commit",
      "git-stash",
      "git-discard",
      "git-push",
      "git-pull",
      "git-branch",
      "git-message",
      "git-ai",
      "git-group-commit",
    ]) {
      expect(screen.queryByTestId(id)).toBeNull();
    }
  });

  it("names the root when initializing a non-repo scope", () => {
    const onInit = vi.fn();
    render(() => (
      <GitStrip
        {...base}
        status={loaded({
          available: false,
          repo_id: "",
          root_ids: ["root-x"],
          ahead: 0,
          behind: 0,
          dirty: false,
          staged_count: 0,
          unstaged_count: 0,
          changed_count: 0,
          files: [],
        })}
        repos={[alpha, bare]}
        rootRefs={[
          { id: "root-1", path: "/project", is_primary: true },
          { id: "root-x", path: "/scratch", is_primary: false },
        ]}
        activeRepoId="ra"
        scopePin={{ kind: "root", rootId: "root-x" }}
        onInit={onInit}
      />
    ));
    expect(screen.getByTestId("git-strip").textContent).toMatch(
      /Initializes in scratch, not the primary root/,
    );
    fireEvent.click(screen.getByTestId("git-init"));
    expect(onInit).toHaveBeenCalledWith("root-x");
  });

  it("does not claim that the sole primary root is non-primary", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded({
          available: false,
          repo_id: "",
          root_ids: ["root-primary"],
          ahead: 0,
          behind: 0,
          dirty: false,
          staged_count: 0,
          unstaged_count: 0,
          changed_count: 0,
          files: [],
        })}
        repos={[
          repo({
            available: false,
            repo_id: "",
            root_ids: ["root-primary"],
            label: "minimal-go-project",
          }),
        ]}
        rootRefs={[
          { id: "root-primary", path: "/minimal-go-project", is_primary: true },
        ]}
      />
    ));
    expect(screen.getByTestId("git-strip").textContent).not.toMatch(
      /not the primary root/,
    );
  });

  it("hides AI auto-commit in the aggregate view", () => {
    render(() => (
      <GitStrip
        {...base}
        status={loaded(dirty)}
        repos={[alpha, beta]}
        scopePin={{ kind: "all" }}
      />
    ));
    expect(screen.queryByTestId("git-group-commit")).toBeNull();
  });

  describe("checkout flow", () => {
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
});


it("rechecks pending repository summaries, pauses while busy and stops after readiness", async () => {
  vi.useFakeTimers();
  try {
    const [repos, setRepos] = createSignal([repo({ repo_id: "rtest", label: "Orchard", status_pending: true })]);
    const [busy, setBusy] = createSignal(true);
    const refresh = vi.fn(async () => undefined);
    const view = render(() => <GitStrip {...base} status={loaded(tracked)} repos={repos()} busy={busy} onRefreshRepos={refresh} />);
    expect(refresh).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    setBusy(false);
    await vi.advanceTimersByTimeAsync(300);
    expect(refresh).toHaveBeenCalledTimes(2);
    setRepos([repo({ repo_id: "rtest", label: "Orchard", status_pending: false })]);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(refresh).toHaveBeenCalledTimes(2);
    setRepos([repo({ repo_id: "rtest", label: "Orchard", status_pending: true })]);
    view.unmount();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(refresh).toHaveBeenCalledTimes(2);
  } finally {
    vi.useRealTimers();
  }
});
