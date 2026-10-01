import { formatQuery, type JsonRequester, jsonRequest } from "../http.ts";
import type {
  ProjectRemovalAssessment,
  ProjectRemovalRequest,
  ProjectRemovalResult,
  ProjectCostReport,
  CreateBlueprintRequest,
  UpdateBlueprintRequest,
  CreateProjectRequest,
  UpdateProjectRequest,
  PromoteProjectRequest,
  CloneProjectRequest,
  FolderDetect,
  AttachProjectRootRequest,
  UpdateProjectRootRequest,
  ProjectAgentContext,
  Blueprint,
  BlueprintApproveRequest,
  BlueprintSummary,
  BlueprintListResponse,
  LaunchBlueprintRequest,
  LaunchBlueprintResponse,
  Project,
  ProjectListResponse,
  ArtifactListResponse,
} from "../types.ts";

export interface ProjectsClient {
  listProjectArtifacts(
    projectId: string,
    query?: { limit?: number; cursor?: string },
  ): Promise<ArtifactListResponse>;
  /** Existing references retain the id and resolve to a deleted artifact. */
  deleteProjectArtifact(
    projectId: string,
    artifactId: string,
  ): Promise<void>;
  listProjects(): Promise<Project[]>;
  createProject(req: CreateProjectRequest): Promise<Project>;
  getProject(id: string): Promise<Project>;
  updateProject(id: string, req: UpdateProjectRequest): Promise<Project>;
  assessProjectRemoval(id: string): Promise<ProjectRemovalAssessment>;
  createProjectRemoval(id: string, request: ProjectRemovalRequest): Promise<ProjectRemovalResult>;
  getProjectRemoval(id: string, operationId: string): Promise<ProjectRemovalResult>;
  promoteProject(id: string, req: PromoteProjectRequest): Promise<Project>;
  cancelProjectPromotion(id: string): Promise<Project>;
  cloneProject(req: CloneProjectRequest): Promise<Project>;
  detectFolder(path: string): Promise<FolderDetect>;
  attachProjectRoot(id: string, req: AttachProjectRootRequest): Promise<Project>;
  detachProjectRoot(
    id: string,
    rootId: string,
    opts?: { force?: boolean },
  ): Promise<Project>;
  updateProjectRoot(id: string, rootId: string, req: UpdateProjectRootRequest): Promise<Project>;
  getProjectAgentContext(
    projectId: string,
    rootId: string,
    opts?: { path?: string; sessionId?: string },
  ): Promise<ProjectAgentContext>;
  listBlueprints(projectId: string, query?: { path?: string }): Promise<BlueprintSummary[]>;
  getBlueprint(projectId: string, blueprintId: string): Promise<Blueprint>;
  createBlueprint(projectId: string, req: CreateBlueprintRequest): Promise<Blueprint>;
  updateBlueprint(
    projectId: string,
    blueprintId: string,
    req: UpdateBlueprintRequest,
  ): Promise<Blueprint>;
  deleteBlueprint(projectId: string, blueprintId: string): Promise<void>;
  approveBlueprint(
    projectId: string,
    blueprintId: string,
    req: BlueprintApproveRequest,
  ): Promise<Blueprint>;
  launchBlueprint(
    projectId: string,
    blueprintId: string,
    req?: LaunchBlueprintRequest,
  ): Promise<LaunchBlueprintResponse>;
  getProjectCostReport(projectId: string, query?: { limit?: number; cursor?: string; search?: string; sort?: "cost" | "tokens" | "activity" | "id" }): Promise<ProjectCostReport>;
}

export function createProjectsClient(j: JsonRequester): ProjectsClient {
  return {
    listProjectArtifacts: (projectId, query) =>
      j(
        `/v1/projects/${projectId}/artifacts${formatQuery({
          limit: query?.limit,
          cursor: query?.cursor,
        })}`,
      ),
    deleteProjectArtifact: (projectId, artifactId) =>
      j(`/v1/projects/${projectId}/artifacts/${artifactId}`, {
        method: "DELETE",
      }),
    listProjects: async () => {
      const res = await j<ProjectListResponse>("/v1/projects");
      return res.projects;
    },
    createProject: (req) =>
      j("/v1/projects", jsonRequest("POST", req)),
    getProject: (id) => j(`/v1/projects/${id}`),
    updateProject: (id, req) =>
      j(`/v1/projects/${id}`, jsonRequest("PATCH", req)),
    assessProjectRemoval: (id) => j(`/v1/projects/${encodeURIComponent(id)}/removal-assessment`),
    createProjectRemoval: (id, req) => j(`/v1/projects/${encodeURIComponent(id)}/removals`, jsonRequest("POST", req)),
    getProjectRemoval: (id, operationId) => j(`/v1/projects/${encodeURIComponent(id)}/removals/${encodeURIComponent(operationId)}`),
    promoteProject: (id, req) =>
      j(`/v1/projects/${id}/promotion`, jsonRequest("POST", req)),
    cancelProjectPromotion: (id) =>
      j(`/v1/projects/${id}/promotion`, { method: "DELETE" }),
    cloneProject: (req) =>
      j("/v1/projects/clone", jsonRequest("POST", req)),
    detectFolder: (path) =>
      j(`/v1/projects/detect${formatQuery({ path })}`),
    attachProjectRoot: (id, req) =>
      j(`/v1/projects/${id}/roots`, jsonRequest("POST", req)),
    detachProjectRoot: (id, rootId, opts) =>
      j(`/v1/projects/${id}/roots/${rootId}${formatQuery({ force: opts?.force })}`, {
        method: "DELETE",
      }),
    updateProjectRoot: (id, rootId, req) =>
      j(`/v1/projects/${id}/roots/${rootId}`, jsonRequest("PATCH", req)),
    listBlueprints: async (projectId, query) => {
      const res = await j<BlueprintListResponse>(
        `/v1/projects/${projectId}/blueprints${formatQuery({ path: query?.path })}`,
      );
      return res.blueprints;
    },
    getBlueprint: (projectId, blueprintId) =>
      j(`/v1/projects/${projectId}/blueprints/${blueprintId}`),
    createBlueprint: (projectId, req) =>
      j(`/v1/projects/${projectId}/blueprints`, jsonRequest("POST", req)),
    updateBlueprint: (projectId, blueprintId, req) =>
      j(`/v1/projects/${projectId}/blueprints/${blueprintId}`, jsonRequest("PATCH", req)),
    deleteBlueprint: (projectId, blueprintId) =>
      j(`/v1/projects/${projectId}/blueprints/${blueprintId}`, { method: "DELETE" }),
    approveBlueprint: (projectId, blueprintId, req) =>
      j(
        `/v1/projects/${projectId}/blueprints/${blueprintId}/approve`,
        jsonRequest("POST", req),
      ),
    launchBlueprint: (projectId, blueprintId, req) =>
      j(
        `/v1/projects/${projectId}/blueprints/${blueprintId}/launch`,
        jsonRequest("POST", req ?? {}),
      ),

    getProjectCostReport: (projectId, query) =>
      j(
        `/v1/projects/${encodeURIComponent(projectId)}/cost-report${formatQuery({
          limit: query?.limit,
          cursor: query?.cursor,
          sort: query?.sort,
          q: (query as { q?: string })?.q ?? query?.search,
        })}`,
      ),
    getProjectAgentContext: (projectId, rootId, opts) => {
      const query = new URLSearchParams({ root_id: rootId });
      if (opts?.path) query.set("path", opts.path);
      if (opts?.sessionId) query.set("session_id", opts.sessionId);
      return j(`/v1/projects/${projectId}/agent-context?${query.toString()}`);
    },
  };
}
