import { bindFilesCommands } from "../commands/files-command-bindings.ts";

import { createFileContextActions, createFilePathIntents } from "../commands/file-context-actions.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { createFilesSelection } from "../tree/files-selection.ts";
import { createFilesTabCommands } from "../tabs/files-tab-commands.ts";
import { createFilesReviewNavigation } from "../history/files-review-navigation.ts";
import { createFilesReviewScope } from "../review/files-review-scope.ts";

import { createFilesPresentationTracking } from "./files-presentation-tracking.ts";
import { observeFilesBufferDemand } from "../documents/files-buffer-demand.ts";
import { createFileDocumentCommands } from "../documents/file-document-commands.ts";
import { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import { createDefinitionNavigation } from "../editor/definition-navigation.ts";
import { createFilesTabMeasurements } from "../tabs/files-tab-measurements.ts";
import { createFilesTabDrag } from "../tabs/files-tab-drag.ts";
import { createFileRestoration } from "../history/file-restoration-commands.ts";
import { createFileConfirmations } from "../commands/file-confirmations.ts";
import { createBufferLoading } from "../documents/files-buffer-loading.ts";
import { createBufferRefresh } from "../documents/files-buffer-refresh.ts";
import { createFileMergeCommands } from "../documents/file-merge-commands.ts";
import { createFileMutationCommands } from "../commands/file-mutation-commands.ts";
import { EmptyFilesTabStrip, FilesTabStrip } from "../tabs/FilesTabStrip.tsx";
import { FilesEmptyEditor } from "./FilesEmptyEditor.tsx";
import { createVersionNavigation } from "../history/version-navigation.ts";
import { createSourceHistoryCommands } from "../history/project-files-history.ts";
import { createFilesResidency } from "../documents/files-residency.ts";

import { createFilesTreeReveal, hasFilesTreeAddress } from "../tree/files-tree-reveal.ts";
import { FilesPaneDeck } from "./FilesPaneDeck.tsx";
import { FilesTreePane } from "./FilesTreePane.tsx";
import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";

import { Show, createEffect, createMemo, createSignal, onCleanup, onMount, untrack } from "solid-js";

import type { LycaonClient } from "../../api/client.ts";

import type { ProjectRoot, SourceChange } from "../../api/types.ts";

import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { EditorCommandBar } from "../../find/EditorCommandBar.tsx";
import { registerEscapeLadderLayer } from "../../find/escape-ladder.ts";
import { editorCommandBarPlacement } from "../../find/find-controller.ts";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { observeElementExtent } from "../../layout/element-extent.ts";
import { FILES_TREE_SURFACE, clampListPaneSizePx, listPaneSizeMinPx, maxListPaneSizePx, resolveListPaneSizePx, type ListPaneAxis } from "../../list/list-pane-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { basenameOfPath } from "../../api/project-path.ts";
import { type FileTabWindowDrag, type ItemWindowDropPosition } from "../../platform/windows/item-windows.ts";

import { specialTabKind } from "../tabs/files-tab-kind.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { widenWindowBy } from "../../platform/windows/window-chrome.ts";
import { editorRevealInTreePref } from "../../settings/editor/editor-prefs.ts";
import { dragRequestsHide, PANE_HIDDEN_PX } from "../../layout/drag-to-hide.ts";
import { beginListPaneResize, layoutViewportWidthPx, listPaneHidePreviewed, listPanePrefs, navCollapsedPref, preferredChatWidthPx, preferredNavWidthPx, resetListPaneSize, splitHostWidthPx, workspaceOrientationPref } from "../../shell/layout-store.ts";
import { filesShowWindowDeficitPx } from "../../shell/responsive-collapse.ts";

import type { AppStore } from "../../store/app-state-model.ts";
import { createPreparation, createPresentationWaiting } from "../../ui/presentation.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { createResidentActivity, useResidentLive } from "../../ui/resident-activity.ts";
import { useSurfaceReveal } from "../../ui/surface-reveal.ts";

import { BrowseStagePanel } from "../../components/browse/BrowseStagePanel.tsx";
import { type ContextMenuAnchor, type ContextMenuItem } from "../../components/ContextMenu.tsx";

import { DenButton } from "../../components/primitives/DenButton.tsx";
import { ResidentPortal } from "../../components/primitives/ResidentPortal.tsx";
import { ResizeHandle } from "../../components/primitives/ResizeHandle.tsx";
import { ProjectLoadingStage } from "../../components/shell/ProjectLoadingStage.tsx";
import { clearScopePreview } from "../../components/source/editor/codemirror-theme.ts";

import { loadEditorConfigForBuffer, refreshOpenEditorConfigs } from "../../components/source/editor/editorconfig-load.ts";

import { inlineEditOpen } from "../../components/source/inline-edit/inline-edit-controller.ts";

import { syncReviewPresence } from "../review/review-live.ts";
import { agentPresenceFor } from "./agent-presence-store.ts";
import { createFilesAgentPresence } from "./files-agent-presence.ts";
import { setBufferLiveHandlers } from "../documents/buffer-live.ts";

import { editorDraftPersistenceError, observeEditorDocument } from "../documents/editor-document.ts";

import { briefingSelectionForBuffer } from "./file-briefing-target.ts";

import { recordInventoryOpen } from "../tree/file-inventory.ts";

import { filesBufferHasCurrentFile } from "../documents/files-buffer-change.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import { createFilesEditorSynchronization } from "../editor/files-editor-synchronization.ts";
import { filesPaneKey, parseFilesPaneKey } from "./files-pane-key.ts";

import { orderWithPinnedGroup } from "../tabs/files-tab-strip.ts";
import { filesTreeClaimed, filesTreeCollapsed, setFilesTreeClaimed, setFilesTreeCollapsed } from "../tree/files-tree-window-state.ts";
import { createFilesVersionHistory } from "../history/files-version-history.ts";
import { selectedFileVersion, setSelectedFileVersion } from "../history/files-version-selection.ts";

import { createFilesWorkspace } from "../source/files-workspace.ts";
import type { FilesScope } from "./files-scope.ts";
import { FilesTree } from "../tree/FilesTree.tsx";
import { type FilesTreeHandle, type FilesTreeSelection } from "../tree/files-tree-context.ts";
import { FileUndoToast } from "../history/FileUndoToast.tsx";

import { FilesStageDialogs } from "./FilesStageDialogs.tsx";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { dirtyFilesBuffersUnderPath, filesBufferKeyAtAddress, focusedFilesBufferKey, openFilesBuffer, renameFilesBuffer, setFilesActiveBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState, type BufferOpenIntent, type FileBuffer } from "../documents/files-buffer-state.ts";
import { filesContextMenuItems, type FilesContextTarget } from "../commands/project-files-context-menu.ts";
import type { NewEntryKind } from "../commands/project-files-create.ts";
import { pushProjectJump } from "./project-files-jump-bridge.ts";
import { type DirtyGuardResult } from "../commands/project-files-lifecycle.ts";
import { applyProjectSourceLoadFailure } from "./project-files-load-error.ts";
import { diffsBufferKey, fileDisplayName, type FileBufferKey } from "./project-files-model.ts";
import { onProjectFilesRevealRequest, takeProjectFilesRevealRequest } from "../tree/project-files-reveal.ts";
import { getFilesStagePaneMode, setFilesStagePaneMode, subscribeFilesStagePaneMode } from "../review/review-pane.ts";
import { ReviewLens } from "../review/ReviewLens.tsx";
import { deletedPathsInScope, isAddedInScope, isInScope, resolvedLensView, resolvedScope, scopeBaseline, scopeChangeKindFor, scopeEffectFor } from "../tree/scope-resolution.ts";
import { StepBar } from "../walk/StepBar.tsx";
import { currentWalkStep, isWalking, leaveWalk, subscribeWalk, walkState } from "../walk/walk-store.ts";

import { diffsAddressKey, diffsPageCopy } from "../review/diffs-address.ts";
import { WalkTransport } from "../walk/WalkTransport.tsx";
import { createWalkPresentation } from "../walk/walk-presentation.ts";
import { PresentationProvider, usePresentationParticipant } from "../../ui/presentation-context.tsx";

type Props = {
  projectId: string;
  projectName?: string;
  appStore: AppStore;
  roots: ProjectRoot[];
  client?: LycaonClient | null;
  /** The shell handles session navigation for gutter jumps. */
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
  onOpenSearch?: (args: {
    seed: string;
    originProjectId: string | null;
  }) => void;
  /** Tab-strip pull-off into an independent peer view. */
  onOpenInNewWindow?: (
    bufferKey?: FileBufferKey,
    position?: ItemWindowDropPosition,
  ) => void | Promise<void>;
  /** Tree-file pop-out into an independent peer view. */
  onOpenFileInNewWindow?: (
    rootId: string,
    path: string,
    title: string,
  ) => void | Promise<void>;
  /** Native destination window used while a tab is being torn off the strip. */
  fileTabWindowDrag?: FileTabWindowDrag;
  /** Called after a tear-off has successfully removed its one source tab. */
  onFileTabMovedOut?: (bufferKey: FileBufferKey) => void | Promise<void>;
  /** A file-addressed peer starts with only its requested tab. */
  restoreHotExit?: boolean;
  /** The window handles close-tab shortcuts when no tab is active. */
  onCloseTabIdle?: () => void;
  /** Document windows omit project navigation. */
  document?: {
    rootId: string;
    path: string;
    initialMarkdownView?: "code" | "preview";
    previewSource?: (source: string) => string;
    onSessionChange?: (session: ProjectFileDocumentSession | null) => void;
  };
};

export type ProjectFileDocumentSession = {
  save: () => Promise<boolean>;
};

export function ProjectFilesView(props: Props) {
  const live = useResidentLive();
  const client = () => props.client ?? getLycaonClient() ?? null;
  const presentationToken = `files:${props.projectId}:${Math.random().toString(36).slice(2, 10)}:editor`;
  /** The selected chat when it belongs to this project. */
  const chatSessionId = createMemo(() => {
    const session = props.appStore.state.currentSession;
    return session?.project_id?.trim() === props.projectId.trim()
      ? session.id?.trim() || undefined
      : undefined;
  });
  /** Requests name the chat selected when they run; nothing observes it. */
  const sourceSessionId = () => untrack(chatSessionId);
  const { filesWorkspaceId, filesSourceAddress, filesRoots, workspaceError, workspaceFault, workspaceSettled,
    treeSettled, setTreeSettled, refreshProjectWorkspace, refreshWatchCoverage, observationNoticeFor, directoryEntries, confirmDirectoryChange, observeDirectory,
  } = createFilesWorkspace({
    projectId: props.projectId, roots: () => props.roots, client, chatSessionId,
    reachable: () => isBackendReachable(props.appStore.state.sidecarStatus),
    restoreHotExit: () => props.restoreHotExit !== false,
  });
  const retryEditorOpening = createFilesEditorSynchronization({
    projectId: props.projectId, client, sessionId: sourceSessionId,
    workspaceId: filesWorkspaceId, workspaceSettled,
    refreshWorkspace: refreshProjectWorkspace,
    reachable: () => isBackendReachable(props.appStore.state.sidecarStatus),
    onUpdated: () => flashUpdatedNote(),
  });
  const reloadEditorConfig = (key: FileBufferKey) => {
    const c = client();
    const buf = projectFilesState(props.projectId).byKey[key];
    if (!c || !buf) return;
    void loadEditorConfigForBuffer({
      client: c,
      projectId: props.projectId,
      key,
      buffer: buf,
      sessionId: sourceSessionId(),
    });
  };
  const state = () => projectFilesState(props.projectId);
  const activeBuffer = (): FileBuffer | null => {
    const key = state().activeKey;
    return key ? (state().byKey[key] ?? null) : null;
  };

  // The strip follows the aim; the editor presents `activeKey`.
  const aimedKey = (): FileBufferKey | null =>
    focusedFilesBufferKey(props.projectId);
  const holdLabel = () =>
    props.document ? basenameOfPath(props.document.path) : props.projectName;
  const holdNotice = (): { message: string; tone: "waiting" | "error" } | null => {
    const fault = workspaceFault();
    if (fault) return { message: fault, tone: "error" };
    const retrying = workspaceError();
    if (retrying) return { message: retrying, tone: "waiting" };
    return props.document ? { message: "Opening file…", tone: "waiting" } : null;
  };
  observeSurfaceFailure(
    {
      code: "files_workspace_syncing",
      title: "Editor sync is unavailable",
      suggestedAction: "Files keep retrying on their own; if it persists, check the connection to the host.",
    },
    workspaceError,
    () => props.projectId,
  );
  const holdHint = () => holdNotice()?.message;
  const holdTone = () => holdNotice()?.tone;
  const workspacePresentationSettled = () =>
    workspaceSettled() && treeSettled();
  // The tree reads its workspace from input handlers too, outside any reactive owner.
  const treeWorkspaceId = createMemo(() => workspaceSettled() ? filesWorkspaceId() : "");
  const stageSettled = () => {
    const buffer = activeBuffer();
    // A buffer showing a past version has content without a source read.
    return workspacePresentationSettled() && (
      !buffer ||
      !buffer.loading ||
      selectedFileVersion(props.projectId, buffer.key) != null
    );
  };
  const { ready: stageReady } = useSurfaceReveal({
    ready: stageSettled,
    name: "files-stage",
    faulted: () => workspaceFault() != null,
    notice: holdNotice,
  });
  const retainedWorkspacePending = () =>
    stageReady() && !workspacePresentationSettled() && workspaceFault() == null;
  const showRetainedWorkspaceWait = createPresentationWaiting(
    retainedWorkspacePending,
  );
  const [displayedPane, setDisplayedPane] = createSignal<string | null>(null);
  const [publishingPane, setPublishingPane] = createSignal<string | null>(null);
  createFilesResidency(props.projectId, () => parseFilesPaneKey(displayedPane() ?? "")?.key ?? null);
  const [editorDockMounts, setEditorDockMounts] = createSignal(new Map<string, HTMLElement>());
  const displayedBuffer = () => {
    const pane = displayedPane();
    const parsed = pane ? parseFilesPaneKey(pane) : null;
    return parsed ? state().byKey[parsed.key] ?? null : null;
  };
  const editorPresentationPending = () => {
    const key = aimedKey();
    const buffer = key ? state().byKey[key] : undefined;
    const requested = key && buffer ? filesPaneKey(key, buffer.kind) : null;
    return requested !== displayedPane();
  };
  const showRetainedEditorWait = createPresentationWaiting(
    editorPresentationPending,
  );
  const [mountedTreeWorkspace, setMountedTreeWorkspace] = createSignal("");
  const displayedTreeFile = createMemo<{ rootId: string; path: string; revision: number } | null>(() => {
    const key = aimedKey();
    const buffer = key ? state().byKey[key] : undefined;
    if (buffer && hasFilesTreeAddress(buffer)) {
      return { rootId: buffer.rootId, path: buffer.path, revision: state().aimRevision };
    }
    const pane = displayedPane();
    const parsed = pane ? parseFilesPaneKey(pane) : null;
    const displayed = parsed ? state().byKey[parsed.key] : undefined;
    return displayed && hasFilesTreeAddress(displayed) ? { rootId: displayed.rootId, path: displayed.path, revision: state().aimRevision } : null;
  });
  // Automatic tree motion follows the editor once its destination publishes.
  const publishedTreeFile = () => editorPresentationPending() ? null : displayedTreeFile();
  const activePane = createMemo(() => {
    const key = state().activeKey;
    if (!key) return undefined;
    const buf = state().byKey[key];
    if (!buf) return undefined;
    return filesPaneKey(key, buf.kind);
  });
  const [saveError, setSaveError] = createSignal<string | null>(null);
  // Members created further down resolve when a controller calls them.
  const scope: FilesScope = {
    projectId: () => props.projectId, client, sourceSessionId, filesWorkspaceId, live, state, activeBuffer, aimedKey,
    editorPresentationPending, filesRoots,
    rootLabelFor: (rootId) => filesRoots().find((root) => root.id === rootId)?.label ?? rootId,
    stripOrder: () => stripOrder(), tabsElement: () => tabsEl, scrollerElement: () => tabsScrollerEl,
    setSaveError,
    versionForBuffer: (buffer) => versionForBuffer(buffer),
    dropFileVersionState: (key) => dropFileVersionState(key),
    loadVersionHistory: (buffer) => loadVersionHistory(buffer),
    refreshScopeMarks: () => refreshScopeMarks(),
    lifecycleHooks: () => lifecycleHooks(),
    reloadBuffer: (key) => reloadBuffer(key),
    closeBuffer: (key, options) => closeBuffer(key, options),
    copyAbsolutePathRef: (rootId, path) => copyAbsolutePathRef(rootId, path),
    copyRelativePathRef: (rootId, path) => copyRelativePathRef(rootId, path),
  };
  const confirmations = createFileConfirmations({
      ...scope,
      closeBuffers: (keys, options) => closeBuffers(keys, options),
      discardBuffer: (key) => discardBuffer(key),
      performSave: (key) => performSave(key), saveable: (buffer) => saveable(buffer),
    });
  const { confirm, setConfirm, setBulkClose, deleteGuard, setDeleteGuard, ensureCleanDiskForEditorAction,
    requestCloseTab, observeDeleteGuard } = confirmations;
  const editorActions = createFilesEditorActions({
    ...scope,
    // These actions send to a chat, so they need this project's chat, not whichever is foreground.
    sessionId: chatSessionId,
    editorPaneEl: () => editorPaneEl(),
    ensureCleanDiskForEditorAction,
    runGoToDefinition: (position) => runGoToDefinition(position),
  });
  const { handoffSelectionToChat, secretScreenReport, openMarkSecretDialog, buildEditorActionContext,
    abortEditorAction, editorContextActions } = editorActions;
  const tabCommands = createFilesTabCommands({
    ...scope, loadBuffer: (buffer) => loadBuffer(buffer), setBulkClose, setConfirm,
  });
  const { filesOpenListOpen, setRovingTabKey, closeBuffers, requestCloseFamily, openFilesList, restoreJumpEntry,
    cycleTab, closeBuffer, reopenClosedTab } = tabCommands;
  const [hiddenTabCount, setHiddenTabCount] = createSignal(0);
  const [showLeadingFade, setShowLeadingFade] = createSignal(false);
  const [showTrailingFade, setShowTrailingFade] = createSignal(false);
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
  const documents = createFileDocumentCommands(scope);
  const { saving, setSaving, setSavedFlash, resetEditorStateChange, saveable, performSave, discardBuffer,
    reloadBuffer, clearSavedFlashTimer } = documents;
  const [restoringVersion, setRestoringVersion] = createSignal(false);
  const [updatedNote, setUpdatedNote] = createSignal(false);
  const definitions = createDefinitionNavigation({ ...scope, cursor: () => cursor() });
  const { runGoToDefinition, clearNoticeTimer, observeKeyboardAndBuffer: observeDefinitionNavigation } = definitions;
  const merge = createFileMergeCommands(scope);
  const { mergeModel } = merge;
  const [cursor, setCursor] = createSignal({ line: 1, col: 1 });
  const [innerLayerOpen, setInnerLayerOpen] = createSignal(false);
  const [paneMode, setPaneMode] = createSignal(
    getFilesStagePaneMode(props.projectId),
  );
  let updatedNoteTimer: ReturnType<typeof setTimeout> | undefined;
  const flashUpdatedNote = () => {
    setUpdatedNote(true);
    if (updatedNoteTimer) clearTimeout(updatedNoteTimer);
    updatedNoteTimer = setTimeout(() => setUpdatedNote(false), 2000);
  };
  onCleanup(() => {
    clearSavedFlashTimer();
    if (updatedNoteTimer) clearTimeout(updatedNoteTimer);
    clearNoticeTimer();
    setBufferLiveHandlers(props.projectId, null);
  });
  onCleanup(
    subscribeFilesStagePaneMode((id) => {
      if (id === props.projectId.trim()) setPaneMode(getFilesStagePaneMode(id));
    }),
  );
  const [walkTick, setWalkTick] = createSignal(0);
  onCleanup(
    subscribeWalk((id) => {
      if (id === props.projectId.trim()) setWalkTick((n) => n + 1);
    }),
  );
  const walking = createMemo(() => {
    void walkTick();
    return isWalking(props.projectId);
  });
  const walkEditorPreparation = createPreparation();
  const walkChrome = createWalkPresentation({
    active: walking,
    loading: () => { void walkTick(); return walkState(props.projectId).status === "loading"; },
    ready: () => {
      void walkTick();
      return !currentWalkStep(props.projectId) || walkEditorPreparation.ready();
    },
  });
  usePresentationParticipant("files-editors", walkEditorPreparation.ready, walkEditorPreparation.notice);
  // Effect steps show a file version; group steps show a summary page.
  const walkPresentation = createMemo(() => {
    void walkTick();
    const record = walkState(props.projectId);
    const step = currentWalkStep(props.projectId);
    if (!record.active || !step) return null;
    if (step.kind !== "effect") return { step, comparison: null, selectionRevision: record.selectionRevision };
    if (!record.comparison) return null;
    return { step, comparison: record.comparison, selectionRevision: record.selectionRevision };
  }, null, {
    equals: (before, after) => before === after || !!(before && after &&
      before.step.key === after.step.key && before.comparison === after.comparison &&
      before.selectionRevision === after.selectionRevision),
  });
  const walkInitialFile = createMemo(() => {
    if (paneMode() !== "files") return null;
    const selection = selectedEntry();
    if (selection?.kind !== "file") return null;
    return { rootId: selection.rootId, path: selection.path };
  });

  const readerHandles = new Map<string, { gotoLine: () => void | Promise<void> }>();
  const versionHistory = createFilesVersionHistory(scope);
  const { versionHistoryForBuffer, loadVersionHistory, forgetVersionHistory } = versionHistory;

  const versions = createVersionNavigation({
    ...scope,
    walking,
    forgetVersionHistory: (key) => forgetVersionHistory(key),
    versionHistoryForBuffer: (b) => versionHistoryForBuffer(b),
    loadVersionHistory: (b) => loadVersionHistory(b),
  });
  const { versionForBuffer, returnToCurrent, dropFileVersionState, selectWalkEffect, stepVersion } = versions;

  const {
    openWalkStepFile,
    openReviewRowFile,
    openFileSummaryLocation,
  } = createFilesReviewNavigation({
    ...scope, walkPresentation, setSelectedEntry,
    scrollActiveTabIntoView: () => scrollActiveTabIntoView(), selectWalkEffect,
  });

  const previousVersionForBuffer = (buffer: FileBuffer): boolean => {
    if (specialTabKind(buffer)) return false;
    if (versionForBuffer(buffer) || buffer.deleted) return true;
    if (filesBufferHasCurrentFile(buffer)) return false;
    const history = versionHistoryForBuffer(buffer);
    if (history.status === "ready" && history.current?.state === "absent") return true;
    void scopeContentVersion();
    const change = scopeEffectFor(props.projectId, buffer.rootId, buffer.path);
    return !buffer.jobId && Boolean(change?.commit) && change?.tip.state === "absent";
  };
  const activeFileSummarySelection = createMemo(() => {
    const buffer = displayedBuffer();
    return briefingSelectionForBuffer(
      buffer,
      buffer ? versionForBuffer(buffer) : null,
    );
  });

  const { scopeMarksVersion, scopeContentVersion, refreshScopeMarks } = createFilesReviewScope({
    ...scope, appStore: props.appStore, chatSessionId, refreshWatchCoverage,
    // A workspace the host has not answered for yet stays chat-scoped.
    sessionScoped: () => filesSourceAddress() !== undefined,
  });

  createFilesPresentationTracking({
    ...scope, presentationToken, stageEl: () => stageEl(), stageReady, scopeContentVersion,
  });

  const agentPresenceForPath = createFilesAgentPresence({
    projectId: () => props.projectId,
    openSessionId: chatSessionId,
  });

  createEffect(() => {
    syncReviewPresence(props.projectId, agentPresenceFor(props.projectId));
  });

  const [stageEl, setStageEl] = createSignal<HTMLDivElement>();

  onMount(() => {
    const innerLayerPresent = () =>
      confirm() != null ||
      deleteGuard() != null ||
      trashConfirm() != null ||
      ctxMenu() != null ||
      filesOpenListOpen() ||
      innerLayerOpen();
    // Inner layers dismiss themselves; the latch keeps their Escape from falling through to the shell.
    onCleanup(registerEscapeLadderLayer("stageLatch", {
      isOpen: innerLayerPresent,
      dismiss: () => {},
    }));
  });

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

  const stripOrder = createMemo(() => {
    const s = state();
    return orderWithPinnedGroup(s.order, s.byKey);
  });

  // The lens page is named by the comparison it follows, so the eye renames its tab.
  createEffect(() => {
    void scopeContentVersion();
    const name = diffsPageCopy({ kind: "lens" }, resolvedLensView(props.projectId)).tab;
    untrack(() => renameFilesBuffer(props.projectId, diffsBufferKey(diffsAddressKey({ kind: "lens" })), name));
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

  createEffect((wasOpen?: boolean) => {
    const isOpen = inlineEditOpen() != null;
    if (wasOpen && !isOpen) {
      const buf = activeBuffer();
      if (buf && !isComposedBufferKind(buf.kind)) {
        const view = getFilesEditorView(props.projectId, buf.key);
        if (view) clearScopePreview(view);
      }
    }
    return isOpen;
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

  const { loadBuffer, reopenBufferAs } = createBufferLoading({ ...scope, refreshProjectWorkspace });

  const refreshBufferPresentation = createBufferRefresh({ ...scope, reloadEditorConfig, flashUpdatedNote });

  setBufferLiveHandlers(props.projectId, {
    onEditorConfigChange: (rootId) => {
      const c = client();
      if (!c) return;
      void refreshOpenEditorConfigs({
        client: c,
        projectId: props.projectId,
        rootId,
        sessionId: sourceSessionId(),
      });
    },
    onCleanDiskChange: (buf) => {
      if (!buf.documentId || buf.kind !== "text") {
        refreshBufferPresentation(buf);
        return;
      }
      if (versionForBuffer(buf) || buf.loading) return;
      void observeEditorDocument(props.projectId, buf).then((document) => {
        const current = projectFilesState(props.projectId).byKey[buf.key];
        // A clean diverged document needs its file presentation refreshed.
        if (document?.diverged && current && !current.dirty) {
          refreshBufferPresentation(current);
        }
      }).catch((error) => {
        const current = projectFilesState(props.projectId).byKey[buf.key];
        if (current && !current.dirty) applyProjectSourceLoadFailure(props.projectId, buf.key, error);
      });
    },
    onDirtyDiskChange: (buf) => {
      flushFilesDraftSyncFor(props.projectId, buf.key);
      void observeEditorDocument(props.projectId, buf).then((document) => {
        if (!document?.diverged) return;
        if (state().activeKey !== buf.key) {
          setFilesActiveBuffer(props.projectId, buf.key, "presentation");
        }
      }).catch(() => undefined);
    },
    onForeignDeleteDirty: (bufs, event) => {
      const path = event.path?.trim() || "";
      const rootId = event.root_id?.trim() || "";
      if (!path || !rootId) return;
      setBulkClose({
        keys: bufs.map((b) => b.key),
        dirtyKeys: bufs.filter((b) => b.dirty).map((b) => b.key),
        foreignDelete: { rootId, path },
      });
      setConfirm({
        intent: "bulk-close",
        fileNames: bufs.map((b) => b.name),
      });
    },
  });

  observeFilesBufferDemand({
    ...scope, workspaceSettled, loadBuffer, displayedPane,
    pendingPreparation: (buffer) => [...walkEditorPreparation.pending(),
      ...(walking() && !walkChrome.reveal.ready() ? ["walk presentation"] : []),
      ...(buffer?.kind === "text" && buffer?.condenseContext && !buffer.loadError && !buffer.deleted && scopeChangeKindFor(props.projectId, buffer.rootId, buffer.path) !== "deleted" ? ["comparison context"] : []),
      ...(buffer && publishingPane() === filesPaneKey(buffer.key, buffer.kind) ? ["pane publication"] : [])],
  });

  // Per-buffer transient editor state resets when the active tab changes.
  createEffect(() => {
    void state().activeKey;
    setInnerLayerOpen(false);
    setSaveError(null);
    setSaving(false);
    setRestoringVersion(false);
    resetEditorStateChange();
    setSavedFlash(false);
  });

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

  const restoration = createFileRestoration({
    ...scope, versionHistoryForBuffer, restoringVersion, setRestoringVersion, returnToCurrent,
  });
  const { runFileRevert, runFileUndo, runEditorHunkReject } = restoration;

  // Document hosts bind one permanent Files buffer.
  createEffect(() => {
    const document = props.document;
    const path = document?.path.trim();
    if (!document || !path) return;
    const root = filesRoots().find((entry) => entry.id === document.rootId);
    if (!root) return;
    openFilesBuffer(props.projectId, {
      rootId: root.id,
      rootLabel: root.label,
      path,
      intent: "permanent",
    });
  });

  observeDefinitionNavigation();

  bindFilesCommands({
    scope, appStore: props.appStore, tree: () => tree, createTargetDir,
    showFilesTree: () => showFilesTree(), toggleFilesTree: () => toggleFilesTree(),
    onCloseTabIdle: () => props.onCloseTabIdle?.(), treeReveal, revealInTree,
    editorPaneEl: () => editorPaneEl(), readerHandles, tabs: tabCommands,
    requestCloseTab, documents, versions, restoration, editorActions,
    runSourceHistory, runGoToDefinition,
  });

  const [editorPaneEl, setEditorPaneEl] = createSignal<HTMLDivElement>();

  bindFindableView({
    id: "files-editor",
    provider: "codemirror",
    root: () => editorPaneEl(),
    getEditorView: () => {
      const key = projectFilesState(props.projectId).activeKey;
      if (!key) return null;
      return getFilesEditorView(props.projectId, key);
    },
    enabled: () => {
      const buf = activeBuffer();
      return (
        buf != null && !isComposedBufferKind(buf.kind) && !buf.loading && !buf.loadError
      );
    },
  });

  createEffect(() => {
    const document = props.document;
    const path = document?.path.trim();
    if (!document || !path) return;
    const root = filesRoots().find((entry) => entry.id === document.rootId);
    if (!root) return;
    const session: ProjectFileDocumentSession = {
      save: async () => {
        const key = filesBufferKeyAtAddress(props.projectId, root.id, path);
        if (!key) return false;
        flushFilesDraftSyncFor(props.projectId, key);
        const buffer = projectFilesState(props.projectId).byKey[key];
        if (!buffer) return false;
        return buffer.dirty ? performSave(key) : true;
      },
    };
    document.onSessionChange?.(session);
    onCleanup(() => document.onSessionChange?.(null));
  });

  observeDeleteGuard();

  // Observed extent avoids layout reads during dragging.
  const stageExtent = observeElementExtent(stageEl);
  const bodySize = (axis: ListPaneAxis) =>
    axis === "width" ? stageExtent.width() : stageExtent.height();
  const treeStyle = () => {
    // A drag past the hide threshold previews the tree closed.
    if (listPaneHidePreviewed(FILES_TREE_SURFACE, "width")) {
      return { "--den-files-tree-w": `${PANE_HIDDEN_PX}px` };
    }
    const prefs = listPanePrefs(FILES_TREE_SURFACE);
    if (prefs.widthPx == null) return undefined;
    return {
      "--den-files-tree-w": `${clampListPaneSizePx(FILES_TREE_SURFACE, "width", prefs.widthPx, bodySize("width"))}px`,
    };
  };
  let lastFilesClaimViewport = layoutViewportWidthPx();
  createEffect(() => {
    const viewport = layoutViewportWidthPx();
    if (viewport === lastFilesClaimViewport) return;
    lastFilesClaimViewport = viewport;
    setFilesTreeClaimed(false);
  });

  const showFilesTree = () => {
    setFilesTreeCollapsed(false);
    setFilesTreeClaimed(true);
    const viewportWidth = layoutViewportWidthPx();
    const hostWidth = splitHostWidthPx();
    const stageWidth = bodySize("width");
    const splitLive = stageWidth != null && hostWidth - stageWidth > 1;
    void widenWindowBy(
      filesShowWindowDeficitPx({
        viewportWidthPx: viewportWidth,
        navWidthPx: preferredNavWidthPx(),
        navCurrentlyVisible: viewportWidth - hostWidth > 1,
        navUserCollapsed: navCollapsedPref(),
        splitColumns: splitLive,
        chatWidthPx: preferredChatWidthPx(),
      }),
    );
  };
  const stripSide = () => workspaceOrientationPref() === "mirrored" ? "right" : "left";
  const hideFilesTree = () => {
    setFilesTreeClaimed(false);
    setFilesTreeCollapsed(true);
  };
  const toggleFilesTree = () => {
    if (filesTreeCollapsed()) showFilesTree();
    else hideFilesTree();
  };

  const hasRoots = () => filesRoots().length > 0;

  // Pointer-driven tab reordering stays separate from native file dragging.
  const tabDrag = createFilesTabDrag({
    ...scope,
    fileTabWindowDrag: () => props.fileTabWindowDrag,
    openInNewWindow: () => props.onOpenInNewWindow,
    onFileTabMovedOut: () => props.onFileTabMovedOut,
  });
  const { pullOffPreview, observeDrops } = tabDrag;
  let tabsScrollerEl: HTMLDivElement | undefined;
  let tabsEl: HTMLDivElement | undefined;
  /** Rebinds observers after strip remounts. */
  const [tabStripMountEpoch, setTabStripMountEpoch] = createSignal(0);
  const noteTabStripElement = () => setTabStripMountEpoch((n) => n + 1);

  const { scrollActiveTabIntoView } = createFilesTabMeasurements({
    ...scope, previousVersionForBuffer, tabStripMountEpoch,
    setRovingTabKey, setHiddenTabCount, setShowLeadingFade, setShowTrailingFade,
  });

  const openFilesMenu = (anchor: ContextMenuAnchor, target: FilesContextTarget) => {
    const items = filesContextMenuItems(target, ctxActions());
    if (items?.length) setCtxMenu({ anchor, items });
  };
  const openTabContextMenu = (key: FileBufferKey, anchor: ContextMenuAnchor) => {
    const buf = state().byKey[key];
    if (buf) openFilesMenu(anchor, { surface: "tab", bufferKey: key, rootId: buf.rootId, path: buf.path, name: buf.name });
  };

  observeDrops();

  return (
    <div
      class="project-files-stage-boundary"
      data-testid="project-files-stage-boundary"
      data-ready={String(stageReady())}
      data-boot={stageReady() ? "ready" : "pending"}
      data-presentation={
        retainedWorkspacePending() ? "preparing" : "published"
      }
      aria-busy={retainedWorkspacePending()}
    >
      <div
        class="project-files-stage-boundary__loading"
        classList={{
          "project-files-stage-boundary__loading--ready": stageReady(),
        }}
        aria-hidden={stageReady()}
        inert={stageReady() ? true : undefined}
      >
        <ProjectLoadingStage
          label={holdLabel()}
          hint={holdHint()}
          tone={holdTone()}
        />
      </div>
      <div
        class="project-files-stage-boundary__content den-retained-presentation"
        classList={{
          "project-files-stage-boundary__content--ready": stageReady(),
        }}
        data-retained={retainedWorkspacePending() ? "true" : "false"}
        aria-hidden={!stageReady()}
        inert={!stageReady() || retainedWorkspacePending() ? true : undefined}
      >
    <div
      class="project-files-view"
      classList={{
        "project-files-view--tree-collapsed": filesTreeCollapsed(),
        "project-files-view--tree-claimed": filesTreeClaimed(),
        "project-files-view--document": props.document != null,
      }}
      data-testid="project-files-view"
      style={treeStyle()}
    >
      <Show when={pullOffPreview()}>
        <ResidentPortal mount={document.body}>
          <div
            class="den-files-tab-pull-preview"
            style={{
              left: `${pullOffPreview()?.x ?? 0}px`,
              top: `${pullOffPreview()?.y ?? 0}px`,
            }}
            role="status"
            aria-label={`Release to move ${
              pullOffPreview()?.name ?? "file"
            } in a new window`}
            data-testid="files-tab-pull-preview"
          >
            <span class="den-files-tab-pull-preview__window" aria-hidden="true">
              <span />
            </span>
            <span class="den-files-tab-pull-preview__name">
              {pullOffPreview()?.name}
            </span>
            <span class="den-files-tab-pull-preview__hint">
              Release to move to new window
            </span>
          </div>
        </ResidentPortal>
      </Show>
      <BrowseStagePanel
        appStore={props.appStore}
        scrollHost="content"
        testId="project-files-stage"
      >
        <div class="den-files-stage" ref={setStageEl}>
          <FilesTreePane
            projectId={props.projectId}
            appStore={props.appStore}
            client={client()}
            paneMode={paneMode()}
            hidden={filesTreeCollapsed()}
            style={treeStyle()}
            history={sourceHistory}
            lifecycleHooks={lifecycleHooks}
            walkInitialFile={walkInitialFile()}
            summarySelection={activeFileSummarySelection()}
            onOpenSummaryLocation={openFileSummaryLocation}
            review={
              <ReviewLens
                projectId={props.projectId}
                client={client()}
                appStore={props.appStore}
                visible={paneMode() === "review"}
                navigationRevision={state().aimRevision}
                onOpenFile={openReviewRowFile}
                onViewReviewed={(version) => {
                  leaveWalk(props.projectId);
                  const root = filesRoots().find((r) => r.id === version.rootId);
                  const key = openFilesBuffer(props.projectId, {
                    rootId: version.rootId, rootLabel: root?.label ?? "repo",
                    fileId: version.fileId, path: version.path, intent: "transient",
                  });
                  setSelectedEntry({ rootId: version.rootId, path: version.path, kind: "file" });
                  setSelectedFileVersion(props.projectId, key, version);
                  recordInventoryOpen(props.projectId, { rootId: version.rootId, path: version.path });
                }}
                onRevertFile={(rootId, path) => void runFileRevert(rootId, path)}
                onRowMenu={(anchor, row) => openFilesMenu({ x: anchor.clientX, y: anchor.clientY },
                  { surface: "changed-file", rootId: row.rootId, path: row.path, name: row.basename })}
              />
            }
            tree={
              <Show
                when={hasRoots()}
                fallback={
                  <p
                    class="den-files-empty"
                    data-testid="files-tree-no-roots"
                  >
                    Attach a folder to browse its files.
                  </p>
                }
              >
                <FilesTree
                  projectId={props.projectId}
                  workspaceId={treeWorkspaceId()}
                  visible={paneMode() === "files"}
                  roots={filesRoots()}
                  onWorkspaceMismatch={refreshProjectWorkspace}
                  onInitialLoadSettled={(workspaceId) => {
                    if (workspaceSettled() && filesWorkspaceId() === workspaceId) {
                      setMountedTreeWorkspace(workspaceId);
                      setTreeSettled(true);
                    }
                  }}
                  client={client()}
                  sessionId={filesSourceAddress()}
                  reviewScope={(() => {
                    void scopeMarksVersion();
                    const baseline = scopeBaseline(props.projectId);
                    return baseline ? { baseline, mark_user_edits: resolvedScope(props.projectId).markUserEdits } : undefined;
                  })()}
                  retainPresentation={!workspaceSettled() || !treeSettled()}
                  autoReveal={editorRevealInTreePref()}
                  navigationRevision={state().aimRevision}
                  navigationOrigin={state().aimOrigin}
                  activeFile={editorRevealInTreePref() ? publishedTreeFile() : null}
                  onRevealMotionStart={scrollActiveTabIntoView}
                  selectedEntry={(() => {
                    const selected = selectedEntry();
                    if (!editorRevealInTreePref() || selected?.kind !== "file") return selected;
                    const explicit = explicitTreeReveal();
                    if (explicit?.revision === state().aimRevision && explicit.rootId === selected.rootId &&
                      explicit.path === selected.path) return selected;
                    const displayed = displayedTreeFile();
                    return displayed ? { ...displayed, kind: "file" as const } : selected;
                  })()}
                  onSelectEntry={setSelectedEntry}
                  agentPresenceForPath={agentPresenceForPath}
                  inScope={(rootId, path) => {
                    void scopeMarksVersion();
                    return isInScope(props.projectId, rootId, path);
                  }}
                  addedInScope={(rootId, path) => {
                    void scopeMarksVersion();
                    return isAddedInScope(props.projectId, rootId, path);
                  }}
                  onOpenFile={openFile}
                  onRevertFile={(rootId, path) => void runFileRevert(rootId, path)}
                  createEntry={createEntry}
                  filterQuery={filterQuery()}
                  onFilterQueryChange={setFilterQuery}
                  filterOpen={filterOpen()}
                  onFilterClose={() => setFilterOpen(false)}
                  renaming={renaming()}
                  onCommitRename={commitRename}
                  onCancelRename={() => setRenaming(null)}
                  onMovePath={onMovePath}
                  onReady={(handle) => {
                    tree = handle;
                    applyPendingTreeReveal();
                  }}
                  onRowMenu={openFilesMenu}
                />
              </Show>
            }
          />
          <ResizeHandle
            class="den-files-split"
            hidden={filesTreeCollapsed()}
            axis="width"
            direction={workspaceOrientationPref() === "mirrored" ? -1 : 1}
            size={(axis) =>
              resolveListPaneSizePx(
                FILES_TREE_SURFACE,
                listPanePrefs(FILES_TREE_SURFACE),
                axis,
                bodySize(axis),
              )
            }
            clamp={(value, axis) =>
              axis === "width" &&
              dragRequestsHide(value, listPaneSizeMinPx(FILES_TREE_SURFACE, axis))
                ? PANE_HIDDEN_PX
                : clampListPaneSizePx(
                    FILES_TREE_SURFACE,
                    axis,
                    value,
                    bodySize(axis),
                  )
            }
            onBegin={(axis, startSize) =>
              beginListPaneResize(
                FILES_TREE_SURFACE,
                axis,
                startSize,
                bodySize(axis),
                axis === "width" ? hideFilesTree : undefined,
              )
            }
            onReset={(axis) => void resetListPaneSize(FILES_TREE_SURFACE, axis)}
            rootClass="den-stage--split-resizing"
            ariaLabel="Resize file tree"
            ariaMin={(axis) => listPaneSizeMinPx(FILES_TREE_SURFACE, axis)}
            ariaMax={(axis) =>
              maxListPaneSizePx(FILES_TREE_SURFACE, axis, bodySize(axis))
            }
            tip="Drag to resize or hide the file tree — double-click to reset"
            testId="files-tree-resize"
          />
          <div class="den-files-editor-pane" ref={setEditorPaneEl}>
            <Show when={editorCommandBarPlacement() === "files-editor"}>
              <div
                class="den-editor-command-bar-host--files"
                data-testid="find-bar-host-files"
              >
                <EditorCommandBar />
              </div>
            </Show>
            <FileUndoToast
              projectId={props.projectId}
              onUndo={(undo) => void runFileUndo(undo)}
            />
            <Show when={walking()}>
              <WalkTransport projectId={props.projectId} presentation={walkChrome.reveal}
                host={editorPaneEl() ?? undefined} dockMount={editorDockMounts().get(displayedPane() ?? "")} />
            </Show>
            <Show
              when={state().order.length > 0}
              fallback={
                <>
                  <EmptyFilesTabStrip
                    treeCollapsed={filesTreeCollapsed()}
                    side={stripSide()}
                    onToggleTree={toggleFilesTree}
                    onAutoRestore={showFilesTree}
                  />
                  <FilesEmptyEditor
                    projectId={props.projectId}
                    sessionId={filesSourceAddress()}
                    client={client()}
                    roots={filesRoots()}
                    rootId={selectedProjectRoot()?.id}
                    folder={selectedFolder()}
                    childCount={(rootId, path) => directoryEntries(rootId, path)?.length ?? null}
                    onCopyPath={copyAbsolutePathRef}
                    onCopyRelativePath={copyRelativePathRef}
                    onOpenFile={(selection) => openFile(selection, "permanent")}
                  />
                </>
              }
            >
              <FilesTabStrip
                treeCollapsed={filesTreeCollapsed()}
                side={stripSide()}
                onToggleTree={toggleFilesTree}
                onAutoRestore={showFilesTree}
                scope={scope}
                tabs={tabCommands}
                drag={tabDrag}
                hiddenTabCount={hiddenTabCount()}
                showLeadingFade={showLeadingFade()}
                showTrailingFade={showTrailingFade()}
                scopeMarksVersion={scopeMarksVersion}
                previousVersionForBuffer={previousVersionForBuffer}
                agentPresenceForPath={agentPresenceForPath}
                mergingKey={mergeModel()?.bufferKey}
                requestCloseTab={requestCloseTab}
                onTabContextMenu={openTabContextMenu}
                onScrollerElement={(el) => {
                  tabsScrollerEl = el;
                  noteTabStripElement();
                }}
                onTabsElement={(el) => {
                  tabsEl = el;
                  noteTabStripElement();
                }}
              />
              <FilesPaneDeck
                projectId={props.projectId}
                sessionId={filesSourceAddress()}
                client={client()}
                roots={filesRoots()}
                scanUpdate={props.appStore.state.latestCodeScan}
                document={props.document}
                onRevealInTranscript={props.onRevealInTranscript}
                activePane={activePane() ?? null}
                onDisplayedChange={setDisplayedPane}
                onPublishingChange={setPublishingPane}
                pending={editorPresentationPending()}
                retainedWait={showRetainedEditorWait()}
                preparation={walkEditorPreparation}
                walking={walking()}
                walkReady={walkChrome.reveal.ready()}
                readerHandles={readerHandles}
                onDockMount={(pane, element) => setEditorDockMounts(previous => {
                  const next = new Map(previous);
                  if (element) next.set(pane, element);
                  else next.delete(pane);
                  return next;
                })}
                saveError={saveError() ?? activeBuffer()?.closeError ?? editorDraftPersistenceError(props.projectId)}
                updatedNote={updatedNote()}
                restoringVersion={restoringVersion()}
                cursor={cursor()}
                onCursor={setCursor}
                onInnerLayerChange={setInnerLayerOpen}
                observationNoticeFor={observationNoticeFor}
                revealInTree={revealInTree}
                retryEditorOpening={retryEditorOpening}
                scrollActiveTabIntoView={scrollActiveTabIntoView}
                openWalkStepFile={openWalkStepFile}
                reopenBufferAs={reopenBufferAs}
                confirmations={confirmations}
                documents={documents}
                versions={versions}
                history={versionHistory}
                restoration={restoration}
                definitions={definitions}
                merge={merge}
                editorActions={editorActions}
              />
            </Show>
          </div>
        </div>
      </BrowseStagePanel>
      <Show when={walking()}>
        <PresentationProvider preparation={walkChrome.preparation}>
          <ResidentPresenceProvider presence={walkChrome.reveal.ready() ? "active" : "pending"}>
            <StepBar
              projectId={props.projectId}
              presentation={walkChrome.reveal}
              onRevealInTranscript={props.onRevealInTranscript}
            />
          </ResidentPresenceProvider>
        </PresentationProvider>
      </Show>
      <Show when={walkChrome.waiting()}>
        <div class="den-presentation-wait" role="status" data-testid="walk-preparing">
          Preparing walk…
          <DenButton class="pointer-events-auto" variant="link" compact onClick={() => leaveWalk(props.projectId)}>Cancel</DenButton>
        </div>
      </Show>
      <FilesStageDialogs
        projectId={props.projectId}
        appStore={props.appStore}
        client={client()}
        roots={filesRoots()}
        browse={browse}
        editorPane={editorPaneEl() ?? null}
        activeBuffer={activeBuffer()}
        saving={saving()}
        setSaveError={setSaveError}
        menu={ctxMenu()}
        onCloseMenu={() => setCtxMenu(null)}
        confirmations={confirmations}
        mutations={mutations}
        editorActions={editorActions}
      />
    </div>
      </div>
      <Show when={showRetainedWorkspaceWait()}>
        <div class="den-presentation-wait" role="status">
          Updating files…
        </div>
      </Show>
    </div>
  );
}

