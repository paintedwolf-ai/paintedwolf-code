import type { ProjectTrustReview, TrustFileChange } from "../../api/types.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { openFilesBuffer } from "../../files/documents/project-files-buffers.ts";
import { fileVersionFromSnapshot, type FileVersionView } from "../../files/history/file-version.ts";

export function showTrustReview(projectId: string): void {
  openFilesSurface({ kind: "trust-review", projectId });
}

export function trustChangeVersion(change: TrustFileChange): FileVersionView {
  return {
    ...fileVersionFromSnapshot({ rootId: change.root_id, path: change.path,
      before: change.kind === "added" ? null : change.before,
      after: change.kind === "removed" ? null : change.after }),
    initialComparison: "before",
  };
}

export function openTrustChange(review: ProjectTrustReview, change: TrustFileChange): void {
  openFilesBuffer(review.project_id, {
    kind: "diff", rootId: change.root_id, rootLabel: change.root_label,
    path: change.path, jobId: `trust:${review.id}:${change.id}`, intent: "permanent",
    diffPreview: { version: trustChangeVersion(change) },
  });
}
