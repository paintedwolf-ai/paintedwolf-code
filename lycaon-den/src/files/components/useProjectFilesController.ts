import { bindFilesEditorSurface } from "../editor/files-editor-surface.ts";
import type { ProjectFilesViewProps } from "./project-files-view-props.ts";
import { observeFilesWorkspaceDemand } from "./files-workspace-presentation.ts";
import { createFilesPresentationTracking } from "./files-presentation-tracking.ts";
import { bindFilesBufferSynchronization } from "../documents/files-buffer-synchronization.ts";
import { createFilesTabCommands } from "../tabs/files-tab-commands.ts";
import { createFileDocumentCommands } from "../documents/file-document-commands.ts";
import { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import { createDefinitionNavigation } from "../editor/definition-navigation.ts";
import { createFileConfirmations } from "../commands/file-confirmations.ts";
import { createBufferLoading } from "../documents/files-buffer-loading.ts";
import { createFileMergeCommands } from "../documents/file-merge-commands.ts";
import { editorDraftPersistenceError } from "../documents/editor-document.ts";
import { createEffect, createMemo, createSignal, on, onCleanup, onMount } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { registerEscapeLadderLayer } from "../../find/escape-ladder.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { createFilesEditorSynchronization } from "../editor/files-editor-synchronization.ts";
import { orderWithPinnedGroup } from "../tabs/files-tab-strip.ts";
import type { createFilesWorkspace } from "../source/files-workspace.ts";
import type { FilesScope } from "./files-scope.ts";
import { focusedFilesBufferKey, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState, type FileBuffer } from "../documents/files-buffer-state.ts";

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

type StageDelegates = {
  editorPresentationPending: () => boolean;
  versionForBuffer: FilesScope["versionForBuffer"];
  dropFileVersionState: FilesScope["dropFileVersionState"];
  loadVersionHistory: FilesScope["loadVersionHistory"];
  refreshScopeMarks: FilesScope["refreshScopeMarks"];
  lifecycleHooks: FilesScope["lifecycleHooks"];
  copyAbsolutePathRef: FilesScope["copyAbsolutePathRef"];
  copyRelativePathRef: FilesScope["copyRelativePathRef"];
};

export function useProjectFilesController(options: {
  props: ProjectFilesViewProps;
  client: () => LycaonClient | null;
  chatSessionId: () => string | undefined;
  sourceSessionId: () => string | undefined;
  workspace: ReturnType<typeof createFilesWorkspace>;
}) {
  const { props, client, sourceSessionId, workspace } = options;
  const state = () => projectFilesState(props.projectId);
  const activeBuffer = (): FileBuffer | null => {
    const key = state().activeKey;
    return key ? (state().byKey[key] ?? null) : null;
  };
  // The strip follows the aim; the editor presents `activeKey`.
  const aimedKey = () => focusedFilesBufferKey(props.projectId);

  const live = useResidentLive();
  const presentationToken = `files:${props.projectId}:${Math.random().toString(36).slice(2, 10)}:editor`;
  const { filesWorkspaceId, filesRoots, workspaceSettled, refreshProjectWorkspace } = workspace;

  const [updatedNote, setUpdatedNote] = createSignal(false);
  let updatedNoteTimer: ReturnType<typeof setTimeout> | undefined;
  const flashUpdatedNote = () => {
    setUpdatedNote(true);
    if (updatedNoteTimer) clearTimeout(updatedNoteTimer);
    updatedNoteTimer = setTimeout(() => setUpdatedNote(false), 2000);
  };

  const retryEditorOpening = createFilesEditorSynchronization({
    projectId: props.projectId, client, sessionId: sourceSessionId,
    workspaceId: filesWorkspaceId, workspaceSettled,
    refreshWorkspace: refreshProjectWorkspace,
    reachable: () => isBackendReachable(props.appStore.state.sidecarStatus),
    onUpdated: flashUpdatedNote,
  });
  const [saveError, setSaveError] = createSignal<string | null>(null);
  const effectiveSaveError = () =>
    saveError() ?? activeBuffer()?.closeError ?? editorDraftPersistenceError(props.projectId);

  let delegates: StageDelegates | undefined;
  const bindDelegates = (next: StageDelegates) => {
    delegates = next;
  };

  let tabsScrollerEl: HTMLDivElement | undefined;
  let tabsEl: HTMLDivElement | undefined;
  const [hiddenTabCount, setHiddenTabCount] = createSignal(0);
  const [loadingMounted, setLoadingMounted] = createSignal(true);
  const [loadingLayer, setLoadingLayer] = createSignal<HTMLDivElement>();
  const [showLeadingFade, setShowLeadingFade] = createSignal(false);
  const [showTrailingFade, setShowTrailingFade] = createSignal(false);

  const scope: FilesScope = {
    projectId: () => props.projectId,
    client,
    sourceSessionId,
    filesWorkspaceId,
    live,
    state,
    activeBuffer,
    aimedKey,
    editorPresentationPending: () => delegates?.editorPresentationPending() ?? false,
    filesRoots,
    rootLabelFor: (rootId) => filesRoots().find((r) => r.id === rootId)?.label ?? rootId,
    stripOrder: () => stripOrder(),
    tabsElement: () => tabsEl,
    scrollerElement: () => tabsScrollerEl,
    setSaveError,
    versionForBuffer: (buf) => delegates!.versionForBuffer(buf),
    dropFileVersionState: (key) => delegates?.dropFileVersionState(key),
    loadVersionHistory: (buf) => delegates!.loadVersionHistory(buf),
    refreshScopeMarks: () => delegates!.refreshScopeMarks(),
    lifecycleHooks: () => delegates!.lifecycleHooks(),
    reloadBuffer: (key) => reloadBuffer(key),
    closeBuffer: (key, opts) => tabCommands.closeBuffer(key, opts),
    copyAbsolutePathRef: (rootId, path) => delegates!.copyAbsolutePathRef(rootId, path),
    copyRelativePathRef: (rootId, path) => delegates!.copyRelativePathRef(rootId, path),
  };

  const documents = createFileDocumentCommands(scope);
  const {
    setSaving, setSavedFlash, resetEditorStateChange, saveable, performSave, discardBuffer,
    reloadBuffer, clearSavedFlashTimer,
  } = documents;
  const confirmations = createFileConfirmations({
    ...scope,
    closeBuffers: (keys, opts) => tabCommands.closeBuffers(keys, opts),
    discardBuffer: (key) => discardBuffer(key),
    performSave: (key) => performSave(key),
    saveable: (buf) => saveable(buf),
  });
  const { setBulkClose, setConfirm } = confirmations;
  const { loadBuffer, reopenBufferAs } = createBufferLoading({ ...scope, refreshProjectWorkspace });
  const tabCommands = createFilesTabCommands({
    ...scope,
    loadBuffer: (buf) => loadBuffer(buf),
    setBulkClose,
    setConfirm,
  });
  const [restoringVersion, setRestoringVersion] = createSignal(false);
  const [cursor, setCursor] = createSignal({ line: 1, col: 1 });
  const definitions = createDefinitionNavigation({ ...scope, cursor: () => cursor() });
  const { runGoToDefinition, clearNoticeTimer, observeKeyboardAndBuffer: observeDefinitionNavigation } = definitions;
  const [editorPaneEl, setEditorPaneEl] = createSignal<HTMLDivElement>();
  const [stageEl, setStageEl] = createSignal<HTMLDivElement>();
  const editorActions = createFilesEditorActions({
    ...scope,
    sessionId: () => props.appStore.state.currentSession?.id?.trim() || undefined,
    editorPaneEl: () => editorPaneEl(),
    ensureCleanDiskForEditorAction: confirmations.ensureCleanDiskForEditorAction,
    runGoToDefinition: (pos) => runGoToDefinition(pos),
  });
  const merge = createFileMergeCommands(scope);
  const [innerLayerOpen, setInnerLayerOpen] = createSignal(false);

  onCleanup(() => {
    clearSavedFlashTimer();
    if (updatedNoteTimer) clearTimeout(updatedNoteTimer);
    clearNoticeTimer();
  });

  const stripOrder = createMemo(() => {
    const s = state();
    return orderWithPinnedGroup(s.order, s.byKey);
  });

  const observeControllerEffects = (stageOptions: {
    stageReady: () => boolean;
    displayedPane: () => string | null;
    publishingPane: () => string | null;
    scopeContentVersion: () => number;
    pendingWalkPreparation: () => string[];
    walking: () => boolean;
    walkReady: () => boolean;
    hasTreeOverlayOpen: () => boolean;
  }) => {
    createFilesPresentationTracking({
      ...scope,
      presentationToken,
      stageEl: () => stageEl(),
      stageReady: stageOptions.stageReady,
      scopeContentVersion: stageOptions.scopeContentVersion,
    });

    onMount(() => {
      const innerLayerPresent = () =>
        confirmations.confirm() != null ||
        confirmations.deleteGuard() != null ||
        stageOptions.hasTreeOverlayOpen() ||
        tabCommands.filesOpenListOpen() ||
        innerLayerOpen();
      // Inner layers dismiss themselves; the latch keeps their Escape from falling through to the shell.
      onCleanup(registerEscapeLadderLayer("stageLatch", {
        isOpen: innerLayerPresent,
        dismiss: () => {},
      }));
    });

    bindFilesBufferSynchronization({ ...scope, flashUpdatedNote, setBulkClose, setConfirm });

    observeFilesWorkspaceDemand({
      ...scope, workspaceSettled, loadBuffer,
      displayedPane: stageOptions.displayedPane,
      publishingPane: stageOptions.publishingPane,
      pendingWalkPreparation: stageOptions.pendingWalkPreparation,
      walking: stageOptions.walking,
      walkReady: stageOptions.walkReady,
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

    bindFilesEditorSurface({
      projectId: props.projectId, activeBuffer, filesRoots,
      editorPaneEl, document: () => props.document, performSave,
    });

    createEffect(
      on(stageOptions.stageReady, (ready) => {
        if (!ready) {
          setLoadingMounted(true);
          return;
        }
        const layer = loadingLayer();
        const waitMs =
          layer && typeof getComputedStyle === "function"
            ? longestTransitionMs(getComputedStyle(layer))
            : 0;
        const timer = setTimeout(() => setLoadingMounted(false), waitMs);
        onCleanup(() => clearTimeout(timer));
      }),
    );
  };

  return {
    state, activeBuffer, aimedKey,
    presentationToken, retryEditorOpening, saveError, setSaveError, effectiveSaveError,
    scope, bindDelegates, confirmations, editorActions, tabCommands,
    hiddenTabCount, setHiddenTabCount, loadingMounted, setLoadingLayer,
    showLeadingFade, setShowLeadingFade, showTrailingFade, setShowTrailingFade,
    documents, restoringVersion, setRestoringVersion, updatedNote, definitions, merge,
    cursor, setCursor, innerLayerOpen, setInnerLayerOpen, reopenBufferAs,
    editorPaneEl, setEditorPaneEl, stageEl, setStageEl,
    setTabsScrollerEl: (el: HTMLDivElement | undefined) => { tabsScrollerEl = el; },
    setTabsEl: (el: HTMLDivElement | undefined) => { tabsEl = el; },
    observeControllerEffects,
  };
}
