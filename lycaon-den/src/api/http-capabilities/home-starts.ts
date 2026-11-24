import { type JsonRequester, jsonRequest } from "../http.ts";
import type {
  HomeStart,
  HomeStartCapabilities,
  ReserveHomeStartRequest,
  ReissueHomeStartFirstPromptRequest,
} from "../types.ts";

export interface HomeStartsClient {
  /** Protocol version, durable store identity and acting person. */
  getHomeStartCapabilities(): Promise<HomeStartCapabilities>;
  /** Reserves or resumes one Home submission; repeats return its latest state. */
  reserveHomeStart(launchId: string, req: ReserveHomeStartRequest): Promise<HomeStart>;
  getHomeStart(launchId: string, opts?: { signal?: AbortSignal }): Promise<HomeStart>;
  stopHomeStart(launchId: string): Promise<HomeStart>;
  reissueHomeStartFirstPrompt(launchId: string, req: ReissueHomeStartFirstPromptRequest): Promise<HomeStart>;
}

export function createHomeStartsClient(j: JsonRequester): HomeStartsClient {
  const path = (launchId: string) => `/v1/home-starts/${encodeURIComponent(launchId)}`;
  return {
    getHomeStartCapabilities: () => j("/v1/home-starts/capabilities"),
    reserveHomeStart: (launchId, req) => j(path(launchId), jsonRequest("PUT", req)),
    getHomeStart: (launchId, opts) =>
      j(path(launchId), opts?.signal ? { signal: opts.signal } : undefined),
    stopHomeStart: (launchId) => j(`${path(launchId)}/stop`, { method: "POST" }),
    reissueHomeStartFirstPrompt: (launchId, req) =>
      j(`${path(launchId)}/reissue-first-prompt`, jsonRequest("POST", req)),
  };
}
