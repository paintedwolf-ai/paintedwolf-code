import { filesBufferText } from "../documents/project-files-buffers.ts";
import { createSignal } from "solid-js";
import { normalizeEolForEditor } from "../../components/source/editor/eol.ts";
import { isSourceWriteConflict, sourceSaveErrorMessage } from "../../components/source/editor/source-editor-model.ts";
import type { BufferMergeModel } from "../documents/buffer-merge.ts";
import { resolveEditorConflict, editorReplica } from "../documents/editor-document.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { applyFilesBufferSaved } from "../documents/project-files-buffers.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import { dropFilesEditorHandlers } from "../editor/files-editor-host.ts";
import type { FilesScope } from "../components/files-scope.ts";

export function createFileMergeCommands({ projectId, client, sourceSessionId, state, refreshScopeMarks,
  setSaveError }: Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "state" | "refreshScopeMarks" | "setSaveError">) {
  const [mergeModel, setMergeModel] = createSignal<BufferMergeModel | null>(
    null,
  );
  const [mergeNote, setMergeNote] = createSignal<string | null>(null);
  const [mergeApplying, setMergeApplying] = createSignal(false);
  const openMergeForBuffer = async (key: FileBufferKey) => {
    flushFilesDraftSyncFor(projectId(), key);
    const buf = state().byKey[key];
    const c = client();
    if (!buf || !c) return;
    try {
      const replica = editorReplica(buf.documentId);
      await replica?.flush();
      await editorReplica(buf.documentId)?.synchronize();
      const reviewedRevision = editorReplica(buf.documentId)?.accepted.revision;
      if (reviewedRevision == null) throw new Error("The editor document is unavailable.");
      const res = await c.getProjectSource(projectId(), buf.path, {
        workerId: buf.jobId,
        rootId: buf.rootId || undefined,
        sessionId: sourceSessionId(),
      });
      const { text } = normalizeEolForEditor(res.content);
      setMergeNote(null);
      setMergeModel({
        bufferKey: key,
        mine: editorReplica(buf.documentId)?.text.toString() ?? filesBufferText(buf),
        theirs: text,
        comparison: "disk",
        theirsSha256: res.sha256?.trim() || "",
        documentRevision: reviewedRevision,
      });
    } catch {
      setSaveError("Could not load the on-disk version for merge.");
    }
  };

  const applyMerge = async (payload: {
    bufferKey: string;
    content: string;
    baseSha256: string;
    documentRevision: number;
  }) => {
    const c = client();
    const key = payload.bufferKey;
    const buf = state().byKey[key];
    if (!c || !buf?.encoding || !buf.documentId || buf.documentRevision == null) {
      setSaveError("The file moved or closed before the merge was applied.");
      return;
    }
    flushFilesDraftSyncFor(projectId(), key);
    const eol = buf.eol;
    const editRevision = buf.editRevision;
    setMergeApplying(true);
    setMergeNote(null);
    try {
      const res = await resolveEditorConflict(projectId(), buf, payload.content, payload.baseSha256, payload.documentRevision);
      applyFilesBufferSaved(projectId(), key, {
        content: payload.content,
        sha256: res.base_sha256,
        sizeBytes: res.size_bytes,
        eol,
        editRevision,
        fileId: res.file_id,
        documentId: res.id,
        documentRevision: res.revision,
      });
      void refreshScopeMarks();
      setMergeModel(null);
      dropFilesEditorHandlers(projectId(), key);
    } catch (err: unknown) {
      if (isSourceWriteConflict(err)) {
        const res = await c.getProjectSource(projectId(), buf.path, {
          workerId: buf.jobId,
          rootId: buf.rootId || undefined,
          sessionId: sourceSessionId(),
        });
        const { text } = normalizeEolForEditor(res.content);
        const replica = editorReplica(buf.documentId);
        await replica?.synchronize();
        const current = replica?.accepted;
        if (!current || !replica) throw new Error("The editor document is unavailable.");
        const documentMoved = current.revision !== payload.documentRevision;
        setMergeModel({
          bufferKey: key,
          mine: documentMoved ? replica.acceptedText : payload.content,
          theirs: documentMoved ? payload.content : text,
          comparison: documentMoved ? "retained" : "disk",
          theirsSha256: res.sha256?.trim() || "",
          documentRevision: current.revision,
        });
        setMergeNote(documentMoved
          ? "The document changed again. Review current edits against your retained merge."
          : "Disk changed again. Review the updated merge.");
      } else {
        setSaveError(sourceSaveErrorMessage(err));
      }
    } finally {
      setMergeApplying(false);
    }
  };

  return { mergeModel, setMergeModel, mergeNote, setMergeNote, mergeApplying, openMergeForBuffer, applyMerge };
}
