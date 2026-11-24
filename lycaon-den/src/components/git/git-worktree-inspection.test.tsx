// @vitest-environment jsdom
import "../../test/document-outbox-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import axe from "axe-core";
import type {
  GitBranchEntry,
  GitRepoEntry,
  GitWorktreeEntry,
  GitWorktreesView,
  GitWorktreeView,
  SourceWorkspace,
  SubmitEditorDocumentUpdateRequest,
  SyncEditorDocumentRequest,
} from "../../api/types.ts";
import type { GitWorkspaceStatus } from "../../chat/actions/git-workspace-status.ts";
import { createAppStore } from "../../store/app-state.ts";
import { loaded } from "../../store/load-state.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { GitStrip } from "../GitStrip.tsx";
import type { GitScope } from "../git-repo-scope.ts";
import { FilesCheckoutPicker } from "./FilesCheckoutPicker.tsx";
import {
  clearRepoWorktreePin,
  groupWorktreeEntries,
  pinRepoWorktreeSelection,
  readCheckoutInspectionState,
  reconcileRepoWorktreeSelection,
} from "./git-worktree-inspection.ts";
import {
  adoptFilesWorkspaceRoots,
  resetWorkspaceRootCheckoutsForTests,
  selectedProjectWorktreeIds,
} from "../../files/source/files-checkout-scope.ts";
import { openFileBufferSource } from "../../files/source/files-source-read.ts";
import {
  configureEditorDocuments,
  editorReplica,
  presentOpenedDocument,
  receiveOpenedEditorDocument,
  resetEditorDocumentsForTests,
} from "../../files/documents/editor-document.ts";
import {
  openFilesBuffer,
  resetProjectFilesForTests,
} from "../../files/documents/project-files-buffer-lifecycle.ts";
import {
  applyFilesBufferEditorDocument,
  applyFilesBufferLoad,
} from "../../files/documents/project-files-buffer-io.ts";
import {
  filesBufferText,
  projectFilesState,
} from "../../files/documents/files-buffer-state.ts";

const projectId = "11111111-1111-4111-8111-111111111111";
const sessionId = "22222222-2222-4222-8222-222222222222";
const repoAlphaId = "repo-alpha";
const repoBetaId = "repo-beta";
const wtPrimary = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const wtWorkerA = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const wtWorkerB = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const wtDetached = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";

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

function makeWorkspace(worktreeId: string, branchId: string, rootPath: string, workspaceId: string): SourceWorkspace {
  return {
    workspace_id: workspaceId,
    session_scoped: branchId !== "",
    outside_root_id: "root-outside",
    inventory: { status: "ready", complete: true, roots_generation: 1, indexed_files: 1 },
    roots: [{
      id: "root-1",
      path: rootPath,
      branch_id: branchId,
      repo_id: repoAlphaId,
      worktree_id: worktreeId,
      watch: { state: "live", recursive: true, unwatched_directories: 0 },
    }],
  };
}

function makeEntry(
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

function makeStatus(overrides: Partial<GitWorkspaceStatus> = {}): GitWorkspaceStatus {
  return {
    available: true,
    repo_id: repoAlphaId,
    worktree_id: wtPrimary,
    root_ids: ["root-1"],
    branch: "main",
    head_short: "1111aaa",
    ahead: 0,
    behind: 0,
    dirty: true,
    staged_count: 0,
    unstaged_count: 1,
    changed_count: 1,
    upstream: "origin/main",
    files: [
      {
        path: "src/index.ts",
        status: " M",
        root_id: "root-1",
        root_relative_path: "src/index.ts",
      },
    ],
    ...overrides,
  };
}

const unboundSessionWorktree: GitWorktreeView = {
  bound: false,
  session_id: sessionId,
  dirty: false,
  ahead_of_base: 0,
  behind_base: 0,
};

function baseStripProps() {
  return {
    projectId,
    sessionId,
    onInit: vi.fn(),
    onCommit: vi.fn(async () => true),
    onStash: vi.fn(),
    onDraftMessage: vi.fn(async () => ""),
    onListBranches: vi.fn(async (): Promise<GitBranchEntry[]> => [{ name: "main", current: true }]),
    onCheckout: vi.fn(async () => {}),
    onDiscard: vi.fn(),
    onPush: vi.fn(),
    onPull: vi.fn(),
    onGroupCommit: vi.fn(),
    onScopePin: vi.fn((_pin: GitScope | null) => {}),
    onSelectRepo: vi.fn(),
    onRefreshWorktree: vi.fn(async () => unboundSessionWorktree),
    onBindWorktree: vi.fn(async () => unboundSessionWorktree),
    onLandWorktree: vi.fn(async () => ({
      landed: true,
      base_branch: "main",
      commits: 1,
      reason: "",
      conflicts: [],
    })),
    onUnbindWorktree: vi.fn(async () => {}),
  };
}

describe("WT05 worktree inspection UI across Git and Files", () => {
  let cleanupDocs: (() => void) | undefined;

  beforeEach(() => {
    resetProjectFilesForTests();
    resetEditorDocumentsForTests();
    resetWorkspaceRootCheckoutsForTests();
  });

  afterEach(() => {
    cleanup();
    cleanupDocs?.();
    cleanupDocs = undefined;
    resetEditorDocumentsForTests();
    resetProjectFilesForTests();
    resetWorkspaceRootCheckoutsForTests();
  });

  it("groups project folder, chat checkouts, and other checkouts without duplicates, marks Bound to chat strictly on actual binding, and expands duplicate basenames with zero axe violations", async () => {
    const entries: GitWorktreeEntry[] = [
      makeEntry({
        worktree_id: wtPrimary,
        kind: "primary",
        path: "/repos/wt-main/app",
        branch: "main",
        head_short: "1111aaa",
        is_project_checkout: true,
        used_by_chat: true, // Also used by chat; must appear only once under Project folder
        status_cached: true,
        dirty: false,
      }),
      makeEntry({
        worktree_id: wtWorkerA,
        path: "/repos/wt-a/app",
        branch: "feat/worker-a",
        head_short: "2222bbb",
        bound_to_session: true,
        used_by_chat: true,
        status_cached: true,
        dirty: true,
        changed_count: 3,
      }),
      makeEntry({
        worktree_id: wtWorkerB,
        path: "/repos/wt-b/app",
        branch: "feat/worker-b",
        head_short: "3333ccc",
        created_by_chat: true,
        used_by_chat: true,
        status_cached: false, // Uncached sibling must not fabricate a change count
      }),
      makeEntry({
        worktree_id: wtDetached,
        path: "/repos/external/spike",
        detached: true,
        head_short: "9abc123",
      }),
    ];

    const grouped = groupWorktreeEntries(entries, wtWorkerA);
    expect(grouped.project.map((r) => r.entry.worktree_id)).toEqual([wtPrimary]);
    expect(grouped.chat.map((r) => r.entry.worktree_id)).toEqual([wtWorkerA, wtWorkerB]);
    expect(grouped.other.map((r) => r.entry.worktree_id)).toEqual([wtDetached]);
    expect(grouped.chat[0]?.boundToChat).toBe(true);
    expect(grouped.chat[1]?.boundToChat).toBe(false);
    expect(grouped.chat[0]?.changeSummary).toBe("3 changed");
    expect(grouped.chat[1]?.changeSummary).toBeUndefined();
    expect(grouped.other[0]?.branchLabel).toBe("Detached HEAD (9abc123)");

    const appStore = createAppStore();
    const view: GitWorktreesView = {
      repo_id: repoAlphaId,
      project_checkout_id: wtPrimary,
      bound_worktree_id: wtWorkerA,
      worktrees: entries,
    };

    const props = baseStripProps();
    const rendered = render(() => (
      <GitStrip
        {...props}
        appStore={appStore}
        status={loaded(makeStatus({ worktree_id: wtWorkerA, branch: "feat/worker-a" }))}
        onListWorktrees={async () => view}
      />
    ));

    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-directory").textContent).toBe("wt-a/app"),
    );

    fireEvent.click(screen.getByTestId("git-branch"));
    await waitFor(() => screen.getByTestId("git-worktree-selector"));
    await assertNoAxeViolations(rendered.container);

    expect(screen.getByTestId("git-worktrees-group-project")).toBeTruthy();
    expect(screen.getByTestId("git-worktrees-group-chat")).toBeTruthy();
    expect(screen.getByTestId("git-worktrees-group-other")).toBeTruthy();

    // "Bound to chat" appears only on wtWorkerA, never on wtWorkerB
    const boundBadges = screen.getAllByTestId("git-worktree-badge-bound");
    expect(boundBadges).toHaveLength(1);

    // Colliding basenames ("app") show shortest distinguishing suffix first and expand on demand
    const expandButtons = screen.getAllByTestId("git-worktree-expand-path");
    expect(expandButtons).toHaveLength(3);
    const firstExpand = expandButtons[0];
    if (!firstExpand) throw new Error("expected expand button");
    fireEvent.click(firstExpand);
    expect(screen.getByTestId("git-worktrees-group-project").textContent).toContain(
      "/repos/wt-main/app",
    );

    // Selecting the detached worktree updates display to explicit Detached HEAD (9abc123)
    const detachedBtn = screen
      .getAllByTestId("git-worktree-option")
      .find((el) => el.getAttribute("data-worktree-id") === wtDetached);
    if (!detachedBtn) throw new Error("expected detached option");
    fireEvent.click(detachedBtn);
    await waitFor(() =>
      expect(screen.getByTestId("git-branch").textContent).toContain("Detached HEAD (9abc123)"),
    );
    expect(screen.getByTestId("git-checkout-mode-linked").textContent).toBe("Linked worktree");
    expect(props.onBindWorktree).not.toHaveBeenCalled();
    expect(props.onCheckout).not.toHaveBeenCalled();
  });

  it("shares selection bidirectionally between Git and Files, opens Files on request, and preserves open tabs, cursors, and unsaved drafts", async () => {
    const appStore = createAppStore();
    const trunkDoc = new DocumentFixture("saved line\n", {
      id: "doc-trunk",
      project_id: projectId,
      workspace_id: "ws-primary",
      root_id: "root-1",
      path: "src/notes.md",
      file_id: "file-notes",
      branch_id: "",
    });

    const entries: GitWorktreeEntry[] = [
      makeEntry({
        worktree_id: wtPrimary,
        kind: "primary",
        path: "/repos/wt-main/app",
        branch: "main",
        head_short: "1111aaa",
        is_project_checkout: true,
      }),
      makeEntry({
        worktree_id: wtWorkerB,
        path: "/repos/wt-b/app",
        branch: "feat/worker-b",
        head_short: "3333ccc",
        used_by_chat: true,
      }),
    ];
    const view: GitWorktreesView = {
      repo_id: repoAlphaId,
      project_checkout_id: wtPrimary,
      worktrees: entries,
    };

    const statusCalls: Array<string | undefined> = [];
    let listCalls = 0;
    const client = stubClient({
      openEditorDocument: async () => trunkDoc.snapshot(),
      syncEditorDocument: async (pid: string, docId: string, req: SyncEditorDocumentRequest) =>
        trunkDoc.sync(pid, docId, req),
      submitEditorDocumentUpdate: async (pid: string, docId: string, req: SubmitEditorDocumentUpdateRequest) =>
        trunkDoc.submit(pid, docId, req),
      publishEditorDocumentPresence: vi.fn(async () => undefined),
      listGitRepos: async () => ({
        repos: [
          {
            repo_id: repoAlphaId,
            label: "app",
            available: true,
            root_ids: ["root-1"],
            branch: "main",
            ahead: 0,
            behind: 0,
            dirty: false,
            staged_count: 0,
            unstaged_count: 0,
            changed_count: 0,
          },
        ],
      }),
      listGitWorktrees: async () => {
        listCalls++;
        return view;
      },
      getGitStatus: async (
        _pid: string,
        _rid: string,
        _sid?: string,
        worktreeId?: string,
      ) => {
        statusCalls.push(worktreeId);
        return makeStatus({
          worktree_id: worktreeId ?? wtPrimary,
          branch: worktreeId === wtWorkerB ? "feat/worker-b" : "main",
        });
      },
    });

    cleanupDocs = configureEditorDocuments(
      projectId,
      () => client,
      () => undefined,
      {
        get: (k) => projectFilesState(projectId).byKey[k],
        all: () => Object.values(projectFilesState(projectId).byKey),
      },
      () => "ws-primary",
    );

    adoptFilesWorkspaceRoots(projectId, makeWorkspace(wtPrimary, "", "/repos/wt-main/app", "ws-primary"));
    let trunkKey = openFilesBuffer(projectId, {
      rootId: "root-1",
      rootLabel: "app",
      path: "src/notes.md",
      revealLine: 4,
      revealColumn: 7,
      intent: "permanent",
    });
    const trunkBuf = projectFilesState(projectId).byKey[trunkKey];
    if (!trunkBuf) throw new Error("expected trunk buffer");
    const openedTrunk = await openFileBufferSource(client, projectId, trunkBuf);
    const presentedTrunk = presentOpenedDocument(openedTrunk.document, openedTrunk.retained);
    if (!openedTrunk.document) throw new Error("expected trunk document");
    const adoptedTrunk = await receiveOpenedEditorDocument(
      projectId,
      client,
      openedTrunk.document,
      presentedTrunk?.confirmed,
      openedTrunk.reservation,
    );
    if (!adoptedTrunk) throw new Error("expected adopted trunk document");
    trunkKey = applyFilesBufferLoad(projectId, trunkKey, openedTrunk.source, presentedTrunk?.loaded);
    applyFilesBufferEditorDocument(projectId, trunkKey, adoptedTrunk);
    openedTrunk.reservation?.release();

    const replica = editorReplica(trunkDoc.wire.id);
    if (!replica) throw new Error("expected trunk replica");
    replica.replaceLocal("unsaved draft in trunk\n");
    await replica.flush();

    const [status, setStatus] = createSignal(makeStatus());
    const onOpenInFiles = vi.fn();
    const props = baseStripProps();

    const rendered = render(() => (
      <div>
        <GitStrip
          {...props}
          appStore={appStore}
          status={loaded(status())}
          onListWorktrees={async () => view}
          onSelectWorktree={async (_rid, entry) => {
            setStatus(makeStatus({ worktree_id: entry.worktree_id, branch: entry.branch }));
          }}
          onOpenInFiles={onOpenInFiles}
        />
        <FilesCheckoutPicker projectId={projectId} client={client} appStore={appStore} />
      </div>
    ));

    const filesTrigger = await waitFor(() => screen.getByTestId("files-checkout-picker"));
    await assertNoAxeViolations(rendered.container);

    // Direction 1: Select wtWorkerB in Git -> Files picker and checkout scope follow immediately
    fireEvent.click(screen.getByTestId("git-branch"));
    await waitFor(() => screen.getByTestId("git-worktree-selector"));
    const optionB = screen
      .getAllByTestId("git-worktree-option")
      .find((el) => el.getAttribute("data-worktree-id") === wtWorkerB);
    if (!optionB) throw new Error("expected worker B option");
    fireEvent.click(optionB);

    await waitFor(() => expect(filesTrigger.textContent).toContain("feat/worker-b"));
    expect(selectedProjectWorktreeIds(appStore, projectId)).toEqual([wtWorkerB]);

    // Explicit commit and discard targets show the selected linked worktree
    expect(screen.getByTestId("git-commit-target").textContent).toContain("feat/worker-b");
    fireEvent.click(screen.getByTestId("git-discard"));
    await waitFor(() => screen.getByTestId("git-discard-modal"));
    expect(screen.getByTestId("git-discard-target").textContent).toContain("feat/worker-b");
    fireEvent.click(screen.getByTestId("git-discard-cancel"));

    // Open in Files from Git
    fireEvent.click(screen.getByTestId("git-open-in-files"));
    expect(onOpenInFiles).toHaveBeenCalledOnce();

    // Direction 2: Switch back to Project folder from Files picker -> Git selection updates
    fireEvent.click(filesTrigger);
    const popover = await waitFor(() => screen.getByTestId("files-checkout-popover"));
    const optionMain = within(popover)
      .getAllByTestId("git-worktree-option")
      .find((el) => el.getAttribute("data-worktree-id") === wtPrimary);
    if (!optionMain) throw new Error("expected primary option");
    fireEvent.click(optionMain);

    await waitFor(() => expect(filesTrigger.textContent).toContain("Project folder"));
    expect(selectedProjectWorktreeIds(appStore, projectId)).toEqual([wtPrimary]);
    expect(statusCalls).toContain(wtPrimary);
    expect(listCalls).toBeGreaterThan(0);

    // Trunk unsaved draft, open tab, and reveal/cursor metadata were preserved untouched across switches
    const preserved = projectFilesState(projectId).byKey[trunkKey];
    if (!preserved) throw new Error("expected preserved buffer");
    expect(projectFilesState(projectId).order).toContain(trunkKey);
    expect(filesBufferText(preserved)).toBe("unsaved draft in trunk\n");
    expect(preserved.dirty).toBe(true);
    expect(preserved.revealLine).toBe(4);
    expect(preserved.revealColumn).toBe(7);
  });

  it("keeps independent selections across two windows and two repositories, does not auto-switch on worker fan-out, survives actor changes when pinned, and disables actions when a pinned checkout disappears", async () => {
    const windowOne = createAppStore();
    const windowTwo = createAppStore();

    const primaryEntry = makeEntry({
      worktree_id: wtPrimary,
      kind: "primary",
      path: "/repos/alpha",
      branch: "main",
      is_project_checkout: true,
    });
    const workerAEntry = makeEntry({
      worktree_id: wtWorkerA,
      path: "/repos/alpha-wt-a",
      branch: "feat/a",
      used_by_chat: true,
    });
    const workerBEntry = makeEntry({
      worktree_id: wtWorkerB,
      path: "/repos/alpha-wt-b",
      branch: "feat/b",
      used_by_chat: true,
    });

    // Worker fan-out (2 active worker checkouts, unbound session): keeps Project folder instead of guessing
    const fanoutView: GitWorktreesView = {
      repo_id: repoAlphaId,
      project_checkout_id: wtPrimary,
      worktrees: [primaryEntry, workerAEntry, workerBEntry],
    };
    const fanout = reconcileRepoWorktreeSelection(windowOne, projectId, repoAlphaId, fanoutView);
    expect(fanout.inspectedWorktreeId).toBe(wtPrimary);
    expect(fanout.pinned).toBe(false);

    // Two windows + two repositories stay independent
    pinRepoWorktreeSelection(windowOne, projectId, repoAlphaId, workerAEntry);
    pinRepoWorktreeSelection(windowOne, projectId, repoBetaId, workerBEntry);
    reconcileRepoWorktreeSelection(windowTwo, projectId, repoAlphaId, fanoutView);

    expect(readCheckoutInspectionState(windowOne, projectId, repoAlphaId).inspectedWorktreeId).toBe(wtWorkerA);
    expect(readCheckoutInspectionState(windowOne, projectId, repoBetaId).inspectedWorktreeId).toBe(wtWorkerB);
    expect(readCheckoutInspectionState(windowTwo, projectId, repoAlphaId).inspectedWorktreeId).toBe(wtPrimary);

    // Pinned selection survives actor binding changes in the repo
    const actorBoundView: GitWorktreesView = {
      ...fanoutView,
      bound_worktree_id: wtWorkerB,
    };
    const afterActorChange = reconcileRepoWorktreeSelection(windowOne, projectId, repoAlphaId, actorBoundView);
    expect(afterActorChange.inspectedWorktreeId).toBe(wtWorkerA);
    expect(afterActorChange.pinned).toBe(true);

    // Clearing pin resumes following the bound checkout
    const unpinned = clearRepoWorktreePin(windowOne, projectId, repoAlphaId, actorBoundView);
    expect(unpinned.inspectedWorktreeId).toBe(wtWorkerB);
    expect(unpinned.pinned).toBe(false);

    // Re-pin wtWorkerA, then remove wtWorkerA from disk -> unavailable banner + disabled Git actions
    pinRepoWorktreeSelection(windowOne, projectId, repoAlphaId, workerAEntry);
    const missingPinView: GitWorktreesView = {
      repo_id: repoAlphaId,
      project_checkout_id: wtPrimary,
      worktrees: [primaryEntry, workerBEntry],
    };
    const [currentView, setCurrentView] = createSignal<GitWorktreesView>(missingPinView);
    const props = baseStripProps();
    const repos: GitRepoEntry[] = [
      {
        repo_id: repoAlphaId,
        label: "alpha",
        available: true,
        root_ids: ["root-1"],
        branch: "main",
        ahead: 1,
        behind: 1,
        dirty: true,
        staged_count: 0,
        unstaged_count: 1,
        changed_count: 1,
      },
    ];

    render(() => (
      <GitStrip
        {...props}
        appStore={windowOne}
        repos={repos}
        activeRepoId={repoAlphaId}
        status={loaded(makeStatus({ ahead: 1, behind: 1, dirty: true }))}
        onListWorktrees={async () => currentView()}
        onOpenInFiles={vi.fn()}
      />
    ));

    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-mode-unavailable").textContent).toBe(
        "Unavailable checkout",
      ),
    );
    expect((screen.getByTestId("git-pull") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("git-push") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("git-stash") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("git-discard") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("git-message") as HTMLTextAreaElement).disabled).toBe(true);

    // Open selector while chat is streaming (gitBusy is false) and see unavailable notice + disabled Open in Files
    fireEvent.click(screen.getByTestId("git-branch"));
    await waitFor(() => screen.getByTestId("git-checkout-target-unavailable"));
    expect((screen.getByTestId("git-open-in-files") as HTMLButtonElement).disabled).toBe(true);

    // Selecting a current checkout clears unavailable state and re-enables actions
    const chooseMain = screen
      .getAllByTestId("git-worktree-option")
      .find((el) => el.getAttribute("data-worktree-id") === wtPrimary);
    if (!chooseMain) throw new Error("expected primary checkout option");
    setCurrentView(missingPinView);
    fireEvent.click(chooseMain);

    await waitFor(() =>
      expect(screen.getByTestId("git-checkout-mode-project").textContent).toBe("Project folder"),
    );
    expect((screen.getByTestId("git-stash") as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByTestId("git-discard") as HTMLButtonElement).disabled).toBe(false);
  });

  it("lists many sibling worktrees without issuing eager per-sibling status queries", async () => {
    const appStore = createAppStore();
    const siblings: GitWorktreeEntry[] = [
      makeEntry({
        worktree_id: wtPrimary,
        kind: "primary",
        path: "/repos/main",
        branch: "main",
        is_project_checkout: true,
      }),
      ...Array.from({ length: 12 }, (_, idx) =>
        makeEntry({
          worktree_id: `00000000-0000-4000-8000-${String(idx + 1).padStart(12, "0")}`,
          path: `/repos/sibling-${idx + 1}`,
          branch: `feat/sibling-${idx + 1}`,
        }),
      ),
    ];

    const getGitStatus = vi.fn();
    const listGitWorktrees = vi.fn(async (): Promise<GitWorktreesView> => ({
      repo_id: repoAlphaId,
      project_checkout_id: wtPrimary,
      worktrees: siblings,
    }));

    const client = stubClient({
      listGitRepos: async () => ({
        repos: [
          {
            repo_id: repoAlphaId,
            label: "main",
            available: true,
            root_ids: ["root-1"],
            branch: "main",
            ahead: 0,
            behind: 0,
            dirty: false,
            staged_count: 0,
            unstaged_count: 0,
            changed_count: 0,
          },
        ],
      }),
      listGitWorktrees,
      getGitStatus,
    });

    render(() => (
      <FilesCheckoutPicker projectId={projectId} client={client} appStore={appStore} />
    ));

    const trigger = await waitFor(() => screen.getByTestId("files-checkout-picker"));
    fireEvent.click(trigger);
    await waitFor(() => expect(screen.getAllByTestId("git-worktree-option")).toHaveLength(13));
    expect(getGitStatus).not.toHaveBeenCalled();
  });
});
