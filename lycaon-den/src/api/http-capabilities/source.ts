import { sourceOperation } from "../source-operation.ts";
import { formatQuery, lycaonBlob, type JsonRequester, jsonRequest } from "../http.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import type {
  ProjectSourceReadResponse,
  SourceEditorConfig,
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
} from "../types.ts";

/** Defaults to the primary root's top folder. */
export type SourceBrowseTarget = {
  rootId?: string;
  dir?: string;
};

export interface SourceClient {
  getProjectSource(
    projectId: string,
    path: string,
    opts?: {
      workerId?: string;
      rootId?: string;
      sessionId?: string;
      decodeAs?: "utf-16le" | "utf-16be";
      includeDeleted?: boolean;
    },
  ): Promise<ProjectSourceReadResponse>;
  getProjectSourceEditorConfig(
    projectId: string,
    path: string,
    rootId: string,
    sessionId?: string,
  ): Promise<SourceEditorConfig>;
  getProjectSourceRaw(
    projectId: string,
    path: string,
    opts: {
      rootId?: string;
      workerId?: string;
      sessionId?: string;
      signal?: AbortSignal;
    },
  ): Promise<Blob>;
  replaceProjectSource(
    projectId: string,
    req: PutProjectSourceRequest,
    sessionId?: string,
  ): Promise<ProjectSourceWriteResponse>;
  makeProjectSourceEditable(
    projectId: string,
    req: MakeProjectSourceEditableRequest,
    sessionId?: string,
  ): Promise<MakeProjectSourceEditableResponse>;
  getSourceWorkspace(projectId: string, sessionId?: string): Promise<SourceWorkspace>;
  createProjectSourceEntry(
    projectId: string,
    req: CreateProjectSourceEntryRequest,
    sessionId?: string,
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
  ): Promise<ProjectSourceLifecycleResponse>;
  copyProjectSource(
    projectId: string,
    req: CopyProjectSourceRequest,
    sessionId?: string,
  ): Promise<ProjectSourceLifecycleResponse>;
  deleteProjectSource(
    projectId: string,
    path: string,
    opts: { operationId: string; rootId?: string; recursive?: boolean; sessionId?: string },
  ): Promise<void>;
  getProjectSourceIndex(
    projectId: string,
    opts?: { sessionId?: string },
  ): Promise<SourceIndexSummary>;
  searchProjectSource(
    projectId: string,
    query: string,
    opts?: {
      rootId?: string;
      limit?: number;
      cursor?: string;
      sessionId?: string;
      signal?: AbortSignal;
    },
  ): Promise<SourceSearchResponse>;
  listProjectSourceSymbols(
    projectId: string,
    path: string,
    opts?: { rootId?: string; sessionId?: string; signal?: AbortSignal },
  ): Promise<SourceSymbolsResponse>;
  resolveProjectSourceDefinition(
    projectId: string,
    req: SourceDefinitionRequest,
    sessionId?: string,
  ): Promise<SourceDefinitionResponse>;
  browseProjectSource(
    projectId: string,
    target?: SourceBrowseTarget,
    sessionId?: string,
  ): Promise<SourceDirListing>;
}

export function createSourceClient(j: JsonRequester, connection: BackendConnection): SourceClient {
  return {
    getProjectSource: (projectId, path, opts) =>
      j(
        `/v1/projects/${projectId}/source${formatQuery({
          path,
          worker_id: opts?.workerId,
          root_id: opts?.rootId,
          session_id: opts?.sessionId,
          decode_as: opts?.decodeAs,
          include_deleted: opts?.includeDeleted,
        })}`,
      ),
    getProjectSourceEditorConfig: (projectId, path, rootId, sessionId) =>
      j(
        `/v1/projects/${projectId}/source/editorconfig${formatQuery({
          path,
          root_id: rootId,
          session_id: sessionId,
        })}`,
      ),
    getProjectSourceRaw: (projectId, path, opts) =>
      lycaonBlob(
        connection,
        `/v1/projects/${projectId}/source/raw${formatQuery({
          path,
          root_id: opts?.rootId,
          worker_id: opts?.workerId,
          session_id: opts?.sessionId,
        })}`,
        { signal: opts.signal },
      ),
    replaceProjectSource: (projectId, req, sessionId) =>
      j(`/v1/projects/${projectId}/source${formatQuery({ session_id: sessionId })}`, {
        method: "PUT",
        body: JSON.stringify(req),
      }),
    makeProjectSourceEditable: (projectId, req, sessionId) =>
      j(`/v1/projects/${projectId}/source/editable${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    getSourceWorkspace: (projectId, sessionId) =>
      j(`/v1/projects/${projectId}/source/workspace${formatQuery({ session_id: sessionId })}`),
    createProjectSourceEntry: (projectId, req, sessionId) =>
      sourceOperation(connection, projectId, req.operation_id,`/v1/projects/${projectId}/source${formatQuery({ session_id: sessionId })}`, {
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
      sourceOperation(connection, projectId, req.operation_id,`/v1/projects/${projectId}/source/history/undo${formatQuery({ session_id: sessionId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    redoProjectSourceHistory: (projectId, req, sessionId) =>
      sourceOperation(connection, projectId, req.operation_id,`/v1/projects/${projectId}/source/history/redo${formatQuery({ session_id: sessionId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    renameProjectSource: (projectId, req, sessionId) =>
      sourceOperation(connection, projectId, req.operation_id,`/v1/projects/${projectId}/source/rename${formatQuery({ session_id: sessionId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    copyProjectSource: (projectId, req, sessionId) =>
      sourceOperation(connection, projectId, req.operation_id,`/v1/projects/${projectId}/source/copy${formatQuery({ session_id: sessionId })}`, {
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
        })}`,
        { method: "DELETE" },
      ),
    getProjectSourceIndex: (projectId, opts) =>
      j(
        `/v1/projects/${projectId}/source/index${formatQuery({ session_id: opts?.sessionId })}`,
      ),
    searchProjectSource: (projectId, query, opts) =>
      j(
        `/v1/projects/${projectId}/source/search${formatQuery({
          q: query,
          root_id: opts?.rootId,
          limit: opts?.limit,
          cursor: opts?.cursor,
          session_id: opts?.sessionId,
        })}`,
        { signal: opts?.signal },
      ),
    listProjectSourceSymbols: (projectId, path, opts) =>
      j(
        `/v1/projects/${projectId}/source/symbols${formatQuery({
          path,
          root_id: opts?.rootId,
          session_id: opts?.sessionId,
        })}`,
        { signal: opts?.signal },
      ),
    resolveProjectSourceDefinition: (projectId, req, sessionId) =>
      j(`/v1/projects/${projectId}/source/definition${formatQuery({ session_id: sessionId })}`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    browseProjectSource: (projectId, target, sessionId) =>
      j(
        `/v1/projects/${projectId}/source/browse${formatQuery({
          root_id: target?.rootId,
          dir: target?.dir,
          session_id: sessionId,
        })}`,
      ),
  };
}
