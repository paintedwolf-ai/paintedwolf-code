import { createFilesTreeLayout } from "../tree/files-tree-layout.ts";
import { FilesEditorWorkspace, FilesTabPullPreview } from "./FilesEditorWorkspace.tsx";
import { createFilesWorkspaceReview } from "../review/files-workspace-review.ts";
import { createFilesTreeInteraction } from "../tree/files-tree-interaction.ts";
import { createFilesWorkspaceWalk, FilesWalkStatus } from "../walk/files-workspace-walk.tsx";
import type { ProjectFilesViewProps } from "./project-files-view-props.ts";
import { createFilesWorkspacePresentation } from "./files-workspace-presentation.ts";
import { bindFilesCommands } from "../commands/files-command-bindings.ts";
import { createFilesTabMeasurements } from "../tabs/files-tab-measurements.ts";
import { createFilesTabDrag } from "../tabs/files-tab-drag.ts";
import { createFileRestoration } from "../history/file-restoration-commands.ts";
import { FilesNavigationPane } from "./FilesNavigationPane.tsx";
import { Show, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { webkitAtspiWorkaroundActive } from "../../platform/desktop/webkit-atspi-workaround.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { BrowseStagePanel } from "../../components/browse/BrowseStagePanel.tsx";
import { ResizeHandle } from "../../components/primitives/ResizeHandle.tsx";
import { ProjectLoadingStage } from "../../components/shell/ProjectLoadingStage.tsx";
import { filesTreeClaimed, filesTreeCollapsed } from "../tree/files-tree-window-state.ts";
import { createFilesWorkspace } from "../source/files-workspace.ts";
import { FilesStageDialogs } from "./FilesStageDialogs.tsx";
import { getFilesStagePaneMode, subscribeFilesStagePaneMode } from "../review/review-pane.ts";
import { useProjectFilesController } from "./useProjectFilesController.ts";

export function ProjectFilesView(props: ProjectFilesViewProps) {
  const client = () => props.client ?? getLycaonClient() ?? null;
  /** The selected chat when it belongs to this project. */
  const chatSessionId = createMemo(() => {
    const session = props.appStore.state.currentSession;
    return session?.project_id?.trim() === props.projectId.trim()
      ? session.id?.trim() || undefined
      : undefined;
  });
  /** Requests name the chat selected when they run; nothing observes it. */
  const sourceSessionId = () => untrack(chatSessionId);
  const workspace = createFilesWorkspace({
    projectId: props.projectId, roots: () => props.roots, client, chatSessionId,
    reachable: () => isBackendReachable(props.appStore.state.sidecarStatus),
    restoreHotExit: () => props.restoreHotExit !== false,
  });
  const {
    filesWorkspaceId, filesSourceAddress, filesRoots, workspaceError, workspaceFault, workspaceSettled,
    treeSettled, setTreeSettled, refreshProjectWorkspace, refreshWatchCoverage, observationNoticeFor,
    directoryEntries, confirmDirectoryChange, observeDirectory,
  } = workspace;
  const controller = useProjectFilesController({
    props, client, chatSessionId, sourceSessionId, workspace,
  });
  const {
    state, activeBuffer, aimedKey, retryEditorOpening, setSaveError, effectiveSaveError,
    scope, bindDelegates, confirmations, editorActions, tabCommands, hiddenTabCount, setHiddenTabCount,
    loadingMounted, setLoadingLayer, showLeadingFade, setShowLeadingFade, showTrailingFade, setShowTrailingFade,
    documents, restoringVersion, setRestoringVersion, updatedNote, definitions, merge,
    cursor, setCursor, setInnerLayerOpen, reopenBufferAs,
    editorPaneEl, setEditorPaneEl, stageEl, setStageEl, setTabsScrollerEl, setTabsEl, observeControllerEffects,
  } = controller;
  const presentation = createFilesWorkspacePresentation({
    projectId: props.projectId, projectName: () => props.projectName,
    document: () => props.document, state, activeBuffer, aimedKey,
    workspaceFault, workspaceError, workspaceSettled, treeSettled, filesWorkspaceId,
  });
  const {
    holdLabel, holdHint, holdTone, treeWorkspaceId, stageReady,
    retainedWorkspacePending, showRetainedWorkspaceWait, displayedPane, setDisplayedPane,
    publishingPane, setPublishingPane, editorDockMounts, setEditorDockMounts,
    displayedBuffer, editorPresentationPending, showRetainedEditorWait,
    mountedTreeWorkspace, setMountedTreeWorkspace, displayedTreeFile,
    publishedTreeFile, activePane,
  } = presentation;
  const { setDeleteGuard, requestCloseTab, observeDeleteGuard } = confirmations;
  const { editorContextActions } = editorActions;
  const { setRovingTabKey, requestCloseFamily, openFilesList } = tabCommands;
  const { saving } = documents;
  const { runGoToDefinition } = definitions;
  const { mergeModel } = merge;

  const treeInteraction = createFilesTreeInteraction({
    props, scope, directoryEntries, confirmDirectoryChange, observeDirectory,
    workspaceSettled, filesWorkspaceId, mountedTreeWorkspace,
    scopeMarksVersion: () => scopeMarksVersion(), stageEl: () => stageEl(),
    showFilesTree: () => showFilesTree(), setDeleteGuard, requestCloseTab,
    requestCloseFamily, openFilesList, editorContextActions,
    openReviewRowFile: (...args) => openReviewRowFile(...args),
    runFileRevert: (...args) => runFileRevert(...args),
  });
  const {
    selectedEntry, setSelectedEntry, selectedProjectRoot, selectedFolder,
    filterOpen, setFilterOpen, filterQuery, setFilterQuery, renaming, setRenaming,
    mutations, trashConfirm, commitRename, onMovePath, sourceHistory, runSourceHistory,
    tree, setTreeHandle, browse, treeReveal, lifecycleHooks, createTargetDir,
    explicitTreeReveal, revealInTree, copyAbsolutePathRef, copyRelativePathRef,
    createEntry, openFile, openFilesMenu, openTabContextMenu, ctxMenu, setCtxMenu,
  } = treeInteraction;

  const [paneMode, setPaneMode] = createSignal(getFilesStagePaneMode(props.projectId));
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
  const {
    versionHistory, versions, versionHistoryForBuffer, loadVersionHistory,
    versionForBuffer, returnToCurrent, dropFileVersionState, openWalkStepFile,
    openReviewRowFile, openFileSummaryLocation, previousVersionForBuffer,
    activeFileSummarySelection, scopeMarksVersion, scopeContentVersion,
    refreshScopeMarks, agentPresenceForPath,
  } = review;

  bindDelegates({
    editorPresentationPending,
    versionForBuffer,
    dropFileVersionState,
    loadVersionHistory,
    refreshScopeMarks,
    lifecycleHooks,
    copyAbsolutePathRef,
    copyRelativePathRef,
  });

  const restoration = createFileRestoration({
    ...scope, versionHistoryForBuffer, restoringVersion, setRestoringVersion, returnToCurrent,
  });
  const { runFileRevert, runFileUndo } = restoration;

  observeControllerEffects({
    stageReady,
    displayedPane,
    publishingPane,
    scopeContentVersion,
    pendingWalkPreparation: walkEditorPreparation.pending,
    walking,
    walkReady: walkChrome.reveal.ready,
    hasTreeOverlayOpen: () => trashConfirm() != null || ctxMenu() != null,
  });

  bindFilesCommands({
    scope, appStore: props.appStore, tree, createTargetDir,
    showFilesTree: () => showFilesTree(), toggleFilesTree: () => toggleFilesTree(),
    onCloseTabIdle: () => props.onCloseTabIdle?.(), treeReveal, revealInTree,
    editorPaneEl: () => editorPaneEl(), readerHandles, tabs: tabCommands,
    requestCloseTab, documents, versions, restoration, editorActions,
    runSourceHistory, runGoToDefinition,
  });

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
      data-presentation={retainedWorkspacePending() ? "preparing" : "published"}
      aria-busy={retainedWorkspacePending()}
    >
      <div
        ref={setLoadingLayer}
        class="project-files-stage-boundary__loading"
        classList={{ "project-files-stage-boundary__loading--ready": stageReady() }}
        aria-hidden={stageReady()}
        inert={stageReady() ? true : undefined}
      >
        <Show when={!webkitAtspiWorkaroundActive() || loadingMounted()}>
          <ProjectLoadingStage label={holdLabel()} hint={holdHint()} tone={holdTone()} />
        </Show>
      </div>
      <div
        class="project-files-stage-boundary__content den-retained-presentation"
        classList={{ "project-files-stage-boundary__content--ready": stageReady() }}
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
          <BrowseStagePanel appStore={props.appStore} scrollHost="content" testId="project-files-stage">
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
                ariaMax={treeLayout.maxSize}
                ariaMin={treeLayout.minSize}
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
                saveError={effectiveSaveError()}
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
                  setTabsScrollerEl(el);
                  noteTabStripElement();
                }}
                onTabsElement={(el) => {
                  setTabsEl(el);
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
