import type { SecretIgnoreList, AddSecretIgnoreRequest, SecretIgnoreCandidate } from "../types.ts";
import { formatQuery, lycaonDownload, type JsonRequester, jsonRequest } from "../http.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import type {
  UpdateSecurityScannersSettingsRequest,
  ProjectTrust,
  UpdateProjectTrustRequest,
  SecurityScannersSettingsResponse,
  ApprovalConfigResponse,
  ApprovalGrant,
  ElevatedAccessSummary,
  RevokeElevatedAccessResponse,
  ApprovalGrantsResponse,
  ApprovalRecentAsksResponse,
  CreateApprovalGrantRequest,
  ResolveSocketGrantRequest,
  ResolveSocketGrantResponse,
  RevokeApprovalGrantsRequest,
  RevokeApprovalGrantsResponse,
  UpdateApprovalsSettingsRequest,
  DetectionPack,
  DetectionPackList,
  DetectionPackImportRequest,
  DetectionPackImportResult,
  CreateManagedSecretRequest,
  ManagedSecret,
  ManagedSecretList,
  ManagedSecretAttestationList,
  ManagedSecretUseList,
  ReplaceManagedSecretValueRequest,
  UpdateManagedSecretRequest,
  CodeScan,
  CodeScanPage,
  ScanQueryRequest,
  ScanQueryResponse,
  SecurityOverview,
  FindingLedgerQueryRequest,
  FindingLedgerResponse,
  FindingIgnoreEntry,
  FindingIgnoreListResponse,
  FindingExportRequest,
  FullScanRequest,
  FullScanResponse,
  ScannerSummary,
  ScannerListResponse,
  ScannerCatalogResponse,
  ScannerCheckResponse,
  CreateScannerRequest,
  UpdateProjectScannerRequest,
  UpdateDetectionPackRequest,
} from "../types.ts";

export interface SecurityClient {
  listProjectSecretIgnores(projectId: string, rootId?: string): Promise<SecretIgnoreList>;
  createProjectSecretIgnore(projectId: string, request: AddSecretIgnoreRequest): Promise<SecretIgnoreList>;
  /** Withdraws one declaration from the ignore file in the named folder. */
  deleteProjectSecretIgnore(projectId: string, entryId: string, rootId: string): Promise<void>;
  getSecretIgnoreCandidate(projectId: string, candidateId: string): Promise<SecretIgnoreCandidate>;

  /** Lists secret metadata without values. */
  listProjectManagedSecrets(projectId: string): Promise<ManagedSecretList>;
  /** Stores a value and returns only its metadata. */
  createProjectManagedSecret(
    projectId: string,
    req: CreateManagedSecretRequest,
  ): Promise<ManagedSecret>;
  /** Updates value-free metadata. */
  updateProjectManagedSecret(
    projectId: string,
    secretId: string,
    req: UpdateManagedSecretRequest,
  ): Promise<ManagedSecret>;
  /** Replaces the protected value behind an unchanged reference. */
  replaceProjectManagedSecretValue(
    projectId: string,
    secretId: string,
    req: ReplaceManagedSecretValueRequest,
  ): Promise<ManagedSecret>;
  /** Recent substitution attempts, newest first. */
  listProjectManagedSecretUses(
    projectId: string,
    secretId: string,
    opts?: { limit?: number; cursor?: string },
  ): Promise<ManagedSecretUseList>;
  /** Presence-verified reveals and releases, newest first. */
  listProjectManagedSecretAttestations(
    projectId: string,
    secretId: string,
  ): Promise<ManagedSecretAttestationList>;
  /** Permanently disables a secret reference. */
  revokeProjectManagedSecret(
    projectId: string,
    secretId: string,
  ): Promise<void>;
  getSecurityScannersSettings(): Promise<SecurityScannersSettingsResponse>;
  updateSecurityScannersSettings(
    req: UpdateSecurityScannersSettingsRequest,
  ): Promise<SecurityScannersSettingsResponse>;
  openProjectTrustReview(projectId: string): Promise<import("../types.ts").ProjectTrust>;
  getProjectTrust(projectId: string): Promise<ProjectTrust>;
  updateProjectTrust(
    projectId: string,
    req: UpdateProjectTrustRequest,
  ): Promise<ProjectTrust>;
  listCodeScans(projectId: string, query?: { root_id?: string; limit?: number; cursor?: string; sort?: "date" | "engine" | "status"; order?: "asc" | "desc" }): Promise<CodeScanPage>;
  getCodeScan(projectId: string, scanId: string, view?: "summary" | "full"): Promise<CodeScan>;
  queryCodeScan(projectId: string, scanId: string, req?: ScanQueryRequest): Promise<ScanQueryResponse>;
  /** Baseline, last full pass, running pass, and per-scanner state for one project. */
  getProjectSecurity(projectId: string, rootId?: string): Promise<SecurityOverview>;
  /** One page of the project's finding ledger across every selected scanner. */
  queryProjectFindings(
    projectId: string,
    req?: FindingLedgerQueryRequest,
    rootId?: string,
  ): Promise<FindingLedgerResponse>;
  /** The project's ignore catalog. */
  listProjectFindingIgnores(projectId: string, rootId?: string): Promise<FindingIgnoreListResponse>;
  /** Append one decision to the project's committed ignore file. */
  createProjectFindingIgnore(
    projectId: string,
    entry: FindingIgnoreEntry,
    rootId?: string,
  ): Promise<FindingIgnoreListResponse>;
  /** Withdraw one decision; findings it covered return to the open list. */
  deleteProjectFindingIgnore(
    projectId: string,
    entryId: string,
    rootId?: string,
  ): Promise<void>;
  /** Exports all store matches for the query, including results outside the loaded page. */
  exportProjectFindings(
    projectId: string,
    req: FindingExportRequest,
    rootId?: string,
  ): Promise<{ blob: Blob; filename: string }>;
  /** Asks for a full pass; the sidecar answers 202 with the scans it opened. */
  startFullScan(projectId: string, req: FullScanRequest, rootId?: string): Promise<FullScanResponse>;
  exportCodeScanSARIF(
    projectId: string,
    scanId: string,
  ): Promise<{ blob: Blob; filename: string }>;
  listScanners(): Promise<ScannerListResponse>;
  /** The scanner catalog with this project's enablement applied. */
  listProjectScanners(projectId: string): Promise<ScannerListResponse>;
  /** Known third-party scanners the host can drive (no binaries shipped). */
  listScannerCatalog(): Promise<ScannerCatalogResponse>;
  /** Probes installed scanner binaries. */
  checkScanners(): Promise<ScannerCheckResponse>;
  /** Choose the one scanner for a slot; the previous occupant is disabled. */
  replaceScannerSlot(
    category: string,
    scannerId: string,
  ): Promise<ScannerListResponse>;
  /** Adds a catalog scanner with its bundled argv, or a custom command. */
  createScanner(body: CreateScannerRequest): Promise<ScannerSummary>;
  deleteScanner(id: string): Promise<void>;
  updateProjectScanner(
    projectId: string,
    scannerId: string,
    body: UpdateProjectScannerRequest,
  ): Promise<ScannerSummary>;
  getApprovalsSettings(projectId?: string): Promise<ApprovalConfigResponse>;
  updateApprovalsSettings(req: UpdateApprovalsSettingsRequest, projectId?: string): Promise<ApprovalConfigResponse>;
  getElevatedAccess(sessionId: string): Promise<ElevatedAccessSummary>;
  revokeElevatedAccess(sessionId: string): Promise<RevokeElevatedAccessResponse>;
  listApprovalGrants(sessionId?: string): Promise<ApprovalGrantsResponse>;
  listApprovalAsks(days?: number, projectId?: string): Promise<ApprovalRecentAsksResponse>;
  createApprovalGrant(req: CreateApprovalGrantRequest): Promise<ApprovalGrant>;
  resolveSocketGrant(req: ResolveSocketGrantRequest): Promise<ResolveSocketGrantResponse>;
  revokeApprovalGrants(
    req: RevokeApprovalGrantsRequest,
  ): Promise<RevokeApprovalGrantsResponse>;
  listDetectionPacks(projectId?: string): Promise<DetectionPackList>;
  updateDetectionPack(packId: string, req: UpdateDetectionPackRequest): Promise<DetectionPack>;
  importDetectionPack(req: DetectionPackImportRequest): Promise<DetectionPackImportResult>;
  deleteDetectionPack(packId: string): Promise<void>;
}

export function createSecurityClient(j: JsonRequester, connection: BackendConnection): SecurityClient {
  return {
    listProjectSecretIgnores: (id, rootId) =>
      j(`/v1/projects/${encodeURIComponent(id)}/secret-ignores${formatQuery({ root_id: rootId })}`),
    createProjectSecretIgnore: (id, request) => j(`/v1/projects/${encodeURIComponent(id)}/secret-ignores`, jsonRequest("POST", request)),
    deleteProjectSecretIgnore: (id, entry, root) =>
      j(
        `/v1/projects/${encodeURIComponent(id)}/secret-ignores/${encodeURIComponent(entry)}${formatQuery({ root_id: root })}`,
        { method: "DELETE" },
      ),
    getSecretIgnoreCandidate: (id, candidate) => j(`/v1/projects/${encodeURIComponent(id)}/secret-ignore-candidates/${encodeURIComponent(candidate)}`),
    listProjectManagedSecrets: (projectId) =>
      j(`/v1/projects/${encodeURIComponent(projectId)}/secrets`),
    createProjectManagedSecret: (projectId, req) =>
      j(`/v1/projects/${encodeURIComponent(projectId)}/secrets`, jsonRequest("POST", req)),
    updateProjectManagedSecret: (projectId, secretId, req) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(secretId)}`,
        jsonRequest("PATCH", req),
      ),
    replaceProjectManagedSecretValue: (projectId, secretId, req) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(secretId)}/value`,
        jsonRequest("PUT", req),
      ),
    listProjectManagedSecretUses: (projectId, secretId, opts) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(secretId)}/uses${formatQuery({ limit: opts?.limit, cursor: opts?.cursor })}`,
      ),
    listProjectManagedSecretAttestations: (projectId, secretId) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(secretId)}/attestations`,
      ),
    revokeProjectManagedSecret: (projectId, secretId) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(secretId)}`,
        { method: "DELETE" },
      ),
    listCodeScans: (projectId, query) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/scans${formatQuery(query ?? {})}`,
      ),
    getCodeScan: (projectId: string, scanId: string, view?: "summary" | "full") => {
      const path = `/v1/projects/${encodeURIComponent(projectId)}/scans/${encodeURIComponent(scanId)}${formatQuery({ view })}`;
      return j(path);
    },
    queryCodeScan: (projectId: string, scanId: string, req?: ScanQueryRequest) => {
      const path = `/v1/projects/${encodeURIComponent(projectId)}/scans/${encodeURIComponent(scanId)}/query`;
      return j(path, jsonRequest("POST", req ?? {}));
    },
    getProjectSecurity: (projectId, rootId) => j(`/v1/projects/${projectId}/security${formatQuery({ root_id: rootId })}`),
    queryProjectFindings: (projectId, req, rootId) =>
      j(`/v1/projects/${projectId}/findings/query${formatQuery({ root_id: rootId })}`, jsonRequest("POST", req ?? {})),
    listProjectFindingIgnores: (projectId, rootId) => j(`/v1/projects/${projectId}/findings/ignores${formatQuery({ root_id: rootId })}`),
    createProjectFindingIgnore: (projectId, entry, rootId) =>
      j(`/v1/projects/${projectId}/findings/ignores${formatQuery({ root_id: rootId })}`, jsonRequest("POST", entry)),
    deleteProjectFindingIgnore: (projectId, entryId, rootId) =>
      j(
        `/v1/projects/${projectId}/findings/ignores/${encodeURIComponent(entryId)}${formatQuery({ root_id: rootId })}`,
        { method: "DELETE" },
      ),
    exportProjectFindings: async (projectId, req, rootId) => {
      const download = await lycaonDownload(
        connection,
        `/v1/projects/${projectId}/findings/export${formatQuery({ root_id: rootId })}`,
        req.format === "sarif" ? "findings.sarif" : "findings.openvex.json",
        jsonRequest("POST", req),
      );
      return { blob: download.blob, filename: download.filename };
    },
    startFullScan: (projectId, req, rootId) =>
      j(`/v1/projects/${encodeURIComponent(projectId)}/scans${formatQuery({ root_id: rootId })}`, jsonRequest("POST", req)),
    exportCodeScanSARIF: async (projectId: string, scanId: string) => {
      const path = `/v1/projects/${encodeURIComponent(projectId)}/scans/${encodeURIComponent(scanId)}/sarif`;
      const download = await lycaonDownload(
        connection,
        path,
        `scan-${scanId}.sarif.json`,
      );
      return { blob: download.blob, filename: download.filename };
    },
    listScanners: () => j(`/v1/scanners`),
    listProjectScanners: (projectId) => j(`/v1/projects/${encodeURIComponent(projectId)}/scanners`),
    listScannerCatalog: () => j(`/v1/scanners/catalog`),
    checkScanners: () =>
      j(`/v1/scanners/check`, {
        method: "POST",
      }),
    replaceScannerSlot: (category, scannerId) =>
      j(`/v1/scanners/slots/${encodeURIComponent(category)}`, {
        method: "PUT",
        body: JSON.stringify({ scanner_id: scannerId }),
      }),
    createScanner: (body) =>
      j(`/v1/scanners`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    deleteScanner: (id) =>
      j(`/v1/scanners/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    updateProjectScanner: (projectId, scannerId, body) =>
      j(`/v1/projects/${encodeURIComponent(projectId)}/scanners/${encodeURIComponent(scannerId)}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    getApprovalsSettings: (projectId) =>
      j(
        `/v1/settings/approvals${formatQuery({
          project_id: projectId,
        })}`,
      ),
    updateApprovalsSettings: (req, projectId) =>
      j(
        `/v1/settings/approvals${formatQuery({
          project_id: projectId,
        })}`,
        {
          method: "PATCH",
          body: JSON.stringify(req),
        },
      ),
    getElevatedAccess: (id) => j(`/v1/sessions/${encodeURIComponent(id)}/elevated-access`),
    revokeElevatedAccess: (id) => j(`/v1/sessions/${encodeURIComponent(id)}/elevated-access/revoke`, { method: "POST" }),
    listApprovalGrants: (sessionId) =>
      j(`/v1/approval-grants${formatQuery({ session_id: sessionId })}`),
    listApprovalAsks: (days, projectId) =>
      j(`/v1/approval-asks${formatQuery({ days, project_id: projectId })}`),
    createApprovalGrant: (req) =>
      j("/v1/approval-grants", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    resolveSocketGrant: (req) =>
      j("/v1/approval-grants/resolve-socket", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    revokeApprovalGrants: (req) =>
      j("/v1/approval-grants/revoke", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    listDetectionPacks: (projectId) =>
      j(`/v1/detection-packs${formatQuery({ project_id: projectId })}`),
    updateDetectionPack: (packId, req) =>
      j(`/v1/detection-packs/${encodeURIComponent(packId)}`, jsonRequest("PATCH", req)),
    importDetectionPack: (req) =>
      j("/v1/detection-packs", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    deleteDetectionPack: (packId) =>
      j(`/v1/detection-packs/${encodeURIComponent(packId)}`, {
        method: "DELETE",
      }),
    getSecurityScannersSettings: () => j("/v1/settings/security-scanners"),
    updateSecurityScannersSettings: (req) =>
      j("/v1/settings/security-scanners", {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    openProjectTrustReview: (projectId) => j(`/v1/projects/${projectId}/trust/review`, { method: "POST" }),
    getProjectTrust: (projectId) => j(`/v1/projects/${projectId}/trust`),
    updateProjectTrust: (projectId, req) =>
      j(`/v1/projects/${projectId}/trust`, jsonRequest("PATCH", req)),
  };
}
