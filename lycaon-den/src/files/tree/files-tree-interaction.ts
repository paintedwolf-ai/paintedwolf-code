import type { ProjectFilesViewProps } from "../components/project-files-view-props.ts";
import { createFileContextActions, createFilePathIntents } from "../commands/file-context-actions.ts";
import { createFilesSelection } from "../tree/files-selection.ts";
import { createFilesTabCommands } from "../tabs/files-tab-commands.ts";
import { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import { createFileRestoration } from "../history/file-restoration-commands.ts";
import { createFileConfirmations } from "../commands/file-confirmations.ts";
import { createFileMutationCommands } from "../commands/file-mutation-commands.ts";
import { createSourceHistoryCommands } from "../history/project-files-history.ts";
import { createFilesTreeReveal } from "../tree/files-tree-reveal.ts";
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import type { SourceChange } from "../../api/types.ts";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import { type ContextMenuAnchor, type ContextMenuItem } from "../../components/ContextMenu.tsx";
import { recordInventoryOpen } from "../tree/file-inventory.ts";
import { createFilesWorkspace } from "../source/files-workspace.ts";
import type { FilesScope } from "../components/files-scope.ts";
import { type FilesTreeHandle, type FilesTreeSelection } from "../tree/files-tree-context.ts";
import { dirtyFilesBuffersUnderPath, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { type BufferOpenIntent } from "../documents/files-buffer-state.ts";
import { filesContextMenuItems, type FilesContextTarget } from "../commands/project-files-context-menu.ts";
import type { NewEntryKind } from "../commands/project-files-create.ts";
import { pushProjectJump } from "../components/project-files-jump-bridge.ts";
import { type DirtyGuardResult } from "../commands/project-files-lifecycle.ts";
import { fileDisplayName, type FileBufferKey } from "../components/project-files-model.ts";
import { onProjectFilesRevealRequest, takeProjectFilesRevealRequest } from "../tree/project-files-reveal.ts";
import { setFilesStagePaneMode } from "../review/review-pane.ts";
import { deletedPathsInScope } from "../tree/scope-resolution.ts";

type TreeInteraction = {
  props: Pick<ProjectFilesViewProps, "projectId" | "document" | "onOpenInNewWindow" | "onOpenFileInNewWindow">;
  scope: Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "state" | "activeBuffer" |
    "filesRoots" | "rootLabelFor" | "lifecycleHooks" | "copyAbsolutePathRef" | "copyRelativePathRef">;
  directoryEntries: ReturnType<typeof createFilesWorkspace>["directoryEntries"];
  confirmDirectoryChange: ReturnType<typeof createFilesWorkspace>["confirmDirectoryChange"];
  observeDirectory: ReturnType<typeof createFilesWorkspace>["observeDirectory"];
  workspaceSettled: () => boolean;
  filesWorkspaceId: () => string;
  mountedTreeWorkspace: () => string;
  scopeMarksVersion: () => number;
  stageEl: () => HTMLDivElement | undefined;
  showFilesTree: () => void;
  setDeleteGuard: ReturnType<typeof createFileConfirmations>["setDeleteGuard"];
  requestCloseTab: ReturnType<typeof createFileConfirmations>["requestCloseTab"];
  requestCloseFamily: ReturnType<typeof createFilesTabCommands>["requestCloseFamily"];
  openFilesList: () => void;
  editorContextActions: ReturnType<typeof createFilesEditorActions>["editorContextActions"];
  openReviewRowFile: Parameters<typeof createFilePathIntents>[0]["openReviewRowFile"];
  runFileRevert: ReturnType<typeof createFileRestoration>["runFileRevert"];
};

export function createFilesTreeInteraction(options: TreeInteraction) {
  const { props, scope, directoryEntries, confirmDirectoryChange, observeDirectory,
    workspaceSettled, filesWorkspaceId, mountedTreeWorkspace, scopeMarksVersion,
    stageEl, showFilesTree, setDeleteGuard, requestCloseTab, requestCloseFamily,
    openFilesList, editorContextActions, openReviewRowFile, runFileRevert } = options;
  const { client, sourceSessionId, state, filesRoots } = scope;
  const [ctxMenu, setCtxMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    items: ContextMenuItem[];
  } | null>(null);
  const [filterOpen, setFilterOpen] = createSignal(false);
  const [filterQuery, setFilterQuery] = createSignal("");
  const { selectedEntry, setSelectedEntry, selectedProjectRoot, selectedFolder } = createFilesSelection({ ...scope, setFilterQuery });
  const [renaming, setRenaming] = createSignal<{
    rootId: string;
    path: string;
    isDir: boolean;
  } | null>(null);
  const mutations = createFileMutationCommands({
      ...scope, directoryEntries,
      noteLifecycleChange: (label) => noteLifecycleChange(label),
      reportHistoryFailure: (error) => sourceHistory.reportHistoryFailure(error),
    });
  const { trashConfirm, setTrashConfirm, setMoveSource, setMoveError, duplicateEntry, commitRename, onMovePath } = mutations;
  const sourceHistory = createSourceHistoryCommands(scope);
  const { noteLifecycleChange, runSourceHistory } = sourceHistory;
  let tree: FilesTreeHandle | undefined;
  const applyPendingTreeReveal = () => {
    if (!tree) return;
    const req = takeProjectFilesRevealRequest(props.projectId);
    if (req) revealInTree(req.rootId, req.path, req.isDir);
  };
  onCleanup(onProjectFilesRevealRequest(applyPendingTreeReveal));
  const browse = async (rootId: string, dir: string) => {
    const c = client();
    if (!c) throw new Error("Sidecar is not connected.");
    const listing = await c.browseProjectSource(
      props.projectId,
      { rootId, dir },
      sourceSessionId(),
    );
    return listing;
  };

  const treeReveal = createFilesTreeReveal({
    projectId: props.projectId,
    workspaceId: filesWorkspaceId,
    available: () => props.document == null && client() != null,
    rootIds: () => filesRoots().map((root) => root.id),
    buffers: () => Object.values(state().byKey),
    deletedPaths: (rootId) => {
      void scopeMarksVersion();
      return deletedPathsInScope(props.projectId, rootId);
    },
    browse,
  });

  const lifecycleHooks = () => ({
    guardDirtyUnderPath: async (
      rootId: string,
      path: string,
    ): Promise<DirtyGuardResult> => {
      const dirty = dirtyFilesBuffersUnderPath(props.projectId, rootId, path);
      if (dirty.length === 0) return "proceed";
      return new Promise((resolve) => {
        setDeleteGuard({ rootId, path, resolve });
      });
    },
    confirmChange: (change: SourceChange) => {
      confirmDirectoryChange(change);
      tree?.confirmChange(change);
    },
    openFile: (args: { rootId: string; rootLabel: string; path: string }) =>
      openFilesBuffer(props.projectId, { ...args, intent: "permanent" }),
    rootLabelFor: scope.rootLabelFor,
  });

  createEffect(() => {
    props.projectId;
    client();
    void sourceHistory.refreshSourceHistory();
  });

  const resolveRootId = (rootId: string) =>
    rootId.trim() || filesRoots()[0]?.id || "";

  const createTargetDir = (): { rootId: string; dir: string } | null => {
    const sel = selectedEntry();
    if (sel?.kind === "folder") {
      return { rootId: sel.rootId, dir: sel.path };
    }
    if (sel?.kind === "file") {
      const slash = sel.path.lastIndexOf("/");
      const dir = slash < 0 ? "." : sel.path.slice(0, slash) || ".";
      return { rootId: sel.rootId, dir };
    }
    const root = filesRoots()[0];
    if (!root) return null;
    return { rootId: root.id, dir: "." };
  };

  const [requestedTreeReveal, setRequestedTreeReveal] = createSignal<{
    rootId: string; path: string; isDir: boolean; current: () => boolean;
  } | null>(null);
  // An explicit reveal selects its file until the reader navigates again.
  const [explicitTreeReveal, setExplicitTreeReveal] = createSignal<{
    rootId: string; path: string; revision: number;
  } | null>(null);
  const revealInTree = (rootId: string, path: string, isDir = false) => {
    const intent = beginSourceNavigation();
    showFilesTree();
    setFilesStagePaneMode(props.projectId, "files");
    setFilterQuery("");
    const resolved = resolveRootId(rootId);
    setExplicitTreeReveal(isDir ? null : { rootId: resolved, path, revision: state().aimRevision });
    setRequestedTreeReveal({ rootId: resolved, path, isDir, current: intent.current });
  };
  createEffect(() => {
    const request = requestedTreeReveal();
    if (!request || !workspaceSettled() || mountedTreeWorkspace() !== filesWorkspaceId()) return;
    untrack(() => {
      setRequestedTreeReveal(null);
      if (request.current()) tree?.revealPath(request.rootId, request.path, request.isDir);
    });
  });

  const { ctxActions, openContextMenu } = createFileContextActions({
    ...scope, resolveRootId,
    startTreeCreate: () => tree?.startCreate, setRenaming, setMoveError, setMoveSource,
    duplicateEntry, setTrashConfirm,
    addPathToChat: (...args) => addPathToChat(...args),
    explainFileInReview: (...args) => explainFileInReview(...args),
    collapseTreeAll: () => tree?.collapseAll,
    expandTreeAll: () => tree?.expandAll, requestCloseTab, requestCloseFamily, openFilesList,
    onOpenInNewWindow: () => props.onOpenInNewWindow,
    onOpenFileInNewWindow: () => props.onOpenFileInNewWindow,
    openFile: (...args) => openFile(...args), runFileRevert: (...args) => runFileRevert(...args),
    revealInTree, treeReveal, activeInfoActions: () => activeInfoActions(), editorContextActions, setCtxMenu,
  });

  createResidentActivity(() => {
    createEffect(() => {
      const folder = selectedFolder();
      if (folder && workspaceSettled()) onCleanup(observeDirectory(folder.root.id, folder.path));
    });
  });

  const { copyAbsolutePathRef, copyRelativePathRef, addPathToChat, explainFileInReview, activeInfoActions } =
    createFilePathIntents({ ...scope, openReviewRowFile });

  createEffect(() => {
    const stage = stageEl();
    if (!stage) return;
    stage.addEventListener("contextmenu", openContextMenu);
    onCleanup(() => {
      stage.removeEventListener("contextmenu", openContextMenu);
    });
  });

  const createEntry = async (
    kind: NewEntryKind,
    rootId: string,
    path: string,
  ) => {
    const c = client();
    if (!c) throw new Error("Sidecar is not connected.");
    const res = await c.createProjectSourceEntry(
      props.projectId,
      { operation_id: crypto.randomUUID(), path, kind, root_id: rootId },
      sourceSessionId(),
    );
    noteLifecycleChange(
      `${kind === "folder" ? "Created folder" : "Created file"} ${fileDisplayName(res.path)}`,
    );
    return res.path;
  };

  const openFile = (
    selection: FilesTreeSelection,
    intent: BufferOpenIntent,
  ) => {
    setSelectedEntry({
      rootId: selection.rootId,
      path: selection.path,
      kind: "file",
    });
    const key = openFilesBuffer(props.projectId, { ...selection, intent });
    recordInventoryOpen(props.projectId, selection);
    pushProjectJump(props.projectId, {
      bufferKey: key,
      rootId: selection.rootId,
      path: selection.path,
      line: 1,
    });
  };

  const openFilesMenu = (anchor: ContextMenuAnchor, target: FilesContextTarget) => {
    const items = filesContextMenuItems(target, ctxActions());
    if (items?.length) setCtxMenu({ anchor, items });
  };
  const openTabContextMenu = (key: FileBufferKey, anchor: ContextMenuAnchor) => {
    const buf = state().byKey[key];
    if (buf) openFilesMenu(anchor, { surface: "tab", bufferKey: key, rootId: buf.rootId, path: buf.path, name: buf.name });
  };


  return { selectedEntry, setSelectedEntry, selectedProjectRoot, selectedFolder,
    filterOpen, setFilterOpen, filterQuery, setFilterQuery, renaming, setRenaming,
    mutations, trashConfirm, commitRename, onMovePath, sourceHistory, runSourceHistory,
    tree: () => tree, setTreeHandle: (handle: FilesTreeHandle) => { tree = handle; applyPendingTreeReveal(); },
    browse, treeReveal, lifecycleHooks, createTargetDir, explicitTreeReveal, revealInTree,
    copyAbsolutePathRef, copyRelativePathRef, createEntry, openFile, openFilesMenu,
    openTabContextMenu, ctxMenu, setCtxMenu };
}
