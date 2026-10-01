import type { WorkerTask } from "../../api/types.ts";

function isWriteScoped(worker: WorkerTask): boolean {
  return worker.scope?.mode === "write";
}

/** Pending, applying, and rebasing overlays remain open; terminal merge states close them. */
export function workerOverlayOpen(worker: WorkerTask): boolean {
  switch (worker.merge_status) {
    case "pending":
    case "applying":
    case "rebasing":
      return true;
    case "merged":
    case "rejected":
    case "orphaned":
    case "aborted":
      return false;
  }
  // Write worker finished before merge_status arrives on SSE.
  return isWriteScoped(worker) && worker.status === "complete";
}

/** Parent chat only receives diffs after the overlay has permanently landed. */
export function workerShowsAcceptedBranchEdits(worker: WorkerTask): boolean {
  return worker.merge_status === "merged";
}

/** Short branch line for task cards and activity rows (no git-merge wording). */
export function workerBranchActivityLine(worker: WorkerTask): string | null {
  switch (worker.merge_status) {
    case "applying":
      return "Landing changes on primary…";
    case "merged":
      return "Landed on primary";
    case "rejected":
      return "Branch closed — changes not applied";
    case "orphaned":
      return "Branch disconnected — parent was closed";
    case "rebasing":
      return "Updating branch after parent landed";
    case "aborted":
      return "Branch discarded";
    case "pending": {
      const paths = worker.result?.change_report?.changed_paths ?? [];
      if (paths.length > 0) {
        const head = paths[0]!;
        const more = paths.length > 1 ? ` (+${paths.length - 1} more)` : "";
        return `Open on branch · ${head}${more}`;
      }
      return "Open on branch — awaiting promote";
    }
  }
  if (workerOverlayOpen(worker)) {
    return "Open on branch — awaiting promote";
  }
  return null;
}
