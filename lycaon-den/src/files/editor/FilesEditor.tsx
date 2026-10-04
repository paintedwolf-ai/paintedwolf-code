import { observeEditorEditingStatus, setEditorPromptContainer } from "../../components/source/editor/editor-editing-style.ts";
import { registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { navigateEditorChange } from "./editor-change-navigation.ts";
import { createEditorFormatActions, indentChipTip, eolChipTip, encodingChipTip, editorConfigStatusLabel, editorConfigStatusTip } from "./editor-format-actions.ts";
import { observeEditorDocumentPresentation, observeEditorDocumentMarks } from "./editor-document-presentation.ts";
import { observeEditorComparison, observeEditorAnnotations } from "./editor-review-marks.ts";
import { createEditorLineFacts } from "./editor-line-facts.ts";
import { createFilesEditorViewport } from "./files-editor-viewport.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { createEditorToolbarMenus, FilesEditorToolbar, FilesEditorStatus } from "./FilesEditorChrome.tsx";
import { openFind } from "../../find/find-controller.ts";
import { contextAction } from "../../components/context-actions.ts";
import type { SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";

import { DocumentWindows } from "../components/DocumentWindows.tsx";
import { DocumentChangeHistory } from "../history/DocumentChangeHistory.tsx";

import { editorReplica } from "../documents/editor-document.ts";
import { closeCompletion } from "@codemirror/autocomplete";
import { unfoldAll } from "@codemirror/language";

import { EditorView } from "@codemirror/view";
import { LAYOUT_BAND_SCALES, bindLayoutBand } from "../../layout/layout-bands.ts";
import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, untrack } from "solid-js";
import type { DenEditorVersionComparison } from "../../../shared/app-state-types.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { CodeScanEvent, ProjectRoot, SecurityFinding, SourceDefinitionCandidate, SourceFileVersion } from "../../api/types.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import type { EditorWindow } from "../../platform/windows/editor-windows.ts";
import { setSelectedVersionComparison, shownVersionComparison, versionComparesBefore } from "../history/files-version-selection.ts";

import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";

import { createPresentationWaiting } from "../../ui/presentation.ts";
import { useResidentInteractive, useResidentPresence } from "../../ui/resident-presence-context.tsx";
import { createResidentActivity, createResidentFocus, useResidentLive } from "../../ui/resident-activity.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";

import { BrowseSegmented } from "../../components/browse/BrowseSegmented.tsx";

import { ContextMenu, type ContextMenuAnchor, type ContextMenuItem } from "../../components/ContextMenu.tsx";

import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { languageLabelForPath } from "../../components/source/editor/codemirror-lang.ts";
import { applyEditorDisplayPrefs, openEditorFind, openEditorGotoLine, sourceUsesSimplifiedDisplay } from "../../components/source/editor/codemirror-theme.ts";

import { setFindingDiagnostics } from "../../components/source/annotations/findings-diagnostics.ts";

import { indentChipLabel } from "../../components/source/editor/indent-detect.ts";
import { enclosingSymbolAt } from "../../components/source/inline-edit/inline-edit-scope.ts";
import { type AttributionMark, type FindingLineMark } from "../../components/source/annotations/knowing-gutter-model.ts";
import { LineFactsCard } from "../../components/source/annotations/LineFactsCard.tsx";
import { setLineGutterAttribution, setLineGutterFindings } from "../../components/source/annotations/line-gutter.ts";
import { setOverviewSymbolMarks } from "../../components/source/annotations/overview-ruler.ts";
import { RemovedLinesPopover } from "../../components/source/diff/RemovedLinesPopover.tsx";
import { editorScopeDiffOriginal } from "../../components/source/diff/scope-diff.ts";
import { getDeletedLines, subscribeDeletedLines } from "../review/review-pane.ts";
import { SECRET_SPAN_COPY } from "../../components/source/secrets/secret-span-copy.ts";
import { setSecretScreen } from "../../components/source/secrets/secret-span-decorations.ts";
import { secretScreenSummary, type SecretScreenGap } from "../../components/source/secrets/secret-span-model.ts";
import { CheckIcon, CopyIcon, FindIcon, GotoLineIcon } from "../../components/source/viewer-icons.tsx";

import { definitionPickerExplainer, DEFINITION_PICKER_TRUNCATION_FOOTER, definitionPickerHeader } from "./definition-jump.ts";
import { editorDocumentScreenStatus } from "../documents/editor-document.ts";
import { editorStateAction, resolveEditorIdentity } from "../documents/editor-identity.ts";

import type { FileHistoryCommit } from "../history/file-history-model.ts";
import { foldCondensedContext } from "../history/file-version-condense.ts";
import { versionRestoreOffered } from "../history/file-version-restore.ts";
import { fileVersionIsReadable, type FileVersionHistory, type FileVersionView } from "../history/file-version.ts";
import { FileMarkdownPreview } from "./FileMarkdownPreview.tsx";

import { currentDisplayPrefs, effectiveIndent } from "./files-editor-display.ts";
import { setFilesEditorPresented } from "./files-editor-host.ts";
import { filesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import { type FilesEditorToolbarMenuKind } from "./files-editor-toolbar-menus.ts";

import { FilesPathCrumbs } from "./FilesPathCrumbs.tsx";
import { filesBufferHasCurrentFile } from "../documents/files-buffer-change.ts";
import { FileVersionDocument } from "../history/FileVersionDocument.tsx";
import { createLastDeletion } from "../history/last-deletion.ts";
import { FileVersionPicker } from "../history/FileVersionPicker.tsx";

import type { DiffLineHunk } from "../review/line-diff.ts";
import { encodingChipLabel } from "../documents/project-files-buffer-kind.ts";
import { clearFilesBufferCondense, openFilesBuffer } from "../documents/project-files-buffers.ts";
import type { EolKind } from "../../components/source/editor/eol.ts";
import { projectFilesState, type FileBuffer } from "../documents/files-buffer-state.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { scopeChangeKindFor, scopeDiffRevision, scopeEffectFor, subscribeResolvedScope } from "../tree/scope-resolution.ts";
import { walkPageTitle } from "../walk/WalkStepPage.tsx";
import { createEditorSymbolNavigation, FilesSymbolMenu } from "./editor-symbol-navigation.tsx";
import type { FilesEditorScope } from "../components/files-scope.ts";

type FilesEditorProps = {
  projectId: string;
  onDockMount?: (element: HTMLDivElement | null) => void;
  sessionId?: string;
  /** Host scan identity and status invalidate the findings presentation. */
  scanUpdate?: Pick<CodeScanEvent, "scan_id" | "status">;
  buffer: FileBuffer;
  roots: ProjectRoot[];
  client?: LycaonClient | null;
  initialMarkdownView?: "code" | "preview";
  previewSource?: (source: string) => string;
  saving: boolean;
  saveError: string | null;
  conflict: boolean;
  /** History retains the agent edit missing from disk. */
  heldAgentEdit: boolean;
  updatedNote: boolean;
  definitionNotice: string | null;
  /** Reason outside edits may be missing. */
  observationNotice: string | null;
  savedFlash: boolean;
  editable: boolean;
  version: FileVersionView | null;
  onReaderHandle?: (handle: SourceReaderHandle | undefined) => void;
  versionHistory: FileVersionHistory;
  restoringVersion: boolean;
  versionRestoreDisabledReason: string | null;
  onRestoreVersion: (version: FileVersionView) => void;
  onSelectVersion: (version: SourceFileVersion) => void;
  onSelectCommit: (entry: FileHistoryCommit) => void;
  onRestoreListedVersion: (version: SourceFileVersion) => void;
  onRestoreListedCommit: (entry: FileHistoryCommit) => void;
  listedRestoreReason: (
    row:
      | { kind: "version"; version: SourceFileVersion; }
      | { kind: "commit"; entry: FileHistoryCommit; },
  ) => string | null;
  onLoadVersionHistory: () => void;
  onLoadEarlierVersions: () => void;
  onSelectCurrent: () => void;
  /** Peer windows include pending document joins. */
  peerViews?: readonly EditorWindow[];
  stateActionBusy?: boolean;
  onOpenCurrent?: () => void;
  onMakeEditable?: () => void;
  onRetryEditing?: () => void;
  /** Line endings change only on an editable working document. */
  onChooseLineEndings?: (eol: EolKind) => void;
  cursor: { line: number; col: number; };
  onCursor: (line: number, col: number) => void;
  onSave: () => void;
  onDiscard: () => void;
  onRejectHunk?: (hunk: DiffLineHunk) => void;
  /** Restores the file the Git baseline still holds. */
  onRevertFile?: () => void;
  /** Restores the retained version a deletion removed. */
  onRestoreDeleted?: (versionId: string) => void;
  onReload: () => void;
  onMerge: () => void;
  /** Open editor menus take priority when handling Escape. */
  onInnerLayerChange: (open: boolean) => void;
  onRevealSegment: (rootId: string, path: string, isDir: boolean) => void;
  onSymbolJump: (line: number) => void;
  onRenameSymbol?: (name: string) => void;
  onCursorTeleport: (line: number) => void;
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
  definitionPicker: {
    symbol: string;
    candidates: readonly SourceDefinitionCandidate[];
    truncated: boolean;
    selected: number;
  } | null;
  onDefinitionPickerChange: (
    next: {
      symbol: string;
      candidates: readonly SourceDefinitionCandidate[];
      truncated: boolean;
      selected: number;
    } | null,
  ) => void;
  onGoToDefinitionAt: (pos: number) => void;
  onJumpDefinitionCandidate: (c: SourceDefinitionCandidate) => void;
};

export function FilesEditor(props: FilesEditorProps) {
  const live = useResidentLive();
  const interactive = useResidentInteractive();
  const presence = useResidentPresence();
  let view: EditorView | undefined;
  const [editingPrompt, setEditingPrompt] = createSignal<HTMLElement>();
  const [editingStatus, setEditingStatus] = createSignal("");
  const [editorView, setEditorView] = createSignal<EditorView | undefined>();
  createEffect(() => {
    const current = editorView();
    setEditingStatus("");
    if (current) onCleanup(observeEditorEditingStatus(current, setEditingStatus));
  });
  createEffect(() => {
    const current = editorView(), container = editingPrompt();
    if (current && container) setEditorPromptContainer(current, container);
  });
  const [chromeReady, setChromeReady] = createSignal(false);
  const historicalActive = () => props.version != null;
  const editor: FilesEditorScope = {
    projectId: () => props.projectId, sessionId: () => props.sessionId, client: () => props.client,
    buffer: () => props.buffer, live, chromeReady, historicalActive, currentView: () => view, editorView,
  };
  const {
    lineFactsCard,
    cancelHidePopover,
    hideLineFacts,
    scheduleHidePopover,
    returnLineFactsFocus,
    removedPreview,
    hideRemovedPreview,
    keepRemovedPreview,
    scheduleHideRemovedPreview,
    openRemovedInPlace,
    rejectRemoved,
    jumpToAttribution,
    activateLineFact,
    findingDiagnosticHandlers,
    installGutterInteractions,
    observePresence,
    clearRemovedTimers,
    clearLineFactsTimers,
  } = createEditorLineFacts({
    ...editor,
    cursorLine: () => props.cursor.line,
    findingsScanId: () => findingsScanId(),
    revealInTranscript: () => props.onRevealInTranscript,
    rejectHunk: () => props.onRejectHunk,
  });
  const [editorScopeRevision, setEditorScopeRevision] = createSignal(
    scopeDiffRevision(props.projectId, props.buffer.rootId, props.buffer.path),
  );
  createResidentActivity(() => {
    setEditorScopeRevision(scopeDiffRevision(props.projectId, props.buffer.rootId, props.buffer.path));
    return subscribeResolvedScope((id) => {
      if (id === props.projectId.trim()) setEditorScopeRevision(scopeDiffRevision(props.projectId, props.buffer.rootId, props.buffer.path));
    });
  });
  const [deletedLines, setDeletedLinesMode] = createSignal(getDeletedLines(props.projectId));
  createResidentActivity(() => {
    setDeletedLinesMode(getDeletedLines(props.projectId));
    return subscribeDeletedLines((id) => {
      if (id === props.projectId.trim()) setDeletedLinesMode(getDeletedLines(props.projectId));
    });
  });
  const commitDeleted = () => {
    void editorScopeRevision();
    const change = scopeEffectFor(props.projectId, props.buffer.rootId, props.buffer.path);
    return !filesBufferHasCurrentFile(props.buffer) && !props.buffer.jobId && change?.commit && (change.tip.state === "absent" || change.commit.op === "delete") ? change.commit : null;
  };
  let hostEl: HTMLDivElement | undefined;
  let indentChipEl: HTMLButtonElement | undefined;
  let eolChipEl: HTMLButtonElement | undefined;
  const [versionHandle, setVersionHandle] = createSignal<SourceReaderHandle>();
  const [readyVersion, setReadyVersion] = createSignal<FileVersionView>();
  const [versionDisplayed, setVersionDisplayed] = createSignal(false);
  createEffect(() => { if (!props.version) setVersionDisplayed(false); });
  const [deletionHandle, setDeletionHandle] = createSignal<SourceReaderHandle>();
  const [deletionReady, setDeletionReady] = createSignal(false);
  const activeReaderHandle = () => historicalActive() ? versionHandle() : currentAbsent() ? deletionHandle() : undefined;
  createEffect(() => { props.onReaderHandle?.(activeReaderHandle()); onCleanup(() => props.onReaderHandle?.(undefined)); });
  const [condensedContext, setCondensedContext] = createSignal(false);
  const dropCondenseRequest = () => {
    if (untrack(() => props.buffer.condenseContext)) {
      clearFilesBufferCondense(props.projectId, untrack(() => props.buffer.key));
    }
  };
  createEffect(() => {
    if (currentAbsent() || props.buffer.loadError) {
      dropCondenseRequest();
    }
  });
  /** Review opens fold unchanged runs in the displayed comparison. */
  const condenseIfRequested = (v: EditorView) => {
    if (!untrack(() => props.buffer.condenseContext)) return;
    const original = editorScopeDiffOriginal(v.state);
    if (original == null) return;
    setCondensedContext(foldCondensedContext(v));
    clearFilesBufferCondense(props.projectId, untrack(() => props.buffer.key));
  };
  const comparisonPreview = () =>
    props.buffer.kind === "diff" && props.buffer.diffPreview != null;
  const currentTipAbsent = () => Boolean(props.buffer.deleted) || Boolean(commitDeleted()) || (
    !filesBufferHasCurrentFile(props.buffer) && (
      scopeChangeKindFor(props.projectId, props.buffer.rootId, props.buffer.path) === "deleted" ||
      scopeEffectFor(props.projectId, props.buffer.rootId, props.buffer.path)?.tip.state === "absent" ||
      (props.versionHistory.status === "ready" && props.versionHistory.current?.state === "absent")
    ));
  const currentAbsent = () =>
    !historicalActive() && currentTipAbsent();
  const lastDeletionState = createLastDeletion({
    absent: currentAbsent, buffer: () => props.buffer, client: () => props.client,
    projectId: () => props.projectId, sessionId: () => props.sessionId,
    committedHead: () => commitDeleted()?.head ?? null,
  });
  const deletionIdentity = (deletion: ReturnType<typeof lastDeletionState>) => deletion.kind !== "deleted" ? deletion.kind :
    JSON.stringify([deletion.version.source, deletion.version.ts, deletion.version.beforeAvailability, deletion.version.secretScreen, deletion.restore]);
  const lastDeletion = createMemo(lastDeletionState, undefined, { equals: (a, b) => deletionIdentity(a) === deletionIdentity(b) });
  const deletionRestore = () => {
    const deletion = lastDeletion();
    const restore = deletion.kind === "deleted" ? deletion.restore : null;
    if (restore?.kind === "version") return props.onRestoreDeleted ? restore : null;
    if (restore?.kind === "commit") return props.onRevertFile ? restore : null;
    return null;
  };
  const deletionVersion = () => {
    const deletion = lastDeletion();
    return deletion.kind === "deleted" ? deletion.version : null;
  };
  createEffect(() => {
    if (deletionVersion()) return;
    setDeletionHandle(undefined);
    setDeletionReady(false);
  });
  const reportCursor = (line: number, col: number) => {
    if (interactive()) props.onCursor(line, col);
  };
  const activeEditorView = () =>
    historicalActive()
      ? undefined
      : currentAbsent()
        ? undefined
        : editorView();
  createEffect(() => {
    if (!interactive()) return;
    const current = activeEditorView();
    if (!current) return;
    const head = current.state.selection.main.head;
    const line = current.state.doc.lineAt(head);
    reportCursor(line.number, head - line.from + 1);
  });
  createResidentFocus(() => {
    const target = activeEditorView()?.contentDOM;
    const active = document.activeElement;
    // A historical version has no editor target, so the host locates the view.
    const filesView = (target ?? hostEl)?.closest<HTMLElement>(".project-files-view");
    if (active instanceof HTMLElement && filesView?.contains(active)) {
      return undefined;
    }
    if (historicalActive()) versionHandle()?.focus();
    return target;
  });
  const bindView = (next: EditorView | undefined) => {
    view = next;
    setEditorView(next);
  };
  const filesFocusClaim = {};
  createEffect(() => {
    if (!interactive()) return;
    const target = editorView()?.contentDOM ?? hostEl;
    if (!target) return;
    const transfer = target !== hostEl && document.activeElement === hostEl;
    registerFocusRegion("files", target, filesFocusClaim);
    if (transfer) target.focus({ preventScroll: true });
    onCleanup(() => releaseFocusRegion("files", filesFocusClaim));
  });
  /** Costly files open with wrap, guides, and highlighting off. */
  const [simplifiedDisplay, setSimplifiedDisplay] = createSignal(false);
  /** Permanent chrome shows incomplete coverage only. */
  const secretScreenGap = createMemo<SecretScreenGap | null>(() => {
    const buffer = props.buffer;
    if (buffer.loading || buffer.kind !== "text") return null;
    if (historicalActive()) {
      if (!props.version || !fileVersionIsReadable(props.version)) return null;
      return secretScreenSummary(props.version.secretScreen).gap;
    }
    if (buffer.deleted?.previous.availability === "available") return secretScreenSummary(buffer.deleted.previous.secret_screen).gap;
    if (!buffer.documentId) return null;
    if (editorDocumentScreenStatus(buffer.documentId) === "pending") return null;
    return secretScreenSummary(filesEditorSecretScreen(buffer.key)).gap;
  });
  const simplifiedStatusVisible = () => {
    if (!historicalActive() && !currentAbsent()) return simplifiedDisplay();
    const historicalView = activeEditorView();
    return historicalView
      ? sourceUsesSimplifiedDisplay(historicalView.state)
      : false;
  };
  const [toolbarMenu, setToolbarMenu] = createSignal<{
    kind: FilesEditorToolbarMenuKind;
    anchor: ContextMenuAnchor;
  } | null>(null);
  const [indentMenu, setIndentMenu] = createSignal<ContextMenuAnchor | null>(
    null,
  );
  const [eolMenu, setEolMenu] = createSignal<ContextMenuAnchor | null>(null);
  const [versionComparisonMenu, setVersionComparisonMenu] =
    createSignal<ContextMenuAnchor | null>(null);
  const symbolNavigation = createEditorSymbolNavigation({
      ...editor,
      onSymbolJump: (line) => props.onSymbolJump(line),
    });
  const { symbolMenuOpen, symbols, closeSymbolMenu, toggleSymbolMenu, observeSource: observeSymbolSource } = symbolNavigation;
  createEffect(() => {
    if (!interactive()) return;
    onCleanup(registerCommandHandler("editor.goToSymbol", () => {
      const trigger = hostEl?.closest(".den-files-editor")?.querySelector<HTMLElement>(".den-files-editor__crumb-file");
      if (trigger) toggleSymbolMenu(trigger);
    }));
    for (const [command, direction] of [["editor.nextChange", 1], ["editor.previousChange", -1]] as const) {
      onCleanup(registerCommandHandler(command, () => {
        const view = activeEditorView();
        if (view) navigateEditorChange(view, direction);
      }));
    }
  });
  const [mdView, setMdView] = createSignal<"code" | "preview">(
    props.initialMarkdownView ?? "code",
  );
  const [previewText, setPreviewText] = createSignal("");
  const [attrMarks, setAttrMarks] = createSignal<AttributionMark[]>([]);
  const [findingMarks, setFindingMarks] = createSignal<FindingLineMark[]>([]);
  // Finding records preserve underline columns omitted by gutter marks.
  const [findingRecords, setFindingRecords] = createSignal<SecurityFinding[]>(
    [],
  );
  const [findingsScanId, setFindingsScanId] = createSignal("");
  onCleanup(clearLineFactsTimers);

  observePresence();
  onCleanup(hideLineFacts);
  onCleanup(clearRemovedTimers);

  createEffect(() =>
    props.onInnerLayerChange(
      lineFactsCard() != null ||
      toolbarMenu() != null ||
      indentMenu() != null ||
      versionComparisonMenu() != null ||
      symbolMenuOpen() ||
      props.definitionPicker != null,
    ),
  );
  onCleanup(() => props.onInnerLayerChange(false));

  createEffect(() => {
    if (!props.version) setVersionComparisonMenu(null);
  });
  const versionComparison = (): DenEditorVersionComparison =>
    props.version ? shownVersionComparison(props.projectId, props.buffer.key, props.version) : "current";

  createEffect(() => {
    if (props.definitionPicker != null) {
      closeSymbolMenu("keep");
      hideLineFacts();
      const v = view;
      if (v) closeCompletion(v);
    }
  });

  observeSymbolSource();

  /** The breadcrumb ends with the innermost symbol containing the caret. */
  const enclosingCrumbSymbol = createMemo(() => {
    const rows = symbols();
    if (rows.length === 0) return null;
    const caret = props.cursor.line;
    const docLines = view?.state.doc.lines ?? caret;
    const enclosing = enclosingSymbolAt(
      rows.map((s) => ({ name: s.name, line: s.line })),
      caret,
      docLines,
    );
    if (!enclosing) return null;
    const kind =
      rows.find(
        (s) => s.name === enclosing.name && s.line === enclosing.startLine,
      )?.kind ?? "";
    return { ...enclosing, kind };
  });

  createEffect(
    on(
      () => props.buffer.key,
      () => closeSymbolMenu("keep"),
      { defer: true },
    ),
  );

  const languageLabel = createMemo(() =>
    languageLabelForPath(props.version?.path ?? props.buffer.path),
  );
  const isMarkdown = () => languageLabel() === "Markdown";
  const previewActive = () => isMarkdown() && mdView() === "preview";
  createEffect(() => {
    editorView();
    setFilesEditorPresented(props.projectId, props.buffer.key,
      presence() === "active" && !previewActive() && !historicalActive() && !currentAbsent());
  });
  const [previewReady, setPreviewReady] = createSignal(false);
  const [previewDisplayed, setPreviewDisplayed] = createSignal(false);
  const previewPending = () => previewActive() && !previewReady();
  const historyPending = () => historicalActive() && readyVersion() !== props.version;
  const showPreviewWait = createPresentationWaiting(() => previewPending() || (historyPending() && !versionDisplayed()));
  usePresentationParticipant("file-document", () => {
    if (props.buffer.loadError) return true;
    if (previewActive()) return previewReady();
    // Historical rows register preparation through their own deck.
    if (historicalActive()) return true;
    if (currentAbsent() && !props.buffer.loading) return lastDeletion().kind === "deleted" ? deletionReady() : lastDeletion().kind === "unrecorded";
    return !props.buffer.loading && (currentAbsent() || (chromeReady() && !props.buffer.condenseContext));
  });
  createEffect(() => {
    if (!previewActive()) {
      setPreviewReady(false);
      setPreviewDisplayed(false);
    }
  });
  const editorIdentity = createMemo(() => resolveEditorIdentity({
    historical: versionRestoreOffered(props.buffer, props.version),
    workerDraft: Boolean(props.buffer.jobId) && !comparisonPreview(),
    deleted: currentAbsent(),
    absentDraft: props.buffer.absent === true,
    writable: props.buffer.writable,
    preview: comparisonPreview() || previewActive(),
    editable: props.editable,
    loading: props.buffer.loading,
    opening: props.buffer.editorOpening,
    paused: props.buffer.editorResolving || !!props.buffer.editorEditingBlock,
    overLimit: props.buffer.overLimit,
    unsupported: props.buffer.kind !== "text" || props.buffer.encoding === null,
    documentPending: !editorReplica(props.buffer.documentId) && !props.buffer.loadError,
  }));
  const stateAction = createMemo(() => {
    const action = editorStateAction(editorIdentity());
    if (action?.id === "retry-editing" && (!props.onRetryEditing || props.buffer.loadError)) return null;
    if (action?.id === "restore-deleted" && !deletionRestore()) return null;
    return action;
  });
  const openingError = () => editorIdentity().id === "editing-error" && !props.buffer.loadError
    ? props.buffer.editorOpening?.status === "error" ? props.buffer.editorOpening.message : "The file could not be opened for editing."
    : null;
  const activeFailure = (failure: () => string | null | undefined) =>
    () => (presence() === "active" ? failure() : null);
  observeSurfaceFailure(
    { code: "files_save_failed", title: "Could not save the file", suggestedAction: "Keep editing; the text stays in the editor. Save again once the problem clears." },
    activeFailure(() => (!historicalActive() && !props.conflict ? props.saveError : null)),
    () => props.projectId,
  );
  observeSurfaceFailure(
    { code: "files_document_unavailable", title: "Could not load the file", suggestedAction: "Reload the file, or reopen it from the file tree." },
    activeFailure(() => (!historicalActive() && !currentAbsent() ? props.buffer.loadError : null)),
    () => props.projectId,
  );
  observeSurfaceFailure(
    { code: "files_editing_unavailable", title: "Could not open the file for editing", suggestedAction: "Use Retry editing in the editor header; the file text is unchanged." },
    activeFailure(openingError),
    () => props.projectId,
  );
  const stateActionDisabledReason = () => {
    const action = stateAction();
    if (!action) return null;
    if (props.stateActionBusy) return "Action in progress";
    if (action.id === "restore-version") return props.versionRestoreDisabledReason;
    if (action.id === "make-editable" && (!props.onMakeEditable || !props.buffer.baseSha256)) {
      return "The file must be reloaded before its permission can change";
    }
    return null;
  };
  const runStateAction = () => {
    const action = stateAction();
    if (!action || stateActionDisabledReason()) return;
    switch (action.id) {
      case "restore-version":
        if (props.version) props.onRestoreVersion(props.version);
        break;
      case "open-current":
        props.onOpenCurrent?.();
        break;
      case "make-editable":
        props.onMakeEditable?.();
        break;
      case "retry-editing":
        props.onRetryEditing?.();
        break;
      case "restore-deleted": {
        const restore = deletionRestore();
        if (restore?.kind === "version") props.onRestoreDeleted?.(restore.versionId);
        else if (restore?.kind === "commit") props.onRevertFile?.();
        break;
      }
    }
  };

  createEffect(() => {
    if (previewActive() && !props.buffer.loading && !props.buffer.loadError) {
      setPreviewText(filesBufferText(props.buffer));
    } else if (!previewActive()) setPreviewText("");
  });
  createEffect(
    on(
      previewActive,
      (active) => {
        if (active) return;
        const v = view;
        if (!v) return;
        v.requestMeasure();
        if (typeof requestAnimationFrame === "function") {
          requestAnimationFrame(() => v.requestMeasure());
        }
      },
      { defer: true },
    ),
  );
  const filesState = () => projectFilesState(props.projectId);
  const dirtyOpenCount = () =>
    filesState().order.filter((k) => filesState().byKey[k]?.dirty).length;

  const { copiedPath, onCopy, findMenuBind, gotoMenuBind, copyMenuBind, toolbarMenuItems } = createEditorToolbarMenus({
    setToolbarMenu, roots: () => props.roots, buffer: () => props.buffer, activeReaderHandle, activeEditorView,
  });

  const installChrome = (v: EditorView, path: string, prefs: ReturnType<typeof currentDisplayPrefs>) => {
      installGutterInteractions(v);
      setLineGutterAttribution(
        v,
        untrack(() => attrMarks()),
      );
      setLineGutterFindings(
        v,
        untrack(() => findingMarks()),
      );
      setOverviewSymbolMarks(
        v,
        untrack(() => symbols()),
      );
      setSecretScreen(
        v,
        untrack(() => filesEditorSecretScreen(props.buffer.key)) ?? null,
      );
      setFindingDiagnostics(
        v,
        path,
        untrack(() => findingRecords()),
        findingDiagnosticHandlers,
      );
      applyEditorDisplayPrefs(v, prefs);
    };

  createFilesEditorViewport({
    ...editor, editable: () => props.editable, hostElement: () => hostEl, bindView, currentAbsent, reportCursor,
    onCursorTeleport: (line) => props.onCursorTeleport(line),
    onGoToDefinitionAt: (position) => props.onGoToDefinitionAt(position),
    setSimplifiedDisplay, setChromeReady, installChrome,
  });

  observeEditorDocumentPresentation({ ...editor, activeReaderHandle, activeEditorView });

  observeEditorComparison({ ...editor, editorScopeRevision, dropCondenseRequest, condenseIfRequested });

  observeEditorDocumentMarks({ ...editor, rejectHunk: () => props.onRejectHunk, deletedLines });

  observeEditorAnnotations({
    ...editor,
    scanUpdate: () => props.scanUpdate,
    setAttrMarks, setFindingMarks, setFindingRecords, setFindingsScanId, findingDiagnosticHandlers,
  });

  const { openIndentMenu, openEolMenu, indentMenuItems } = createEditorFormatActions({
    ...editor, editable: () => props.editable, indentChipEl: () => indentChipEl, eolChipEl: () => eolChipEl,
    setIndentMenu, setEolMenu,
  });

  return (
    <div
      ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.filesEditor)}
      class="den-files-editor"
      data-testid="files-editor"
    >
      <FilesEditorToolbar actions={<>
          <Show when={!comparisonPreview()}>
            <FileVersionPicker
              selected={props.version}
              currentAbsent={currentTipAbsent()}
              editHistory={(dismiss) => <Show keyed when={props.client && props.buffer.documentId
                ? { client: props.client, documentId: props.buffer.documentId } : undefined}>
                {(document) => <DocumentChangeHistory client={document.client} projectId={props.projectId} documentId={document.documentId} onApplied={() => { dismiss(); queueMicrotask(() => activeEditorView()?.focus()); }} />}
              </Show>}
              history={props.versionHistory}
              onCurrent={props.onSelectCurrent}
              onSelect={props.onSelectVersion}
              onSelectCommit={props.onSelectCommit}
              onRestoreVersion={props.onRestoreListedVersion}
              onRestoreCommit={props.onRestoreListedCommit}
              restoreReason={props.listedRestoreReason}
              onOpen={props.onLoadVersionHistory}
              onLoadMore={props.onLoadEarlierVersions}
            />
          </Show>
          <Show when={stateAction()} keyed>
            {(action) => (
              <DenButton
                variant={
                  action.id === "restore-version" && props.version?.availability === "absent"
                    ? "danger"
                    : "primary"
                }
                compact
                class="den-files-editor__state-action"
                data-testid={`files-editor-${action.id}`}
                data-state-action={action.id}
                disabled={stateActionDisabledReason() != null}
                aria-label={props.stateActionBusy ? `${action.label} in progress` : action.label}
                data-tip={stateActionDisabledReason() ?? action.tip}
                data-tip-pos="below"
                onClick={runStateAction}
              >
                <ThemeIcon slot={action.icon} size={14} />
              </DenButton>
            )}
          </Show>
          <Show when={props.savedFlash && !props.buffer.dirty && !historicalActive()}>
            <span
              class="den-files-editor__saved"
              data-testid="files-editor-saved"
              role="status"
              aria-live="polite"
            >
              <CheckIcon size={13} /> Saved
            </span>
          </Show>
          <Show when={props.editable && props.buffer.dirty}>
            <DenButton
              variant="ghost"
              compact
              data-testid="files-editor-discard"
              disabled={props.saving}
              onClick={() => props.onDiscard()}
            >
              Discard
            </DenButton>
            <DenButton
              variant="primary"
              compact
              data-testid="files-editor-save"
              data-tip={`Save (${bindingForHandler("files.save")})`}
              data-tip-pos="below"
              disabled={props.saving || props.conflict}
              onClick={() => props.onSave()}
            >
              {props.saving ? "Saving…" : "Save"}
            </DenButton>
          </Show>
          <Show when={isMarkdown() && !historicalActive()}>
            <BrowseSegmented
              class="den-files-editor__seg"
              testId="files-editor-md-toggle"
              ariaLabel="Markdown view"
              value={mdView()}
              onChange={(id) => {
                if (id === "preview") {
                  setPreviewText(
                    view?.state.doc.toString() ?? filesBufferText(props.buffer),
                  );
                  setMdView("preview");
                } else if (id === "code") {
                  setMdView("code");
                }
              }}
              options={[
                { id: "code", label: "Code", testId: "files-editor-md-code" },
                {
                  id: "preview",
                  label: "Preview",
                  testId: "files-editor-md-preview",
                },
              ]}
            />
          </Show>
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            data-testid="files-editor-find"
            data-tip={`Find (${bindingForHandler("find.inView")})`}
            data-tip-pos="below"
            aria-label="Find in file"
            aria-haspopup="menu"
            aria-expanded={toolbarMenu()?.kind === "find"}
            disabled={
              props.buffer.loading || previewActive() || (!activeEditorView() && !activeReaderHandle())
            }
            onClick={() => {
              const active = activeEditorView();
              if (active) openEditorFind(active);
            else { activeReaderHandle()?.focus(); openFind(); }
            }}
            onContextMenu={findMenuBind.onContextMenu}
            onKeyDown={findMenuBind.onKeyDown}
          >
            <FindIcon />
          </button>
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            data-testid="files-editor-goto"
            data-tip={`Go to line (${bindingForHandler("editor.goToLine")})`}
            data-tip-pos="below"
            aria-label="Go to line"
            aria-haspopup="menu"
            aria-expanded={toolbarMenu()?.kind === "goto"}
            disabled={
              props.buffer.loading || previewActive() || (!activeEditorView() && !activeReaderHandle())
            }
            onClick={() => {
              const active = activeEditorView();
              if (active) openEditorGotoLine(active);
            else void activeReaderHandle()?.gotoLine();
            }}
            onContextMenu={gotoMenuBind.onContextMenu}
            onKeyDown={gotoMenuBind.onKeyDown}
          >
            <GotoLineIcon />
          </button>
          <button
            type="button"
            class="den-files-editor__tool den-inset-icon-btn"
            classList={{ "den-files-editor__tool--ok": copiedPath() }}
            data-testid="files-editor-copy"
            data-tip="Copy path"
            data-tip-pos="below"
            aria-label="Copy path"
            aria-haspopup="menu"
            aria-expanded={toolbarMenu()?.kind === "copy"}
            onClick={onCopy}
            onContextMenu={copyMenuBind.onContextMenu}
            onKeyDown={copyMenuBind.onKeyDown}
          >
            <Show when={copiedPath()} fallback={<CopyIcon />}>
              <CheckIcon />
            </Show>
          </button>

      </>}>
        <FilesPathCrumbs
          rootId={props.buffer.rootId}
          rootLabel={props.buffer.rootLabel}
          path={props.buffer.path}
          testId="files-editor-crumb"
          onRevealSegment={props.onRevealSegment}
          fileSegment={(seg) => (
            <button
              type="button"
              class="den-files-editor__crumb-seg den-files-editor__crumb-file"
              data-files-ctx="breadcrumb"
              data-root={props.buffer.rootId}
              data-path={seg.path}
              data-isdir="false"
              aria-expanded={symbolMenuOpen()}
              aria-haspopup="listbox"
              onClick={(e) => toggleSymbolMenu(e.currentTarget)}
            >
              {seg.name}
            </button>
          )}
        >
          <Show when={enclosingCrumbSymbol()}>
            {(sym) => (
              <>
                <span class="den-files-editor__crumb-sep" aria-hidden="true">
                  ›
                </span>
                <button
                  type="button"
                  class="den-files-editor__crumb-seg den-files-editor__crumb-symbol"
                  data-testid="files-crumb-symbol-seg"
                  data-tip={`${sym().kind ? `${formatSentenceCase(sym().kind)} ` : ""}${sym().name} — lines ${sym().startLine}–${sym().endLine}. Click for symbols; right-click to rename.`}
                  aria-expanded={symbolMenuOpen()}
                  aria-haspopup="listbox"
                  onClick={(e) => toggleSymbolMenu(e.currentTarget)}
                  onContextMenu={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    props.onRenameSymbol?.(sym().name);
                  }}
                >
                  <Show when={sym().kind}>
                    <span class="den-files-editor__crumb-symbol-kind">
                      {formatSentenceCase(sym().kind)}
                    </span>
                  </Show>
                  {sym().name}
                </button>
              </>
            )}
          </Show>
        </FilesPathCrumbs>

      </FilesEditorToolbar>
      <FilesSymbolMenu symbols={symbolNavigation} />
      <Show when={!historicalActive() && props.conflict}>
        <div
          class="den-files-editor__conflict"
          role="alert"
          data-testid="files-editor-conflict"
        >
          <span class="flex-1">
            This file changed on disk while you have unsaved edits.
            <Show when={props.heldAgentEdit}>
              {" "}
              <span data-testid="files-editor-conflict-held">
                The AI's edit is kept in version history.
              </span>
            </Show>
          </span>
          <DenButton
            variant="primary"
            compact
            data-testid="files-editor-conflict-merge"
            onClick={() => props.onMerge()}
          >
            Merge…
          </DenButton>
          <DenButton
            variant="ghost"
            compact
            data-testid="files-editor-conflict-reload"
            onClick={() => props.onReload()}
          >
            Reload file
          </DenButton>
        </div>
      </Show>
      <div class="den-files-editor__document-stage" aria-busy={historyPending() || previewPending() || editorIdentity().id === "opening" || editorIdentity().id === "reconnecting"}>
        <div
          class="den-files-editor__host-wrap den-retained-presentation"
          hidden={(previewActive() && previewDisplayed()) || (historicalActive() && versionDisplayed()) || currentAbsent()}
          data-retained={showPreviewWait() ? "true" : "false"}
          aria-hidden={previewActive() || historicalActive() ? true : undefined}
          inert={previewActive() || historicalActive() ? true : undefined}
        >
          <div
            class="den-files-editor__host"
            data-testid="files-editor-host"
            tabIndex={-1}
            ref={hostEl}
          />
          <Show when={condensedContext()}>
            <div class="den-document-actions">
              <button
                type="button"
                class="den-document-action"
                onClick={() => {
                  const v = editorView();
                  if (v) {
                    unfoldAll(v);
                    v.requestMeasure();
                  }
                  setCondensedContext(false);
                }}
              >
                Full file
              </button>
            </div>
          </Show>
          <Show when={props.definitionPicker} keyed>
            {(picker) => (
              <AnchoredSurface
                class="den-files-editor__symbol-menu den-files-editor__definition-picker"
                role="listbox"
                ariaLabel={`Definitions of ${picker.symbol}`}
                testId="files-definition-picker"
                tabIndex={0}
                anchor={() => {
                  const active = activeEditorView();
                  const head = active?.state.selection.main.head;
                  const coords = head === undefined ? null : active?.coordsAtPos(head);
                  return coords
                    ? {
                      top: coords.top,
                      right: coords.right,
                      bottom: coords.bottom,
                      left: coords.left,
                      width: coords.right - coords.left,
                      height: coords.bottom - coords.top,
                    }
                    : hostEl;
                }}
                preferredSide="bottom"
                align="start"
                onDismiss={() => props.onDefinitionPickerChange(null)}
                ref={(el) => {
                  queueMicrotask(() => el?.focus());
                }}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.stopPropagation();
                    props.onDefinitionPickerChange(null);
                    view?.focus();
                    return;
                  }
                  if (e.key === "ArrowDown") {
                    e.preventDefault();
                    props.onDefinitionPickerChange({
                      ...picker,
                      selected: Math.min(
                        picker.selected + 1,
                        picker.candidates.length - 1,
                      ),
                    });
                    return;
                  }
                  if (e.key === "ArrowUp") {
                    e.preventDefault();
                    props.onDefinitionPickerChange({
                      ...picker,
                      selected: Math.max(picker.selected - 1, 0),
                    });
                    return;
                  }
                  if (e.key === "Enter") {
                    e.preventDefault();
                    const c = picker.candidates[picker.selected];
                    if (c) props.onJumpDefinitionCandidate(c);
                  }
                }}
              >
                <div
                  class="den-files-editor__definition-header"
                  data-testid="files-definition-picker-header"
                >
                  {definitionPickerHeader(
                    picker.symbol,
                    picker.candidates.length,
                  )}
                </div>
                <p class="den-files-editor__definition-explainer">
                  {definitionPickerExplainer(picker.truncated)}
                </p>
                <ul class="den-files-editor__symbol-list">
                  <For each={[...picker.candidates]}>
                    {(c, i) => (
                      <li>
                        <button
                          type="button"
                          class="den-files-editor__symbol-row den-files-editor__definition-row"
                          classList={{
                            "den-files-editor__symbol-row--active":
                              i() === picker.selected,
                          }}
                          role="option"
                          aria-selected={i() === picker.selected}
                          data-testid="files-definition-picker-row"
                          onClick={() => props.onJumpDefinitionCandidate(c)}
                        >
                          <span class="den-files-editor__definition-row-meta">
                            <span class="den-files-editor__symbol-kind">
                              {formatSentenceCase(c.kind)}
                            </span>
                            <span class="den-files-editor__symbol-path">
                              {c.path}
                            </span>
                            <span class="den-files-editor__symbol-line">
                              :{c.line}
                            </span>
                          </span>
                          <Show when={c.snippet}>
                            <span
                              class="den-files-editor__definition-row-snippet"
                              data-testid="files-definition-picker-snippet"
                            >
                              {c.snippet}
                            </span>
                          </Show>
                        </button>
                      </li>
                    )}
                  </For>
                </ul>
                <Show when={picker.truncated}>
                  <p
                    class="den-files-editor__definition-footer"
                    data-testid="files-definition-picker-truncated"
                  >
                    {DEFINITION_PICKER_TRUNCATION_FOOTER}
                  </p>
                </Show>
              </AnchoredSurface>
            )}
          </Show>
        </div>
        <Show when={props.buffer.diffPreview?.title}>
          <div class="den-git-review__preview" data-testid="git-review-preview">
            <Show when={props.buffer.diffPreview?.walkStep}>
              {(step) => (
                <button
                  type="button"
                  class="den-git-review__button"
                  onClick={() => openFilesBuffer(props.projectId, {
                    kind: "walk",
                    walkStep: step(),
                    walkGitSelection: props.buffer.diffPreview?.walkSelection,
                    name: walkPageTitle(step()),
                    rootId: "", rootLabel: "", path: "",
                    intent: "permanent", origin: "reader",
                  })}
                >
                  Back to Git review
                </button>
              )}
            </Show>
            <strong>{props.buffer.diffPreview?.title}</strong>
            <span>{props.buffer.diffPreview?.detail}</span>
          </div>
        </Show>
        <Show when={historicalActive()}>
          <div class="den-file-version__document" classList={{ "den-files-editor__preview--preparing": !versionDisplayed() }}>
          <FileVersionDocument
            client={props.client} projectId={props.projectId} sessionId={props.sessionId}
            buffer={props.buffer}
            version={() => props.version ?? null}
            preview={comparisonPreview()}
            comparison={versionComparison}
            currentContent={() =>
              comparisonPreview()
                ? props.version?.source.kind === "text" ? props.version.source.before : null
                : currentTipAbsent()
                  ? null
                  : editorView()?.state.doc.toString() ?? null
            }
            comparisonMenuOpen={() => versionComparisonMenu() != null}
            onOpenComparisonMenu={setVersionComparisonMenu}
            onCurrent={props.onSelectCurrent}
            onContributor={(author, line) => jumpToAttribution({ sessionId: author.session_id ?? "", toolCallId: author.tool_call_id ?? "", turn: author.turn, ts: "" }, line)}
            onHandle={setVersionHandle}
            onReady={ready => {
              setReadyVersion(ready ? props.version ?? undefined : undefined);
              if (ready) setVersionDisplayed(true);
            }}
          />
          </div>
        </Show>
        <Show when={currentAbsent()}>
          <Show when={deletionVersion()} fallback={
            <Show when={lastDeletion().kind === "unrecorded"}>
              <div class="den-file-version__document" data-testid="file-current-absent">
                <div class="den-file-version__state" role="status">
                  <ThemeIcon slot="delete" size={20} />
                  <strong>This file has been deleted.</strong>
                  <span>Select an earlier version to view its contents.</span>
                </div>
              </div>
            </Show>
          }>
            <div class="den-file-version__document" data-testid="file-deletion-document">
              <FileVersionDocument client={props.client} projectId={props.projectId} sessionId={props.sessionId} buffer={props.buffer}
                version={deletionVersion} preview comparison={() => "before"} currentContent={() => null}
                comparisonMenuOpen={() => false} onOpenComparisonMenu={() => {}} onCurrent={() => {}}
                onReady={setDeletionReady} onHandle={setDeletionHandle} />
            </div>
          </Show>
        </Show>
        <LineFactsCard
          state={lineFactsCard()}
          onKeepOpen={cancelHidePopover}
          onLeave={scheduleHidePopover}
          onDismiss={hideLineFacts}
          onReturnFocus={returnLineFactsFocus}
          onActivate={activateLineFact}
        />
        <RemovedLinesPopover
          preview={removedPreview()?.preview ?? null}
          canReject={!props.buffer.dirty && !!props.onRejectHunk}
          onShowInPlace={openRemovedInPlace}
          onReject={rejectRemoved}
          onKeepOpen={keepRemovedPreview}
          onLeave={scheduleHideRemovedPreview}
          onDismiss={hideRemovedPreview}
        />
        <Show when={previewActive()}>
          <FileMarkdownPreview
            source={props.previewSource?.(previewText()) ?? previewText()}
            projectId={props.projectId}
            preparing={!previewDisplayed()}
            waitingVisible={false}
            onReadyChange={(ready) => {
              setPreviewReady(ready);
              if (ready) setPreviewDisplayed(true);
            }}
          />
        </Show>
        <Show when={showPreviewWait()}>
          <div class="den-presentation-wait" role="status">{historicalActive() ? "Loading…" : "Preparing preview…"}</div>
        </Show>
      </div>
      <div class="den-files-editor-dock" ref={element => {
        props.onDockMount?.(element);
        onCleanup(() => props.onDockMount?.(null));
      }} />
      <FilesEditorStatus identity={editorIdentity()} dirtyCount={dirtyOpenCount()}
        cursor={!historicalActive() && !currentAbsent() ? props.cursor : undefined} language={languageLabel()}
        simplified={simplifiedStatusVisible()}
        windows={<DocumentWindows projectId={props.projectId} replica={editorReplica(props.buffer.documentId)} peerViews={props.peerViews} />}
        notices={<>
        <span class="den-files-editor__command-prompt" ref={setEditingPrompt} />
        <Show when={!historicalActive() && editingStatus()}>
          <span role="status" data-testid="files-editor-editing-state">{editingStatus()}</span>
        </Show>
        <Show when={editorIdentity().id === "reconnecting" && props.buffer.editorOpening?.status === "reconnecting"}>
          <span class="den-files-editor__status-detail" role="status">
            {props.buffer.editorOpening?.status === "reconnecting" ? props.buffer.editorOpening.message : ""}
          </span>
        </Show>
        <Show when={!historicalActive() && props.buffer.editorEditingBlock}>
          <span role="status" data-testid="files-editor-synchronization">
            {props.buffer.editorEditingBlock === "capacity" ? "Editing paused: pending draft limit reached"
              : "Editing paused: draft storage failed"}
          </span>
        </Show>
        <Show when={!historicalActive() && props.buffer.absent}>
          <span role="status" data-testid="files-editor-absent">
            This file was deleted on disk. Save recreates it from this draft.
          </span>
        </Show>
        <Show when={!historicalActive() && props.buffer.editorRefusal}>
          <span role="status" data-testid="files-editor-refusal">{props.buffer.editorRefusal}</span>
        </Show>
        <Show
          when={!historicalActive() &&
            (props.updatedNote || props.definitionNotice || props.observationNotice)}
        >
          <span
            class="den-files-editor__status-detail"
            data-testid="files-editor-status-note"
            role="status"
            aria-live="polite"
          >
            {props.updatedNote
              ? "Updated to latest"
              : props.definitionNotice ?? props.observationNotice}
          </span>
        </Show>
        </>}
        indent={!historicalActive() && !currentAbsent() ? {label:indentChipLabel(effectiveIndent(props.buffer)),tip:indentChipTip(props.buffer),onOpen:openIndentMenu,ref:element=>{indentChipEl=element;}} : undefined}
        eol={!historicalActive() && !currentAbsent() ? {label:props.buffer.mixedEol ? "Mixed EOL" : props.buffer.eol.toUpperCase(),tip:eolChipTip(props.buffer),onOpen:openEolMenu,ref:element=>{eolChipEl=element;}} : undefined}
        details={<>
        <Show
          when={!historicalActive() && encodingChipLabel(props.buffer.encoding)}
          keyed
        >
          {(label) => (
            <span
              class="den-files-editor__status-detail"
              data-testid="files-editor-encoding-chip"
              data-tip={encodingChipTip(props.buffer)}
            >
              {label}
            </span>
          )}
        </Show>
        <Show
          when={!historicalActive() && editorConfigStatusLabel(props.buffer)}
          keyed
        >
          {(label) => (
            <span
              class="den-files-editor__status-detail"
              data-testid="files-editor-editorconfig-chip"
              data-tip={editorConfigStatusTip(props.buffer)}
            >
              {label}
            </span>
          )}
        </Show>
        <Show when={secretScreenGap()} keyed>
          {(gap) => (
            <span
              class="den-files-editor__status-detail"
              data-testid="files-editor-secret-gap-chip"
              data-tip={
                gap === "truncated"
                  ? SECRET_SPAN_COPY.chipTruncatedTip(
                    (historicalActive()
                      ? props.version?.secretScreen
                      : filesEditorSecretScreen(props.buffer.key)
                    )?.screened_bytes ?? 0,
                  )
                  : SECRET_SPAN_COPY.chipNotScreenedTip
              }
            >
              {gap === "truncated"
                ? SECRET_SPAN_COPY.chipTruncated
                : SECRET_SPAN_COPY.chipNotScreened}
            </span>
          )}
        </Show>
        </>}
      />
      <Show when={toolbarMenu()} keyed>
        {(menu) => (
          <ContextMenu
            anchor={menu.anchor}
            items={toolbarMenuItems(menu.kind)}
            onDismiss={() => setToolbarMenu(null)}
          />
        )}
      </Show>
      <Show when={indentMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            side="top"
            gap={4}
            items={[
              {
                label: "Applies to new edits in this file",
                disabled: true,
                testId: "files-indent-hint",
              } satisfies ContextMenuItem,
              ...indentMenuItems(),
            ]}
            onDismiss={() => setIndentMenu(null)}
          />
        )}
      </Show>
      <Show when={eolMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            side="top"
            gap={4}
            items={[
              ...(props.buffer.mixedEol
                ? [{
                  label: "Mixed endings will normalize on save",
                  disabled: true,
                  testId: "files-eol-hint",
                } satisfies ContextMenuItem]
                : []),
              contextAction("eolLf", {
                testId: "files-eol-lf",
                disabled: !props.editable || !props.onChooseLineEndings,
                onSelect: () => props.onChooseLineEndings?.("lf"),
              }),
              contextAction("eolCrlf", {
                testId: "files-eol-crlf",
                disabled: !props.editable || !props.onChooseLineEndings,
                onSelect: () => props.onChooseLineEndings?.("crlf"),
              }),
            ]}
            onDismiss={() => setEolMenu(null)}
          />
        )}
      </Show>
      <Show when={versionComparisonMenu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            side="top"
            gap={4}
            items={[
              {
                label: "Compare selected version with",
                description:
                  "Current shows changes if restored. Before shows what this version changed.",
                header: true,
              },
              contextAction("currentVersion", {
                checked: versionComparison() === "current",
                testId: "file-version-compare-current",
                onSelect: () => setSelectedVersionComparison(props.projectId, props.buffer.key, "current"),
              }),
              contextAction("previousVersion", {
                checked: versionComparison() === "before",
                disabled: !props.version || !versionComparesBefore(props.version),
                testId: "file-version-compare-before",
                onSelect: () => setSelectedVersionComparison(props.projectId, props.buffer.key, "before"),
              }),
            ]}
            onDismiss={() => setVersionComparisonMenu(null)}
          />
        )}
      </Show>
    </div>
  );
}
