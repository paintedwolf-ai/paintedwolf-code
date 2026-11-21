import { batch } from "solid-js";
import type { OpenSourceRequest } from "../../platform/navigation/open-source.ts";
import { markFilesBufferLoading } from "../documents/project-files-buffer-io.ts";
import { openFilesBuffer } from "../documents/project-files-buffer-lifecycle.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { pushProjectJump } from "./project-files-jump-bridge.ts";
import { setSelectedFileVersion } from "../history/files-version-selection.ts";
import { leaveWalk } from "../walk/walk-store.ts";
import { retargetOpenFilesUnderPath } from "../commands/project-files-retarget.ts";
import { fileBufferKey } from "./project-files-model.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";

/** Walk state outlives the stage, so opening a working file leaves Walk. */
export function openSourceInFilesStage(args: {
  request: OpenSourceRequest;
  rootLabelFor: (rootId: string) => string;
  navigate: () => void;
}): void {
  const { request, rootLabelFor, navigate } = args;
  const projectId = request.projectId.trim();
  if (!projectId) return;
  const jobId = request.jobId?.trim() || undefined;

  const bufferKey = batch(() => {
    leaveWalk(projectId);
    if (request.fileId && !jobId) {
      const key = fileBufferKey(request.rootId, request.path, undefined, request.fileId);
      const current = projectFilesState(projectId).byKey[key];
      if (current && (current.rootId !== request.rootId || current.path !== request.path)) {
        retargetOpenFilesUnderPath(projectId, current.rootId, current.path, request.path, request.rootId);
      }
    }
    const key = openFilesBuffer(projectId, {
      rootId: request.rootId,
      fileId: request.fileId,
      rootLabel: rootLabelFor(request.rootId),
      path: request.path,
      intent: request.intent,
      jobId,
      revealLine: request.line,
      revealColumn: request.column,
      revealEndLine: request.endLine,
      revealFocus: request.focus,
    });
    setSelectedFileVersion(projectId, key, null);
    flushFilesDraftSyncFor(projectId, key);
    // A reopen rechecks the path, which may have been deleted or recreated; a draft is kept.
    const buffer = projectFilesState(projectId).byKey[key];
    if (buffer && !buffer.loading && !buffer.dirty) markFilesBufferLoading(projectId, key);
    return key;
  });

  pushProjectJump(projectId, {
    bufferKey,
    rootId: request.rootId,
    path: request.path,
    ...(jobId ? { jobId } : {}),
    line: request.line ?? 1,
  });
  navigate();
}
