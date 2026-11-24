// @vitest-environment jsdom
import "../../test/document-outbox-fixture.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import type { SourceWorkspace, SubmitEditorDocumentUpdateRequest, SyncEditorDocumentRequest } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { getAppStateSnapshot, resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { adoptFilesWorkspaceRoots, bufferCheckoutWorktreeId, selectedProjectWorktreeIds } from "../source/files-checkout-scope.ts";
import { createAppStore } from "../../store/app-state.ts";
import { applySourceChangesEvent } from "../source/source-events.ts";
import { openFileBufferSource } from "../source/files-source-read.ts";
import { filesTabNames } from "../tabs/files-tab-names.ts";
import { setBufferLiveHandlers } from "./buffer-live.ts";
import {
  configureEditorDocuments,
  editorReplica,
  presentOpenedDocument,
  receiveOpenedEditorDocument,
  resetEditorDocumentsForTests,
} from "./editor-document.ts";
import { createFileDocumentCommands } from "./file-document-commands.ts";
import {
  applyFilesBufferDocumentStatus,
  filesBufferText,
  markFilesBufferCheckoutUnavailable,
  projectFilesState,
} from "./files-buffer-state.ts";
import { saveProjectFileDocument } from "./project-file-document-save.ts";
import {
  flushFilesHotExitToDisk,
  restoreFilesHotExitForProject,
  scheduleFilesHotExitPersist,
  syncFilesHotExitFromSnapshot,
} from "./files-hot-exit.ts";
import { applyFilesBufferEditorDocument, applyFilesBufferLoad } from "./project-files-buffer-io.ts";
import { openFilesBuffer, resetProjectFilesForTests } from "./project-files-buffer-lifecycle.ts";
import { setFilesActiveBuffer } from "./project-files-buffers.ts";

const projectId = "proj-wt-buffers";
const primaryWorktreeId = "11111111-1111-4111-8111-111111111111";
const siblingWorktreeId = "22222222-2222-4222-8222-222222222222";

const roots = [{
  id: "root-repo",
  path: "/work/primary",
  label: "repo",
  is_primary: true,
  added_at: "2026-01-01T00:00:00Z",
  kind: "attached" as const,
  admitted: true,
  admission_code: null,
}];

function makeWorkspace(worktreeId: string, branchId: string, path: string, workspaceId: string): SourceWorkspace {
  return {
    workspace_id: workspaceId,
    session_scoped: false,
    outside_root_id: "outside",
    inventory: { status: "ready", complete: true, roots_generation: 1, indexed_files: 1 },
    roots: [{
      id: "root-repo",
      path,
      branch_id: branchId,
      repo_id: "repo-1",
      worktree_id: worktreeId,
      watch: { state: "live", recursive: true, unwatched_directories: 0 },
    }],
  };
}

describe("multi-checkout Files buffers, hot exit, and unavailable checkout recovery", () => {
  let cleanupConnection: (() => void) | undefined;

  beforeEach(() => {
    resetProjectFilesForTests();
    resetEditorDocumentsForTests();
    syncFilesHotExitFromSnapshot();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  });

  afterEach(() => {
    cleanupConnection?.();
    cleanupConnection = undefined;
    resetEditorDocumentsForTests();
    resetProjectFilesForTests();
  });

  it("reads per-project selected checkout UUIDs from shared gitWorktreeTargets state", () => {
    const appStore = createAppStore();
    expect(selectedProjectWorktreeIds(appStore, projectId)).toEqual([]);
    appStore.actions.setGitWorktreeTarget(projectId, "repo-1", siblingWorktreeId);
    appStore.actions.setGitWorktreeTarget("other-project", "repo-1", primaryWorktreeId);
    expect(selectedProjectWorktreeIds(appStore, projectId)).toEqual([siblingWorktreeId]);
    appStore.actions.clearGitWorktreeTargets(projectId);
    expect(selectedProjectWorktreeIds(appStore, projectId)).toEqual([]);
  });

  it("keeps same-named files from primary and sibling checkouts distinct across switches, source events, and hot-exit restart", async () => {
    const primaryHost = new DocumentFixture("export const source = 'primary';\n", {
      id: "doc-primary",
      project_id: projectId,
      workspace_id: "ws-primary",
      root_id: "root-repo",
      path: "src/app.ts",
      file_id: "file-primary",
      branch_id: "",
    });

    const siblingHost = new DocumentFixture("export const source = 'sibling';\n", {
      id: "doc-sibling",
      project_id: projectId,
      workspace_id: "ws-sibling",
      root_id: "root-repo",
      path: "src/app.ts",
      file_id: "file-sibling",
      branch_id: `worktree:${siblingWorktreeId}`,
    });

    let activeWorkspaceId = "ws-primary";
    const openEditorDocument = vi.fn(async (_pid: string, req: { document_id?: string; path?: string }, _sid?: string, wt?: string | readonly string[]) => {
      if (req.document_id === primaryHost.wire.id) return primaryHost.snapshot();
      if (req.document_id === siblingHost.wire.id) return siblingHost.snapshot();
      const targetWt = Array.isArray(wt) ? wt[0] : wt;
      if (targetWt === siblingWorktreeId) return siblingHost.snapshot();
      return primaryHost.snapshot();
    });

    const client = stubClient({
      openEditorDocument,
      syncEditorDocument: async (pid: string, docId: string, req: SyncEditorDocumentRequest) =>
        docId === siblingHost.wire.id ? siblingHost.sync(pid, docId, req) : primaryHost.sync(pid, docId, req),
      submitEditorDocumentUpdate: async (pid: string, docId: string, req: SubmitEditorDocumentUpdateRequest) =>
        docId === siblingHost.wire.id ? siblingHost.submit(pid, docId, req) : primaryHost.submit(pid, docId, req),
      publishEditorDocumentPresence: vi.fn(async () => undefined),
    });

    cleanupConnection = configureEditorDocuments(
      projectId,
      () => client,
      () => undefined,
      {
        get: (k) => projectFilesState(projectId).byKey[k],
        all: () => Object.values(projectFilesState(projectId).byKey),
      },
      () => activeWorkspaceId,
    );

    // 1. Adopt primary workspace and open src/app.ts.
    adoptFilesWorkspaceRoots(projectId, makeWorkspace(primaryWorktreeId, "", "/work/primary", "ws-primary"));
    let primaryKey = openFilesBuffer(projectId, {
      rootId: "root-repo",
      rootLabel: "repo",
      path: "src/app.ts",
      intent: "permanent",
    });
    const openedPrimary = await openFileBufferSource(client, projectId, projectFilesState(projectId).byKey[primaryKey]!);
    const presentedPrimary = presentOpenedDocument(openedPrimary.document, openedPrimary.retained);
    const adoptedPrimary = await receiveOpenedEditorDocument(projectId, client, openedPrimary.document!, presentedPrimary?.confirmed, openedPrimary.reservation);
    primaryKey = applyFilesBufferLoad(projectId, primaryKey, openedPrimary.source, presentedPrimary?.loaded);
    applyFilesBufferEditorDocument(projectId, primaryKey, adoptedPrimary!);
    openedPrimary.reservation?.release();

    // 2. Switch visible workspace to sibling checkout and open same-named src/app.ts.
    activeWorkspaceId = "ws-sibling";
    adoptFilesWorkspaceRoots(projectId, makeWorkspace(siblingWorktreeId, `worktree:${siblingWorktreeId}`, "/work/sibling", "ws-sibling"));
    let siblingKey = openFilesBuffer(projectId, {
      rootId: "root-repo",
      rootLabel: "repo",
      path: "src/app.ts",
      intent: "permanent",
    });
    expect(siblingKey).not.toBe(primaryKey);

    const openedSibling = await openFileBufferSource(client, projectId, projectFilesState(projectId).byKey[siblingKey]!);
    const presentedSibling = presentOpenedDocument(openedSibling.document, openedSibling.retained);
    const adoptedSibling = await receiveOpenedEditorDocument(projectId, client, openedSibling.document!, presentedSibling?.confirmed, openedSibling.reservation);
    siblingKey = applyFilesBufferLoad(projectId, siblingKey, openedSibling.source, presentedSibling?.loaded);
    applyFilesBufferEditorDocument(projectId, siblingKey, adoptedSibling!);
    openedSibling.reservation?.release();

    expect(projectFilesState(projectId).order).toEqual([primaryKey, siblingKey]);

    // Tab names disambiguate same-path files across checkouts.
    const tabLabels = filesTabNames(Object.values(projectFilesState(projectId).byKey));
    expect(tabLabels.get(primaryKey)?.qualifier).toContain("Project folder");
    expect(tabLabels.get(siblingKey)?.qualifier).toContain(`Worktree ${siblingWorktreeId.slice(0, 8)}`);

    // 3. Edit both replicas and switch checkouts repeatedly.
    editorReplica(primaryHost.wire.id)!.replaceLocal("export const source = 'primary edited';\n");
    await editorReplica(primaryHost.wire.id)!.flush();
    editorReplica(siblingHost.wire.id)!.replaceLocal("export const source = 'sibling edited';\n");
    await editorReplica(siblingHost.wire.id)!.flush();

    activeWorkspaceId = "ws-primary";
    adoptFilesWorkspaceRoots(projectId, makeWorkspace(primaryWorktreeId, "", "/work/primary", "ws-primary"));
    activeWorkspaceId = "ws-sibling";
    adoptFilesWorkspaceRoots(projectId, makeWorkspace(siblingWorktreeId, `worktree:${siblingWorktreeId}`, "/work/sibling", "ws-sibling"));

    expect(filesBufferText(projectFilesState(projectId).byKey[primaryKey]!)).toBe("export const source = 'primary edited';\n");
    expect(filesBufferText(projectFilesState(projectId).byKey[siblingKey]!)).toBe("export const source = 'sibling edited';\n");
    expect(bufferCheckoutWorktreeId(projectId, projectFilesState(projectId).byKey[primaryKey]!)).toBe(primaryWorktreeId);
    expect(bufferCheckoutWorktreeId(projectId, projectFilesState(projectId).byKey[siblingKey]!)).toBe(siblingWorktreeId);

    // 4. A disk delete event in primary workspace must not close or clobber the sibling buffer.
    const cleanChanges: string[] = [];
    const liveSub = setBufferLiveHandlers(projectId, {
      onCleanDiskChange: (b) => cleanChanges.push(b.key),
      onDirtyDiskChange: (b) => cleanChanges.push(b.key),
      onForeignDeleteDirty: () => {},
    });
    applySourceChangesEvent({
      project_id: projectId,
      workspace_id: "ws-primary",
      workspace_kind: "project",
      resync: false,
      changes: [{
        root_id: "root-repo",
        path: "src/app.ts",
        op: "write",
        origin: "external",
        is_dir: false,
        changed_at: "2026-01-01T00:00:00Z",
      }],
    });
    liveSub.dispose();
    expect(cleanChanges).toEqual([primaryKey]);

    // 5. Persist hot exit with sibling tab active, reset in-memory state, and restore across restart.
    setFilesActiveBuffer(projectId, siblingKey);
    scheduleFilesHotExitPersist(projectId);
    await flushFilesHotExitToDisk();

    const persisted = getAppStateSnapshot().filesHotExit?.byProject[projectId];
    expect(persisted?.buffers).toHaveLength(2);
    expect(persisted?.activeIndex).toBe(1);

    resetProjectFilesForTests();
    resetEditorDocumentsForTests();
    restoreFilesHotExitForProject(projectId, roots);

    const restoredState = projectFilesState(projectId);
    expect(restoredState.order).toHaveLength(2);
    expect(restoredState.byKey[restoredState.activeKey!]?.documentId).toBe(siblingHost.wire.id);

    openEditorDocument.mockClear();
    for (const key of restoredState.order) {
      const opened = await openFileBufferSource(client, projectId, restoredState.byKey[key]!);
      opened.reservation?.release();
    }
    expect(openEditorDocument.mock.calls.map((c) => c[1].document_id)).toEqual([
      primaryHost.wire.id,
      siblingHost.wire.id,
    ]);
  });

  it("disables in-place save when a checkout is unavailable while preserving draft for Save As to an active checkout", async () => {
    const removedHost = new DocumentFixture("export const orphan = 'unsaved work';\n", {
      id: "doc-orphan",
      project_id: projectId,
      workspace_id: "ws-removed",
      root_id: "root-repo",
      path: "src/orphan.ts",
      file_id: "file-orphan",
      branch_id: `worktree:${siblingWorktreeId}`,
      dirty: true,
      absent: true,
      checkout_unavailable: true,
    });

    const replaceProjectSource = vi.fn(async () => ({
      path: "src/orphan.ts",
      size_bytes: 36,
      sha256: "deadbeef",
    }));
    const getProjectSource = vi.fn(async () => {
      throw new LycaonApiError("not found", 404, "source_not_found");
    });
    const createProjectSourceEntry = vi.fn(async () => ({
      root_id: "root-repo",
      path: "src/orphan.ts",
      kind: "file" as const,
    }));

    const client = stubClient({
      openEditorDocument: vi.fn(async () => removedHost.snapshot()),
      syncEditorDocument: removedHost.sync,
      submitEditorDocumentUpdate: removedHost.submit,
      publishEditorDocumentPresence: vi.fn(async () => undefined),
      getProjectSource,
      createProjectSourceEntry,
      replaceProjectSource,
    });

    cleanupConnection = configureEditorDocuments(
      projectId,
      () => client,
      () => undefined,
      {
        get: (k) => projectFilesState(projectId).byKey[k],
        all: () => Object.values(projectFilesState(projectId).byKey),
      },
      () => "ws-primary",
    );

    let key = openFilesBuffer(projectId, {
      rootId: "root-repo",
      rootLabel: "repo",
      path: "src/orphan.ts",
      documentId: removedHost.wire.id,
      intent: "permanent",
    });
    const opened = await openFileBufferSource(client, projectId, projectFilesState(projectId).byKey[key]!);
    const presented = presentOpenedDocument(opened.document, opened.retained);
    const adopted = await receiveOpenedEditorDocument(projectId, client, opened.document!, presented?.confirmed, opened.reservation);
    key = applyFilesBufferLoad(projectId, key, opened.source, presented?.loaded);
    applyFilesBufferEditorDocument(projectId, key, adopted!);
    opened.reservation?.release();

    const buf = projectFilesState(projectId).byKey[key]!;
    expect(buf.checkoutUnavailable).toBe(true);
    expect(buf.dirty).toBe(true);

    let saveError: string | null = null;
    const commands = createFileDocumentCommands({
      projectId: () => projectId,
      client: () => client,
      sourceSessionId: () => undefined,
      state: () => projectFilesState(projectId),
      versionForBuffer: () => null,
      refreshScopeMarks: async () => undefined,
      setSaveError: (msg) => { saveError = msg; },
    });

    expect(commands.editable(buf)).toBe(true);
    expect(commands.saveable(buf)).toBe(false);
    expect(await commands.performSave(key)).toBe(false);
    const directUnavailable = await saveProjectFileDocument({
      projectId,
      client,
      key,
    });
    expect(directUnavailable.status).toBe("unavailable");
    expect(directUnavailable).toMatchObject({ message: "That checkout is no longer available on disk." });

    const liveKey = await commands.saveToActiveCheckout(key, { worktreeId: primaryWorktreeId });
    expect(saveError).toBeNull();
    expect(liveKey).toBeTruthy();
    expect(createProjectSourceEntry).toHaveBeenCalledTimes(1);
    expect(replaceProjectSource).toHaveBeenCalledWith(
      projectId,
      expect.objectContaining({
        root_id: "root-repo",
        path: "src/orphan.ts",
        content: "export const orphan = 'unsaved work';\n",
      }),
      undefined,
      primaryWorktreeId,
    );
  });

  it("marks buffers unavailable when save hits worktree_unavailable and updates cold tab checkout metadata", async () => {
    const host = new DocumentFixture("export const live = 1;\n", {
      id: "doc-save-vanish",
      project_id: projectId,
      workspace_id: "ws-sibling",
      root_id: "root-repo",
      path: "src/vanish.ts",
      file_id: "file-vanish",
      branch_id: `worktree:${siblingWorktreeId}`,
    });

    const observeEditorDocument = vi.fn(async () => ({
      ...host.snapshot(),
      absent: true,
      checkout_unavailable: true,
    }));
    const client = stubClient({
      openEditorDocument: vi.fn(async () => host.snapshot()),
      syncEditorDocument: host.sync,
      submitEditorDocumentUpdate: vi.fn(async (pid: string, docId: string, req: SubmitEditorDocumentUpdateRequest) => {
        if (req.save) {
          throw new LycaonApiError("That checkout is no longer available.", 409, "worktree_unavailable");
        }
        return host.submit(pid, docId, req);
      }),
      observeEditorDocument,
      publishEditorDocumentPresence: vi.fn(async () => undefined),
    });

    cleanupConnection = configureEditorDocuments(
      projectId,
      () => client,
      () => undefined,
      {
        get: (k) => projectFilesState(projectId).byKey[k],
        all: () => Object.values(projectFilesState(projectId).byKey),
      },
      () => "ws-sibling",
    );

    adoptFilesWorkspaceRoots(projectId, makeWorkspace(siblingWorktreeId, `worktree:${siblingWorktreeId}`, "/work/sibling", "ws-sibling"));
    let key = openFilesBuffer(projectId, {
      rootId: "root-repo",
      rootLabel: "repo",
      path: "src/vanish.ts",
      intent: "permanent",
    });
    const opened = await openFileBufferSource(client, projectId, projectFilesState(projectId).byKey[key]!);
    const presented = presentOpenedDocument(opened.document, opened.retained);
    const adopted = await receiveOpenedEditorDocument(projectId, client, opened.document!, presented?.confirmed, opened.reservation);
    key = applyFilesBufferLoad(projectId, key, opened.source, presented?.loaded);
    applyFilesBufferEditorDocument(projectId, key, adopted!);
    opened.reservation?.release();

    const replica = editorReplica(host.wire.id)!;
    replica.replaceLocal("export const live = 2;\n");
    await replica.flush();
    applyFilesBufferEditorDocument(projectId, key, replica.accepted);
    const outcome = await saveProjectFileDocument({ projectId, client, key });
    expect(outcome.status).toBe("unavailable");
    expect(projectFilesState(projectId).byKey[key]?.checkoutUnavailable).toBe(true);
    expect(observeEditorDocument).toHaveBeenCalledTimes(1);

    markFilesBufferCheckoutUnavailable(projectId, key, false);
    expect(projectFilesState(projectId).byKey[key]?.checkoutUnavailable).toBe(false);

    // Cold restored tab receives branch_id, workspace_id, and checkout_unavailable from status reads.
    const coldKey = openFilesBuffer(projectId, {
      rootId: "root-repo",
      rootLabel: "repo",
      path: "src/cold.ts",
      documentId: "doc-cold-wt",
      intent: "permanent",
    });
    applyFilesBufferDocumentStatus(projectId, {
      ...host.snapshot(),
      id: "doc-cold-wt",
      revision: 5,
      branch_id: `worktree:${siblingWorktreeId}`,
      workspace_id: "ws-orphan-cold",
      checkout_unavailable: true,
      absent: true,
    });
    const coldBuf = projectFilesState(projectId).byKey[coldKey]!;
    expect(coldBuf.worktreeId).toBe(siblingWorktreeId);
    expect(coldBuf.workspaceId).toBe("ws-orphan-cold");
    expect(coldBuf.checkoutUnavailable).toBe(true);
    expect(coldBuf.sourcePresent).toBe(false);
  });
});
