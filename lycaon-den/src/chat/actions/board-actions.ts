import type { LycaonClient } from "../../api/client.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { valueOf, loading, loadFailed } from "../../store/load-state.ts";
import { loadPendingCheckpointsForSessionView } from "../checkpoint/checkpoint-session-scope.ts";
import { projectIdForPath } from "../../store/app-state.ts";
import { refreshGitStatus } from "./git-status-reads.ts";
import { resolveScope } from "../../components/git-repo-scope.ts";
import { createCoalescedAsyncScheduler } from "../../store/coalesced-async.ts";
import {
  boardHasGitPulse,
  gitStatusFromBoardPulse,
  shouldSkipGitStatusFetch,
} from "./board-git-coalesce.ts";

function resolveProjectId(
  appStore: AppStore,
  projectDir: string,
  projects: readonly Project[],
): string | undefined {
  return (
    projectIdForPath(projects, projectDir) ??
    appStore.state.currentSession?.project_id
  );
}

export const BOARD_REFRESH_COALESCE_MS = 300;

type BoardFlight = {
  client: LycaonClient;
  promise: Promise<void>;
  sessionViewEpoch: number;
  boardEventEpoch: number;
  workerEventEpoch: number;
};

const boardFlights = new WeakMap<AppStore, Map<string, BoardFlight>>();

export type BoardRefreshScheduler = {
  scheduleBoard: (
    projectDir: string,
    sessionId: string,
    includeGit?: boolean,
  ) => void;
  cancel: () => void;
};

export function createBoardRefreshScheduler(
  appStore: AppStore,
  getClient: () => LycaonClient | null,
  projects: () => readonly Project[],
): BoardRefreshScheduler {
  let target:
    | { projectDir: string; sessionId: string; includeGit: boolean; epoch: number }
    | undefined;

  const board = createCoalescedAsyncScheduler(async () => {
    const current = target;
    target = undefined;
    const client = getClient();
    if (!client || !current || current.epoch !== appStore.state.sessionViewEpoch) return;
    await refreshBoard(
      appStore,
      client,
      current.projectDir,
      projects(),
      current.sessionId,
      { includeGit: current.includeGit },
    );
  }, BOARD_REFRESH_COALESCE_MS);

  return {
    scheduleBoard: (projectDir, sessionId, includeGit = false) => {
      target = {
        projectDir,
        sessionId,
        epoch: appStore.state.sessionViewEpoch,
        includeGit: includeGit || target?.includeGit === true,
      };
      board.schedule();
    },
    cancel: () => {
      board.cancel();
      target = undefined;
    },
  };
}

export function refreshBoardSnapshot(
  appStore: AppStore,
  client: LycaonClient,
  projectDir: string,
  projects: readonly Project[],
  sessionId?: string,
): Promise<void> {
  const projectId = resolveProjectId(appStore, projectDir, projects);
  const sid =
    sessionId?.trim() || appStore.state.currentSession?.id?.trim() || "";
  if (!projectId || !sid) return Promise.resolve();
  let flights = boardFlights.get(appStore);
  if (!flights) {
    flights = new Map();
    boardFlights.set(appStore, flights);
  }
  const key = `${projectId}\u0000${sid}`;
  const existing = flights.get(key);
  const epoch = appStore.state.sessionViewEpoch;
  const eventEpoch = appStore.state.boardEventEpoch;
  const workerEventEpoch = appStore.state.workerEventEpochs[sid] ?? 0;
  if (
    existing?.client === client &&
    existing.sessionViewEpoch === epoch &&
    existing.boardEventEpoch === eventEpoch &&
    existing.workerEventEpoch === workerEventEpoch
  ) {
    return existing.promise;
  }
  appStore.actions.setBoardLoad(sid, epoch, eventEpoch, loading(appStore.state.boardLoad));
  const flight: Promise<void> = client
    .getBoard(projectId, sid)
    .then((board) => {
      if (
        flights?.get(key)?.promise === flight &&
        appStore.state.sessionViewEpoch === epoch &&
        appStore.state.currentSession?.id === sid
      ) {
        appStore.actions.setBoardSnapshot(
          sid,
          epoch,
          eventEpoch,
          workerEventEpoch,
          board,
        );
      }
    })
    .catch((err) => {
      if (flights?.get(key)?.promise === flight) {
        appStore.actions.setBoardLoad(sid, epoch, eventEpoch, loadFailed(err, appStore.state.boardLoad));
      }
      throw err;
    })
    .finally(() => {
      if (flights?.get(key)?.promise === flight) flights.delete(key);
    });
  flights.set(key, {
    client,
    promise: flight,
    sessionViewEpoch: epoch,
    boardEventEpoch: eventEpoch,
    workerEventEpoch,
  });
  return flight;
}

export async function refreshBoard(
  appStore: AppStore,
  client: LycaonClient,
  projectDir: string,
  projects: readonly Project[],
  sessionId?: string,
  options?: { includeGit?: boolean },
): Promise<void> {
  const epoch = appStore.state.sessionViewEpoch;
  const sid = sessionId?.trim() || appStore.state.currentSession?.id;
  const projectId = resolveProjectId(appStore, projectDir, projects);
  const isCurrent = () =>
    !!sid &&
    appStore.state.sessionViewEpoch === epoch &&
    appStore.state.currentSession?.id === sid &&
    appStore.state.currentSession?.project_id === projectId;
  if (!isCurrent()) return;
  await refreshBoardSnapshot(appStore, client, projectDir, projects, sid);
  if (!isCurrent() || options?.includeGit === false) return;
  await maybeRefreshGitAfterBoard(appStore, client, projectDir, projects);
}

export async function maybeRefreshGitAfterBoard(
  appStore: AppStore,
  client: LycaonClient,
  projectDir: string,
  projects: readonly Project[],
): Promise<"skipped" | "fetched" | "none"> {
  const projectId = resolveProjectId(appStore, projectDir, projects);
  if (!projectId) return "none";
  const board = appStore.state.board;
  const prev = valueOf(appStore.state.gitStatus);
  const repos = appStore.state.gitRepos ?? [];
  if (repos.length > 0) {
    const scope = resolveScope(
      repos,
      appStore.state.gitActiveRepoId ?? "",
      appStore.state.gitScopePin ?? null,
    );
    // Scoped repository views keep their own status.
    if (scope.kind === "all" || scope.kind === "root") {
      return "none";
    }
    if (boardHasGitPulse(board)) {
      const pulseRepo = (board.git.repo_id ?? "").trim();
      if (pulseRepo && pulseRepo !== scope.repoId) {
        return "none";
      }
    }
    if (boardHasGitPulse(board)) {
      if (shouldSkipGitStatusFetch(board.git, prev)) {
        return "skipped";
      }
      appStore.actions.setGitStatus(gitStatusFromBoardPulse(board.git, prev));
    }
    await refreshGitStatus(appStore, client, projectId, appStore.state.currentSession?.id, {
      refreshSet: false,
      repoId: scope.repoId,
    });
    return "fetched";
  }
  if (boardHasGitPulse(board)) {
    if (shouldSkipGitStatusFetch(board.git, prev)) {
      return "skipped";
    }
    appStore.actions.setGitStatus(gitStatusFromBoardPulse(board.git, prev));
  }
  const pulseRepo = boardHasGitPulse(board)
    ? (board.git.repo_id ?? "").trim()
    : "";
  await refreshGitStatus(appStore, client, projectId, appStore.state.currentSession?.id, {
    refreshSet: false,
    repoId: pulseRepo || undefined,
  });
  return "fetched";
}

const workerRefreshes = new WeakMap<AppStore, object>();
const checkpointRefreshes = new WeakMap<AppStore, object>();

export async function refreshSessionWorkers(
  appStore: AppStore,
  client: LycaonClient,
  projectDir: string,
  projects: readonly Project[],
  sessionId: string,
): Promise<void> {
  const projectId = resolveProjectId(appStore, projectDir, projects);
  const sid = sessionId.trim();
  if (!projectId || !sid) return;
  const sessionEpoch = appStore.state.sessionViewEpoch;
  const eventEpoch = appStore.state.workerEventEpochs[sid] ?? 0;
  const request = {};
  if (appStore.state.currentSession?.id?.trim() === sid) workerRefreshes.set(appStore, request);
  const workers = await client.listWorkers(projectId, { sessionId });
  if (
    workerRefreshes.get(appStore) !== request ||
    appStore.state.currentSession?.id?.trim() !== sid ||
    appStore.state.sessionViewEpoch !== sessionEpoch
  ) {
    return;
  }
  if (!appStore.actions.mergeSessionWorkers(sid, workers, eventEpoch)) {
    return;
  }
  await refreshPendingCheckpointsForSession(
    appStore,
    client,
    sessionId,
  ).catch(() => undefined);
}

export async function refreshPendingCheckpointsForSession(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
): Promise<void> {
  const sid = sessionId.trim();
  if (!sid) return;
  const sessionEpoch = appStore.state.sessionViewEpoch;
  const checkpointEventEpoch = appStore.state.checkpointEventEpoch;
  const request = {};
  if (appStore.state.currentSession?.id?.trim() === sid) checkpointRefreshes.set(appStore, request);
  const pending = await loadPendingCheckpointsForSessionView(
    client,
    sid,
  );
  if (checkpointRefreshes.get(appStore) !== request) return;
  appStore.actions.setPendingCheckpoints(
    sid,
    sessionEpoch,
    checkpointEventEpoch,
    pending,
  );
}
