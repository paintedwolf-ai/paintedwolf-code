// @vitest-environment jsdom
import "../../test/document-outbox-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type {
  GitWorktreeEntry,
  GitWorktreesView,
  GitWorktreeView,
} from "../../api/types.ts";
import type { GitWorkspaceStatus } from "../../chat/actions/git-workspace-status.ts";
import { resetEditorDocumentsForTests } from "../../files/documents/editor-document.ts";
import {
  openFilesBuffer,
  resetProjectFilesForTests,
} from "../../files/documents/project-files-buffer-lifecycle.ts";
import { resetWorkspaceRootCheckoutsForTests } from "../../files/source/files-checkout-scope.ts";
import {
  registerOpenRunningStageSink,
  resetOpenRunningForTests,
} from "../../platform/navigation/open-running.ts";
import {
  registerOpenSourceProjectLookup,
  registerOpenSourceSink,
  resetOpenSourceForTests,
} from "../../platform/navigation/open-source.ts";
import { createAppStore } from "../../store/app-state.ts";
import { GitCheckout } from "../GitCheckout.tsx";
import {
  pinRepoWorktreeSelection,
  readCheckoutInspectionState,
  reconcileRepoWorktreeSelection,
} from "./git-worktree-inspection.ts";
import {
  flushCheckoutBuffersBeforeRemoval,
  resolveWorktreeBlockerNavigation,
} from "./worktree-lifecycle-blocker.ts";

const PROJECT_ID = "11111111-1111-4111-8111-111111111111";
const ROOT_ID = "22222222-2222-4222-8222-222222222222";
const SESSION_ID = "sess-lifecycle";
const REPO_ID = "repo_main";
const WT_PRIMARY = "33333333-3333-4333-8333-333333333333";
const WT_BOUND_A = "44444444-4444-4444-8444-444444444444";
const WT_SIBLING_B = "55555555-5555-4555-8555-555555555555";

function makeWorktreeEntry(
  overrides: Partial<GitWorktreeEntry> & Pick<GitWorktreeEntry, "worktree_id" | "path">,
): GitWorktreeEntry {
  return {
    kind: "linked",
    detached: false,
    locked: false,
    prunable: false,
    available: true,
    managed: false,
    is_project_checkout: false,
    bound_to_session: false,
    created_by_chat: false,
    used_by_chat: false,
    worker_ids: [],
    status_cached: false,
    ...overrides,
  };
}

function makeInventoryView(): GitWorktreesView {
  return {
    repo_id: REPO_ID,
    project_checkout_id: WT_PRIMARY,
    bound_worktree_id: WT_BOUND_A,
    worktrees: [
      makeWorktreeEntry({
        worktree_id: WT_PRIMARY,
        kind: "primary",
        path: "/repos/primary",
        branch: "main",
        head_short: "1111aaa",
        is_project_checkout: true,
      }),
      makeWorktreeEntry({
        worktree_id: WT_BOUND_A,
        path: "/managed/sess-lifecycle",
        branch: "session/managed-a",
        base_branch: "main",
        head_short: "2222bbb",
        managed: true,
        bound_to_session: true,
        created_by_chat: true,
        used_by_chat: true,
      }),
      makeWorktreeEntry({
        worktree_id: WT_SIBLING_B,
        path: "/repos/sibling-b",
        branch: "feat/sibling-b",
        head_short: "3333ccc",
        managed: false,
        used_by_chat: true,
        worker_ids: ["worker-b"],
      }),
    ],
  };
}

describe("WT07 worktree inspection and lifecycle independence", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    resetEditorDocumentsForTests();
    resetWorkspaceRootCheckoutsForTests();
    resetOpenSourceForTests();
  });

  afterEach(() => {
    cleanup();
    resetProjectFilesForTests();
    resetEditorDocumentsForTests();
    resetWorkspaceRootCheckoutsForTests();
    resetOpenSourceForTests();
  });

  it("keeps checkout inspection available during running turns while mutations remain disabled", async () => {
    const appStore = createAppStore();
    const view = makeInventoryView();
    reconcileRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, view);
    let menuOpen = true;

    const status: GitWorkspaceStatus = {
      available: true,
      repo_id: REPO_ID,
      worktree_id: WT_BOUND_A,
      root_ids: [ROOT_ID],
      branch: "session/managed-a",
      head_short: "2222bbb",
      dirty: false,
      ahead: 0,
      behind: 0,
      staged_count: 0,
      unstaged_count: 0,
      changed_count: 0,
      files: [],
    };
    const boundView: GitWorktreeView = {
      bound: true,
      session_id: SESSION_ID,
      worktree_id: WT_BOUND_A,
      repo_id: REPO_ID,
      branch: "session/managed-a",
      base_branch: "main",
      state: "ready",
      dirty: false,
      ahead_of_base: 1,
      behind_base: 0,
    };

    const onSelectWorktree = vi.fn((_rid: string, entry: GitWorktreeEntry) => {
      pinRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, entry);
    });

    render(() => (
      <GitCheckout
        appStore={appStore}
        projectId={PROJECT_ID}
        sessionId={SESSION_ID}
        repoId={REPO_ID}
        repoLabel="repo"
        repositoryCount={1}
        status={status}
        busy={true}
        menuOpen={menuOpen}
        onMenuOpenChange={(next) => {
          menuOpen = next;
        }}
        onSelectWorktreeRepo={() => {}}
        onListBranches={async () => []}
        onCheckout={async () => {}}
        onRefreshWorktree={async () => boundView}
        onListWorktrees={async () => view}
        onSelectWorktree={onSelectWorktree}
        onLandWorktree={async () => ({ landed: true, base_branch: "main", commits: 1, reason: "", conflicts: [] })}
        onUnbindWorktree={async () => {}}
      />
    ));

    // Trigger is enabled for viewing even while busy=true!
    const trigger = await screen.findByTestId("git-branch");
    expect(trigger.getAttribute("aria-disabled")).toBeNull();

    // Lifecycle mutation buttons stay disabled while busy=true.
    expect((screen.getByTestId("git-worktree-land") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("git-worktree-unbind") as HTMLButtonElement).disabled).toBe(true);

    // Selecting sibling B for inspection works during the running turn.
    const rows = await screen.findAllByTestId("git-worktree-option");
    const siblingRow = rows.find((r) => r.textContent?.includes("feat/sibling-b"));
    if (!siblingRow) throw new Error("expected sibling B row");
    fireEvent.click(siblingRow);
    expect(onSelectWorktree).toHaveBeenCalledTimes(1);
    expect(readCheckoutInspectionState(appStore, PROJECT_ID, REPO_ID).inspectedWorktreeId).toBe(WT_SIBLING_B);
  });

  it("displays the actual bound checkout when inspecting another, never retargets return-and-remove, and navigates to concrete blockers", async () => {
    const appStore = createAppStore();
    const view = makeInventoryView();
    reconcileRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, view);
    const siblingEntry = view.worktrees[2];
    if (!siblingEntry) throw new Error("expected sibling entry");
    pinRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, siblingEntry);

    registerOpenSourceProjectLookup(() => ({
      roots: [{ id: ROOT_ID, label: "repo", path: "/repos/primary" }],
    }));
    const openedSource: unknown[] = [];
    registerOpenSourceSink((req) => {
      openedSource.push(req);
    });

    const siblingStatus: GitWorkspaceStatus = {
      available: true,
      repo_id: REPO_ID,
      worktree_id: WT_SIBLING_B,
      root_ids: [ROOT_ID],
      branch: "feat/sibling-b",
      head_short: "3333ccc",
      dirty: true,
      ahead: 0,
      behind: 0,
      staged_count: 0,
      unstaged_count: 1,
      changed_count: 1,
      files: [{ path: "app.go", status: " M", root_id: ROOT_ID, root_relative_path: "app.go" }],
    };

    let currentBound: GitWorktreeView = {
      bound: true,
      session_id: SESSION_ID,
      worktree_id: WT_BOUND_A,
      repo_id: REPO_ID,
      branch: "session/managed-a",
      base_branch: "main",
      state: "ready",
      dirty: false,
      ahead_of_base: 2,
      behind_base: 0,
    };

    let blocked = true;
    const onUnbindWorktree = vi.fn(async () => {
      if (blocked) {
        throw new LycaonApiError(
          "Save or discard unsaved editor changes in app.go before returning to the project folder.",
          409,
          "worktree_dirty",
          {
            details: {
              blocker: "unsaved_editor",
              repo_id: REPO_ID,
              worktree_id: WT_BOUND_A,
              root_id: ROOT_ID,
              path: "app.go",
              document_id: "doc-bound-a",
            },
          },
        );
      }
      currentBound = {
        bound: false,
        session_id: SESSION_ID,
        dirty: false,
        ahead_of_base: 0,
        behind_base: 0,
      };
    });

    render(() => (
      <GitCheckout
        appStore={appStore}
        projectId={PROJECT_ID}
        sessionId={SESSION_ID}
        repoId={REPO_ID}
        repoLabel="repo"
        repositoryCount={1}
        status={siblingStatus}
        busy={false}
        menuOpen={false}
        onMenuOpenChange={() => {}}
        onSelectWorktreeRepo={() => {}}
        onListBranches={async () => []}
        onCheckout={async () => {}}
        onRefreshWorktree={async () => currentBound}
        onListWorktrees={async () => view}
        onSelectWorktree={() => {}}
        onLandWorktree={async () => ({ landed: true, base_branch: "main", commits: 2, reason: "", conflicts: [] })}
        onUnbindWorktree={onUnbindWorktree}
      />
    ));

    // Even while inspecting feat/sibling-b, the bound worktree card explicitly names session/managed-a and main!
    const summary = await screen.findByTestId("git-worktree-bound-summary");
    expect(summary.textContent).toContain("Chat bound to session/managed-a");
    expect(summary.textContent).toContain("Based on main");
    expect(screen.getByTestId("git-branch").textContent).toContain("feat/sibling-b");

    // Open Return to project folder modal: description names session/managed-a (NOT feat/sibling-b!).
    fireEvent.click(screen.getByTestId("git-worktree-unbind"));
    const modalDesc = await screen.findByTestId("git-worktree-unbind-description");
    expect(modalDesc.textContent).toContain("session/managed-a");
    expect(modalDesc.textContent).not.toContain("feat/sibling-b");

    // First attempt fails with structured unsaved_editor blocker and renders direct navigation button!
    fireEvent.click(screen.getByTestId("git-worktree-unbind-confirm"));
    const blockerBtn = await screen.findByTestId("git-worktree-blocker-action");
    expect(blockerBtn.textContent).toContain("Open app.go in Files");

    fireEvent.click(blockerBtn);
    await waitFor(() => {
      expect(openedSource).toContainEqual(
        expect.objectContaining({
          projectId: PROJECT_ID,
          rootId: ROOT_ID,
          path: "app.go",
          repoId: REPO_ID,
          worktreeId: WT_BOUND_A,
          selectCheckout: true,
        }),
      );
    });

    // Resolve blocker and retry Return and remove worktree -> succeeds!
    blocked = false;
    fireEvent.click(screen.getByTestId("git-worktree-unbind"));
    fireEvent.click(await screen.findByTestId("git-worktree-unbind-confirm"));
    await waitFor(() => {
      expect(onUnbindWorktree).toHaveBeenCalledTimes(2);
      expect(screen.queryByTestId("git-worktree")).toBeNull();
    });
  });

  it("flushes matching checkout buffers before removal and routes dirty-worktree and active-work blockers", async () => {
    const appStore = createAppStore();
    const view = makeInventoryView();
    reconcileRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, view);

    openFilesBuffer(PROJECT_ID, {
      rootId: ROOT_ID,
      rootLabel: "repo",
      path: "bound.ts",
      worktreeId: WT_BOUND_A,
      documentId: "doc-flush-a",
      intent: "permanent",
    });
    openFilesBuffer(PROJECT_ID, {
      rootId: ROOT_ID,
      rootLabel: "repo",
      path: "other.ts",
      worktreeId: WT_SIBLING_B,
      intent: "permanent",
    });
    await flushCheckoutBuffersBeforeRemoval(PROJECT_ID, WT_BOUND_A);

    const dismissed = vi.fn();
    const selectedCallback = vi.fn();
    const dirtyViaCallback = resolveWorktreeBlockerNavigation({
      projectId: PROJECT_ID,
      blocker: { message: "dirty", kind: "dirty_worktree", repoId: REPO_ID, worktreeId: WT_SIBLING_B },
      onSelectWorktreeId: selectedCallback,
      onDismissDialog: dismissed,
    });
    expect(dirtyViaCallback?.label).toBe("Inspect worktree changes");
    dirtyViaCallback?.run();
    expect(selectedCallback).toHaveBeenCalledWith(REPO_ID, WT_SIBLING_B);

    const dirtyViaStore = resolveWorktreeBlockerNavigation({
      projectId: PROJECT_ID,
      appStore,
      blocker: { message: "dirty", kind: "dirty_worktree", repoId: REPO_ID, worktreeId: WT_SIBLING_B },
      onDismissDialog: dismissed,
    });
    dirtyViaStore?.run();
    expect(readCheckoutInspectionState(appStore, PROJECT_ID, REPO_ID).inspectedWorktreeId).toBe(WT_SIBLING_B);

    const runningOpened: unknown[] = [];
    const disposeRunning = registerOpenRunningStageSink((req) => runningOpened.push(req));
    try {
      const activeWorkNav = resolveWorktreeBlockerNavigation({
        projectId: PROJECT_ID,
        blocker: { message: "running", kind: "active_work" },
        onDismissDialog: dismissed,
      });
      expect(activeWorkNav?.label).toBe("Open in Running");
      activeWorkNav?.run();
      expect(runningOpened).toEqual([{ projectId: PROJECT_ID }]);
    } finally {
      disposeRunning();
      resetOpenRunningForTests();
    }

    expect(
      resolveWorktreeBlockerNavigation({
        projectId: PROJECT_ID,
        blocker: { message: "unmanaged", kind: "unmanaged_worktree" },
        onDismissDialog: dismissed,
      }),
    ).toBeUndefined();
  });
});
