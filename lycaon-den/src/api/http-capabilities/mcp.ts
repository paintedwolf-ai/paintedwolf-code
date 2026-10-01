import { formatQuery, type JsonRequester } from "../http.ts";
import type {
  McpCheckResponse,
  McpCheckRow,
  McpRecipeCatalogResponse,
  McpProvider,
  McpProviderListResponse,
  McpToolInfo,
  McpToolListResponse,
  McpOAuthStartResponse,
  McpOAuthCompleteRequest,
  McpOAuthCancelRequest,
  CreateMcpProviderRequest,
  UpdateMcpProviderRequest,
} from "../types.ts";

export interface McpClient {
  listMcpRecipes(projectId?: string): Promise<McpRecipeCatalogResponse>;
  listMcpProviders(projectId?: string): Promise<McpProvider[]>;
  /** Adds a provider from a bundled recipe or a custom connection. */
  createMcpProvider(
    body: CreateMcpProviderRequest,
    projectId?: string,
  ): Promise<McpProvider>;
  updateMcpProvider(
    id: string,
    body: UpdateMcpProviderRequest,
    projectId?: string,
  ): Promise<McpProvider>;
  deleteMcpProvider(id: string, projectId?: string): Promise<void>;
  listMcpProviderTools(
    id: string,
    projectId?: string,
  ): Promise<McpToolInfo[]>;
  refreshMcpProvider(id: string, projectId?: string): Promise<McpProvider>;
  startMcpOAuth(id: string, projectId?: string): Promise<McpOAuthStartResponse>;
  cancelMcpOAuth(id: string, body: McpOAuthCancelRequest, projectId?: string): Promise<void>;
  completeMcpOAuth(
    id: string,
    body: McpOAuthCompleteRequest,
    projectId?: string,
  ): Promise<McpProvider>;
  revokeMcpOAuth(id: string, projectId?: string): Promise<void>;
  checkMcpProviders(projectId?: string): Promise<McpCheckRow[]>;
}

export function createMcpClient(j: JsonRequester): McpClient {
  return {
    listMcpRecipes: (projectId) =>
      j(`/v1/mcp/recipes${formatQuery({ project_id: projectId })}`),
    listMcpProviders: async (projectId) => {
      const res = await j<McpProviderListResponse>(`/v1/mcp/providers${formatQuery({ project_id: projectId })}`);
      return res.providers;
    },
    createMcpProvider: (body, projectId) =>
      j(`/v1/mcp/providers${formatQuery({ project_id: projectId })}`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    updateMcpProvider: (id, body, projectId) =>
      j(`/v1/mcp/providers/${encodeURIComponent(id)}${formatQuery({ project_id: projectId })}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    deleteMcpProvider: (id, projectId) =>
      j(`/v1/mcp/providers/${encodeURIComponent(id)}${formatQuery({ project_id: projectId })}`, {
        method: "DELETE",
      }),
    listMcpProviderTools: async (id, projectId) => {
      const res = await j<McpToolListResponse>(
        `/v1/mcp/providers/${encodeURIComponent(id)}/tools${formatQuery({ project_id: projectId })}`,
      );
      return res.tools;
    },
    refreshMcpProvider: (id, projectId) =>
      j(`/v1/mcp/providers/${encodeURIComponent(id)}/refresh${formatQuery({ project_id: projectId })}`, { method: "POST" }),
    startMcpOAuth: (id, projectId) =>
      j(`/v1/mcp/providers/${encodeURIComponent(id)}/oauth/start${formatQuery({ project_id: projectId })}`, { method: "POST" }),
    cancelMcpOAuth: (id, body, projectId) =>
      j(`/v1/mcp/providers/${encodeURIComponent(id)}/oauth/cancel${formatQuery({ project_id: projectId })}`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    completeMcpOAuth: (id, body, projectId) =>
      j(`/v1/mcp/providers/${encodeURIComponent(id)}/oauth/complete${formatQuery({ project_id: projectId })}`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    revokeMcpOAuth: (id, projectId) =>
      j(`/v1/mcp/providers/${encodeURIComponent(id)}/oauth${formatQuery({ project_id: projectId })}`, { method: "DELETE" }),
    checkMcpProviders: async (projectId) => {
      const res = await j<McpCheckResponse>(`/v1/mcp/providers/check${formatQuery({ project_id: projectId })}`, {
        method: "POST",
      });
      return res.providers;
    },
  };
}
