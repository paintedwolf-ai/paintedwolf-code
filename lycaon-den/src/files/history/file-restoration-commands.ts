import type { SourceFileVersion } from "../../api/types.ts";
import { focusedRestoreHunk } from "../../components/source/annotations/line-gutter.ts";
import { editorScopeDiffOriginal } from "../../components/source/diff/scope-diff.ts";
import type { FileHistoryCommit } from "../history/file-history-model.ts";
import { applyFileUndos, type FileMutationOutcome, type FileUndo } from "../commands/file-mutations.ts";
import { rejectHunkInFile, revertFileToBaseline } from "../history/file-revert.ts";
import { showFileUndoToast } from "../history/file-undo-toast.ts";
import { listedRestoreDisabledReason, restoreListedCommit, restoreRetainedVersion, versionRestoreDisabledReason } from "../history/file-version-restore.ts";
import { type FileVersionView } from "../history/file-version.ts";
import { getFilesEditorView } from "../editor/files-editor-host.ts";
import type { DiffLineHunk } from "../review/line-diff.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import { scopeBaseline, scopeComparisonTarget, scopeEffectFor } from "../tree/scope-resolution.ts";
import type { FileVersionHistory } from "./file-version.ts";
import type { FilesScope } from "../components/files-scope.ts";

type RestorationDependencies = Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "activeBuffer" |
  "setSaveError" | "reloadBuffer" | "refreshScopeMarks"> & {
  versionHistoryForBuffer: (buffer: FileBuffer) => FileVersionHistory;
  restoringVersion: () => boolean;
  setRestoringVersion: (value: boolean) => void;
  returnToCurrent: (key: FileBufferKey) => void;
};

export function createFileRestoration({ projectId, client, sourceSessionId, activeBuffer,
  versionHistoryForBuffer, restoringVersion, setRestoringVersion, setSaveError,
  returnToCurrent, reloadBuffer, refreshScopeMarks }: RestorationDependencies) {
  const runFileRevert = async (rootId: string, path: string) => {
    const c = client();
    if (!c) return;
    const baseline = scopeBaseline(projectId());
    if (!baseline) {
      setSaveError("Choose a comparison to revert against.");
      return;
    }
    // Revert uses the file’s selected comparison baseline.
    const change = scopeEffectFor(projectId(), rootId, path);
    const res = await revertFileToBaseline(c, projectId(), {
      fileId: change?.fileId ?? "",
      rootId,
      path,
      baseline,
      comparisonTarget: scopeComparisonTarget(projectId(), rootId, path),
      tip: change?.tip ?? null,
      sessionId: sourceSessionId(),
    });
    if (!res.ok) {
      setSaveError(res.conflict ? "Disk changed — review again." : res.error);
      return;
    }
    showFileUndoToast({
      projectId: projectId(),
      label: res.label,
      undo: [res.undo],
    });
    void refreshScopeMarks();
  };

  const settleVersionRestore = (
    buffer: FileBuffer,
    result: FileMutationOutcome,
  ) => {
    setRestoringVersion(false);
    if (!result.ok) {
      setSaveError(result.conflict ? "Disk changed — reload before restoring a version." : result.error);
      return;
    }
    showFileUndoToast({
      projectId: projectId(),
      label: result.label,
      undo: [result.undo],
    });
    returnToCurrent(buffer.key);
    reloadBuffer(buffer.key);
    void refreshScopeMarks();
  };

  const runSelectedVersionRestore = async (
    buffer: FileBuffer,
    version: FileVersionView,
  ) => {
    const c = client();
    const history = versionHistoryForBuffer(buffer);
    if (!c || versionRestoreDisabledReason({
      buffer,
      history,
      version,
      restoring: restoringVersion(),
    })) return;
    setRestoringVersion(true);
    setSaveError(null);
    // Commits use object bytes; retained versions use the ledger.
    const result = version.commit
      ? await restoreListedCommit({
          client: c,
          projectId: projectId(),
          sessionId: sourceSessionId(),
          buffer,
          history,
          entry: { commit: version.commit, previousBlobOid: null },
        })
      : await restoreRetainedVersion({
          client: c,
          projectId: projectId(),
          sessionId: sourceSessionId(),
          buffer,
          history,
          versionId: version.versionId,
        });
    settleVersionRestore(buffer, result);
  };

  const runListedRestore = async (
    buffer: FileBuffer,
    row:
      | { kind: "version"; version: SourceFileVersion }
      | { kind: "commit"; entry: FileHistoryCommit },
  ) => {
    const c = client();
    const history = versionHistoryForBuffer(buffer);
    if (!c || listedRestoreDisabledReason({
      buffer,
      history,
      restoring: restoringVersion(),
      row,
    })) return;
    setRestoringVersion(true);
    setSaveError(null);
    const result = row.kind === "version"
      ? await restoreRetainedVersion({
          client: c,
          projectId: projectId(),
          sessionId: sourceSessionId(),
          buffer,
          history,
          versionId: row.version.id,
        })
      : await restoreListedCommit({
          client: c,
          projectId: projectId(),
          sessionId: sourceSessionId(),
          buffer,
          history,
          entry: row.entry,
        });
    settleVersionRestore(buffer, result);
  };

  /** Brings back the retained state a deletion removed. */
  const runDeletedFileRestore = async (buffer: FileBuffer, versionId: string) => {
    const c = client();
    if (!c || restoringVersion()) return;
    setRestoringVersion(true);
    setSaveError(null);
    const result = await restoreRetainedVersion({
      client: c,
      projectId: projectId(),
      sessionId: sourceSessionId(),
      buffer,
      history: versionHistoryForBuffer(buffer),
      versionId,
    });
    settleVersionRestore(buffer, result);
  };

  const runFileUndo = async (undos: FileUndo[]) => {
    const c = client();
    if (!c) return;
    const res = await applyFileUndos(
      c,
      projectId(),
      undos,
      sourceSessionId(),
    );
    if (!res.ok) {
      setSaveError(res.conflict ? "Disk changed — review again." : res.error);
      return;
    }
    void refreshScopeMarks();
  };

  const runEditorHunkReject = async (hunkArg?: DiffLineHunk) => {
    const c = client();
    const buf = activeBuffer();
    if (!c || !buf || isComposedBufferKind(buf.kind)) return;
    const view = getFilesEditorView(projectId(), buf.key);
    if (!view) return;
    const before = editorScopeDiffOriginal(view.state);
    const hunk = hunkArg ?? focusedRestoreHunk(view);
    if (before == null || !hunk) return;
    const res = await rejectHunkInFile(c, projectId(), {
      rootId: buf.rootId,
      path: buf.path,
      hunk,
      sessionId: sourceSessionId(),
      before,
      after: view.state.doc.toString(),
      encoding: buf.encoding ?? undefined,
      baseSha256: buf.baseSha256 ?? undefined,
      tip: scopeEffectFor(projectId(), buf.rootId, buf.path)?.tip ?? null,
    });
    if (!res.ok) {
      setSaveError(res.conflict ? "Disk changed — review again." : res.error);
      return;
    }
    showFileUndoToast({
      projectId: projectId(),
      label: res.label,
      undo: [res.undo],
    });
    void refreshScopeMarks();
  };

  return { runFileRevert, runSelectedVersionRestore, runListedRestore, runDeletedFileRestore, runFileUndo, runEditorHunkReject };
}
