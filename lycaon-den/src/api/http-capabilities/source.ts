import { sourceOperation } from "../source-operation.ts";
import { formatQuery, lycaonBlob, type JsonRequester, jsonRequest } from "../http.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import type {
  ProjectSourceReadResponse,
  SourceFormatSettings,
  ProjectSourceWriteResponse,
  SourceWorkspace,
  PutProjectSourceRequest,
  MakeProjectSourceEditableRequest,
  MakeProjectSourceEditableResponse,
  CreateProjectSourceEntryRequest,
  ProjectSourceEntryCreatedResponse,
  RenameProjectSourceRequest,
  CopyProjectSourceRequest,
  ProjectSourceLifecycleResponse,
  SourceHistoryState,
  SourceOperationList,
  SourceOperationStatus,
  SourceHistoryMutationRequest,
  SourceHistoryMutationResponse,
  SourceIndexSummary,
  SourceSearchResponse,
  SourceSymbolsResponse,
  SourceDefinitionRequest,
  SourceDefinitionResponse,
  SourceDirListing,
  OpenOutsideFileRequest,
  OpenChatLocationFileRequest,
  OpenedOutsideFile,
  SourceChatLocation,
  UpdateSourceChatLocationRequest,
} from "../types.ts";

/** Defaults to the primary root's top folder. */
export type SourceBrowseTarget = {
  rootId?: string;
  dir?: string;
  worktreeId?: string | readonly string[];
};

export interface SourceClient {
  getProjectSource(
    projectId: string,
    path: string,
    opts?: {
      fileId?: string;
      workerId?: string;
      rootId?: string;
      sessionId?: string;
      worktreeId?: string | readonly string[];
      decodeAs?: "utf-16le" | "utf-16be";
      includeDeleted?: boolean;
    },
  ): Promise<ProjectSourceReadResponse>;
  getProjectSourceFormatSettings(
    projectId: string,
    path: string,
    rootId: string,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<SourceFormatSettings>;
  getProjectSourceRaw(
    projectId: string,
    path: string,
    opts: {
      fileId?: string;
      rootId?: string;
      workerId?: string;
      sessionId?: string;
      worktreeId?: string | readonly string[];
      signal?: AbortSignal;
    },
  ): Promise<Blob>;
  replaceProjectSource(
    projectId: string,
    req: PutProjectSourceRequest,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<ProjectSourceWriteResponse>;
  makeProjectSourceEditable(
    projectId: string,
    req: MakeProjectSourceEditableRequest,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<MakeProjectSourceEditableResponse>;
  getSourceWorkspace(projectId: string, sessionId?: string, worktreeId?: string | readonly string[]): Promise<SourceWorkspace>;
  /** Adds a file the person picked outside the project to a chat's Files section. */
  openChatLocationFile(projectId: string, locationId: string, req: OpenChatLocationFileRequest): Promise<OpenedOutsideFile>;
  openOutsideFile(projectId: string, req: OpenOutsideFileRequest): Promise<OpenedOutsideFile>;
  /** Hides or shows a chat location; it revokes and deletes nothing. */
  updateSourceChatLocation(projectId: string, locationId: string, req: UpdateSourceChatLocationRequest): Promise<SourceChatLocation>;
  createProjectSourceEntry(
    projectId: string,
    req: CreateProjectSourceEntryRequest,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<ProjectSourceEntryCreatedResponse>;
  listSourceOperations(projectId: string): Promise<SourceOperationList>;
  getSourceOperation(projectId: string, id: string): Promise<SourceOperationStatus>;
  cancelSourceOperation(projectId: string, id: string): Promise<void>;
  retrySourceOperation(projectId: string, id: string): Promise<unknown>;
  getProjectSourceHistory(projectId: string): Promise<SourceHistoryState>;
  undoProjectSourceHistory(
    projectId: string,
    req: SourceHistoryMutationRequest,
    sessionId?: string,
  ): Promise<SourceHistoryMutationResponse>;
  redoProjectSourceHistory(
    projectId: string,
    req: SourceHistoryMutationRequest,
    sessionId?: string,
  ): Promise<SourceHistoryMutationResponse>;
  renameProjectSource(
    projectId: string,
    req: RenameProjectSourceRequest,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<ProjectSourceLifecycleResponse>;
  copyProjectSource(
    projectId: string,
    req: CopyProjectSourceRequest,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<ProjectSourceLifecycleResponse>;
  deleteProjectSource(
    projectId: string,
    path: string,
    opts: { operationId: string; rootId?: string; recursive?: boolean; sessionId?: string; worktreeId?: string | readonly string[] },
  ): Promise<void>;
  getProjectSourceIndex(
    projectId: string,
    opts?: { sessionId?: string; worktreeId?: string | readonly string[] },
  ): Promise<SourceIndexSummary>;
  searchProjectSource(
    projectId: string,
    query: string,
    opts?: {
      rootId?: string;
      limit?: number;
      cursor?: string;
      sessionId?: string;
      worktreeId?: string | readonly string[];
      signal?: AbortSignal;
    },
  ): Promise<SourceSearchResponse>;
  listProjectSourceSymbols(
    projectId: string,
    path: string,
    opts?: { rootId?: string; sessionId?: string; worktreeId?: string | readonly string[]; signal?: AbortSignal },
  ): Promise<SourceSymbolsResponse>;
  resolveProjectSourceDefinition(
    projectId: string,
    req: SourceDefinitionRequest,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<SourceDefinitionResponse>;
  browseProjectSource(
    projectId: string,
    target?: SourceBrowseTarget,
    sessionId?: string,
    worktreeId?: string | readonly string[],
  ): Promise<SourceDirListing>;
}

export function createSourceClient(j: JsonRequester, connection: BackendConnection): SourceClient {
  return {
    getProjectSource: (projectId, path, opts) =>
      j(
        `/v1/projects/${projectId}/source${formatQuery({
          file_id: opts?.fileId,
          path: opts?.fileId ? undefined : path,
          worker_id: opts?.fileId ? undefined : opts?.workerId,
          root_id: opts?.fileId ? undefined : opts?.rootId,
          session_id: opts?.sessionId,
          worktree_id: opts?.worktreeId,
          decode_as: opts?.decodeAs,
          include_deleted: opts?.includeDeleted,
        })}`,
      ),
    getProjectSourceFormatSettings: (projectId, path, rootId, sessionId, worktreeId) =>
      j(
        `/v1/projects/${projectId}/source/format-settings${formatQuery({
          path,
          root_id: rootId,
          session_id: sessionId,
          worktree_id: worktreeId,
        })}`,
      ),
    getProjectSourceRaw: (projectId, path, opts) =>
      lycaonBlob(
        connection,
        `/v1/projects/${projectId}/source/raw${formatQuery({
          file_id: opts?.fileId,
          path: opts?.fileId ? undefined : path,
          root_id: opts?.fileId ? undefined : opts?.rootId,
          worker_id: opts?.fileId ? undefined : opts?.workerId,
          session_id: opts?.sessionId,
          worktree_id: opts?.worktreeId,
        })}`,
        { signal: opts.signal },
      ),
    replaceProjectSource: (projectId, req, sessionId, worktreeId) =>
      j(`/v1/projects/${projectId}/source${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, {
        method: "PUT",
        body: JSON.stringify(req),
      }),
    makeProjectSourceEditable: (projectId, req, sessionId, worktreeId) =>
      j(`/v1/projects/${projectId}/source/editable${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, jsonRequest("POST", req)),
    getSourceWorkspace: (projectId, sessionId, worktreeId) =>
      j(`/v1/projects/${projectId}/source/workspace${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`),
    openChatLocationFile: (projectId, locationId, req) =>
      j(`/v1/projects/${projectId}/source/outside-locations/${encodeURIComponent(locationId)}/files`, jsonRequest("POST", req)),
    openOutsideFile: (projectId, req) =>
      j(`/v1/projects/${projectId}/source/outside-files`, { method: "POST", body: JSON.stringify(req) }),
    updateSourceChatLocation: (projectId, locationId, req) =>
      j(`/v1/projects/${projectId}/source/outside-locations/${locationId}`, { method: "PATCH", body: JSON.stringify(req) }),
    createProjectSourceEntry: (projectId, req, sessionId, worktreeId) =>
      sourceOperation(connection, projectId, req.operation_id, `/v1/projects/${projectId}/source${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    listSourceOperations: (projectId) => j(`/v1/projects/${projectId}/source/operations`),
    getSourceOperation: (projectId, id) => j(`/v1/projects/${projectId}/source/operations/${id}`),
    cancelSourceOperation: (projectId, id) => j(`/v1/projects/${projectId}/source/operations/${id}/cancel`, { method: "POST" }),
    retrySourceOperation: (projectId, id) => sourceOperation(connection, projectId, id,
      `/v1/projects/${projectId}/source/operations/${id}/retry`, { method: "POST" }),
    getProjectSourceHistory: (projectId) =>
      j(`/v1/projects/${projectId}/source/history`),
    undoProjectSourceHistory: (projectId, req, sessionId) =>
      sourceOperation(connection, projectId, req.operation_id, `/v1/projects/${projectId}/source/history/undo${formatQuery({ session_id: sessionId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    redoProjectSourceHistory: (projectId, req, sessionId) =>
      sourceOperation(connection, projectId, req.operation_id, `/v1/projects/${projectId}/source/history/redo${formatQuery({ session_id: sessionId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    renameProjectSource: (projectId, req, sessionId, worktreeId) =>
      sourceOperation(connection, projectId, req.operation_id, `/v1/projects/${projectId}/source/rename${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    copyProjectSource: (projectId, req, sessionId, worktreeId) =>
      sourceOperation(connection, projectId, req.operation_id, `/v1/projects/${projectId}/source/copy${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    deleteProjectSource: (projectId, path, opts) =>
      sourceOperation(connection, projectId, opts.operationId,
        `/v1/projects/${projectId}/source${formatQuery({
          path,
          operation_id: opts.operationId,
          root_id: opts?.rootId,
          recursive: opts?.recursive,
          session_id: opts?.sessionId,
          worktree_id: opts?.worktreeId,
        })}`,
        { method: "DELETE" },
      ),
    getProjectSourceIndex: (projectId, opts) =>
      j(
        `/v1/projects/${projectId}/source/index${formatQuery({ session_id: opts?.sessionId, worktree_id: opts?.worktreeId })}`,
      ),
    searchProjectSource: (projectId, query, opts) =>
      j(
        `/v1/projects/${projectId}/source/search${formatQuery({
          q: query,
          root_id: opts?.rootId,
          limit: opts?.limit,
          cursor: opts?.cursor,
          session_id: opts?.sessionId,
          worktree_id: opts?.worktreeId,
        })}`,
        { signal: opts?.signal },
      ),
    listProjectSourceSymbols: (projectId, path, opts) =>
      j(
        `/v1/projects/${projectId}/source/symbols${formatQuery({
          path,
          root_id: opts?.rootId,
          session_id: opts?.sessionId,
          worktree_id: opts?.worktreeId,
        })}`,
        { signal: opts?.signal },
      ),
    resolveProjectSourceDefinition: (projectId, req, sessionId, worktreeId) =>
      j(`/v1/projects/${projectId}/source/definition${formatQuery({ session_id: sessionId, worktree_id: worktreeId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    browseProjectSource: (projectId, target, sessionId, worktreeId) =>
      j(
        `/v1/projects/${projectId}/source/browse${formatQuery({
          root_id: target?.rootId,
          dir: target?.dir,
          session_id: sessionId,
          worktree_id: worktreeId ?? target?.worktreeId,
        })}`,
      ),
  };
}
