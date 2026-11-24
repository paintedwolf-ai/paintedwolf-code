// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  GitDiffStatItem,
  GitWorktreesView,
  NavigationTarget,
  SourceComparisonSelector,
} from "../../api/types.ts";
import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { fileEditStepsFromPart, foldFileEdits } from "../../chat/file-edit/file-edit-fold.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { ActionLinks } from "../../components/action/ActionLinks.tsx";
import { FileEditDiff } from "../../components/FileEditDiff.tsx";
import {
  pinRepoWorktreeSelection,
  readCheckoutInspectionState,
  reconcileRepoWorktreeSelection,
  selectRepoWorktreeById,
} from "../../components/git/git-worktree-inspection.ts";
import { SourcePathLink } from "../../components/source/SourcePathLink.tsx";
import { openSourceInFilesStage } from "../components/project-files-open.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import {
  openFilesBuffer,
  resetProjectFilesForTests,
} from "../documents/project-files-buffer-lifecycle.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import {
  applyOpenFilesSurfaceRequest,
  registerOpenFilesSurfaceSink,
  resetOpenFilesSurfaceForTests,
} from "../../platform/navigation/open-files-surface.ts";
import {
  registerOpenSourceProjectLookup,
  registerOpenSourceSink,
  resetOpenSourceForTests,
} from "../../platform/navigation/open-source.ts";
import { createAppStore } from "../../store/app-state.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import {
  activityFileOpenTarget,
  fileOpenTarget,
  gitChangeOpenTarget,
  gitDiffSideOpenTarget,
  openVerifiedFileTarget,
} from "./file-open-target.ts";
import {
  adoptFilesWorkspaceRoots,
  resetWorkspaceRootCheckoutsForTests,
} from "./files-checkout-scope.ts";

const PROJECT_ID = "11111111-1111-4111-8111-111111111111";
const ROOT_ID = "22222222-2222-4222-8222-222222222222";
const REPO_ID = "repo_main";
const PRIMARY_WT = "33333333-3333-4333-8333-333333333333";
const WORKER_WT = "44444444-4444-4444-8444-444444444444";

const ROOTS = [{ id: ROOT_ID, label: "repo", path: "/repos/primary", is_primary: true }];

function worktreesFixture(): GitWorktreesView {
  return {
    repo_id: REPO_ID,
    project_checkout_id: PRIMARY_WT,
    worktrees: [
      {
        worktree_id: PRIMARY_WT,
        kind: "primary",
        path: "/repos/primary",
        branch: "main",
        head_sha: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
        head_short: "aaaa111",
        detached: false,
        available: true,
        is_project_checkout: true,
        managed: false,
        locked: false,
        prunable: false,
        bound_to_session: false,
        created_by_chat: false,
        used_by_chat: false,
        worker_ids: [],
        status_cached: false,
      },
      {
        worktree_id: WORKER_WT,
        kind: "linked",
        path: "/repos/wt-worker",
        branch: "feat/worker",
        head_sha: "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222",
        head_short: "bbbb222",
        detached: false,
        available: true,
        is_project_checkout: false,
        managed: true,
        locked: false,
        prunable: false,
        bound_to_session: false,
        created_by_chat: true,
        used_by_chat: true,
        worker_ids: ["worker-1"],
        status_cached: false,
      },
    ],
  };
}

describe("WT06 checkout and revision open targets", () => {
  beforeEach(() => {
    resetProjectFilesForTests();
    resetWorkspaceRootCheckoutsForTests();
    resetOpenSourceForTests();
    resetOpenFilesSurfaceForTests();
  });

  afterEach(() => {
    cleanup();
    resetProjectFilesForTests();
    resetWorkspaceRootCheckoutsForTests();
    resetOpenSourceForTests();
    resetOpenFilesSurfaceForTests();
  });

  it("selects a worker checkout on deliberate Open in Files while keeping open buffers and ignoring background activity", async () => {
    const appStore = createAppStore();
    const view = worktreesFixture();
    reconcileRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, view);
    const primaryEntry = view.worktrees[0];
    if (!primaryEntry) throw new Error("expected primary worktree entry");
    pinRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, primaryEntry);
    adoptFilesWorkspaceRoots(PROJECT_ID, {
      workspace_id: "ws-primary",
      session_scoped: false,
      outside_root_id: "outside-1",
      inventory: { status: "ready", complete: true, roots_generation: 1, indexed_files: 1 },
      roots: [{
        id: ROOT_ID,
        path: "/repos/primary",
        branch_id: "",
        repo_id: REPO_ID,
        worktree_id: PRIMARY_WT,
        watch: { state: "live", recursive: true, unwatched_directories: 0 },
      }],
    });

    // Open a primary buffer AND pre-open the worker checkout's buffer with an existing documentId.
    const primaryKey = openFilesBuffer(PROJECT_ID, {
      rootId: ROOT_ID,
      rootLabel: "repo",
      path: "src/app.ts",
      intent: "permanent",
      documentId: "doc-primary",
    });
    const existingWorkerKey = openFilesBuffer(PROJECT_ID, {
      rootId: ROOT_ID,
      rootLabel: "repo",
      path: "src/app.ts",
      intent: "permanent",
      worktreeId: WORKER_WT,
      documentId: "doc-worker-existing",
    });

    registerOpenSourceProjectLookup(() => ({ roots: ROOTS }));
    registerOpenSourceSink((req) => {
      if (req.selectCheckout && req.worktreeId) {
        selectRepoWorktreeById(appStore, req.projectId, req.repoId, req.worktreeId, req.rootId);
      }
      if (!("action" in req)) {
        openSourceInFilesStage({
          request: req,
          rootLabelFor: () => "repo",
          navigate: () => {},
        });
      }
    });

    const workerLocation: NavigationTarget = {
      project_id: PROJECT_ID,
      repo_id: REPO_ID,
      worktree_id: WORKER_WT,
      root_id: ROOT_ID,
      path: "src/app.ts",
      entry_kind: "file",
    };
    const part = {
      id: "call-1",
      toolCallId: "call-1",
      messageId: "msg-1",
      tool: "edit",
      kind: "write",
      status: "completed",
      fileEdits: [
        fileEditPreviewFixture({
          path: "/repos/wt-worker/src/app.ts",
          before: "export const v = 1;\n",
          after: "export const v = 2;\n",
        }),
      ],
      sourceLocations: [workerLocation],
    } as unknown as ToolPartView;

    const steps = fileEditStepsFromPart(part);
    expect(steps[0]?.navigationTarget).toEqual(workerLocation);
    const fold = foldFileEdits(steps)[0];
    if (!fold) throw new Error("expected folded file edit");

    const client = stubFilesClient({
      readComparison: vi.fn(async () => ({
        op: "write" as const,
        summary: { added: 1, removed: 1, changed_hunks: 1 },
        before: { state: "content" as const, availability: "available" as const, size_bytes: 20, content: "export const v = 1;\n" },
        after: { state: "content" as const, availability: "available" as const, size_bytes: 20, content: "export const v = 2;\n" },
      })),
    });
    setLycaonClientForTest(client);

    // Background activity renders without changing pinned primary inspection selection.
    render(() => (
      <FileEditDiff
        projectId={PROJECT_ID}
        sessionId="sess-1"
        rootRefs={ROOTS}
        fold={fold}
      />
    ));
    expect(readCheckoutInspectionState(appStore, PROJECT_ID, REPO_ID).inspectedWorktreeId).toBe(PRIMARY_WT);

    // Deliberate click on Open in Files selects the worker checkout and focuses the preserved worker document.
    const toggle = screen.getByTestId("file-edit-diff-toggle");
    if (toggle.getAttribute("aria-expanded") === "false") {
      fireEvent.click(toggle);
    }
    const openBtn = await screen.findByRole("button", { name: "Open in Files" });
    fireEvent.click(openBtn);

    await waitFor(() => {
      expect(readCheckoutInspectionState(appStore, PROJECT_ID, REPO_ID).inspectedWorktreeId).toBe(WORKER_WT);
    });
    const state = projectFilesState(PROJECT_ID);
    expect(state.activeKey).toBe(existingWorkerKey);
    expect(state.byKey[existingWorkerKey]?.documentId).toBe("doc-worker-existing");
    expect(state.byKey[primaryKey]?.documentId).toBe("doc-primary");
    expect(state.order).toHaveLength(2);

    // Local/external fileOpenTarget resolves the worker checkout's physical path.
    const workerBuf = state.byKey[existingWorkerKey];
    if (!workerBuf) throw new Error("expected worker buffer");
    const localTarget = fileOpenTarget(workerBuf, ROOTS, 12, undefined, PROJECT_ID);
    expect(localTarget?.absolutePath).toBe("/repos/wt-worker/src/app.ts");
    expect(localTarget?.projectRoots).toContain("/repos/wt-worker");
  });

  it("binds Git change rows, renamed files, and both sides of staged and unstaged diffs to verified revisions or unavailability", async () => {
    registerOpenFilesSurfaceSink((req) => {
      applyOpenFilesSurfaceRequest(req, ROOTS);
    });

    // Git change row carries checkout identity.
    const changeTarget = gitChangeOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      file: {
        path: "src/new-name.ts",
        status: "R ",
        root_id: ROOT_ID,
        root_relative_path: "src/new-name.ts",
        old_path: "src/old-name.ts",
        old_root_id: ROOT_ID,
        old_root_relative_path: "src/old-name.ts",
      },
    });
    expect(changeTarget).toEqual({
      kind: "live",
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      rootId: ROOT_ID,
      path: "src/new-name.ts",
      entryKind: "file",
      selectCheckout: true,
    });

    const renamedStagedItem: GitDiffStatItem = {
      path: "src/new-name.ts",
      insertions: 2,
      deletions: 1,
      root_id: ROOT_ID,
      root_relative_path: "src/new-name.ts",
      old_path: "src/old-name.ts",
      old_root_id: ROOT_ID,
      old_root_relative_path: "src/old-name.ts",
      before_blob_oid: "1111111111111111111111111111111111111111",
      after_blob_oid: "2222222222222222222222222222222222222222",
    };

    const stagedBefore = gitDiffSideOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      stage: "staged",
      side: "before",
      item: renamedStagedItem,
      headSha: "bbbb2222",
    });
    const stagedAfter = gitDiffSideOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      stage: "staged",
      side: "after",
      item: renamedStagedItem,
      headSha: "bbbb2222",
    });
    expect(stagedBefore).toMatchObject({
      kind: "historical-diff",
      path: "src/old-name.ts",
      beforePath: "src/old-name.ts",
      side: "before",
      revision: "1111111111111111111111111111111111111111",
      binding: {
        kind: "git-blobs",
        beforeBlobOid: "1111111111111111111111111111111111111111",
        afterBlobOid: "2222222222222222222222222222222222222222",
      },
    });
    expect(stagedAfter).toMatchObject({
      kind: "historical-diff",
      path: "src/new-name.ts",
      side: "after",
      revision: "2222222222222222222222222222222222222222",
    });

    const unstagedItem: GitDiffStatItem = {
      path: "src/live.ts",
      insertions: 3,
      deletions: 1,
      root_id: ROOT_ID,
      root_relative_path: "src/live.ts",
      before_blob_oid: "3333333333333333333333333333333333333333",
      after_blob_oid: "",
      after_sha256: "sha-captured-at-diff-time",
    };

    const unstagedBefore = gitDiffSideOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      stage: "unstaged",
      side: "before",
      item: unstagedItem,
      headSha: "bbbb2222",
    });
    const unstagedAfter = gitDiffSideOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      stage: "unstaged",
      side: "after",
      item: unstagedItem,
      headSha: "bbbb2222",
    });
    expect(unstagedBefore).toMatchObject({
      kind: "historical-diff",
      side: "before",
      revision: "3333333333333333333333333333333333333333",
      binding: { kind: "git-blobs" },
    });
    expect(unstagedAfter).toMatchObject({
      kind: "historical-diff",
      side: "after",
      binding: {
        kind: "git-stage",
        stage: "unstaged",
        worktreeId: WORKER_WT,
        beforeBlobOid: "3333333333333333333333333333333333333333",
        expectedAfterSha256: "sha-captured-at-diff-time",
      },
    });

    // Simulate branch switch / move / deletion / pathname reuse:
    // 1. Staged blob comparison still resolves exact historical bytes from Git object store.
    // 2. Unstaged historical comparison after pathname reuse returns explicit unavailable after-side, NEVER today's file!
    let diskFileToday = "export const live = 'TODAY REUSED FILE';\n";
    const getProjectSource = vi.fn(async () => {
      throw new Error(`Historical diff must never read today's file (${diskFileToday})`);
    });
    const seenSelectors: SourceComparisonSelector[] = [];
    const readerHost = sourceReaderFixture(async (_pid, source) => {
      seenSelectors.push(source);
      if (source.kind === "blob") {
        return {
          in_range: true,
          location_changed: true,
          truncated: false,
          op: "rename",
          before: {
            path: "src/old_name.ts",
            state: "content",
            availability: "available",
            size_bytes: 18,
            content: "old historical v1\n",
          },
          after: {
            path: "src/new_name.ts",
            state: "content",
            availability: "available",
            size_bytes: 18,
            content: "new historical v2\n",
          },
        };
      }
      return {
        in_range: true,
        location_changed: false,
        truncated: false,
        op: "write",
        before: {
          path: "src/reused.ts",
          state: "content",
          availability: "available",
          size_bytes: 9,
          content: "index v1\n",
        },
        after: {
          path: "src/reused.ts",
          state: "unresolved",
          availability: "unavailable",
          reason: "content_unavailable",
          size_bytes: 0,
          content: "",
        },
      };
    });
    const client = stubFilesClient({
      ...readerHost.methods,
      getProjectSource,
    });

    diskFileToday = "export const live = 'MOVED AND REUSED';\n";
    if (!stagedAfter || !unstagedAfter) throw new Error("expected stagedAfter and unstagedAfter");
    const openedStaged = await openVerifiedFileTarget(stagedAfter, { client });
    expect(openedStaged.status).toBe("opened-historical");
    if (openedStaged.status === "opened-historical") {
      expect(openedStaged.version.availability).toBe("available");
      expect(openedStaged.version.source.kind).toBe("reader");
    }

    const openedReusedUnstaged = await openVerifiedFileTarget(unstagedAfter, { client });
    expect(openedReusedUnstaged.status).toBe("opened-historical");
    if (openedReusedUnstaged.status === "opened-historical") {
      expect(openedReusedUnstaged.version.availability).toBe("unavailable");
      expect(openedReusedUnstaged.version.beforeAvailability).toBe("available");
    }
    expect(getProjectSource).not.toHaveBeenCalled();
    expect(seenSelectors.some((s) => s.kind === "commit" && s.expected_after_sha256 === "sha-captured-at-diff-time")).toBe(true);
  });

  it("presents a checkout picker for a legacy ambiguous live path and an explicit unavailable state for unretained historical diffs", async () => {
    const appStore = createAppStore();
    reconcileRepoWorktreeSelection(appStore, PROJECT_ID, REPO_ID, worktreesFixture());
    registerOpenSourceProjectLookup(() => ({ roots: ROOTS }));
    registerOpenFilesSurfaceSink((req) => {
      applyOpenFilesSurfaceRequest(req, ROOTS);
    });

    const openedRequests: unknown[] = [];
    registerOpenSourceSink((req) => {
      openedRequests.push(req);
      if (req.selectCheckout && req.worktreeId) {
        selectRepoWorktreeById(appStore, req.projectId, req.repoId, req.worktreeId, req.rootId);
      }
    });

    const client = stubClient({
      listGitRepos: vi.fn(async () => ({
        repos: [{ repo_id: REPO_ID, label: "repo", available: true, root_ids: [ROOT_ID] }],
      })),
      listGitWorktrees: vi.fn(async () => worktreesFixture()),
      browseProjectSource: vi.fn(async (_pid, input: { rootId: string; dir: string }, _sid, worktreeId?: string | readonly string[]) => ({
        workspace_id: `ws-${String(worktreeId ?? "primary")}`,
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: [{ name: "shared.ts", is_dir: false }],
      })),
    });
    setLycaonClientForTest(client);

    // 1. Legacy activity card lacking sourceLocations probes checkouts and opens NavigationChooser when ambiguous!
    const legacyPart = {
      id: "legacy-1",
      toolCallId: "legacy-1",
      tool: "edit",
      kind: "write",
      status: "completed",
      args: { path: "src/shared.ts" },
    } as unknown as ToolPartView;

    render(() => (
      <ActionLinks
        projectId={PROJECT_ID}
        sessionId="sess-1"
        rootRefs={ROOTS}
        part={legacyPart}
        action={{
          label: "Edited src/shared.ts",
          status: "done",
          ran: { text: "src/shared.ts", noun: "path" },
          facts: [],
          row: [],
          links: [],
          opener: { kind: "file", path: "src/shared.ts" },
        }}
      />
    ));

    const openLink = screen.getByRole("button", { name: "Open in Files" });
    fireEvent.click(openLink);

    const chooser = await screen.findByTestId("navigation-chooser");
    expect(chooser.textContent).toContain("Project folder (primary)");
    expect(chooser.textContent).toContain("feat/worker (wt-worker)");

    const candidateButtons = chooser.querySelectorAll("button.den-context-menu__item");
    const workerChoice = [...candidateButtons].find((btn) => btn.textContent?.includes("feat/worker"));
    if (!workerChoice) throw new Error("expected worker checkout choice");
    fireEvent.click(workerChoice);

    await waitFor(() => {
      expect(readCheckoutInspectionState(appStore, PROJECT_ID, REPO_ID).inspectedWorktreeId).toBe(WORKER_WT);
    });
    expect(openedRequests).toContainEqual(
      expect.objectContaining({
        projectId: PROJECT_ID,
        rootId: ROOT_ID,
        path: "src/shared.ts",
        repoId: REPO_ID,
        worktreeId: WORKER_WT,
        selectCheckout: true,
      }),
    );

    // 2. Legacy historical event lacking retained revision produces explicit unavailable state, never today's file.
    const unretainedHistorical = activityFileOpenTarget({
      projectId: PROJECT_ID,
      mode: "historical-diff",
      path: "src/shared.ts",
      rootId: ROOT_ID,
    });
    expect(unretainedHistorical).toMatchObject({
      kind: "historical-diff",
      binding: { kind: "unavailable" },
    });
    if (!unretainedHistorical) throw new Error("expected unretained historical target");
    const historicalRes = await openVerifiedFileTarget(unretainedHistorical, { client });
    expect(historicalRes.status).toBe("opened-historical");
    if (historicalRes.status === "opened-historical") {
      expect(historicalRes.version.source).toEqual({ kind: "unavailable" });
      expect(historicalRes.version.availability).toBe("unavailable");
    }

    // Also verify SourcePathLink with explicit candidateTargets opens chooser directly.
    cleanup();
    render(() => (
      <SourcePathLink
        projectId={PROJECT_ID}
        path="src/shared.ts"
        rootRefs={ROOTS}
        candidateTargets={[
          { project_id: PROJECT_ID, root_id: ROOT_ID, repo_id: REPO_ID, worktree_id: PRIMARY_WT, path: "src/shared.ts", entry_kind: "file" },
          { project_id: PROJECT_ID, root_id: ROOT_ID, repo_id: REPO_ID, worktree_id: WORKER_WT, path: "src/shared.ts", entry_kind: "file" },
        ]}
      />
    ));
    fireEvent.click(screen.getByTestId("source-path-link"));
    expect(await screen.findByTestId("navigation-chooser")).toBeTruthy();
  });

  it("resolves live, git-blob, effect, version, and ambiguous activity and diff open targets", () => {
    const liveDiff = gitDiffSideOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      stage: "working",
      side: "after",
      mode: "live",
      item: { path: "src/a.ts", status: "M", root_id: ROOT_ID, root_relative_path: "src/a.ts" } as GitDiffStatItem,
    });
    expect(liveDiff).toMatchObject({ kind: "live", worktreeId: WORKER_WT, selectCheckout: true });

    const blobAfter = gitDiffSideOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      stage: "working",
      side: "after",
      item: {
        path: "src/a.ts",
        status: "A",
        root_id: ROOT_ID,
        root_relative_path: "src/a.ts",
        after_blob_oid: "aaaa1111",
      } as GitDiffStatItem,
    });
    expect(blobAfter).toMatchObject({ kind: "historical-diff", revision: "aaaa1111", binding: { kind: "git-blobs" } });

    const uncapturedWorking = gitDiffSideOpenTarget({
      projectId: PROJECT_ID,
      repoId: REPO_ID,
      worktreeId: WORKER_WT,
      stage: "working",
      side: "after",
      item: { path: "src/a.ts", status: "M", root_id: ROOT_ID, root_relative_path: "src/a.ts" } as GitDiffStatItem,
    });
    expect(uncapturedWorking).toMatchObject({ kind: "historical-diff", binding: { kind: "unavailable" } });

    const singleLoc: NavigationTarget = {
      project_id: PROJECT_ID,
      root_id: ROOT_ID,
      repo_id: REPO_ID,
      worktree_id: WORKER_WT,
      path: "src/one.ts",
      entry_kind: "file",
    };
    const otherLoc: NavigationTarget = {
      project_id: PROJECT_ID,
      root_id: ROOT_ID,
      repo_id: REPO_ID,
      worktree_id: PRIMARY_WT,
      path: "src/two.ts",
      entry_kind: "file",
    };

    expect(
      activityFileOpenTarget({
        projectId: PROJECT_ID,
        mode: "live",
        sourceLocations: [singleLoc],
      }),
    ).toMatchObject({ kind: "live", path: "src/one.ts", worktreeId: WORKER_WT, selectCheckout: true });

    expect(
      activityFileOpenTarget({
        projectId: PROJECT_ID,
        mode: "live",
        path: "src/two.ts",
        sourceLocations: [singleLoc, otherLoc],
      }),
    ).toMatchObject({ kind: "live", path: "src/two.ts", worktreeId: PRIMARY_WT, selectCheckout: true });

    expect(
      activityFileOpenTarget({
        projectId: PROJECT_ID,
        mode: "live",
        path: "src/shared.ts",
        sourceLocations: [
          { ...singleLoc, path: "src/shared.ts" },
          { ...otherLoc, path: "src/shared.ts" },
        ],
      }),
    ).toMatchObject({ kind: "ambiguous" });

    expect(
      activityFileOpenTarget({
        projectId: PROJECT_ID,
        mode: "live",
        path: "src/bare.ts",
        rootId: ROOT_ID,
      }),
    ).toMatchObject({ kind: "live", path: "src/bare.ts", rootId: ROOT_ID });

    expect(
      activityFileOpenTarget({
        projectId: PROJECT_ID,
        mode: "historical-diff",
        effectId: "eff-1",
        sourceLocations: [singleLoc],
      }),
    ).toMatchObject({ kind: "historical-diff", revision: "eff-1", binding: { kind: "effect", effectId: "eff-1" } });

    expect(
      activityFileOpenTarget({
        projectId: PROJECT_ID,
        mode: "historical-diff",
        versionId: "ver-1",
        sourceLocations: [singleLoc],
      }),
    ).toMatchObject({ kind: "historical-diff", revision: "ver-1", binding: { kind: "version", versionId: "ver-1" } });
  });
});
