import { Show, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { CodeScanEvent, ProjectRoot, SourceDefinitionCandidate, SourceFileVersion } from "../../api/types.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import type { EolKind } from "../../components/source/editor/eol.ts";
import type { SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { LAYOUT_BAND_SCALES, bindLayoutBand } from "../../layout/layout-bands.ts";
import type { EditorWindow } from "../../platform/windows/editor-windows.ts";
import { DocumentWindows } from "../components/DocumentWindows.tsx";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { DocumentChangeHistory } from "../history/DocumentChangeHistory.tsx";
import type { FileHistoryCommit } from "../history/file-history-model.ts";
import type { FileVersionHistory, FileVersionView } from "../history/file-version.ts";
import { FileVersionDocument } from "../history/FileVersionDocument.tsx";
import { FileVersionPicker } from "../history/FileVersionPicker.tsx";
import type { DiffLineHunk } from "../review/line-diff.ts";
import { FileMarkdownPreview } from "./FileMarkdownPreview.tsx";
import { FilesEditorHeaderChrome, FilesEditorStatus } from "./FilesEditorChrome.tsx";
import {
  type DefinitionPickerState, createFilesEditorMenus, FilesEditorContextMenus,
  FilesEditorDefinitionPicker, FilesEditorGitReviewPreview, FilesEditorOverlays,
  FilesEditorStatusDetails, FilesSymbolMenu,
} from "./FilesEditorMenus.tsx";
import { useFilesEditorAnnotations } from "./useFilesEditorAnnotations.ts";
import { useFilesEditorCodeMirror } from "./useFilesEditorCodeMirror.ts";
import { useFilesEditorHistoryState } from "./useFilesEditorHistoryState.ts";

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
    row: { kind: "version"; version: SourceFileVersion } | { kind: "commit"; entry: FileHistoryCommit },
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
  cursor: { line: number; col: number };
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
  definitionPicker: DefinitionPickerState | null;
  onDefinitionPickerChange: (next: DefinitionPickerState | null) => void;
  onGoToDefinitionAt: (pos: number) => void;
  onJumpDefinitionCandidate: (c: SourceDefinitionCandidate) => void;
};

export function FilesEditor(props: FilesEditorProps) {
  const historicalActive = () => props.version != null;
  const cm = useFilesEditorCodeMirror({
    projectId: () => props.projectId, sessionId: () => props.sessionId,
    client: () => props.client, buffer: () => props.buffer, editable: () => props.editable,
    version: () => props.version, historicalActive,
    onCursor: (line, col) => props.onCursor(line, col),
    onCursorTeleport: (line) => props.onCursorTeleport(line),
    onGoToDefinitionAt: (pos) => props.onGoToDefinitionAt(pos),
    onRejectHunk: () => props.onRejectHunk, saveError: () => props.saveError,
    conflict: () => props.conflict, stateActionBusy: () => props.stateActionBusy,
    versionRestoreDisabledReason: () => props.versionRestoreDisabledReason,
    onRestoreVersion: (version) => props.onRestoreVersion(version),
    onOpenCurrent: () => props.onOpenCurrent, onMakeEditable: () => props.onMakeEditable,
    onRetryEditing: () => props.onRetryEditing, onRestoreDeleted: () => props.onRestoreDeleted,
    onRevertFile: () => props.onRevertFile,
  });

  const history = useFilesEditorHistoryState({
    projectId: () => props.projectId, sessionId: () => props.sessionId,
    client: () => props.client, buffer: () => props.buffer, version: () => props.version,
    versionHistory: () => props.versionHistory, onReaderHandle: () => props.onReaderHandle,
    onRestoreDeleted: () => props.onRestoreDeleted, onRevertFile: () => props.onRevertFile,
    editorView: cm.editorView,
  });

  const annotations = useFilesEditorAnnotations({
    ...cm.editor, version: () => props.version, initialMarkdownView: () => props.initialMarkdownView,
    cursorLine: () => props.cursor.line, scanUpdate: () => props.scanUpdate,
    revealInTranscript: () => props.onRevealInTranscript, rejectHunk: () => props.onRejectHunk,
    onSymbolJump: (line) => props.onSymbolJump(line), definitionPicker: () => props.definitionPicker,
    currentAbsent: history.currentAbsent, lastDeletionKind: () => history.lastDeletion().kind,
    deletionReady: history.deletionReady, historyPending: history.historyPending,
    versionDisplayed: history.versionDisplayed,
  });

  const {
    activeEditorView, simplifiedStatusVisible, editorIdentity,
    stateAction, stateActionDisabledReason, runStateAction,
  } = cm.bindEditorFeatures({
    currentAbsent: history.currentAbsent, comparisonPreview: history.comparisonPreview,
    previewActive: annotations.previewActive, deletionRestore: history.deletionRestore,
    versionHandle: history.versionHandle, activeReaderHandle: history.activeReaderHandle,
    editorScopeRevision: history.editorScopeRevision, deletedLines: history.deletedLines,
    dropCondenseRequest: history.dropCondenseRequest, condenseIfRequested: history.condenseIfRequested,
    toggleSymbolMenu: annotations.toggleSymbolMenu,
    installGutterInteractions: annotations.lineFacts.installGutterInteractions,
    attrMarks: annotations.attrMarks, findingMarks: annotations.findingMarks,
    symbols: annotations.symbols, findingRecords: annotations.findingRecords,
    findingDiagnosticHandlers: annotations.lineFacts.findingDiagnosticHandlers,
  });

  const menus = createFilesEditorMenus({
    projectId: () => props.projectId, buffer: () => props.buffer, currentView: cm.currentView,
    editable: () => props.editable, roots: () => props.roots, version: () => props.version,
    activeReaderHandle: history.activeReaderHandle, activeEditorView,
    lineFactsOpen: () => annotations.lineFacts.lineFactsCard() != null,
    symbolMenuOpen: annotations.symbolMenuOpen,
    definitionPickerOpen: () => props.definitionPicker != null,
    onInnerLayerChange: (open) => props.onInnerLayerChange(open),
  });

  return (
    <div
      ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.filesEditor)}
      class="den-files-editor"
      data-testid="files-editor"
    >
      <FilesEditorHeaderChrome
        buffer={props.buffer}
        versionAvailability={props.version?.availability}
        versionPicker={
          <Show when={!history.comparisonPreview()}>
            <FileVersionPicker
              selected={props.version}
              currentAbsent={history.currentTipAbsent()}
              editHistory={(dismiss) => (
                <Show
                  keyed
                  when={props.client && props.buffer.documentId ? { client: props.client, documentId: props.buffer.documentId } : undefined}
                >
                  {(document) => (
                    <DocumentChangeHistory
                      client={document.client}
                      projectId={props.projectId}
                      documentId={document.documentId}
                      onApplied={() => { dismiss(); queueMicrotask(() => activeEditorView()?.focus()); }}
                    />
                  )}
                </Show>
              )}
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
        }
        stateAction={stateAction()}
        stateActionBusy={props.stateActionBusy}
        stateActionDisabledReason={stateActionDisabledReason()}
        onRunStateAction={runStateAction}
        savedNotice={(checkIcon) => (
          <Show when={props.savedFlash && !props.buffer.dirty && !historicalActive()}>
            <span class="den-files-editor__saved" data-testid="files-editor-saved" role="status" aria-live="polite">
              {checkIcon} Saved
            </span>
          </Show>
        )}
        editable={props.editable}
        saving={props.saving}
        conflict={props.conflict}
        onDiscard={() => props.onDiscard()}
        onSave={() => props.onSave()}
        isMarkdown={annotations.isMarkdown()}
        historicalActive={historicalActive()}
        mdView={annotations.mdView()}
        onChangeMdView={(id) => {
          if (id === "preview") {
            annotations.setPreviewText(cm.currentView()?.state.doc.toString() ?? filesBufferText(props.buffer));
            annotations.setMdView("preview");
          } else annotations.setMdView("code");
        }}
        previewActive={annotations.previewActive()}
        activeEditorView={activeEditorView}
        activeReaderHandle={history.activeReaderHandle}
        toolbarMenus={menus.toolbarMenus}
        onRevealSegment={props.onRevealSegment}
        symbolMenuOpen={annotations.symbolMenuOpen()}
        onToggleSymbolMenu={annotations.toggleSymbolMenu}
        enclosingCrumbSymbol={annotations.enclosingCrumbSymbol()}
        onRenameSymbol={props.onRenameSymbol}
      />
      <FilesSymbolMenu symbols={annotations.symbolNavigation} />
      <Show when={!historicalActive() && props.conflict}>
        <div class="den-files-editor__conflict" role="alert" data-testid="files-editor-conflict">
          <span class="flex-1">
            This file changed on disk while you have unsaved edits.
            <Show when={props.heldAgentEdit}>
              {" "}
              <span data-testid="files-editor-conflict-held">The AI's edit is kept in version history.</span>
            </Show>
          </span>
          <DenButton variant="primary" compact data-testid="files-editor-conflict-merge" onClick={() => props.onMerge()}>
            Merge…
          </DenButton>
          <DenButton variant="ghost" compact data-testid="files-editor-conflict-reload" onClick={() => props.onReload()}>
            Reload file
          </DenButton>
        </div>
      </Show>
      <div
        class="den-files-editor__document-stage"
        aria-busy={
          history.historyPending() || annotations.previewPending() ||
          editorIdentity().id === "opening" || editorIdentity().id === "reconnecting"
        }
      >
        <div
          class="den-files-editor__host-wrap den-retained-presentation"
          hidden={
            (annotations.previewActive() && annotations.previewDisplayed()) ||
            (historicalActive() && history.versionDisplayed()) || history.currentAbsent()
          }
          data-retained={annotations.showPreviewWait() ? "true" : "false"}
          aria-hidden={annotations.previewActive() || historicalActive() ? true : undefined}
          inert={annotations.previewActive() || historicalActive() ? true : undefined}
        >
          <div class="den-files-editor__host" data-testid="files-editor-host" tabIndex={-1} ref={cm.setHostElement} />
          <Show when={history.condensedContext()}>
            <div class="den-document-actions">
              <button type="button" class="den-document-action" onClick={history.expandCondensedContext}>Full file</button>
            </div>
          </Show>
          <FilesEditorDefinitionPicker
            picker={props.definitionPicker}
            activeEditorView={activeEditorView}
            currentView={cm.currentView}
            hostElement={cm.hostElement}
            onDefinitionPickerChange={props.onDefinitionPickerChange}
            onJumpDefinitionCandidate={props.onJumpDefinitionCandidate}
          />
        </div>
        <FilesEditorGitReviewPreview projectId={props.projectId} buffer={props.buffer} />
        <Show when={historicalActive()}>
          <div
            class="den-file-version__document"
            classList={{ "den-files-editor__preview--preparing": !history.versionDisplayed() }}
          >
            <FileVersionDocument
              client={props.client}
              projectId={props.projectId}
              sessionId={props.sessionId}
              buffer={props.buffer}
              version={() => props.version ?? null}
              preview={history.comparisonPreview()}
              comparison={history.versionComparison}
              currentContent={() =>
                history.comparisonPreview()
                  ? props.version?.source.kind === "text" ? props.version.source.before : null
                  : history.currentTipAbsent() ? null : cm.editorView()?.state.doc.toString() ?? null
              }
              comparisonMenuOpen={() => menus.versionComparisonMenu() != null}
              onOpenComparisonMenu={menus.setVersionComparisonMenu}
              onCurrent={props.onSelectCurrent}
              onContributor={(author, line) =>
                annotations.lineFacts.jumpToAttribution(
                  { sessionId: author.session_id ?? "", toolCallId: author.tool_call_id ?? "", turn: author.turn, ts: "" }, line,
                )
              }
              onHandle={history.setVersionHandle}
              onReady={(ready) => {
                history.setReadyVersion(ready ? props.version ?? undefined : undefined);
                if (ready) history.setVersionDisplayed(true);
              }}
            />
          </div>
        </Show>
        <Show when={history.currentAbsent()}>
          <Show
            when={history.deletionVersion()}
            fallback={
              <Show when={history.lastDeletion().kind === "unrecorded"}>
                <div class="den-file-version__document" data-testid="file-current-absent">
                  <div class="den-file-version__state" role="status">
                    <ThemeIcon slot="delete" size={20} />
                    <strong>This file has been deleted.</strong>
                    <span>Select an earlier version to view its contents.</span>
                  </div>
                </div>
              </Show>
            }
          >
            <div class="den-file-version__document" data-testid="file-deletion-document">
              <FileVersionDocument
                client={props.client}
                projectId={props.projectId}
                sessionId={props.sessionId}
                buffer={props.buffer}
                version={history.deletionVersion}
                preview
                comparison={() => "before"}
                currentContent={() => null}
                comparisonMenuOpen={() => false}
                onOpenComparisonMenu={() => {}}
                onCurrent={() => {}}
                onReady={history.setDeletionReady}
                onHandle={history.setDeletionHandle}
              />
            </div>
          </Show>
        </Show>
        <FilesEditorOverlays lineFacts={annotations.lineFacts} canRejectRemoved={!props.buffer.dirty && !!props.onRejectHunk} />
        <Show when={annotations.previewActive()}>
          <FileMarkdownPreview
            source={props.previewSource?.(annotations.previewText()) ?? annotations.previewText()}
            projectId={props.projectId}
            preparing={!annotations.previewDisplayed()}
            waitingVisible={false}
            onReadyChange={(ready) => {
              annotations.setPreviewReady(ready);
              if (ready) annotations.setPreviewDisplayed(true);
            }}
          />
        </Show>
        <Show when={annotations.showPreviewWait()}>
          <div class="den-presentation-wait" role="status">
            {historicalActive() ? "Loading…" : "Preparing preview…"}
          </div>
        </Show>
      </div>
      <div
        class="den-files-editor-dock"
        ref={(element) => { props.onDockMount?.(element); onCleanup(() => props.onDockMount?.(null)); }}
      />
      <FilesEditorStatus
        identity={editorIdentity()}
        dirtyCount={annotations.dirtyOpenCount()}
        cursor={!historicalActive() && !history.currentAbsent() ? props.cursor : undefined}
        language={annotations.languageLabel()}
        simplified={simplifiedStatusVisible()}
        windows={<DocumentWindows projectId={props.projectId} replica={cm.replica()} peerViews={props.peerViews} />}
        notices={
          <>
            <span class="den-files-editor__command-prompt" ref={cm.setEditingPrompt} />
            <Show when={!historicalActive() && cm.editingStatus()}>
              <span role="status" data-testid="files-editor-editing-state">{cm.editingStatus()}</span>
            </Show>
            <Show when={editorIdentity().id === "reconnecting" && props.buffer.editorOpening?.status === "reconnecting"}>
              <span class="den-files-editor__status-detail" role="status">
                {props.buffer.editorOpening?.status === "reconnecting" ? props.buffer.editorOpening.message : ""}
              </span>
            </Show>
            <Show when={!historicalActive() && props.buffer.editorEditingBlock}>
              <span role="status" data-testid="files-editor-synchronization">
                {props.buffer.editorEditingBlock === "capacity"
                  ? "Editing paused: pending draft limit reached"
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
            <Show when={!historicalActive() && (props.updatedNote || props.definitionNotice || props.observationNotice)}>
              <span class="den-files-editor__status-detail" data-testid="files-editor-status-note" role="status" aria-live="polite">
                {props.updatedNote ? "Updated to latest" : props.definitionNotice ?? props.observationNotice}
              </span>
            </Show>
          </>
        }
        indent={menus.indentChip(!historicalActive() && !history.currentAbsent())}
        eol={menus.eolChip(!historicalActive() && !history.currentAbsent())}
        details={
          <FilesEditorStatusDetails
            historicalActive={historicalActive()}
            buffer={props.buffer}
            version={props.version}
            secretScreenGap={annotations.secretScreenGap()}
          />
        }
      />
      <FilesEditorContextMenus
        projectId={props.projectId}
        buffer={props.buffer}
        editable={props.editable}
        version={props.version}
        versionComparison={history.versionComparison()}
        onChooseLineEndings={props.onChooseLineEndings}
        menus={menus}
      />
    </div>
  );
}
