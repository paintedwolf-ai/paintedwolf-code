import { formatQuery, type JsonRequester, jsonRequest } from "../http.ts";
import type {
  AgentWatch,
  AgentWatchListResponse,
  CancelWatchResponse,
  CreateStandingWatchRequest,
  KeepWatchRequest,
  KeepWatchResponse,
  ProjectWatch,
  ProjectWatchListResponse,
  StandingWatch,
  StandingWatchRevisionRequest,
  UpdateStandingWatchRequest,
  UpdateWatchDeliveryRequest,
  WatchRun,
  WatchRunListResponse,
} from "../types.ts";

export interface WatchesClient {
  listProjectWatches(
    projectId: string,
    opts?: { owner?: "agent" | "person" | "all" },
  ): Promise<ProjectWatchListResponse>;
  createStandingWatch(
    projectId: string,
    req: CreateStandingWatchRequest,
  ): Promise<StandingWatch>;
  getProjectWatch(projectId: string, watchId: string): Promise<ProjectWatch>;
  updateStandingWatch(
    projectId: string,
    watchId: string,
    req: UpdateStandingWatchRequest,
  ): Promise<StandingWatch>;
  deleteStandingWatch(projectId: string, watchId: string): Promise<void>;
  pauseStandingWatch(
    projectId: string,
    watchId: string,
    req: StandingWatchRevisionRequest,
  ): Promise<StandingWatch>;
  resumeStandingWatch(
    projectId: string,
    watchId: string,
    req: StandingWatchRevisionRequest,
  ): Promise<StandingWatch>;
  runStandingWatchNow(projectId: string, watchId: string): Promise<WatchRun>;
  listWatchRuns(
    projectId: string,
    watchId: string,
    opts?: { cursor?: string; limit?: number },
  ): Promise<WatchRunListResponse>;
  listWatches(sessionId: string): Promise<AgentWatchListResponse>;
  getWatch(sessionId: string, watchHandle: string): Promise<AgentWatch>;
  updateWatchDelivery(
    sessionId: string,
    watchHandle: string,
    req: UpdateWatchDeliveryRequest,
  ): Promise<AgentWatch>;
  cancelWatch(
    sessionId: string,
    watchHandle: string,
  ): Promise<CancelWatchResponse>;
  keepWatch(
    sessionId: string,
    watchHandle: string,
    req: KeepWatchRequest,
  ): Promise<KeepWatchResponse>;
}

export function createWatchesClient(j: JsonRequester): WatchesClient {
  return {
    listProjectWatches: (projectId, opts) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches${formatQuery({
          owner: opts?.owner,
        })}`,
      ),
    createStandingWatch: (projectId, req) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches`,
        jsonRequest("POST", req),
      ),
    getProjectWatch: (projectId, watchId) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches/${encodeURIComponent(watchId)}`,
      ),
    updateStandingWatch: (projectId, watchId, req) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches/${encodeURIComponent(watchId)}`,
        jsonRequest("PATCH", req),
      ),
    deleteStandingWatch: (projectId, watchId) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches/${encodeURIComponent(watchId)}`,
        { method: "DELETE" },
      ),
    pauseStandingWatch: (projectId, watchId, req) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches/${encodeURIComponent(watchId)}/pause`,
        jsonRequest("POST", req),
      ),
    resumeStandingWatch: (projectId, watchId, req) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches/${encodeURIComponent(watchId)}/resume`,
        jsonRequest("POST", req),
      ),
    runStandingWatchNow: (projectId, watchId) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches/${encodeURIComponent(watchId)}/run`,
        { method: "POST" },
      ),
    listWatchRuns: (projectId, watchId, opts) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/watches/${encodeURIComponent(watchId)}/runs${formatQuery({
          cursor: opts?.cursor,
          limit: opts?.limit,
        })}`,
      ),
    listWatches: (sessionId) =>
      j(`/v1/sessions/${encodeURIComponent(sessionId)}/watches`),
    getWatch: (sessionId, watchHandle) =>
      j(
        `/v1/sessions/${encodeURIComponent(sessionId)}/watches/${encodeURIComponent(watchHandle)}`,
      ),
    updateWatchDelivery: (sessionId, watchHandle, req) =>
      j(
        `/v1/sessions/${encodeURIComponent(sessionId)}/watches/${encodeURIComponent(watchHandle)}`,
        jsonRequest("PATCH", req),
      ),
    cancelWatch: (sessionId, watchHandle) =>
      j(
        `/v1/sessions/${encodeURIComponent(sessionId)}/watches/${encodeURIComponent(watchHandle)}/cancel`,
        { method: "POST" },
      ),
    keepWatch: (sessionId, watchHandle, req) =>
      j(
        `/v1/sessions/${encodeURIComponent(sessionId)}/watches/${encodeURIComponent(watchHandle)}/keep`,
        jsonRequest("POST", req),
      ),
  };
}
