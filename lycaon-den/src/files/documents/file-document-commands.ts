import { createSignal } from "solid-js";
import { canEditLoadedSource, sourceSaveErrorMessage } from "../../components/source/editor/source-editor-model.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { SourceEncoding } from "../../api/types.ts";
import { discardEditorDocument, editorReplica, publishEditorDocumentDraft } from "./editor-document.ts";
import { recordInventoryOpen } from "../tree/file-inventory.ts";
import { LatestOperation } from "./files-buffer-request-tracking.ts";
import { flushFilesDraftSyncFor } from "./files-draft-sync.ts";
import { filesBufferText, type FileBuffer } from "./files-buffer-state.ts";
import { activeProjectWorktreeIds } from "../source/files-checkout-scope.ts";
import { saveProjectFileDocument } from "./project-file-document-save.ts";
import { applyFilesBufferEditorDocument, discardFilesBufferDraft, markFilesBufferLoading, setFilesBufferEol } from "./project-files-buffer-io.ts";
import { closeFilesBuffer, openFilesBuffer } from "./project-files-buffer-lifecycle.ts";
import { setFilesActiveBuffer } from "./project-files-buffers.ts";
import type { EolKind } from "../../components/source/editor/eol.ts";
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
    (Boolean(buf.checkoutUnavailable && buf.documentId) || sourceEditable(buf)) &&
    editorReplica(buf.documentId) != null;
  const saveable = (buf: FileBuffer) =>
    versionForBuffer(buf) == null &&
    !buf.checkoutUnavailable &&
    sourceEditable(buf) &&
    !!buf.documentId &&
    !buf.diverged;
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

  /** Saves an unavailable checkout's retained draft into the currently inspected live checkout. */
  const saveToActiveCheckout = async (
    key: FileBufferKey,
    destination?: { rootId?: string; path?: string; worktreeId?: string },
  ): Promise<FileBufferKey | null> => {
    flushFilesDraftSyncFor(projectId(), key);
    const buf = state().byKey[key];
    const c = client();
    if (!buf || !c || saving()) return null;
    const targetRootId = destination?.rootId?.trim() || buf.rootId;
    const targetPath = destination?.path?.trim() || buf.path;
    const targetWorktreeId = destination?.worktreeId?.trim() || activeProjectWorktreeIds(projectId())?.[0];
    const text = filesBufferText(buf);
    setSaving(true);
    setSaveError(null);
    try {
      let baseSha256 = "";
      try {
        const existing = await c.getProjectSource(projectId(), targetPath, {
          rootId: targetRootId,
          sessionId: sourceSessionId(),
          ...(targetWorktreeId ? { worktreeId: targetWorktreeId } : {}),
        });
        baseSha256 = existing.sha256 ?? "";
      } catch (readErr) {
        if (readErr instanceof LycaonApiError && readErr.code === "source_not_found") {
          const createReq = { operation_id: crypto.randomUUID(), path: targetPath, kind: "file" as const, root_id: targetRootId };
          if (targetWorktreeId) await c.createProjectSourceEntry(projectId(), createReq, sourceSessionId(), targetWorktreeId);
          else await c.createProjectSourceEntry(projectId(), createReq, sourceSessionId());
          baseSha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";
        } else {
          throw readErr;
        }
      }
      const encoding: SourceEncoding = buf.encoding ?? "utf-8";
      const req = {
        operation_id: crypto.randomUUID(),
        root_id: targetRootId,
        path: targetPath,
        content: text,
        encoding,
        base_sha256: baseSha256,
      };
      const written = targetWorktreeId
        ? await c.replaceProjectSource(projectId(), req, sourceSessionId(), targetWorktreeId)
        : await c.replaceProjectSource(projectId(), req, sourceSessionId());
      void refreshScopeMarks();
      const nextKey = openFilesBuffer(projectId(), {
        rootId: targetRootId,
        rootLabel: buf.rootLabel,
        path: written.path || targetPath,
        intent: "permanent",
        ...(targetWorktreeId ? { worktreeId: targetWorktreeId } : { branchId: state().rootBranches[targetRootId] }),
      });
      void closeFilesBuffer(projectId(), key, { discardDraft: true });
      return nextKey;
    } catch (error) {
      setSaveError(sourceSaveErrorMessage(error));
      return null;
    } finally {
      setSaving(false);
    }
  };

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
    saveToActiveCheckout,
    discardBuffer,
    reloadBuffer,
    openCurrentProjectFile,
    makeBufferEditable,
    chooseLineEndings,
    toggleLineEndings,
    clearSavedFlashTimer,
  };
}
