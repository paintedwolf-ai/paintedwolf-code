import { formatQuery, type JsonRequester, jsonRequest } from "../http.ts";
import type {
  HostInfo,
  HostResourcesResponse,
  UpdateHostResourceRequest,
  PreflightReport,
  AttentionView,
} from "../types.ts";

export interface HostClient {
  getHostResources(projectId?: string): Promise<HostResourcesResponse>;
  refreshHostResources(): Promise<HostResourcesResponse>;
  updateHostResource(id: string, req: UpdateHostResourceRequest, projectId?: string): Promise<HostResourcesResponse>;
  /** The host handshake: install identity, served contract, caller, and capabilities. */
  getHost(): Promise<HostInfo>;
  getPreflight(): Promise<PreflightReport>;
  getAttention(): Promise<AttentionView>;
}

export function createHostClient(j: JsonRequester): HostClient {
  return {
    getHostResources: (projectId) =>
      j(`/v1/host-resources${formatQuery({ project_id: projectId })}`),
    refreshHostResources: () =>
      j("/v1/host-resources/refresh", { method: "POST" }),
    updateHostResource: (id, req, projectId) =>
      j(
        `/v1/host-resources/${encodeURIComponent(id)}${formatQuery({ project_id: projectId })}`,
        jsonRequest("PATCH", req),
      ),
    getHost: () => j("/v1/host"),
    getPreflight: () => j("/v1/preflight"),
    getAttention: () => j("/v1/attention"),
  };
}
