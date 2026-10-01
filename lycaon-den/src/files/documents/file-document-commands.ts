import { createSignal } from "solid-js";
import { canEditLoadedSource, sourceSaveErrorMessage } from "../../components/source/editor/source-editor-model.ts";
import { discardEditorDocument, editorReplica, publishEditorDocumentDraft } from "./editor-document.ts";
import { recordInventoryOpen } from "../tree/file-inventory.ts";
import { LatestOperation } from "./files-buffer-request-tracking.ts";
import { flushFilesDraftSyncFor } from "./files-draft-sync.ts";
import { saveProjectFileDocument } from "./project-file-document-save.ts";
import {
  applyFilesBufferEditorDocument,
  discardFilesBufferDraft,
  markFilesBufferLoading,
  openFilesBuffer,
  setFilesActiveBuffer,
  setFilesBufferEol,
} from "./project-files-buffers.ts";
import type { EolKind } from "../../components/source/editor/eol.ts";
import type { FileBuffer } from "./files-buffer-state.ts";
import { pushProjectJump } from "../components/project-files-jump-bridge.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import { dropFilesEditorHandlers } from "../editor/files-editor-host.ts";
import type { FilesScope } from "../components/files-scope.ts";

export function createFileDocumentCommands({ projectId, client, sourceSessionId, state, versionForBuffer,
  refreshScopeMarks, setSaveError }: Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "state" |
  "versionForBuffer" | "refreshScopeMarks" | "setSaveError">) {
  let savedFlashTimer: ReturnType<typeof setTimeout> | undefined;
  const [saving, setSaving] = createSignal(false);
  const [changingEditorState, setChangingEditorState] = createSignal(false);
  const editorStateChanges = new LatestOperation();
  const beginEditorStateChange = () => {
    const generation = editorStateChanges.begin();
    setChangingEditorState(true);
    return {
      isCurrent: () => editorStateChanges.isCurrent(generation),
      finish: () => {
        if (!editorStateChanges.isCurrent(generation)) return;
        editorStateChanges.finish(generation);
        setChangingEditorState(false);
      },
    };
  };
  const resetEditorStateChange = () => {
    editorStateChanges.invalidate();
    setChangingEditorState(false);
  };
  const [savedFlash, setSavedFlash] = createSignal(false);
  const sourceEditable = (buf: FileBuffer) =>
    canEditLoadedSource({
      kind: buf.kind,
      overLimit: buf.overLimit,
      sha256: buf.baseSha256,
      encoding: buf.encoding,
      jobId: buf.jobId,
      writable: buf.writable,
    });
  const editable = (buf: FileBuffer) =>
    versionForBuffer(buf) == null &&
    !buf.loading && !buf.loadError && !buf.editorResolving && !buf.editorEditingBlock &&
    !buf.editorOpening &&
    sourceEditable(buf) &&
    editorReplica(buf.documentId) != null;
  const saveable = (buf: FileBuffer) => versionForBuffer(buf) == null && sourceEditable(buf) && !!buf.documentId && !buf.diverged;
  const performSave = async (key: FileBufferKey): Promise<boolean> => {
    flushFilesDraftSyncFor(projectId(), key);
    const buf = state().byKey[key];
    const c = client();
    if (!buf || !c || !buf.dirty || buf.diverged || saving()) return false;
    if (!saveable(buf)) return false;
    setSaving(true);
    setSaveError(null);
    const result = await saveProjectFileDocument({
      client: c,
      projectId: projectId(),
      key,
      sessionId: sourceSessionId(),
    });
    setSaving(false);
    if (result.status === "saved") {
      void refreshScopeMarks();
      setSavedFlash(true);
      if (savedFlashTimer) clearTimeout(savedFlashTimer);
      savedFlashTimer = setTimeout(() => setSavedFlash(false), 1600);
      return true;
    }
    if (result.status === "clean") return true;
    setSaveError(result.message);
    return false;
  };

  const discardBuffer = async (key: FileBufferKey) => {
    const buffer = state().byKey[key];
    if (buffer?.documentId) {
      const document = await discardEditorDocument(projectId(), buffer);
      if (document) applyFilesBufferEditorDocument(projectId(), key, document);
      return;
    }
    discardFilesBufferDraft(projectId(), key);
  };
  const reloadBuffer = (key: FileBufferKey) => {
    dropFilesEditorHandlers(projectId(), key);
    markFilesBufferLoading(projectId(), key);
  };

  const openCurrentProjectFile = (buffer: FileBuffer) => {
    const current = state().order
      .map((key) => state().byKey[key])
      .find((candidate) =>
        candidate != null &&
        !candidate.jobId &&
        candidate.rootId === buffer.rootId &&
        candidate.path === buffer.path
      );
    if (current) {
      setFilesActiveBuffer(projectId(), current.key);
      return;
    }
    const key = openFilesBuffer(projectId(), {
      rootId: buffer.rootId,
      rootLabel: buffer.rootLabel,
      path: buffer.path,
      intent: "permanent",
    });
    recordInventoryOpen(projectId(), buffer);
    pushProjectJump(projectId(), {
      bufferKey: key,
      rootId: buffer.rootId,
      path: buffer.path,
      ...(buffer.jobId ? { jobId: buffer.jobId } : {}),
      line: 1,
    });
  };

  const makeBufferEditable = (buffer: FileBuffer) => {
    const c = client();
    if (!c || !buffer.baseSha256 || !buffer.rootId) return;
    const change = beginEditorStateChange();
    setSaveError(null);
    void c.makeProjectSourceEditable(
      projectId(),
      {
        path: buffer.path,
        root_id: buffer.rootId,
        base_sha256: buffer.baseSha256,
      },
      sourceSessionId(),
    ).then(
      () => reloadBuffer(buffer.key),
      (err) => {
        if (change.isCurrent()) setSaveError(sourceSaveErrorMessage(err));
      },
    ).finally(change.finish);
  };

  /** Line endings belong to an editable working document; other tabs have none to change. */
  const chooseLineEndings = (buf: FileBuffer, eol: EolKind) => {
    if (!editable(buf)) return;
    setFilesBufferEol(projectId(), buf.key, eol);
    const next = state().byKey[buf.key];
    if (next) void publishEditorDocumentDraft(projectId(), next);
  };
  const toggleLineEndings = (buf: FileBuffer) => chooseLineEndings(buf, buf.eol === "crlf" ? "lf" : "crlf");

  const clearSavedFlashTimer = () => { if (savedFlashTimer) clearTimeout(savedFlashTimer); };
  return {
    saving,
    setSaving,
    savedFlash,
    setSavedFlash,
    changingEditorState,
    resetEditorStateChange,
    editable,
    saveable,
    performSave,
    discardBuffer,
    reloadBuffer,
    openCurrentProjectFile,
    makeBufferEditable,
    chooseLineEndings,
    toggleLineEndings,
    clearSavedFlashTimer,
  };
}
