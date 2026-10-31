import { unfoldAll } from "@codemirror/language";
import type { EditorView } from "@codemirror/view";
import { createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import type { DenEditorVersionComparison } from "../../../shared/app-state-types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { editorScopeDiffOriginal } from "../../components/source/diff/scope-diff.ts";
import type { SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import { filesBufferHasCurrentFile } from "../documents/files-buffer-change.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { clearFilesBufferCondense } from "../documents/project-files-buffers.ts";
import { foldCondensedContext } from "../history/file-version-condense.ts";
import type { FileVersionHistory, FileVersionView } from "../history/file-version.ts";
import { shownVersionComparison } from "../history/files-version-selection.ts";
import { createLastDeletion } from "../history/last-deletion.ts";
import { getDeletedLines, subscribeDeletedLines } from "../review/review-pane.ts";
import {
  scopeChangeKindFor,
  scopeDiffRevision,
  scopeEffectFor,
  subscribeResolvedScope,
} from "../tree/scope-resolution.ts";

export function useFilesEditorHistoryState(options: {
  projectId: () => string;
  sessionId: () => string | undefined;
  client: () => LycaonClient | null | undefined;
  buffer: () => FileBuffer;
  version: () => FileVersionView | null;
  versionHistory: () => FileVersionHistory;
  onReaderHandle: () => ((handle: SourceReaderHandle | undefined) => void) | undefined;
  onRestoreDeleted: () => ((versionId: string) => void) | undefined;
  onRevertFile: () => (() => void) | undefined;
  editorView: () => EditorView | undefined;
}) {
  const historicalActive = () => options.version() != null;

  const [editorScopeRevision, setEditorScopeRevision] = createSignal(
    scopeDiffRevision(options.projectId(), options.buffer().rootId, options.buffer().path),
  );
  createResidentActivity(() => {
    setEditorScopeRevision(
      scopeDiffRevision(options.projectId(), options.buffer().rootId, options.buffer().path),
    );
    return subscribeResolvedScope((id) => {
      if (id === options.projectId().trim()) {
        setEditorScopeRevision(
          scopeDiffRevision(options.projectId(), options.buffer().rootId, options.buffer().path),
        );
      }
    });
  });

  const [deletedLines, setDeletedLinesMode] = createSignal(getDeletedLines(options.projectId()));
  createResidentActivity(() => {
    setDeletedLinesMode(getDeletedLines(options.projectId()));
    return subscribeDeletedLines((id) => {
      if (id === options.projectId().trim()) {
        setDeletedLinesMode(getDeletedLines(options.projectId()));
      }
    });
  });

  const commitDeleted = () => {
    void editorScopeRevision();
    const change = scopeEffectFor(
      options.projectId(),
      options.buffer().rootId,
      options.buffer().path,
    );
    return !filesBufferHasCurrentFile(options.buffer()) &&
      !options.buffer().jobId &&
      change?.commit &&
      (change.tip.state === "absent" || change.commit.op === "delete")
      ? change.commit
      : null;
  };

  const [versionHandle, setVersionHandle] = createSignal<SourceReaderHandle>();
  const [readyVersion, setReadyVersion] = createSignal<FileVersionView>();
  const [versionDisplayed, setVersionDisplayed] = createSignal(false);
  createEffect(() => {
    if (!options.version()) setVersionDisplayed(false);
  });

  const [deletionHandle, setDeletionHandle] = createSignal<SourceReaderHandle>();
  const [deletionReady, setDeletionReady] = createSignal(false);

  const comparisonPreview = () =>
    options.buffer().kind === "diff" && options.buffer().diffPreview != null;

  const currentTipAbsent = () =>
    Boolean(options.buffer().deleted) ||
    Boolean(commitDeleted()) ||
    (!filesBufferHasCurrentFile(options.buffer()) &&
      (scopeChangeKindFor(options.projectId(), options.buffer().rootId, options.buffer().path) ===
        "deleted" ||
        scopeEffectFor(options.projectId(), options.buffer().rootId, options.buffer().path)?.tip
          .state === "absent" ||
        (options.versionHistory().status === "ready" &&
          options.versionHistory().current?.state === "absent")));

  const currentAbsent = () => !historicalActive() && currentTipAbsent();

  const activeReaderHandle = () =>
    historicalActive()
      ? versionHandle()
      : currentAbsent()
        ? deletionHandle()
        : undefined;

  createEffect(() => {
    options.onReaderHandle()?.(activeReaderHandle());
    onCleanup(() => options.onReaderHandle()?.(undefined));
  });

  const [condensedContext, setCondensedContext] = createSignal(false);
  const dropCondenseRequest = () => {
    if (untrack(() => options.buffer().condenseContext)) {
      clearFilesBufferCondense(options.projectId(), untrack(() => options.buffer().key));
    }
  };
  createEffect(() => {
    if (currentAbsent() || options.buffer().loadError) {
      dropCondenseRequest();
    }
  });

  /** Review opens fold unchanged runs in the displayed comparison. */
  const condenseIfRequested = (v: EditorView) => {
    if (!untrack(() => options.buffer().condenseContext)) return;
    const original = editorScopeDiffOriginal(v.state);
    if (original == null) return;
    setCondensedContext(foldCondensedContext(v));
    clearFilesBufferCondense(options.projectId(), untrack(() => options.buffer().key));
  };

  const expandCondensedContext = () => {
    const v = options.editorView();
    if (v) {
      unfoldAll(v);
      v.requestMeasure();
    }
    setCondensedContext(false);
  };

  const lastDeletionState = createLastDeletion({
    absent: currentAbsent,
    buffer: options.buffer,
    client: options.client,
    projectId: options.projectId,
    sessionId: options.sessionId,
    committedHead: () => commitDeleted()?.head ?? null,
  });
  const deletionIdentity = (deletion: ReturnType<typeof lastDeletionState>) =>
    deletion.kind !== "deleted"
      ? deletion.kind
      : JSON.stringify([
          deletion.version.source,
          deletion.version.ts,
          deletion.version.beforeAvailability,
          deletion.version.secretScreen,
          deletion.restore,
        ]);
  const lastDeletion = createMemo(lastDeletionState, undefined, {
    equals: (a, b) => deletionIdentity(a) === deletionIdentity(b),
  });

  const deletionRestore = () => {
    const deletion = lastDeletion();
    const restore = deletion.kind === "deleted" ? deletion.restore : null;
    if (restore?.kind === "version") return options.onRestoreDeleted() ? restore : null;
    if (restore?.kind === "commit") return options.onRevertFile() ? restore : null;
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

  const versionComparison = (): DenEditorVersionComparison => {
    const ver = options.version();
    return ver ? shownVersionComparison(options.projectId(), options.buffer().key, ver) : "current";
  };

  const historyPending = () => historicalActive() && readyVersion() !== options.version();

  return {
    historicalActive,
    editorScopeRevision,
    deletedLines,
    versionHandle,
    setVersionHandle,
    readyVersion,
    setReadyVersion,
    versionDisplayed,
    setVersionDisplayed,
    deletionHandle,
    setDeletionHandle,
    deletionReady,
    setDeletionReady,
    activeReaderHandle,
    condensedContext,
    dropCondenseRequest,
    condenseIfRequested,
    expandCondensedContext,
    comparisonPreview,
    currentTipAbsent,
    currentAbsent,
    lastDeletion,
    deletionRestore,
    deletionVersion,
    versionComparison,
    historyPending,
  };
}
