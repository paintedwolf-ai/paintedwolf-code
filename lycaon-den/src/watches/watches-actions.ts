import type { LycaonClient } from "../api/client.ts";
import type {
  AgentWatch,
  CancelWatchResponse,
  CreateStandingWatchRequest,
  KeepWatchRequest,
  KeepWatchResponse,
  StandingWatch,
  UpdateStandingWatchRequest,
  WatchRun,
} from "../api/types.ts";
import {
  refreshProjectWatches,
  refreshSessionWatches,
  removeStandingWatchFromStore,
  sessionWatchesFor,
  upsertAgentWatchInStore,
  upsertStandingWatchInStore,
} from "./watches-store.ts";

export async function keepSessionWatchAction(
  client: LycaonClient,
  sessionId: string,
  handle: string,
  req: KeepWatchRequest,
): Promise<KeepWatchResponse> {
  const res = await client.keepWatch(sessionId, handle, req);
  upsertStandingWatchInStore(res.standing_watch);
  const existing = sessionWatchesFor(sessionId).byHandle.get(handle);
  if (existing) {
    upsertAgentWatchInStore(
      {
        ...existing,
        state: "terminal",
        terminal_outcome: "kept",
        final_receipt: res.receipt,
        revision: existing.revision + 1,
      },
      res.receipt,
    );
  } else {
    void refreshSessionWatches(client, sessionId);
  }
  return res;
}

export async function cancelSessionWatchAction(
  client: LycaonClient,
  sessionId: string,
  handle: string,
): Promise<CancelWatchResponse> {
  const res = await client.cancelWatch(sessionId, handle);
  upsertAgentWatchInStore(res.watch, res.receipt);
  return res;
}

export async function setSessionWatchDeliveryAction(
  client: LycaonClient,
  sessionId: string,
  handle: string,
  delivery: "collect" | "wake",
): Promise<AgentWatch> {
  const updated = await client.updateWatchDelivery(sessionId, handle, {
    delivery,
  });
  upsertAgentWatchInStore(updated);
  return updated;
}

export async function inspectSessionWatchAction(
  client: LycaonClient,
  sessionId: string,
  handle: string,
): Promise<AgentWatch> {
  const fetched = await client.getWatch(sessionId, handle);
  upsertAgentWatchInStore(fetched, fetched.final_receipt);
  return fetched;
}

export async function createProjectStandingWatchAction(
  client: LycaonClient,
  projectId: string,
  req: CreateStandingWatchRequest,
): Promise<StandingWatch> {
  const created = await client.createStandingWatch(projectId, req);
  upsertStandingWatchInStore(created);
  return created;
}

export async function updateProjectStandingWatchAction(
  client: LycaonClient,
  projectId: string,
  watchId: string,
  req: UpdateStandingWatchRequest,
): Promise<StandingWatch> {
  const updated = await client.updateStandingWatch(projectId, watchId, req);
  upsertStandingWatchInStore(updated);
  return updated;
}

export async function pauseProjectStandingWatchAction(
  client: LycaonClient,
  projectId: string,
  watchId: string,
  revision: number,
): Promise<StandingWatch> {
  const paused = await client.pauseStandingWatch(projectId, watchId, {
    revision,
  });
  upsertStandingWatchInStore(paused);
  return paused;
}

export async function resumeProjectStandingWatchAction(
  client: LycaonClient,
  projectId: string,
  watchId: string,
  revision: number,
): Promise<StandingWatch> {
  const resumed = await client.resumeStandingWatch(projectId, watchId, {
    revision,
  });
  upsertStandingWatchInStore(resumed);
  return resumed;
}

export async function runProjectStandingWatchNowAction(
  client: LycaonClient,
  projectId: string,
  watchId: string,
): Promise<WatchRun> {
  const run = await client.runStandingWatchNow(projectId, watchId);
  void refreshProjectWatches(client, projectId);
  return run;
}

export async function deleteProjectStandingWatchAction(
  client: LycaonClient,
  projectId: string,
  watchId: string,
): Promise<void> {
  await client.deleteStandingWatch(projectId, watchId);
  removeStandingWatchFromStore(projectId, watchId);
}
