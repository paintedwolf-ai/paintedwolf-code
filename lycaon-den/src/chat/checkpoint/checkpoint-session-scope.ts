import type { LycaonClient } from "../../api/client.ts";
import type { WorkerTask } from "../../api/types.ts";
import { workersForSession } from "../worker/workers-model.ts";
import {
  pendingFromEvent,
  type PendingCheckpoint,
} from "./checkpoint-model.ts";

export function childSessionIdsForParent(
  workers: readonly WorkerTask[],
  parentSessionId: string,
): string[] {
  const parent = parentSessionId.trim();
  if (!parent) return [];
  const ids = new Set<string>();
  for (const worker of workersForSession(workers, parent)) {
    const child = worker.child_session_id?.trim();
    if (child) ids.add(child);
  }
  return [...ids];
}

/** True when a checkpoint belongs to the coordinator session or its worker children. */
export function checkpointAppliesToSessionView(
  checkpointSessionId: string,
  parentSessionId: string | undefined,
  workers: readonly WorkerTask[],
): boolean {
  const eventSid = checkpointSessionId.trim();
  if (!eventSid) return false;
  const parent = parentSessionId?.trim();
  if (!parent) return true;
  if (eventSid === parent) return true;
  return childSessionIdsForParent(workers, parent).includes(eventSid);
}

export function pendingCheckpointsForSessionView(
  pending: readonly PendingCheckpoint[],
  sessionId: string,
  workers: readonly WorkerTask[],
  opts?: { exact?: boolean },
): PendingCheckpoint[] {
  const sid = sessionId.trim();
  if (!sid) return [...pending];
  if (opts?.exact) {
    return pending.filter((cp) => cp.sessionId.trim() === sid);
  }
  return pending.filter((cp) =>
    checkpointAppliesToSessionView(cp.sessionId, sid, workers),
  );
}

export async function loadPendingCheckpointsForSessionView(
  client: LycaonClient,
  parentSessionId: string,
): Promise<PendingCheckpoint[]> {
  const parent = parentSessionId.trim();
  if (!parent) return [];
  const rows = await client.listCheckpoints(parent, { status: "pending", includeChildren: true });
  const out: PendingCheckpoint[] = [];
  const seen = new Set<string>();
  for (const row of rows) {
    const cp = pendingFromEvent(row);
    if (cp.status !== "pending" || seen.has(cp.checkpointId)) continue;
    seen.add(cp.checkpointId);
    out.push(cp);
  }
  return out;
}
