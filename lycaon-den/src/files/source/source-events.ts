import { invalidateSourceQueries } from "./source-invalidation.ts";
import type { SourceChangesEvent } from "../../api/types.ts";
import { noteReviewLanding } from "../review/review-live.ts";
import { applyBufferSourceChanged, applyBufferSourceResync } from "../documents/buffer-live.ts";
import { markFileBriefingSourceChanged } from "../components/file-briefing-live.ts";
import { requestSourceRefresh } from "./source-refresh.ts";
import { applySourceTreeChanges, requestSourceTreeResync } from "../tree/source-tree-store.ts";
import { requestWalkRefresh } from "../walk/walk-store.ts";
import { refreshWalkPreviews } from "../walk/walk-preview.ts";
import { invalidateTurnDiffs } from "../review/turn-diffs.ts";

/** Routes changes by workspace kind. */
export function applySourceChangesEvent(event: SourceChangesEvent): void {
  const pid = event.project_id.trim();
  if (!pid) return;
  const projectWorkspace = event.workspace_kind === "project";
  if (projectWorkspace) {
    applySourceTreeChanges(event);
    invalidateSourceQueries({ projectId: pid, workspaceId: event.workspace_id });
  }
  if (projectWorkspace && event.resync) applyBufferSourceResync(pid);
  for (const change of event.changes) {
    // Branch and address select the matching buffer.
    if (!projectWorkspace || !event.resync) {
      applyBufferSourceChanged(pid, event.workspace_kind, change);
    }
    if (!projectWorkspace) continue;
    markFileBriefingSourceChanged(pid, change);
    // A worker's overlay write lands nothing; its promotion does.
    noteReviewLanding(pid, change);
  }
  requestWalkRefresh(pid);
  // A running turn's inventory grows; a settled turn re-reads once.
  invalidateTurnDiffs(pid);
  if (event.changes.length === 0) refreshWalkPreviews(pid);
  for (const sessionId of new Set(event.changes.map((change) => change.session_id?.trim()).filter(Boolean))) {
    refreshWalkPreviews(pid, sessionId);
  }
  if (!projectWorkspace) return;
  requestSourceRefresh(pid, { watchCoverage: event.resync });
}

/** Reconcile every source projection through the same path after missed events. */
export function requestSourceProjectionResync(projectId: string): void {
  const pid = projectId.trim();
  if (!pid) return;
  requestSourceTreeResync(pid);
  applyBufferSourceResync(pid);
  requestSourceRefresh(pid, { watchCoverage: true });
  requestWalkRefresh(pid);
  refreshWalkPreviews(pid);
  invalidateTurnDiffs(pid);
  invalidateSourceQueries({ projectId: pid });
}
