import { createFilesTreeLayout } from "../tree/files-tree-layout.ts";
import { FilesEditorWorkspace, FilesTabPullPreview } from "./FilesEditorWorkspace.tsx";
import { editorDraftPersistenceError } from "../documents/editor-document.ts";
import { createFilesWorkspaceReview } from "../review/files-workspace-review.ts";
import { createFilesTreeInteraction } from "../tree/files-tree-interaction.ts";
import { createFilesWorkspaceWalk, FilesWalkStatus } from "../walk/files-workspace-walk.tsx";
import { bindFilesEditorSurface } from "../editor/files-editor-surface.ts";
import type { ProjectFilesViewProps } from "./project-files-view-props.ts";
import { createFilesWorkspacePresentation, observeFilesWorkspaceDemand } from "./files-workspace-presentation.ts";
import { bindFilesBufferSynchronization } from "../documents/files-buffer-synchronization.ts";
import { bindFilesCommands } from "../commands/files-command-bindings.ts";
import { createFilesTabCommands } from "../tabs/files-tab-commands.ts";
import { createFilesPresentationTracking } from "./files-presentation-tracking.ts";
import { createFileDocumentCommands } from "../documents/file-document-commands.ts";
import { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import { createDefinitionNavigation } from "../editor/definition-navigation.ts";
import { createFilesTabMeasurements } from "../tabs/files-tab-measurements.ts";
import { createFilesTabDrag } from "../tabs/files-tab-drag.ts";
import { createFileRestoration } from "../history/file-restoration-commands.ts";
import { createFileConfirmations } from "../commands/file-confirmations.ts";
import { createBufferLoading } from "../documents/files-buffer-loading.ts";
import { createFileMergeCommands } from "../documents/file-merge-commands.ts";
import { FilesNavigationPane } from "./FilesNavigationPane.tsx";
import { Show, createEffect, createMemo, createSignal, on, onCleanup, onMount, untrack } from "solid-js";
import { registerEscapeLadderLayer } from "../../find/escape-ladder.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { webkitAtspiWorkaroundActive } from "../../platform/desktop/webkit-atspi-workaround.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { BrowseStagePanel } from "../../components/browse/BrowseStagePanel.tsx";
import { ResizeHandle } from "../../components/primitives/ResizeHandle.tsx";
import { ProjectLoadingStage } from "../../components/shell/ProjectLoadingStage.tsx";
import { createFilesEditorSynchronization } from "../editor/files-editor-synchronization.ts";
import { orderWithPinnedGroup } from "../tabs/files-tab-strip.ts";
import { filesTreeClaimed, filesTreeCollapsed } from "../tree/files-tree-window-state.ts";
import { createFilesWorkspace } from "../source/files-workspace.ts";
import type { FilesScope } from "./files-scope.ts";
import { FilesStageDialogs } from "./FilesStageDialogs.tsx";
import { focusedFilesBufferKey, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState, type FileBuffer } from "../documents/files-buffer-state.ts";
import { type FileBufferKey } from "./project-files-model.ts";
import { getFilesStagePaneMode, subscribeFilesStagePaneMode } from "../review/review-pane.ts";

export function ProjectFilesView(props: ProjectFilesViewProps) {
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
  const state = () => projectFilesState(props.projectId);
  const activeBuffer = (): FileBuffer | null => {
    const key = state().activeKey;
    return key ? (state().byKey[key] ?? null) : null;
  };

  // The strip follows the aim; the editor presents `activeKey`.
  const aimedKey = (): FileBufferKey | null =>
    focusedFilesBufferKey(props.projectId);
  const presentation = createFilesWorkspacePresentation({
    projectId: props.projectId, projectName: () => props.projectName,
    document: () => props.document, state, activeBuffer, aimedKey,
    workspaceFault, workspaceError, workspaceSettled, treeSettled, filesWorkspaceId,
  });
  const { holdLabel, holdHint, holdTone, treeWorkspaceId, stageReady,
    retainedWorkspacePending, showRetainedWorkspaceWait, displayedPane, setDisplayedPane,
    publishingPane, setPublishingPane, editorDockMounts, setEditorDockMounts,
    displayedBuffer, editorPresentationPending, showRetainedEditorWait,
    mountedTreeWorkspace, setMountedTreeWorkspace, displayedTreeFile,
    publishedTreeFile, activePane } = presentation;
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
  const { editorContextActions } = editorActions;
  const tabCommands = createFilesTabCommands({
    ...scope, loadBuffer: (buffer) => loadBuffer(buffer), setBulkClose, setConfirm,
  });
  const { filesOpenListOpen, setRovingTabKey, closeBuffers, requestCloseFamily, openFilesList, closeBuffer } = tabCommands;
  const [hiddenTabCount, setHiddenTabCount] = createSignal(0);
  // Under the WebKitGTK AT-SPI workaround the loading text leaves the DOM once its layer has faded.
  const [loadingMounted, setLoadingMounted] = createSignal(true);
  let loadingLayer: HTMLDivElement | undefined;
  createEffect(on(stageReady, (ready) => {
    if (!ready) {
      setLoadingMounted(true);
      return;
    }
    const fadeMs = loadingLayer ? longestTransitionMs(getComputedStyle(loadingLayer)) : 0;
    if (fadeMs <= 0) {
      setLoadingMounted(false);
      return;
    }
    // A timer, not transitionend: a layer that fades while its surface is retained raises no event.
    const timer = setTimeout(() => {
      if (stageReady()) setLoadingMounted(false);
    }, fadeMs);
    onCleanup(() => clearTimeout(timer));
  }));
  const [showLeadingFade, setShowLeadingFade] = createSignal(false);
  const [showTrailingFade, setShowTrailingFade] = createSignal(false);
  const treeInteraction = createFilesTreeInteraction({
    props, scope, directoryEntries, confirmDirectoryChange, observeDirectory,
    workspaceSettled, filesWorkspaceId, mountedTreeWorkspace,
    scopeMarksVersion: () => scopeMarksVersion(), stageEl: () => stageEl(),
    showFilesTree: () => showFilesTree(), setDeleteGuard, requestCloseTab,
    requestCloseFamily, openFilesList, editorContextActions,
    openReviewRowFile: (...args) => openReviewRowFile(...args),
    runFileRevert: (...args) => runFileRevert(...args),
  });
  const { selectedEntry, setSelectedEntry, selectedProjectRoot, selectedFolder,
    filterOpen, setFilterOpen, filterQuery, setFilterQuery, renaming, setRenaming,
    mutations, trashConfirm, commitRename, onMovePath, sourceHistory, runSourceHistory,
    tree, setTreeHandle, browse, treeReveal, lifecycleHooks, createTargetDir,
    explicitTreeReveal, revealInTree, copyAbsolutePathRef, copyRelativePathRef,
    createEntry, openFile, openFilesMenu, openTabContextMenu, ctxMenu, setCtxMenu } = treeInteraction;
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
  });
  onCleanup(
    subscribeFilesStagePaneMode((id) => {
      if (id === props.projectId.trim()) setPaneMode(getFilesStagePaneMode(id));
    }),
  );
  const workspaceWalk = createFilesWorkspaceWalk({ projectId: () => props.projectId, paneMode, selectedEntry });
  const { walking, walkEditorPreparation, walkChrome, walkPresentation, walkInitialFile } = workspaceWalk;
  const readerHandles = new Map<string, { gotoLine: () => void | Promise<void> }>();
  const review = createFilesWorkspaceReview({
    scope, appStore: props.appStore, chatSessionId, filesSourceAddress,
    refreshWatchCoverage, displayedBuffer, walking, walkPresentation, setSelectedEntry,
    scrollActiveTabIntoView: () => scrollActiveTabIntoView(),
  });
  const { versionHistory, versions, versionHistoryForBuffer, loadVersionHistory,
    versionForBuffer, returnToCurrent, dropFileVersionState, openWalkStepFile,
    openReviewRowFile, openFileSummaryLocation, previousVersionForBuffer,
    activeFileSummarySelection, scopeMarksVersion, scopeContentVersion,
    refreshScopeMarks, agentPresenceForPath } = review;

  createFilesPresentationTracking({
    ...scope, presentationToken, stageEl: () => stageEl(), stageReady, scopeContentVersion,
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

  const stripOrder = createMemo(() => {
    const s = state();
    return orderWithPinnedGroup(s.order, s.byKey);
  });

  const { loadBuffer, reopenBufferAs } = createBufferLoading({ ...scope, refreshProjectWorkspace });

  bindFilesBufferSynchronization({ ...scope, flashUpdatedNote, setBulkClose, setConfirm });

  observeFilesWorkspaceDemand({
    ...scope, workspaceSettled, loadBuffer, displayedPane, publishingPane,
    pendingWalkPreparation: walkEditorPreparation.pending,
    walking, walkReady: walkChrome.reveal.ready,
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

  const restoration = createFileRestoration({
    ...scope, versionHistoryForBuffer, restoringVersion, setRestoringVersion, returnToCurrent,
  });
  const { runFileRevert, runFileUndo } = restoration;

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
    scope, appStore: props.appStore, tree, createTargetDir,
    showFilesTree: () => showFilesTree(), toggleFilesTree: () => toggleFilesTree(),
    onCloseTabIdle: () => props.onCloseTabIdle?.(), treeReveal, revealInTree,
    editorPaneEl: () => editorPaneEl(), readerHandles, tabs: tabCommands,
    requestCloseTab, documents, versions, restoration, editorActions,
    runSourceHistory, runGoToDefinition,
  });

  const [editorPaneEl, setEditorPaneEl] = createSignal<HTMLDivElement>();

  bindFilesEditorSurface({ projectId: props.projectId, activeBuffer, filesRoots,
    editorPaneEl, document: () => props.document, performSave });

  observeDeleteGuard();

  const treeLayout = createFilesTreeLayout(stageEl);
  const { treeStyle, showFilesTree, toggleFilesTree, stripSide } = treeLayout;

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
        ref={(element) => {
          loadingLayer = element;
        }}
        class="project-files-stage-boundary__loading"
        classList={{
          "project-files-stage-boundary__loading--ready": stageReady(),
        }}
        aria-hidden={stageReady()}
        inert={stageReady() ? true : undefined}
      >
        <Show when={!webkitAtspiWorkaroundActive() || loadingMounted()}>
          <ProjectLoadingStage
            label={holdLabel()}
            hint={holdHint()}
            tone={holdTone()}
          />
        </Show>
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
      <FilesTabPullPreview preview={pullOffPreview()} />
      <BrowseStagePanel
        appStore={props.appStore}
        scrollHost="content"
        testId="project-files-stage"
      >
        <div class="den-files-stage" ref={setStageEl}>
          <FilesNavigationPane
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
            onOpenReviewFile={openReviewRowFile}
            workspaceId={treeWorkspaceId()}
            roots={filesRoots()}
            onWorkspaceMismatch={refreshProjectWorkspace}
            onInitialLoadSettled={(workspaceId) => {
              if (workspaceSettled() && filesWorkspaceId() === workspaceId) {
                setMountedTreeWorkspace(workspaceId);
                setTreeSettled(true);
              }
            }}
            sessionId={filesSourceAddress()}
            scopeMarksVersion={scopeMarksVersion()}
            retainPresentation={!workspaceSettled() || !treeSettled()}
            navigationRevision={state().aimRevision}
            navigationOrigin={state().aimOrigin}
            publishedTreeFile={publishedTreeFile()}
            displayedTreeFile={displayedTreeFile()}
            explicitTreeReveal={explicitTreeReveal()}
            onRevealMotionStart={scrollActiveTabIntoView}
            selectedEntry={selectedEntry()}
            onSelectEntry={setSelectedEntry}
            agentPresenceForPath={agentPresenceForPath}
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
            onReady={setTreeHandle}
            onRowMenu={openFilesMenu}
          />
          <ResizeHandle
            class="den-files-split"
            hidden={filesTreeCollapsed()}
            axis="width"
            direction={treeLayout.resizeDirection()}
            size={treeLayout.size}
            clamp={treeLayout.clamp}
            onBegin={treeLayout.beginResize}
            onReset={treeLayout.resetSize}
            rootClass="den-stage--split-resizing"
            ariaLabel="Resize file tree"
            ariaMin={treeLayout.minSize}
            ariaMax={treeLayout.maxSize}
            tip="Drag to resize or hide the file tree — double-click to reset"
            testId="files-tree-resize"
          />
          <FilesEditorWorkspace
            onEditorElement={setEditorPaneEl}
            editorElement={editorPaneEl() ?? undefined}
            walkPresentation={walkChrome.reveal}
            walkDockMount={editorDockMounts().get(displayedPane() ?? "")}
            onUndo={(undo) => void runFileUndo(undo)}
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
            rootId={selectedProjectRoot()?.id}
            folder={selectedFolder()}
            childCount={(rootId, path) => directoryEntries(rootId, path)?.length ?? null}
            onCopyPath={copyAbsolutePathRef}
            onCopyRelativePath={copyRelativePathRef}
            onOpenFile={(selection) => openFile(selection, "permanent")}
          />
        </div>
      </BrowseStagePanel>
      <FilesWalkStatus projectId={props.projectId} walk={workspaceWalk} onRevealInTranscript={props.onRevealInTranscript} />
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

function durationMs(value: string): number {
  const amount = Number.parseFloat(value);
  if (!Number.isFinite(amount)) return 0;
  return value.trim().endsWith("ms") ? amount : amount * 1000;
}

/** The longest duration plus delay among an element's transitions. */
function longestTransitionMs(style: CSSStyleDeclaration): number {
  const durations = style.transitionDuration.split(",");
  const delays = style.transitionDelay.split(",");
  return durations.reduce((longest, duration, index) =>
    Math.max(longest, durationMs(duration) + durationMs(delays[index % delays.length] ?? "0s")), 0);
}
