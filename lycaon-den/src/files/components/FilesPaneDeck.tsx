import { Show, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { CodeScanEvent, ProjectRoot } from "../../api/types.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { SurfaceDeck } from "../../components/primitives/SurfaceDeck.tsx";
import { beginRenameCard } from "../../components/source/inline-edit/run-editor-action.ts";
import { TrustReviewPage } from "../../components/trust/TrustReviewPage.tsx";
import { editorWindow, type EditorWindow } from "../../platform/windows/editor-windows.ts";
import { itemWindowViews } from "../../platform/windows/item-windows.ts";
import type { Preparation } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import type { createFileConfirmations } from "../commands/file-confirmations.ts";
import type { createFileDocumentCommands } from "../documents/file-document-commands.ts";
import type { createFileMergeCommands } from "../documents/file-merge-commands.ts";
import { filesBufferNeedsBody, projectFilesState, type FileBuffer } from "../documents/files-buffer-state.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { openFilesBuffer } from "../documents/project-files-buffers.ts";
import { BufferMergeView } from "../editor/BufferMergeView.tsx";
import type { createDefinitionNavigation } from "../editor/definition-navigation.ts";
import type { createFilesEditorActions } from "../editor/files-editor-actions.ts";
import { FilesEditor } from "../editor/FilesEditor.tsx";
import { FilesImageViewer } from "../editor/FilesImageViewer.tsx";
import type { createFileRestoration } from "../history/file-restoration-commands.ts";
import { listedRestoreDisabledReason, versionRestoreDisabledReason } from "../history/file-version-restore.ts";
import type { createFilesReviewNavigation } from "../history/files-review-navigation.ts";
import type { createFilesVersionHistory } from "../history/files-version-history.ts";
import { selectedFileVersion } from "../history/files-version-selection.ts";
import type { createVersionNavigation } from "../history/version-navigation.ts";
import { scopeChangeKindFor } from "../tree/scope-resolution.ts";
import { DiffsPage } from "../review/DiffsPage.tsx";
import { openGitReviewFile } from "../review/git-review-preview.ts";
import { walkStepEffectForVersion } from "../walk/walk-model.ts";
import { enterWalk, leaveWalk, setWalkAt, walkState } from "../walk/walk-store.ts";
import { WalkStepPage } from "../walk/WalkStepPage.tsx";
import { ChatContentPage } from "./ChatContentPage.tsx";
import { parseFilesPaneKey } from "./files-pane-key.ts";
import { FilesInfoCard } from "./FilesInfoCard.tsx";
import { pushProjectJump } from "./project-files-jump-bridge.ts";
import type { FileBufferKey } from "./project-files-model.ts";

/** Each open buffer's pane, keyed by buffer and kind, presented through one deck. */
export function FilesPaneDeck(props: {
  projectId: string;
  sessionId: string | undefined;
  client: LycaonClient | null;
  roots: ProjectRoot[];
  scanUpdate: Pick<CodeScanEvent, "scan_id" | "status"> | undefined;
  document?: { initialMarkdownView?: "code" | "preview"; previewSource?: (source: string) => string };
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
  activePane: string | null;
  onDisplayedChange: (pane: string | null) => void;
  onPublishingChange: (pane: string | null) => void;
  pending: boolean;
  retainedWait: boolean;
  preparation: Preparation;
  walking: boolean;
  walkReady: boolean;
  readerHandles: Map<string, { gotoLine: () => void | Promise<void> }>;
  onDockMount: (pane: string, element: HTMLElement | null) => void;
  saveError: string | null;
  updatedNote: boolean;
  restoringVersion: boolean;
  cursor: { line: number; col: number };
  onCursor: (cursor: { line: number; col: number }) => void;
  onInnerLayerChange: (open: boolean) => void;
  observationNoticeFor: (buffer: FileBuffer) => string | null;
  revealInTree: (rootId: string, path: string, isDir?: boolean) => void;
  retryEditorOpening: (key: FileBufferKey) => void;
  scrollActiveTabIntoView: () => void;
  openWalkStepFile: ReturnType<typeof createFilesReviewNavigation>["openWalkStepFile"];
  reopenBufferAs: (buffer: FileBuffer, decodeAs: "utf-16le" | "utf-16be") => void;
  confirmations: ReturnType<typeof createFileConfirmations>;
  documents: ReturnType<typeof createFileDocumentCommands>;
  versions: ReturnType<typeof createVersionNavigation>;
  history: ReturnType<typeof createFilesVersionHistory>;
  restoration: ReturnType<typeof createFileRestoration>;
  definitions: ReturnType<typeof createDefinitionNavigation>;
  merge: ReturnType<typeof createFileMergeCommands>;
  editorActions: ReturnType<typeof createFilesEditorActions>;
}): JSX.Element {
  const state = () => projectFilesState(props.projectId);
  /** Peer windows include pending document joins. */
  const peerViewsFor = (buffer: FileBuffer): EditorWindow[] => {
    const views = itemWindowViews();
    return views
      .filter(
        (view) =>
          view.kind === "file" &&
          view.projectId === props.projectId &&
          view.rootId === buffer.rootId &&
          view.path === buffer.path,
      )
      .flatMap((view) => {
        const window = editorWindow({ client_id: `window:${view.label}` }, views);
        return window ? [window] : [];
      });
  };
  return (
    <div
      class="den-files-pane-presentation"
      data-testid="files-pane-presentation"
      data-pending={String(props.pending)}
      aria-busy={props.pending}
    >
      <div
        class="den-files-pane-presentation__content den-retained-presentation"
        data-retained={props.retainedWait ? "true" : "false"}
        aria-hidden={props.pending}
        inert={props.pending ? true : undefined}
      >
        <PresentationProvider preparation={props.preparation}>
          <SurfaceDeck active={props.activePane} class="den-files-tab-deck" retainOutgoing
            generation={(pane) => { const parsed = parseFilesPaneKey(pane); return (parsed ? state().byKey[parsed.key]?.payloadGeneration : 0) ?? 0; }}
            payloadAvailable={(pane) => {
              const parsed = parseFilesPaneKey(pane);
              const buffer = parsed ? state().byKey[parsed.key] : undefined;
              return !!buffer && (isComposedBufferKind(buffer.kind) || !filesBufferNeedsBody(buffer) || !!buffer.loadError || selectedFileVersion(props.projectId, buffer.key) !== null);
            }}
            waitingVisible={false} interactive={!props.pending} onDisplayedChange={props.onDisplayedChange}
            onPublishingChange={props.onPublishingChange}
            canPublish={(pane) => {
              const parsed = parseFilesPaneKey(pane);
              const buffer = parsed ? state().byKey[parsed.key] : undefined;
              const historical = parsed && selectedFileVersion(props.projectId, parsed.key) !== null;
              const unreadyCondense = buffer?.kind === "text" && !!buffer?.condenseContext && !buffer.loadError && !buffer.deleted && scopeChangeKindFor(props.projectId, buffer.rootId, buffer.path) !== "deleted";
              return (historical || ((buffer?.content.state !== "suspended" || !!buffer?.loadError) && !buffer?.loading)) && !unreadyCondense && (!props.walking || props.walkReady);
            }}
            retain={(pane) => {
              const parsed = parseFilesPaneKey(pane);
              return Boolean(parsed && state().byKey[parsed.key]?.kind === parsed.kind);
            }}>
            {(paneKey) => {
              const parsed = parseFilesPaneKey(paneKey);
              if (!parsed) return null;
              const key = parsed.key;
              const paneKind = parsed.kind;
              const buf = state().byKey[key];
              if (!buf) return null;
              const payload = () => {
                if (paneKind === "image") {
                  return (
                    <FilesImageViewer
                      projectId={props.projectId}
                      sessionId={props.sessionId}
                      buffer={buf}
                      roots={props.roots}
                      client={props.client}
                      onInnerLayerChange={(open) => { if (props.activePane === paneKey) props.onInnerLayerChange(open); }}
                      onRevealSegment={props.revealInTree}
                    />
                  );
                }
                if (paneKind === "chat" && buf.chatContent) return <ChatContentPage projectId={props.projectId} document={buf.chatContent} client={props.client}
                  onHandle={handle=>{if(handle) props.readerHandles.set(key,handle);else props.readerHandles.delete(key);}} />;
                if (paneKind === "trust") return <TrustReviewPage projectId={props.projectId} />;
                if (paneKind === "diffs" && buf.diffs) {
                  return (
                    <DiffsPage
                      projectId={props.projectId}
                      client={props.client}
                      address={buf.diffs}
                      rootRefs={props.roots}
                      onWalkTurn={(address) => {
                        void enterWalk(props.projectId, props.client, address.sessionId, undefined, {
                          transition: "replace", startMessageId: address.messageId,
                        });
                        props.scrollActiveTabIntoView();
                      }}
                    />
                  );
                }
                if (paneKind === "walk") {
                  return (
                    <WalkStepPage
                      projectId={props.projectId}
                      buffer={buf}
                      client={props.client}
                      sessionId={props.sessionId}
                      rootLabelFor={(rootId) => props.roots.find((root) => root.id === rootId)?.label}
                      onOpenGitFile={(step, review, file, comparison) => {
                        openGitReviewFile(
                          props.projectId,
                          props.roots.find((root) => root.id === step.change.root_id)?.label ?? "Project root",
                          step, review, file, comparison,
                        );
                        props.scrollActiveTabIntoView();
                      }}
                      onOpenFile={props.openWalkStepFile}
                    />
                  );
                }
                if (paneKind === "info") {
                  return (
                    <FilesInfoCard
                      projectId={props.projectId} client={props.client} sessionId={props.sessionId}
                      onReaderHandle={handle => { if (handle) props.readerHandles.set(key, handle); else props.readerHandles.delete(key); }}
                      buffer={buf}
                      roots={props.roots}
                      onInnerLayerChange={(open) => { if (props.activePane === paneKey) props.onInnerLayerChange(open); }}
                      onRevealSegment={props.revealInTree}
                      onReload={() => props.documents.reloadBuffer(buf.key)}
                      onReopenAsUTF16={(decodeAs) => props.reopenBufferAs(buf, decodeAs)}
                    />
                  );
                }
                return (
                  // Reset the view when its merge model changes.
                  <Show
                    keyed
                    when={
                      props.merge.mergeModel()?.bufferKey === key ? props.merge.mergeModel() : null
                    }
                    fallback={
                      <FilesEditor
                    onReaderHandle={handle => { if (handle) props.readerHandles.set(key, handle); else props.readerHandles.delete(key); }}
                        projectId={props.projectId}
                        onDockMount={element => props.onDockMount(paneKey, element)}
                        sessionId={props.sessionId}
                        scanUpdate={props.scanUpdate}
                        buffer={buf}
                        roots={props.roots}
                        client={props.client}
                        initialMarkdownView={
                          props.document?.initialMarkdownView
                        }
                        previewSource={props.document?.previewSource}
                        saving={props.documents.saving()}
                        saveError={props.saveError}
                        conflict={buf.diverged}
                        heldAgentEdit={buf.heldAgentVersionId != null}
                        updatedNote={props.updatedNote}
                        definitionNotice={props.definitions.definitionNotice()}
                        observationNotice={props.observationNoticeFor(buf)}
                        savedFlash={props.documents.savedFlash()}
                        editable={props.documents.editable(buf)}
                        version={props.versions.versionForBuffer(buf)}
                        versionHistory={props.history.versionHistoryForBuffer(buf)}
                        restoringVersion={props.restoringVersion}
                        versionRestoreDisabledReason={(() => {
                          const version = props.versions.versionForBuffer(buf);
                          if (!version) return null;
                          return versionRestoreDisabledReason({
                            buffer: buf,
                            history: props.history.versionHistoryForBuffer(buf),
                            version,
                            restoring: props.restoringVersion,
                          });
                        })()}
                        onRestoreVersion={(version) =>
                          void props.restoration.runSelectedVersionRestore(buf, version)
                        }
                        onSelectVersion={(version) => {
                          if (props.walking) {
                            const steps = walkState(props.projectId).walk.steps;
                            const index = steps.findIndex((step) =>
                              walkStepEffectForVersion(step, version.id) !== null
                            );
                            if (index >= 0) {
                              setWalkAt(props.projectId, index);
                              return;
                            }
                            leaveWalk(props.projectId);
                          }
                          props.versions.selectStoredVersion(version);
                        }}
                        onSelectCommit={(entry) => {
                          if (props.walking) leaveWalk(props.projectId);
                          props.versions.selectCommitPreview(buf, entry);
                        }}
                        onRestoreListedVersion={(version) =>
                          void props.restoration.runListedRestore(buf, { kind: "version", version })
                        }
                        onRestoreListedCommit={(entry) =>
                          void props.restoration.runListedRestore(buf, { kind: "commit", entry })
                        }
                        listedRestoreReason={(row) =>
                          listedRestoreDisabledReason({
                            buffer: buf,
                            history: props.history.versionHistoryForBuffer(buf),
                            restoring: props.restoringVersion,
                            row,
                          })}
                        onLoadVersionHistory={() => props.history.loadVersionHistory(buf)}
                        onLoadEarlierVersions={() => props.history.loadEarlierVersions(buf)}
                        onSelectCurrent={() => props.versions.returnToCurrent(buf.key)}
                        peerViews={peerViewsFor(buf)}
                        stateActionBusy={props.documents.changingEditorState() || props.restoringVersion}
                        onOpenCurrent={() => props.documents.openCurrentProjectFile(buf)}
                        onMakeEditable={props.client ? () => props.documents.makeBufferEditable(buf) : undefined}
                        onChooseLineEndings={(eol) => props.documents.chooseLineEndings(buf, eol)}
                        onRetryEditing={() => props.retryEditorOpening(key)}
                        onCursor={(line, col) => { if (props.activePane === paneKey) props.onCursor({ line, col }); }}
                        cursor={props.cursor}
                        onSave={() => void props.documents.performSave(key)}
                        onDiscard={() => props.confirmations.requestDiscard(key)}
                        onRejectHunk={(hunk) => void props.restoration.runEditorHunkReject(hunk)}
                        onRevertFile={() => void props.restoration.runFileRevert(buf.rootId, buf.path)}
                        onRestoreDeleted={(versionId) => void props.restoration.runDeletedFileRestore(buf, versionId)}
                        onReload={() => {
                          props.confirmations.requestReload(key);
                        }}
                        onMerge={() => void props.merge.openMergeForBuffer(key)}
                        onInnerLayerChange={(open) => { if (props.activePane === paneKey) props.onInnerLayerChange(open); }}
                        onRevealSegment={(rootId, path, isDir) =>
                          props.revealInTree(rootId, path, isDir)
                        }
                        onSymbolJump={(line) => {
                          pushProjectJump(props.projectId, {
                            bufferKey: key,
                            rootId: buf.rootId,
                            path: buf.path,
                            ...(buf.jobId ? { jobId: buf.jobId } : {}),
                            line,
                          });
                          openFilesBuffer(props.projectId, {
                            rootId: buf.rootId,
                            rootLabel: buf.rootLabel,
                            path: buf.path,
                            jobId: buf.jobId,
                            intent: "permanent",
                            revealLine: line,
                          });
                        }}
                        onRenameSymbol={(name) => {
                          const ctx = props.editorActions.buildEditorActionContext();
                          if (ctx) {
                            void beginRenameCard(ctx, { symbol: name });
                          }
                        }}
                        onCursorTeleport={(line) => {
                          pushProjectJump(props.projectId, {
                            bufferKey: key,
                            rootId: buf.rootId,
                            path: buf.path,
                            ...(buf.jobId ? { jobId: buf.jobId } : {}),
                            line,
                          });
                        }}
                        onRevealInTranscript={props.onRevealInTranscript}
                        definitionPicker={props.definitions.definitionPicker()}
                        onDefinitionPickerChange={props.definitions.setDefinitionPicker}
                        onGoToDefinitionAt={(pos) =>
                          void props.definitions.runGoToDefinition(pos)
                        }
                        onJumpDefinitionCandidate={props.definitions.jumpToDefinitionCandidate}
                      />
                    }
                  >
                    {(model) => (
                      <BufferMergeView
                        model={model}
                        applying={props.merge.mergeApplying()}
                        applyNote={props.merge.mergeNote()}
                        onApply={(payload) => void props.merge.applyMerge(payload)}
                        onCancel={() => {
                          props.merge.setMergeModel(null);
                          props.merge.setMergeNote(null);
                        }}
                      />
                    )}
                  </Show>
                );
              };
              return <Show when={isComposedBufferKind(buf.kind) || !filesBufferNeedsBody(buf) || !!buf.loadError || selectedFileVersion(props.projectId, key) !== null}>{payload()}</Show>;
            }}
          </SurfaceDeck>
        </PresentationProvider>
      </div>
    </div>
  );
}
