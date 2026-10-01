import { formatQuery, type JsonRequester } from "../http.ts";
import type {
  FileSummariesSettingsResponse,
  UpdateFileSummariesSettingsRequest,
  PowerSettingsResponse,
  UpdatePowerSettingsRequest,
  TrustSettingsResponse,
  UpdateTrustSettingsRequest,
  ReviewSettingsResponse,
  UpdateReviewSettingsRequest,
  VerifySettingsResponse,
  UpdateVerifySettingsRequest,
  DismissVerifySettingsRequest,
  SettingsPricing,
  SettingsPricingResponse,
  PricingSourceMeta,
  ModelPolicy,
  ModelPolicyPatch,
  SettingsLimitsPatch,
  SettingsLimitsResponse,
  ProviderKindTemplate,
  ProviderKindListResponse,
  ProviderMeta,
  ProviderListResponse,
  ProviderProbeResult,
  WebResearchIndexStatus,
  WebResearchProvidersResponse,
  WebResearchSettings,
  UpdateWebResearchSettingsRequest,
  UpdateWebResearchProviderRequest,
  WebResearchProviderMeta,
  CreateProviderRequest,
  UpdateProviderRequest,
} from "../types.ts";

export interface SettingsClient {
  getFileSummariesSettings(): Promise<FileSummariesSettingsResponse>;
  updateFileSummariesSettings(
    req: UpdateFileSummariesSettingsRequest,
  ): Promise<FileSummariesSettingsResponse>;
  getPowerSettings(): Promise<PowerSettingsResponse>;
  updatePowerSettings(req: UpdatePowerSettingsRequest): Promise<PowerSettingsResponse>;
  getPricingSettings(): Promise<SettingsPricingResponse>;
  updatePricingSettings(body: SettingsPricing): Promise<SettingsPricingResponse>;
  refreshPricingSource(id: string): Promise<PricingSourceMeta>;
  getTrustSettings(): Promise<TrustSettingsResponse>;
  updateTrustSettings(req: UpdateTrustSettingsRequest): Promise<TrustSettingsResponse>;
  getWebResearchProviders(): Promise<WebResearchProvidersResponse>;
  getWebResearchIndex(): Promise<WebResearchIndexStatus>;
  getWebResearchSettings(): Promise<WebResearchSettings>;
  updateWebResearchSettings(req: UpdateWebResearchSettingsRequest): Promise<WebResearchSettings>;
  updateWebResearchProvider(id: string, req: UpdateWebResearchProviderRequest): Promise<WebResearchProviderMeta>;
  replaceWebResearchCredential(id: string, apiKey: string): Promise<WebResearchProviderMeta>;
  deleteWebResearchCredential(id: string): Promise<void>;
  testWebResearchProvider(id: string): Promise<ProviderProbeResult>;
  listProviders(): Promise<ProviderMeta[]>;
  listProviderKinds(): Promise<ProviderKindTemplate[]>;
  createProvider(req: CreateProviderRequest): Promise<ProviderMeta>;
  updateProvider(id: string, req: UpdateProviderRequest): Promise<ProviderMeta>;
  deleteProvider(id: string): Promise<void>;
  replaceProviderCredential(id: string, apiKey: string): Promise<ProviderMeta>;
  deleteProviderCredential(id: string): Promise<void>;
  testProvider(id: string, signal?: AbortSignal): Promise<ProviderProbeResult>;
  refreshProviderModels(id: string): Promise<ProviderMeta>;
  /** Settings reads and writes name a project for its overlay and omit it for the global layer. */
  getModelPolicySettings(projectId?: string, effective?: boolean): Promise<ModelPolicy>;
  updateModelPolicySettings(policy: ModelPolicyPatch, projectId?: string): Promise<ModelPolicy>;
  getLimitsSettings(projectId?: string): Promise<SettingsLimitsResponse>;
  updateLimitsSettings(limits: SettingsLimitsPatch, projectId?: string): Promise<SettingsLimitsResponse>;
  getReviewSettings(projectId?: string): Promise<ReviewSettingsResponse>;
  updateReviewSettings(req: UpdateReviewSettingsRequest, projectId?: string): Promise<ReviewSettingsResponse>;
  getVerifySettings(projectId?: string): Promise<VerifySettingsResponse>;
  updateVerifySettings(req: UpdateVerifySettingsRequest, projectId?: string): Promise<VerifySettingsResponse>;
  dismissVerifySettings(req: DismissVerifySettingsRequest, projectId?: string): Promise<VerifySettingsResponse>;
}

export function createSettingsClient(j: JsonRequester): SettingsClient {
  return {
    getWebResearchProviders: () => j("/v1/web-research/providers"),
    getWebResearchIndex: () => j("/v1/web-research/index"),
    getWebResearchSettings: () => j("/v1/settings/web-research"),
    updateWebResearchSettings: (req) =>
      j("/v1/settings/web-research", {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    updateWebResearchProvider: (id, req) =>
      j(`/v1/web-research/providers/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    replaceWebResearchCredential: (id, apiKey) =>
      j(`/v1/web-research/providers/${encodeURIComponent(id)}/credential`, {
        method: "PUT",
        body: JSON.stringify({ api_key: apiKey }),
      }),
    deleteWebResearchCredential: (id) =>
      j(`/v1/web-research/providers/${encodeURIComponent(id)}/credential`, { method: "DELETE" }),
    testWebResearchProvider: (id) =>
      j(`/v1/web-research/providers/${encodeURIComponent(id)}/test`, { method: "POST" }),

    listProviders: async () => {
      const res = await j<ProviderListResponse>("/v1/providers");
      return res.providers;
    },
    listProviderKinds: async () => {
      const res = await j<ProviderKindListResponse>("/v1/provider-kinds");
      return res.kinds;
    },
    createProvider: (req) => j("/v1/providers", { method: "POST", body: JSON.stringify(req) }),
    updateProvider: (id, req) =>
      j(`/v1/providers/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(req) }),
    deleteProvider: (id) => j(`/v1/providers/${encodeURIComponent(id)}`, { method: "DELETE" }),
    replaceProviderCredential: (id, apiKey) =>
      j(`/v1/providers/${encodeURIComponent(id)}/credential`, {
        method: "PUT",
        body: JSON.stringify({ api_key: apiKey }),
      }),
    deleteProviderCredential: (id) =>
      j(`/v1/providers/${encodeURIComponent(id)}/credential`, { method: "DELETE" }),
    testProvider: (id, signal) =>
      j(`/v1/providers/${encodeURIComponent(id)}/test`, { method: "POST", signal }),
    refreshProviderModels: (id) =>
      j(`/v1/providers/${encodeURIComponent(id)}/refresh`, { method: "POST" }),
    getModelPolicySettings: (projectId, effective) =>
      j(
        `/v1/settings/model-policy${formatQuery({
          project_id: projectId,
          effective: effective ? "true" : undefined,
        })}`,
      ),
    updateModelPolicySettings: (policy, projectId) =>
      j(
        `/v1/settings/model-policy${formatQuery({
          project_id: projectId,
        })}`,
        {
          method: "PATCH",
          body: JSON.stringify(policy),
        },
      ),

    getLimitsSettings: (projectId) =>
      j(
        `/v1/settings/limits${formatQuery({
          project_id: projectId,
        })}`,
      ),
    updateLimitsSettings: (limits, projectId) =>
      j(
        `/v1/settings/limits${formatQuery({
          project_id: projectId,
        })}`,
        {
          method: "PATCH",
          body: JSON.stringify(limits),
        },
      ),
    getReviewSettings: (projectId) =>
      j(
        `/v1/settings/review${formatQuery({
          project_id: projectId,
        })}`,
      ),
    updateReviewSettings: (req, projectId) =>
      j(
        `/v1/settings/review${formatQuery({
          project_id: projectId,
        })}`,
        {
          method: "PATCH",
          body: JSON.stringify(req),
        },
      ),
    getVerifySettings: (projectId) =>
      j(
        `/v1/settings/verify${formatQuery({
          project_id: projectId,
        })}`,
      ),
    updateVerifySettings: (req, projectId) =>
      j(
        `/v1/settings/verify${formatQuery({
          project_id: projectId,
        })}`,
        {
          method: "PATCH",
          body: JSON.stringify(req),
        },
      ),
    dismissVerifySettings: (req, projectId) =>
      j(
        `/v1/settings/verify/dismiss${formatQuery({
          project_id: projectId,
        })}`,
        {
          method: "POST",
          body: JSON.stringify(req),
        },
      ),
    getFileSummariesSettings: () => j("/v1/settings/file-summaries"),
    updateFileSummariesSettings: (req) =>
      j("/v1/settings/file-summaries", {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    getPowerSettings: () => j("/v1/settings/power"),
    updatePowerSettings: (req) =>
      j("/v1/settings/power", {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    getPricingSettings: () => j("/v1/settings/pricing"),
    updatePricingSettings: (body) =>
      j("/v1/settings/pricing", {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    refreshPricingSource: (id) =>
      j(`/v1/settings/pricing/sources/${encodeURIComponent(id)}/refresh`, {
        method: "POST",
      }),
    getTrustSettings: () => j("/v1/settings/project-trust"),
    updateTrustSettings: (req) =>
      j("/v1/settings/project-trust", {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
  };
}
