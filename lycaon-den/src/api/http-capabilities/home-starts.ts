import { type JsonRequester, jsonRequest } from "../http.ts";
import type {
  HomeStart,
  HomeStartCapabilities,
  PutHomeStartRequest,
  ReplaceHomeStartFirstPromptRequest,
} from "../types.ts";

export interface HomeStartsClient {
  /** Protocol version, durable store identity and acting person. */
  getHomeStartCapabilities(): Promise<HomeStartCapabilities>;
  /** Reserves or resumes one Home submission; repeats return its latest state. */
  putHomeStart(launchId: string, req: PutHomeStartRequest): Promise<HomeStart>;
  getHomeStart(launchId: string, opts?: { signal?: AbortSignal }): Promise<HomeStart>;
  stopHomeStart(launchId: string): Promise<HomeStart>;
  replaceHomeStartFirstPrompt(launchId: string, req: ReplaceHomeStartFirstPromptRequest): Promise<HomeStart>;
}

export function createHomeStartsClient(j: JsonRequester): HomeStartsClient {
  const path = (launchId: string) => `/v1/home-starts/${encodeURIComponent(launchId)}`;
  return {
    getHomeStartCapabilities: () => j("/v1/home-starts/capabilities"),
    putHomeStart: (launchId, req) => j(path(launchId), jsonRequest("PUT", req)),
    getHomeStart: (launchId, opts) =>
      j(path(launchId), opts?.signal ? { signal: opts.signal } : undefined),
    stopHomeStart: (launchId) => j(`${path(launchId)}/stop`, { method: "POST" }),
    replaceHomeStartFirstPrompt: (launchId, req) =>
      j(`${path(launchId)}/replace-first-prompt`, jsonRequest("POST", req)),
  };
}
