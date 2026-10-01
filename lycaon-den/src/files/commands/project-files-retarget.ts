/** Retarget every open-file address in one reactive update. */

import { batch } from "solid-js";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { rekeyFilesEditorHostsUnderPath } from "../editor/files-editor-host.ts";
import { retargetFilesBufferStoreUnderPath } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { rekeySessionFidelityUnderPath } from "../editor/editor-session-fidelity.ts";
import { retargetProjectJumpHistory } from "../components/project-files-jump-bridge.ts";
import { retargetPathUnderPath } from "../components/project-files-model.ts";

export function retargetOpenFilesUnderPath(
  projectId: string,
  rootId: string,
  fromPath: string,
  toPath: string,
): void {
  const state = projectFilesState(projectId);
  for (const key of state.order) {
    const buffer = state.byKey[key];
    if (
      buffer?.rootId === rootId &&
      !buffer.jobId &&
      retargetPathUnderPath(buffer.path, fromPath, toPath) != null
    ) {
      flushFilesDraftSyncFor(projectId, key);
    }
  }
  batch(() => {
    rekeyFilesEditorHostsUnderPath(projectId, rootId, fromPath, toPath);
    rekeySessionFidelityUnderPath(rootId, fromPath, toPath);
    retargetFilesBufferStoreUnderPath(projectId, rootId, fromPath, toPath);
    retargetProjectJumpHistory(projectId, rootId, fromPath, toPath);
  });
}
