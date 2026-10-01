import {
  formatQuery,
  lycaonFetch,
  requireSuccessfulResponse,
  type JsonRequester,
  jsonRequest,
} from "../http.ts";
import type {
  ContributionFrameResponse,
  ExtensionsCatalogView,
  CommandInvokeRequest,
  CommandInvokeResponse,
  ContributionChoiceRequest,
  ContributionChoiceResponse,
  ContributionSearchRequest,
  ContributionSearchResponse,
  ExtensionUnitDetail,
  ExtensionInstallRequest,
  ExtensionInstallResponse,
  ExtensionMetaPackInstallRequest,
  ExtensionPackUpdateStatus,
  ExtensionPackActionResponse,
  ExtensionMutationResponse,
  ExtensionRevisionRequest,
  ExtensionConfigurationRequest,
  ExtensionSuggestionsResponse,
  ExtensionSuggestionsAcceptRequest,
  ExtensionsDesiredScope,
  UpdateExtensionPackRequest,
  UpdateExtensionUnitRequest,
  UpdateExtensionMetaPackRequest,
} from "../types.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";

/** Project scope requires projectId; device scope ignores it. */
export type ExtensionsWriteTarget = {
  scope: ExtensionsDesiredScope;
  projectId?: string;
};

export interface ExtensionsClient {
  /** One complete projection of the captured device contribution frame. */
  getContributions(): Promise<ContributionFrameResponse>;
  /** Invoke a project-bound contribution command (presentation kinds). */
  invokeProjectCommand(
    projectId: string,
    commandId: string,
    req: CommandInvokeRequest,
  ): Promise<CommandInvokeResponse>;
  /** Invoke a session-bound contribution command (effectful kinds). */
  invokeSessionCommand(
    sessionId: string,
    commandId: string,
    req: CommandInvokeRequest,
  ): Promise<CommandInvokeResponse>;
  resolveContributionChoices(
    projectId: string,
    commandId: string,
    stepId: string,
    req: ContributionChoiceRequest,
    signal?: AbortSignal,
  ): Promise<ContributionChoiceResponse>;
  searchContributionSource(
    projectId: string,
    sourceId: string,
    req: ContributionSearchRequest,
    signal?: AbortSignal,
  ): Promise<ContributionSearchResponse>;
  /** The catalog generation in force — packs, units, diagnostics, and desired state. */
  getExtensionsCatalog(projectId?: string): Promise<ExtensionsCatalogView>;
  getExtensionSuggestions(projectId: string): Promise<ExtensionSuggestionsResponse>;
  acceptExtensionSuggestions(
    projectId: string,
    req: ExtensionSuggestionsAcceptRequest,
  ): Promise<ExtensionsCatalogView>;
  getExtensionUnit(
    unitId: string,
    projectId?: string,
  ): Promise<ExtensionUnitDetail>;
  installExtensionPack(
    req: ExtensionInstallRequest,
  ): Promise<ExtensionInstallResponse>;
  deleteExtensionPack(
    packId: string,
    expectedRevision: string,
  ): Promise<void>;
  updateExtensionPack(
    packId: string,
    req: UpdateExtensionPackRequest,
  ): Promise<ExtensionMutationResponse>;
  applyExtensionPackUpdate(
    packId: string,
    req: ExtensionRevisionRequest,
  ): Promise<ExtensionPackActionResponse>;
  getExtensionPackUpdate(packId: string): Promise<ExtensionPackUpdateStatus>;
  reloadExtensionPack(
    packId: string,
    req: ExtensionRevisionRequest,
  ): Promise<ExtensionPackActionResponse>;
  applyExtensionPackProfile(
    packId: string,
    profileName: string,
    req: ExtensionRevisionRequest,
  ): Promise<ExtensionMutationResponse>;
  updateExtensionConfiguration(
    req: ExtensionConfigurationRequest,
  ): Promise<ExtensionMutationResponse>;
  applyExtensionMetaPack(
    metaPackId: string,
    req?: ExtensionRevisionRequest,
  ): Promise<ExtensionMutationResponse>;
  updateExtensionMetaPack(
    metaPackId: string,
    req: UpdateExtensionMetaPackRequest,
  ): Promise<ExtensionMutationResponse>;
  installExtensionMetaPack(
    req: ExtensionMetaPackInstallRequest,
  ): Promise<ExtensionMutationResponse>;
  deleteExtensionMetaPack(
    metaPackId: string,
    expectedRevision?: string,
  ): Promise<void>;
  updateExtensionUnit(
    unitId: string,
    req: UpdateExtensionUnitRequest,
    target?: ExtensionsWriteTarget,
  ): Promise<ExtensionMutationResponse>;
}

/** Desired-state writes identify their target in the query. */
function extensionsTargetQuery(
  target: ExtensionsWriteTarget,
  extra?: Record<string, string | undefined>,
): string {
  return formatQuery({ project_id: target.projectId, scope: target.scope, ...extra });
}

export function createExtensionsClient(j: JsonRequester, connection: BackendConnection): ExtensionsClient {
  const contributionFrame = new Map<string, ContributionFrameResponse>();

  const getContributions = async (): Promise<ContributionFrameResponse> => {
    const key = "device";
    const cached = contributionFrame.get(key);
    const headers = new Headers();
    if (cached?.frame_revision) {
      headers.set("If-None-Match", `"${cached.frame_revision}"`);
    }
    const response = await lycaonFetch(connection, "/v1/contributions", {
      headers,
    });
    if (response.status === 304 && cached) return cached;
    await requireSuccessfulResponse(response);
    const frame = (await response.json()) as ContributionFrameResponse;
    contributionFrame.set(key, frame);
    return frame;
  };

  const updateExtensionUnit = (
    unitId: string,
    req: UpdateExtensionUnitRequest,
    target?: ExtensionsWriteTarget,
  ) =>
    j<ExtensionMutationResponse>(
      `/v1/extensions/units/${encodeURIComponent(unitId)}${target ? extensionsTargetQuery(target) : ""}`,
      { method: "PATCH", body: JSON.stringify(req) },
    );

  const updateExtensionPack = (
    packId: string,
    req: UpdateExtensionPackRequest,
  ) =>
    j<ExtensionMutationResponse>(
      `/v1/extensions/packs/${encodeURIComponent(packId)}`,
      { method: "PATCH", body: JSON.stringify(req) },
    );


  return {
    getContributions,
    invokeProjectCommand: (projectId, commandId, req) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/commands/${encodeURIComponent(commandId)}/invoke`,
        jsonRequest("POST", req),
      ),
    invokeSessionCommand: (sessionId, commandId, req) =>
      j(
        `/v1/sessions/${encodeURIComponent(sessionId)}/commands/${encodeURIComponent(commandId)}/invoke`,
        jsonRequest("POST", req),
      ),
    resolveContributionChoices: (projectId, commandId, stepId, req, signal) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/commands/${encodeURIComponent(commandId)}/choices/${encodeURIComponent(stepId)}`,
        { ...jsonRequest("POST", req), signal },
      ),
    searchContributionSource: (projectId, sourceId, req, signal) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/contribution-search/${encodeURIComponent(sourceId)}`,
        { ...jsonRequest("POST", req), signal },
      ),
    getExtensionsCatalog: (projectId) =>
      j(`/v1/extensions${formatQuery({ project_id: projectId })}`),
    getExtensionSuggestions: (projectId) =>
      j(`/v1/extensions/suggestions${formatQuery({ project_id: projectId })}`),
    acceptExtensionSuggestions: (projectId, req) =>
      j(`/v1/extensions/suggestions/accept${formatQuery({ project_id: projectId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    getExtensionUnit: (unitId, projectId) =>
      j(
        `/v1/extensions/units/${encodeURIComponent(unitId)}${formatQuery({
          project_id: projectId,
        })}`,
      ),
    installExtensionPack: (req) =>
      j(`/v1/extensions/packs`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    deleteExtensionPack: (packId, expectedRevision) =>
      j(
        `/v1/extensions/packs/${encodeURIComponent(packId)}${formatQuery({ expected_revision: expectedRevision })}`,
        { method: "DELETE" },
      ),
    updateExtensionPack,
    applyExtensionPackUpdate: (packId, req) =>
      j(
        `/v1/extensions/packs/${encodeURIComponent(packId)}/update`,
        { method: "POST", body: JSON.stringify(req) },
      ),
    getExtensionPackUpdate: (packId) =>
      j(`/v1/extensions/packs/${encodeURIComponent(packId)}/update`),
    reloadExtensionPack: (packId, req) =>
      j(
        `/v1/extensions/packs/${encodeURIComponent(packId)}/reload`,
        { method: "POST", body: JSON.stringify(req) },
      ),
    applyExtensionPackProfile: (packId, profileName, req) =>
      j(
        `/v1/extensions/packs/${encodeURIComponent(packId)}/profiles/${encodeURIComponent(profileName)}/apply`,
        { method: "POST", body: JSON.stringify(req) },
      ),
    updateExtensionConfiguration: (req) =>
      j(`/v1/extensions/configuration`, { method: "PATCH", body: JSON.stringify(req) }),
    applyExtensionMetaPack: (metaPackId, req) =>
      j(`/v1/extensions/meta-packs/${encodeURIComponent(metaPackId)}/apply`, {
        method: "POST",
        body: JSON.stringify(req ?? {}),
      }),
    updateExtensionMetaPack: (metaPackId, req) =>
      j(`/v1/extensions/meta-packs/${encodeURIComponent(metaPackId)}`, jsonRequest("PATCH", req)),
    installExtensionMetaPack: (req) =>
      j(`/v1/extensions/meta-packs`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    deleteExtensionMetaPack: (metaPackId, expectedRevision) =>
      j(
        `/v1/extensions/meta-packs/${encodeURIComponent(metaPackId)}${formatQuery({ expected_revision: expectedRevision })}`,
        { method: "DELETE" },
      ),
    updateExtensionUnit,
  };
}
