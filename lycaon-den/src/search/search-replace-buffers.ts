import type { LycaonClient } from "../api/client.ts";
import { applyFilesBufferRefresh, filesBufferKeyAtAddress } from "../files/documents/project-files-buffers.ts";
import { filesBufferNeedsBody, projectFilesState } from "../files/documents/files-buffer-state.ts";

/** Refresh admitted clean buffers after replace-apply writes. */
export async function refreshBuffersAfterReplace(
  client: LycaonClient | null,
  projectId: string,
  applied: readonly { root_id: string; path: string }[],
): Promise<void> {
  if (!client || !projectId.trim() || applied.length === 0) return;
  const state = projectFilesState(projectId);
  for (const file of applied) {
    const key = filesBufferKeyAtAddress(
      projectId,
      file.root_id,
      file.path,
    );
    if (!key) continue;
    const buf = state.byKey[key];
    if (!buf || buf.dirty || filesBufferNeedsBody(buf)) continue;
    const revision = buf.editRevision;
    try {
      const res = await client.getProjectSource(projectId, file.path, {
        rootId: file.root_id,
      });
      applyFilesBufferRefresh(projectId, key, buf, revision, res);
    } catch {
      // Leave the existing buffer unchanged.
    }
  }
}
