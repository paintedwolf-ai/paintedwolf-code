import { formatQuery, type JsonRequester, jsonRequest } from "../http.ts";
import { operationPath } from "../operations.generated.ts";
import type {
  EditorReplicaFrame,
  EditorDocument,
  EditorDocumentChanges,
  RevertEditorDocumentChangeRequest,
  OpenEditorDocumentRequest,
  ReplaceEditorDocumentRequest,
  EditorDocumentCommandRequest,
  ObserveEditorDocumentRequest,
  SyncEditorDocumentRequest,
  ResolveEditorDocumentRequest,
  SubmitEditorDocumentUpdateRequest,
  UpdateEditorDocumentPresenceRequest,
  ReplaceProjectViewRequest,
  PinEditorDocumentRequest,
  LeaveEditorDocumentRequest,
  SaveEditorDocumentRequest,
  ManagedSecret,
  SecretMarkPreview,
  SecretMarkRange,
  SecretMarkRequest,
} from "../types.ts";

export interface DocumentsClient {
  replaceEditorDocumentRetention(projectId: string, clientId: string, documentIds: string[], retainedClients?: string[]): Promise<void>;
  replaceProjectView(projectId: string, req: ReplaceProjectViewRequest): Promise<void>;
  readEditorDocumentStatuses(projectId: string, documentIds: string[]): Promise<import("../types.ts").EditorDocumentStatuses>;
  openEditorDocument(projectId: string, req: OpenEditorDocumentRequest, sessionId?: string): Promise<EditorDocument>;
  replaceEditorDocument(projectId: string, documentId: string, req: ReplaceEditorDocumentRequest): Promise<EditorDocument>;
  submitEditorDocumentUpdate(projectId: string, documentId: string, req: SubmitEditorDocumentUpdateRequest): Promise<EditorReplicaFrame>;
  publishEditorDocumentPresence(projectId: string, documentId: string, req: UpdateEditorDocumentPresenceRequest): Promise<void>;
  listEditorDocumentChanges(projectId: string, documentId: string, cursor?: string): Promise<EditorDocumentChanges>;
  revertEditorDocumentChange(projectId: string, documentId: string, changeId: string, req: RevertEditorDocumentChangeRequest): Promise<EditorDocument>;
  createEditorDocumentSnapshot(projectId: string, documentId: string, req: PinEditorDocumentRequest): Promise<EditorDocument>;
  resolveEditorDocumentConflict(projectId: string, documentId: string, req: ResolveEditorDocumentRequest, sessionId?: string): Promise<EditorDocument>;
  syncEditorDocument(projectId: string, documentId: string, req: SyncEditorDocumentRequest): Promise<EditorReplicaFrame>;
  leaveEditorDocument(projectId: string, documentId: string, req: LeaveEditorDocumentRequest): Promise<void>;
  discardEditorDocument(projectId: string, documentId: string, req: EditorDocumentCommandRequest): Promise<EditorDocument>;
  reloadEditorDocument(projectId: string, documentId: string, req: EditorDocumentCommandRequest, sessionId?: string): Promise<EditorDocument>;
  observeEditorDocument(projectId: string, documentId: string, req: ObserveEditorDocumentRequest): Promise<EditorDocument>;
  saveEditorDocument(projectId: string, documentId: string, req: SaveEditorDocumentRequest, sessionId?: string): Promise<EditorDocument>;
  /** Report what marking a rune range in this document would capture. Nothing is stored. */
  previewEditorSecretMark(
    projectId: string,
    documentId: string,
    range: SecretMarkRange,
  ): Promise<SecretMarkPreview>;
  /** Mint a project-scoped managed secret from bytes already in this file. The file is not modified. */
  markEditorSecret(
    projectId: string,
    documentId: string,
    req: SecretMarkRequest,
  ): Promise<ManagedSecret>;

}

export function createDocumentsClient(j: JsonRequester): DocumentsClient {
  return {
    replaceEditorDocumentRetention: (projectId, clientId, documentIds, retainedClients) =>
      j(operationPath("replaceEditorDocumentRetention", { id: projectId }), { ...jsonRequest("PUT", { client_id: clientId, document_ids: documentIds, retained_clients: retainedClients }), signal: AbortSignal.timeout(30_000) }),
    replaceProjectView: (projectId, req) =>
      j(operationPath("replaceProjectView", { id: projectId }), { ...jsonRequest("PUT", req), keepalive: true, signal: AbortSignal.timeout(30_000) }),
    readEditorDocumentStatuses: (projectId, documentIds) =>
      j(operationPath("readEditorDocumentStatuses", { id: projectId }), { ...jsonRequest("POST", { document_ids: documentIds }), signal: AbortSignal.timeout(30_000) }),
    openEditorDocument: (projectId, req, sessionId) =>
      j(`${operationPath("openEditorDocument", { id: projectId })}${formatQuery({ session_id: sessionId })}`, { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    replaceEditorDocument: (projectId, documentId, req) =>
      j(operationPath("replaceEditorDocument", { id: projectId, document_id: documentId }), { method: "PUT", body: JSON.stringify(req) }),
    submitEditorDocumentUpdate: (projectId, documentId, req) =>
      j(operationPath("submitEditorDocumentUpdate", { id: projectId, document_id: documentId }), { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    publishEditorDocumentPresence: (projectId, documentId, req) =>
      j(operationPath("publishEditorDocumentPresence", { id: projectId, document_id: documentId }), { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    listEditorDocumentChanges: (projectId, documentId, cursor) =>
      j(`${operationPath("listEditorDocumentChanges", { id: projectId, document_id: documentId })}${formatQuery({ cursor })}`),
    revertEditorDocumentChange: (projectId, documentId, changeId, req) =>
      j(operationPath("revertEditorDocumentChange", { id: projectId, document_id: documentId, change_id: changeId }), jsonRequest("POST", req)),
    createEditorDocumentSnapshot: (projectId, documentId, req) =>
      j(operationPath("createEditorDocumentSnapshot", { id: projectId, document_id: documentId }), { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    resolveEditorDocumentConflict: (projectId, documentId, req, sessionId) =>
      j(`${operationPath("resolveEditorDocumentConflict", { id: projectId, document_id: documentId })}${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    syncEditorDocument: (projectId, documentId, req) =>
      j(operationPath("syncEditorDocument", { id: projectId, document_id: documentId }), { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    leaveEditorDocument: (projectId, documentId, req) =>
      // Keepalive lets a leave sent while the page unloads still arrive.
      j(operationPath("leaveEditorDocument", { id: projectId, document_id: documentId }), { ...jsonRequest("POST", req), keepalive: true, signal: AbortSignal.timeout(30_000) }),
    discardEditorDocument: (projectId, documentId, req) =>
      j(operationPath("discardEditorDocument", { id: projectId, document_id: documentId }), jsonRequest("POST", req)),
    reloadEditorDocument: (projectId, documentId, req, sessionId) =>
      j(`${operationPath("reloadEditorDocument", { id: projectId, document_id: documentId })}${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    observeEditorDocument: (projectId, documentId, req) =>
      j(operationPath("observeEditorDocument", { id: projectId, document_id: documentId }), jsonRequest("POST", req)),
    saveEditorDocument: (projectId, documentId, req, sessionId) =>
      j(`${operationPath("saveEditorDocument", { id: projectId, document_id: documentId })}${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    previewEditorSecretMark: (projectId, documentId, range) =>
      j(
        operationPath("previewEditorSecretMark", { id: projectId, document_id: documentId }),
        jsonRequest("POST", range),
      ),
    markEditorSecret: (projectId, documentId, req) =>
      j(
        operationPath("markEditorSecret", { id: projectId, document_id: documentId }),
        jsonRequest("POST", req),
      ),
  };
}
