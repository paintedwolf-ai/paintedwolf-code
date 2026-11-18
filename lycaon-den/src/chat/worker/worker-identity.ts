import type { WorkerTask } from "../../api/types.ts";

function sameWorkerIdentity(a: WorkerTask, b: WorkerTask): boolean {
  if (a.id === b.id) return true;
  const aRec = a.worker_record_id?.trim();
  const bRec = b.worker_record_id?.trim();
  if (aRec && bRec && aRec === bRec) return true;
  const aChild = a.child_session_id?.trim();
  const bChild = b.child_session_id?.trim();
  if (aChild && bChild && aChild === bChild) return true;
  const aName = a.worker_name?.trim();
  const bName = b.worker_name?.trim();
  if (aName && bName && aName === bName) {
    const aParent = a.parent_session_id?.trim() ?? "";
    const bParent = b.parent_session_id?.trim() ?? "";
    return !aParent || !bParent || aParent === bParent;
  }
  return false;
}

function compareWorkerRounds(a: WorkerTask, b: WorkerTask): number {
  const ar = a.round ?? 0;
  const br = b.round ?? 0;
  if (ar !== br) return ar - br;
  const at = Date.parse(a.created_at);
  const bt = Date.parse(b.created_at);
  if (!Number.isNaN(at) && !Number.isNaN(bt) && at !== bt) return at - bt;
  return a.id.localeCompare(b.id);
}

function mergeWorkerRoundGroup(group: WorkerTask[]): WorkerTask {
  if (group.length === 1) return group[0]!;
  const ordered = [...group].sort(compareWorkerRounds);
  const latest = ordered[ordered.length - 1]!;
  let recordId = latest.worker_record_id?.trim() || undefined;
  let workerName = latest.worker_name?.trim() || undefined;
  let childSessionId = latest.child_session_id?.trim() || undefined;
  let parentSessionId = latest.parent_session_id?.trim() || undefined;
  let projectId = latest.project_id?.trim() || undefined;
  let brief = latest.brief?.trim() || undefined;
  let loopsUsed = latest.tool_loops_used;
  let callsUsed = latest.tool_calls_used;

  for (let i = ordered.length - 2; i >= 0; i--) {
    const row = ordered[i]!;
    if (!recordId && row.worker_record_id?.trim()) recordId = row.worker_record_id.trim();
    if (!workerName && row.worker_name?.trim()) workerName = row.worker_name.trim();
    if (!childSessionId && row.child_session_id?.trim()) childSessionId = row.child_session_id.trim();
    if (!parentSessionId && row.parent_session_id?.trim()) parentSessionId = row.parent_session_id.trim();
    if (!projectId && row.project_id?.trim()) projectId = row.project_id.trim();
    if (!brief && row.brief?.trim()) brief = row.brief.trim();
    if (row.tool_loops_used != null) {
      loopsUsed = Math.max(loopsUsed ?? 0, row.tool_loops_used);
    }
    if (row.tool_calls_used != null) {
      callsUsed = Math.max(callsUsed ?? 0, row.tool_calls_used);
    }
  }

  return {
    ...latest,
    worker_record_id: recordId,
    worker_name: workerName,
    child_session_id: childSessionId,
    parent_session_id: parentSessionId,
    project_id: projectId,
    brief,
    tool_loops_used: loopsUsed,
    tool_calls_used: callsUsed,
  };
}

/** Collapse multi-round job rows into one canonical row per worker. */
export function collapseWorkersByIdentity(
  workers: readonly WorkerTask[],
): WorkerTask[] {
  if (workers.length <= 1) return [...workers];
  const groups: WorkerTask[][] = [];
  for (const row of workers) {
    let matchedIndex = -1;
    for (let i = 0; i < groups.length; i++) {
      if (groups[i]!.some((member) => sameWorkerIdentity(member, row))) {
        if (matchedIndex === -1) {
          groups[i]!.push(row);
          matchedIndex = i;
        } else {
          groups[matchedIndex]!.push(...groups[i]!);
          groups.splice(i, 1);
          i -= 1;
        }
      }
    }
    if (matchedIndex === -1) {
      groups.push([row]);
    }
  }
  return groups.map(mergeWorkerRoundGroup);
}

/** Resolve a canonical worker row by job id, worker record id, name, or child session id. */
export function findSessionWorker(
  workers: readonly WorkerTask[],
  targetId: string | null | undefined,
  allJobRows?: readonly WorkerTask[],
): WorkerTask | undefined {
  const id = targetId?.trim();
  if (!id) return undefined;
  const direct = workers.find(
    (w) =>
      w.id === id ||
      w.worker_record_id?.trim() === id ||
      w.worker_name?.trim() === id ||
      w.child_session_id?.trim() === id,
  );
  if (direct) return direct;
  if (!allJobRows || allJobRows.length === 0) return undefined;
  const historical = allJobRows.find(
    (r) =>
      r.id === id ||
      r.worker_record_id?.trim() === id ||
      r.worker_name?.trim() === id ||
      r.child_session_id?.trim() === id,
  );
  if (!historical) return undefined;
  return workers.find((w) => sameWorkerIdentity(w, historical));
}
