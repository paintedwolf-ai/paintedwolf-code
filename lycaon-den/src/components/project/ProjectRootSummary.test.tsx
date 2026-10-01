import { invalidateWorkspace } from "../../files/source/workspace-invalidation.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { gitStatusReads } from "../../test/git-status-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectRoot, SourceIndexRootCoverage } from "../../api/types.ts";
import { resetFileInventoryForTests } from "../../files/tree/file-inventory.ts";
import { ProjectRootSummary } from "./ProjectRootSummary.tsx";
import { invalidateSourceQueries } from "../../files/source/source-invalidation.ts";
import { SOURCE_REFRESH_MAX_WAIT_MS } from "../../files/source/source-refresh.ts";
import { createPreparation } from "../../ui/presentation.ts";
import { checkoutBranch } from "../../chat/actions/git-actions.ts";
import { createAppStore } from "../../store/app-state.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import {
  requestedFirstTimeTips,
  resetFirstTimeTipRequestsForTests,
} from "../../first-time-tips/first-time-tips-service.ts";

const ROOT: ProjectRoot = {
  id: "r1",
  path: "/work/painted-wolf",
  label: "painted-wolf",
  is_primary: true,
  added_at: "2026-08-01T18:00:00Z",
  kind: "attached",
};

function rootCoverage(refreshing = false): SourceIndexRootCoverage[] {
  return [{ root_id: "r1", state: "ready", discovery_complete: true, refreshing,
    bounded_directories: 0, failed_directories: 0 }];
}

function summaryClient(): LycaonClient {
  return stubClient({
    listGitRepos: vi.fn(async () => ({
      active_repo_id: "repo-1",
      repos: [
        {
          repo_id: "repo-1",
          label: "painted-wolf",
          root_ids: ["r1"],
          available: true,
          branch: "main",
          head_short: "abc1234",
          upstream: "origin/main",
          ahead: 2,
          behind: 0,
          dirty: true,
          staged_count: 1,
          unstaged_count: 2,
          changed_count: 2,
        },
      ],
    })),
    ...gitStatusReads(() => ({
      available: true,
      repo_id: "repo-1",
      root_ids: ["r1"],
      branch: "main",
      head_short: "abc1234",
      upstream: "origin/main",
      ahead: 2,
      behind: 0,
      dirty: true,
      staged_count: 1,
      unstaged_count: 2,
      changed_count: 2,
      files: [
        {
          path: "README.md",
          status: " M",
          root_id: "r1",
          root_relative_path: "README.md",
        },
        {
          path: "src/app.ts",
          status: " M",
          root_id: "r1",
          root_relative_path: "src/app.ts",
        },
      ],
    })),
    getProjectSourceIndex: vi.fn(async () => ({
      state: "ready" as const,
      revision: 1,
      refreshing: false,
      coverage: [...rootCoverage(), { ...rootCoverage()[0]!, root_id: "r2" }],
      file_count: 4,
      roots: [
        {
          root_id: "r1",
          file_count: 3,
          resources: [
            { kind: "readme" as const, path: "README.md" },
            { kind: "agents" as const, path: "AGENTS.md" },
          ],
        },
        { root_id: "r2", file_count: 1, resources: [] },
      ],
    })),
    getProjectAgentContext: vi.fn(async () => ({
      project_id: "p1",
      root_id: "r1",
      path: ".",
      instructions_enabled: true,
      skills_enabled: true,
      instructions: [{ path: "AGENTS.md" }],
      skills: [
        { name: "release-check", description: "Check a release", path: ".agents/skills/release-check/SKILL.md" },
      ],
    })),
  });
}

describe("ProjectRootSummary", () => {
  it("does not infer a clean or detached checkout from pending Git status", async () => {
    const client = summaryClient();
    const response = await client.listGitRepos("p1");
    client.listGitRepos = async () => ({ ...response, repos: response.repos.map((repo) => ({
      ...repo, status_pending: true, branch: undefined, upstream: undefined, dirty: false,
    })) });
    render(() => <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
      onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
    await waitFor(() => expect(screen.getAllByText("Checking…").length).toBeGreaterThan(0));
    expect(screen.queryByText("Clean")).toBeNull();
    expect(screen.queryByText("Detached HEAD")).toBeNull();
    expect(screen.queryByText("No upstream")).toBeNull();
  });
  beforeEach(() => {
    resetFileInventoryForTests();
    resetFirstTimeTipRequestsForTests();
  });

  it("keeps its resource rows mounted while the index count changes under it", async () => {
    const client = summaryClient();
    let fileCount = 3;
    vi.mocked(client.getProjectSourceIndex).mockImplementation(async () => ({
      state: "warming" as const,
      revision: 1,
      refreshing: true,
      coverage: rootCoverage(true),
      file_count: fileCount,
      roots: [
        {
          root_id: "r1",
          file_count: fileCount,
          resources: [
            { kind: "readme" as const, path: "README.md" },
            { kind: "agents" as const, path: "AGENTS.md" },
          ],
        },
      ],
    }));
    render(() => (
      <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
        onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />
    ));
    const readme = await screen.findByText("README");
    const row = readme.closest("button")!;
    expect(screen.getByTestId("files-root-summary-file-count").textContent).toBe("Indexing…");

    fileCount = 30;
    await waitFor(() => expect(vi.mocked(client.getProjectSourceIndex).mock.calls.length).toBeGreaterThan(1));
    await waitFor(() => expect(screen.getByText("README").closest("button")).toBe(row));
  });

  it("polls a readable partial index until the final count arrives", async () => {
    const client = summaryClient();
    const complete = await client.getProjectSourceIndex("p1");
    const request = vi.mocked(client.getProjectSourceIndex);
    request.mockClear();
    request.mockResolvedValue({ ...complete, refreshing: true, coverage: rootCoverage(true) });
    const view = render(() => (
      <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
        onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />
    ));
    expect(await screen.findByText("Indexing…")).toBeTruthy();
    const row = screen.getByText("README").closest("button");
    request.mockResolvedValue({ ...complete, file_count: 460_498,
      roots: [{ ...complete.roots[0]!, file_count: 460_498 }] });
    expect(await screen.findByText("460,498 indexed")).toBeTruthy();
    expect(screen.getByText("README").closest("button")).toBe(row);
    const calls = request.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(request).toHaveBeenCalledTimes(calls);
    view.unmount();
  });

  it("stops automatic retries for a failed index and offers an explicit retry", async () => {
    const client = summaryClient();
    const request = vi.mocked(client.getProjectSourceIndex);
    request.mockResolvedValueOnce({ state: "failed", revision: 0, refreshing: false,
      file_count: 0, roots: [], coverage: [{ ...rootCoverage()[0]!, state: "failed", error: "unavailable" }] });
    render(() => (
      <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
        onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />
    ));
    await waitFor(() => expect(screen.getByTestId("files-root-summary-file-count").textContent).toBe("—"));
    expect(screen.queryByText("0 indexed")).toBeNull();
    await new Promise(resolve => setTimeout(resolve, 600));
    expect(request).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Retry file index" }));
    expect(await screen.findByText("3 indexed")).toBeTruthy();
  });

  it("requests its first-time tip when the overview is rendered", async () => {
    render(() => (
      <ProjectRootSummary
        projectId="p1"
        root={ROOT}
        roots={[ROOT]}
        client={summaryClient()}
        onOpenFile={vi.fn()}
        onOpenChanges={vi.fn()}
      />
    ));
    expect(requestedFirstTimeTips()).toContain("files-project-roots");
  });

  it("keeps a healthy root settled while another root is warming", async () => {
    const client = summaryClient();
    const complete = await client.getProjectSourceIndex("p1");
    vi.mocked(client.getProjectSourceIndex).mockClear().mockResolvedValue({ ...complete, refreshing: true,
      coverage: [...rootCoverage(), { ...rootCoverage(true)[0]!, root_id: "other", state: "warming", discovery_complete: false }] });
    render(() => <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
      onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
    expect(await screen.findByText("3 indexed")).toBeTruthy();
    expect(screen.queryByTestId("files-root-summary-coverage")).toBeNull();
    await new Promise(resolve => setTimeout(resolve, 600));
    expect(client.getProjectSourceIndex).toHaveBeenCalledTimes(1);
  });

  it("shows settled discovery gaps alongside resources without polling forever", async () => {
    const client = summaryClient();
    const complete = await client.getProjectSourceIndex("p1");
    vi.mocked(client.getProjectSourceIndex).mockClear().mockResolvedValue({ ...complete,
      coverage: [{ ...rootCoverage()[0]!, bounded_directories: 1, failed_directories: 1 }] });
    render(() => <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
      onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
    expect(await screen.findByText("3 indexed")).toBeTruthy();
    expect(screen.getByText("README")).toBeTruthy();
    const note = screen.getByTestId("files-root-summary-coverage").textContent;
    expect(note).toContain("indexing budget");
    expect(note).toContain("could not be read");
    await new Promise(resolve => setTimeout(resolve, 600));
    expect(client.getProjectSourceIndex).toHaveBeenCalledTimes(1);
  });

  it("stays pending until every summary request settles", async () => {
    let resolveContext!: (
      value: Awaited<ReturnType<LycaonClient["getProjectAgentContext"]>>,
    ) => void;
    const context = new Promise<
      Awaited<ReturnType<LycaonClient["getProjectAgentContext"]>>
    >((resolve) => {
      resolveContext = resolve;
    });
    const client = summaryClient();
    vi.mocked(client.getProjectAgentContext).mockReturnValue(context);
    const workspace = createPreparation();
    render(() => (
      <PresentationProvider preparation={workspace}>
      <ProjectRootSummary
        projectId="p1"
        root={ROOT}
        roots={[ROOT]}
        client={client}
        onOpenFile={vi.fn()}
        onOpenChanges={vi.fn()}
      />
      </PresentationProvider>
    ));

    const summary = screen.getByTestId("files-root-summary").closest("[data-boot]")!;
    expect(summary.getAttribute("data-boot")).toBe("pending");
    expect(workspace.ready()).toBe(true);
    expect(await screen.findByText("Preparing project overview…")).toBeTruthy();
    resolveContext({
      project_id: "p1",
      root_id: "r1",
      path: ".",
      instructions_enabled: true,
      skills_enabled: true,
      instructions: [],
      skills: [],
    });
    await waitFor(() => expect(summary.getAttribute("data-boot")).toBe("ready"));
  });

  it("turns existing repository, inventory, and agent data into an overview", async () => {
    const onOpenFile = vi.fn();
    const onOpenChanges = vi.fn();
    render(() => (
      <ProjectRootSummary
        projectId="p1"
        root={ROOT}
        roots={[ROOT]}
        client={summaryClient()}
        onOpenFile={onOpenFile}
        onOpenChanges={onOpenChanges}
      />
    ));

    await waitFor(() => {
      expect(
        screen.getByTestId("files-root-summary-branch").textContent,
      ).toContain("main");
      expect(
        screen.getByTestId("files-root-summary-working-tree").textContent,
      ).toBe("2 changed");
      expect(screen.getByTestId("files-root-summary-sync").textContent).toBe(
        "2 ahead",
      );
      expect(
        screen.getByTestId("files-root-summary-file-count").textContent,
      ).toBe("3 indexed");
    });
    await waitFor(() =>
      expect(screen.getByTestId("files-root-summary").closest("[data-boot]")?.getAttribute("data-boot")).toBe("ready"),
    );

    fireEvent.click(screen.getByTestId("files-root-summary-working-tree"));
    expect(onOpenChanges).toHaveBeenCalledOnce();
    expect(screen.getAllByText("AGENTS.md").length).toBeGreaterThan(0);
    expect(screen.getByText("release-check")).toBeTruthy();
    fireEvent.click(screen.getByTestId("files-root-summary-agents-link"));
    expect(onOpenFile).toHaveBeenCalledWith({
      rootId: "r1",
      rootLabel: "painted-wolf",
      path: "AGENTS.md",
    });
    fireEvent.click(screen.getByRole("button", { name: /README/ }));
    expect(onOpenFile).toHaveBeenCalledWith({
      rootId: "r1",
      rootLabel: "painted-wolf",
      path: "README.md",
    });
  });

  it("loads project context", async () => {
    const client = summaryClient();
    render(() => (
      <ProjectRootSummary
        projectId="p1"
        sessionId="session-worktree"
        root={ROOT}
        roots={[ROOT]}
        client={client}
        onOpenFile={vi.fn()}
        onOpenChanges={vi.fn()}
      />
    ));

    expect(await screen.findByText("Applied")).toBeTruthy();
    expect(client.listGitRepos).toHaveBeenCalledWith("p1", "session-worktree");
    expect(client.getProjectSourceIndex).toHaveBeenCalledWith("p1", {
      sessionId: "session-worktree",
    });
    expect(client.getProjectAgentContext).toHaveBeenCalledWith("p1", "r1", {
      sessionId: "session-worktree",
    });
  });

  it("publishes the overview while its source index warms", async () => {
    const client = summaryClient();
    vi.mocked(client.getProjectSourceIndex).mockResolvedValue({
      state: "warming",
      revision: 0,
      refreshing: true,
      coverage: rootCoverage(true),
      retry_after_ms: 2000,
      file_count: 0,
      roots: [],
    });
    render(() => (
      <ProjectRootSummary
        projectId="p1"
        root={ROOT}
        roots={[ROOT]}
        client={client}
        onOpenFile={vi.fn()}
        onOpenChanges={vi.fn()}
      />
    ));

    expect(await screen.findByText("Indexing…")).toBeTruthy();
    expect(client.getProjectSourceIndex).toHaveBeenCalledOnce();
    await waitFor(() =>
      expect(
        screen
          .getByTestId("files-root-summary")
          .closest("[data-boot]")
          ?.getAttribute("data-boot"),
      ).toBe("ready"),
    );
  });

  it("holds a complete overview while another root is loading", async () => {
    const other: ProjectRoot = {
      ...ROOT,
      id: "r2",
      path: "/work/other",
      label: "other",
      is_primary: false,
    };
    let resolveContext!: (
      value: Awaited<ReturnType<LycaonClient["getProjectAgentContext"]>>,
    ) => void;
    const pendingContext = new Promise<
      Awaited<ReturnType<LycaonClient["getProjectAgentContext"]>>
    >((resolve) => {
      resolveContext = resolve;
    });
    const client = summaryClient();
    const initialContext = await client.getProjectAgentContext("p1", "r1");
    vi.mocked(client.getProjectAgentContext).mockImplementation(
      async (_projectId, rootId) =>
        rootId === "r2" ? pendingContext : initialContext,
    );
    const [root, setRoot] = createSignal(ROOT);
    render(() => (
      <ProjectRootSummary
        projectId="p1"
        root={root()}
        roots={[ROOT, other]}
        client={client}
        onOpenFile={vi.fn()}
        onOpenChanges={vi.fn()}
      />
    ));

    expect(await screen.findByText("@painted-wolf")).toBeTruthy();
    await waitFor(() => {
      const summary = screen.getByTestId("files-root-summary");
      expect(summary.closest("[data-boot]")?.getAttribute("data-boot")).toBe(
        "ready",
      );
      expect(summary.dataset.retained).toBe("false");
    });
    setRoot(other);
    await waitFor(() =>
      expect(screen.getByTestId("files-root-summary").dataset.retained).toBe(
        "true",
      ),
    );
    expect(screen.getByText("@painted-wolf")).toBeTruthy();
    expect(screen.queryByText("@other")).toBeNull();

    resolveContext({ ...initialContext, root_id: "r2" });
    expect(await screen.findByText("@other")).toBeTruthy();
    expect(screen.queryByText("@painted-wolf")).toBeNull();
    expect(screen.getByTestId("files-root-summary").dataset.retained).toBe(
      "false",
    );
  });

  it("distinguishes disabled context from an empty root", async () => {
    const client = summaryClient();
    vi.mocked(client.getProjectAgentContext).mockResolvedValue({
      project_id: "p1",
      root_id: "r1",
      path: ".",
      instructions_enabled: false,
      skills_enabled: true,
      instructions: [],
      skills: [],
    });

    render(() => (
      <ProjectRootSummary
        projectId="p1"
        root={ROOT}
        roots={[ROOT]}
        client={client}
        onOpenFile={vi.fn()}
        onOpenChanges={vi.fn()}
      />
    ));

    expect(await screen.findByText("Project instructions are off.")).toBeTruthy();
    expect(screen.getByText("No project skills apply at this root.")).toBeTruthy();
    expect(screen.getByTestId("files-root-summary-context-status").textContent).toBe("None");
  });
});


it("refreshes the root overview when a chat binds the same root ID to a new checkout", async () => {
  const client = summaryClient();
  const [root, setRoot] = createSignal(ROOT);
  render(() => <ProjectRootSummary projectId="p1" sessionId="chat" root={root()} roots={[root()]} client={client} onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
  expect(await screen.findByText(ROOT.path)).toBeTruthy();
  const repos = await client.listGitRepos("p1", "chat");
  vi.mocked(client.listGitRepos).mockResolvedValue({ ...repos, repos: repos.repos.map((repo) => ({ ...repo, branch: "chore/checklist-review" })) });
  setRoot({ ...ROOT, path: "/work/chat-checkout" });
  expect(await screen.findByText("/work/chat-checkout")).toBeTruthy();
  expect(await screen.findByText("chore/checklist-review")).toBeTruthy();
  expect(screen.queryByText(ROOT.path)).toBeNull();
});

it("keeps the overview for another chat on the same checkout and still follows that chat's checkout changes", async () => {
  const client = summaryClient();
  const [chat, setChat] = createSignal("chat-a");
  render(() => <ProjectRootSummary projectId="p1" sessionId={chat()} root={ROOT} roots={[ROOT]} client={client} onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
  expect(await screen.findByText(ROOT.path)).toBeTruthy();
  const reads = vi.mocked(client.listGitRepos).mock.calls.length;

  setChat("chat-b");
  await Promise.resolve();
  await Promise.resolve();
  expect(vi.mocked(client.listGitRepos)).toHaveBeenCalledTimes(reads);
  expect(screen.getByTestId("files-root-summary-path").textContent).toBe(ROOT.path);

  invalidateWorkspace(client, "p1", "chat-a");
  await Promise.resolve();
  expect(vi.mocked(client.listGitRepos)).toHaveBeenCalledTimes(reads);
  invalidateWorkspace(client, "p1", "chat-b");
  await vi.waitFor(() => expect(vi.mocked(client.listGitRepos).mock.calls.length).toBeGreaterThan(reads));
});

it("refreshes the mounted summary after a branch checkout without changing the root path", async () => {
  const client = summaryClient();
  render(() => <ProjectRootSummary projectId="p1" sessionId="chat" root={ROOT} roots={[ROOT]} client={client} onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
  expect(await screen.findByText("main")).toBeTruthy();
  const repos = await client.listGitRepos("p1", "chat");
  client.checkoutGit = vi.fn(async () => {
    vi.mocked(client.listGitRepos).mockResolvedValue({ ...repos, repos: repos.repos.map((repo) => ({ ...repo, branch: "chore/branch-refresh" })) });
    return { repo_id: "repo-1", revision: 2, refreshing: false };
  });
  await checkoutBranch(createAppStore(), client, "p1", "repo-1", "chore/branch-refresh", true, "chat");
  expect(await screen.findByText("chore/branch-refresh")).toBeTruthy();
  expect(screen.getByTestId("files-root-summary-path").textContent).toBe(ROOT.path);
});


it.each(["git", "inventory"] as const)("settles a pending %s fact by polling only that fact", async (pending) => {
  vi.useFakeTimers();
  try {
    const client = summaryClient();
    const repos = await client.listGitRepos("p1");
    const inventory = await client.getProjectSourceIndex("p1");
    vi.mocked(client.listGitRepos).mockClear();
    vi.mocked(client.getProjectSourceIndex).mockClear();
    vi.mocked(client.getProjectAgentContext).mockClear();
    vi.mocked(client.listGitRepos)
      .mockResolvedValueOnce({ ...repos, repos: repos.repos.map((repo) => ({ ...repo, status_pending: pending === "git" })) })
      .mockResolvedValue({ ...repos, repos: repos.repos.map((repo) => ({ ...repo, branch: "chore/settled-branch", status_pending: false })) });
    vi.mocked(client.getProjectSourceIndex)
      .mockResolvedValueOnce({ ...inventory, state: pending === "inventory" ? "warming" : "ready", coverage: rootCoverage(pending === "inventory") })
      .mockResolvedValue({ ...inventory, state: "ready", file_count: 40, roots: [{ ...inventory.roots[0]!, file_count: 40 }] });
    const view = render(() => <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
      onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(0);
    expect(pending === "git" ? screen.getAllByText("Checking…").length : screen.getAllByText("Indexing…").length).toBeGreaterThan(0);
    await vi.advanceTimersByTimeAsync(300);
    if (pending === "git") {
      expect(screen.getByTestId("files-root-summary-branch").textContent).toContain("chore/settled-branch");
    } else {
      expect(screen.getByTestId("files-root-summary-file-count").textContent).toBe("40 indexed");
      expect(screen.getByTestId("files-root-summary-branch").textContent).toContain("main");
    }
    expect(client.listGitRepos).toHaveBeenCalledTimes(pending === "git" ? 2 : 1);
    expect(client.getProjectSourceIndex).toHaveBeenCalledTimes(pending === "inventory" ? 2 : 1);
    expect(client.getProjectAgentContext).toHaveBeenCalledOnce();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(client.listGitRepos).toHaveBeenCalledTimes(pending === "git" ? 2 : 1);
    expect(client.getProjectSourceIndex).toHaveBeenCalledTimes(pending === "inventory" ? 2 : 1);
    view.unmount();
  } finally {
    vi.useRealTimers();
  }
});

it("keeps branch and sync settled while a long index refresh is polled", async () => {
  vi.useFakeTimers();
  try {
    const client = summaryClient();
    const inventory = await client.getProjectSourceIndex("p1");
    vi.mocked(client.listGitRepos).mockClear();
    vi.mocked(client.getProjectSourceIndex).mockClear().mockResolvedValue({ ...inventory, refreshing: true, coverage: rootCoverage(true) });
    const view = render(() => <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
      onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
    const shown: string[] = [];
    for (let elapsed = 0; elapsed < 20_000; elapsed += 100) {
      await vi.advanceTimersByTimeAsync(100);
      shown.push(`${screen.getByTestId("files-root-summary-branch").textContent} | ${screen.getByTestId("files-root-summary-sync").textContent}`);
    }
    expect(vi.mocked(client.getProjectSourceIndex).mock.calls.length).toBeGreaterThan(5);
    expect(client.listGitRepos).toHaveBeenCalledOnce();
    expect(new Set(shown)).toEqual(new Set(["mainabc1234 | 2 ahead"]));
    view.unmount();
  } finally {
    vi.useRealTimers();
  }
});

it("refreshes repository and agent facts from source invalidations without a pending state", async () => {
  vi.useFakeTimers();
  try {
    const client = summaryClient();
    const repos = await client.listGitRepos("p1");
    vi.mocked(client.listGitRepos).mockClear();
    vi.mocked(client.getProjectAgentContext).mockClear();
    const view = render(() => <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
      onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByTestId("files-root-summary-sync").textContent).toBe("2 ahead");
    vi.mocked(client.listGitRepos).mockResolvedValue({ ...repos, repos: repos.repos.map((repo) => ({ ...repo, ahead: 0, behind: 3 })) });
    invalidateSourceQueries({ projectId: "other" });
    invalidateSourceQueries({ projectId: "p1" });
    const shown: string[] = [];
    for (let elapsed = 0; elapsed < SOURCE_REFRESH_MAX_WAIT_MS + 500; elapsed += 50) {
      await vi.advanceTimersByTimeAsync(50);
      shown.push(screen.getByTestId("files-root-summary-sync").textContent ?? "");
    }
    expect(shown).not.toContain("Checking…");
    expect(shown.at(-1)).toBe("3 behind");
    expect(client.listGitRepos).toHaveBeenCalledTimes(2);
    expect(client.getProjectAgentContext).toHaveBeenCalledTimes(2);
    view.unmount();
  } finally {
    vi.useRealTimers();
  }
});

it("cancels pending summary polling on disposal", async () => {
  vi.useFakeTimers();
  try {
    const client = summaryClient();
    const repos = await client.listGitRepos("p1");
    vi.mocked(client.listGitRepos).mockClear().mockResolvedValue({
      ...repos, repos: repos.repos.map((repo) => ({ ...repo, status_pending: true })),
    });
    const view = render(() => <ProjectRootSummary projectId="p1" root={ROOT} roots={[ROOT]} client={client}
      onOpenFile={vi.fn()} onOpenChanges={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(0);
    view.unmount();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(client.listGitRepos).toHaveBeenCalledTimes(1);
  } finally {
    vi.useRealTimers();
  }
});
