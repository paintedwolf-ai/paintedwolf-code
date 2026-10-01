import type { Project } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import {
  refreshSessionWorkers,
  createBoardRefreshScheduler,
  type BoardRefreshScheduler,
} from "../actions/board-actions.ts";
import { refreshCoordinatorContext } from "../workflow/coordinator-context-actions.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import {
  createCoalescedAsyncScheduler,
  createThrottledAsyncScheduler,
} from "../../store/coalesced-async.ts";

/** SSE projection burst window. */
export const SESSION_PROJECTION_COALESCE_MS = 200;
/** Baseline cost refresh interval. */
const COST_REFRESH_THROTTLE_MS = 5000;
/** Live cost refresh interval. */
const COST_REFRESH_LIVE_MS = 1000;

export type SessionInvalidationSchedulers = {
  scheduleWorkers: (projectDir: string, sessionId: string) => void;
  scheduleBoard: (projectDir: string, sessionId: string, includeGit?: boolean) => void;
  scheduleCost: (refresh: () => Promise<void>) => void;
  scheduleWorkflow: (refresh: () => Promise<void>) => void;
  scheduleProgress: (refresh: () => Promise<void>) => void;
  cancel: () => void;
};

export function createSessionInvalidationSchedulers(
  appStore: AppStore,
  getClient: () => LycaonClient | null,
  projects: () => readonly Project[],
  isCostLive: () => boolean,
): SessionInvalidationSchedulers {
  let workersTarget: { projectDir: string; sessionId: string } | undefined;
  let refreshCost: { run: () => Promise<void>; epoch: number; client: LycaonClient | null } | undefined;
  let refreshWorkflow: (() => Promise<void>) | undefined;
  let refreshProgressProjection: (() => Promise<void>) | undefined;

  const workers = createCoalescedAsyncScheduler(async () => {
    const client = getClient();
    const target = workersTarget;
    if (!client || !target) return;
    await refreshSessionWorkers(
      appStore,
      client,
      target.projectDir,
      projects(),
      target.sessionId,
    );
    await refreshCoordinatorContext(appStore, client, target.sessionId).catch(
      () => undefined,
    );
  }, SESSION_PROJECTION_COALESCE_MS);

  const board: BoardRefreshScheduler = createBoardRefreshScheduler(
    appStore,
    getClient,
    projects,
  );
  const cost = createThrottledAsyncScheduler(
    async () => {
      const target = refreshCost;
      if (!target || target.epoch !== appStore.state.sessionViewEpoch || target.client !== getClient()) return;
      await target.run();
    },
    () => (isCostLive() ? COST_REFRESH_LIVE_MS : COST_REFRESH_THROTTLE_MS),
  );
  const workflow = createCoalescedAsyncScheduler(async () => {
    await refreshWorkflow?.();
  }, SESSION_PROJECTION_COALESCE_MS);
  const progress = createCoalescedAsyncScheduler(async () => {
    await refreshProgressProjection?.();
  }, SESSION_PROJECTION_COALESCE_MS);

  return {
    scheduleWorkers: (projectDir, sessionId) => {
      workersTarget = { projectDir, sessionId };
      workers.schedule();
    },
    scheduleBoard: (projectDir, sessionId, includeGit) => {
      board.scheduleBoard(projectDir, sessionId, includeGit);
    },
    scheduleCost: (refresh) => {
      refreshCost = { run: refresh, epoch: appStore.state.sessionViewEpoch, client: getClient() };
      cost.schedule();
    },
    scheduleWorkflow: (refresh) => {
      refreshWorkflow = refresh;
      workflow.schedule();
    },
    scheduleProgress: (refresh) => {
      refreshProgressProjection = refresh;
      progress.schedule();
    },
    cancel: () => {
      workers.cancel();
      board.cancel();
      cost.cancel();
      workflow.cancel();
      progress.cancel();
      workersTarget = undefined;
      refreshCost = undefined;
      refreshWorkflow = undefined;
      refreshProgressProjection = undefined;
    },
  };
}
