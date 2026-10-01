import { formatQuery, type JsonRequester, jsonRequest } from "../http.ts";
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
      j(`/v1/projects/${projectId}/editor-documents/retention`, { ...jsonRequest("PUT", { client_id: clientId, document_ids: documentIds, retained_clients: retainedClients }), signal: AbortSignal.timeout(30_000) }),
    readEditorDocumentStatuses: (projectId, documentIds) =>
      j(`/v1/projects/${projectId}/editor-documents/status`, { ...jsonRequest("POST", { document_ids: documentIds }), signal: AbortSignal.timeout(30_000) }),
    openEditorDocument: (projectId, req, sessionId) =>
      j(`/v1/projects/${projectId}/editor-documents${formatQuery({ session_id: sessionId })}`, { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    replaceEditorDocument: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}`, { method: "PUT", body: JSON.stringify(req) }),
    submitEditorDocumentUpdate: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/updates`, { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    publishEditorDocumentPresence: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/presence`, { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    listEditorDocumentChanges: (projectId, documentId, cursor) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/changes${formatQuery({ cursor })}`),
    revertEditorDocumentChange: (projectId, documentId, changeId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/changes/${encodeURIComponent(changeId)}/revert`, jsonRequest("POST", req)),
    createEditorDocumentSnapshot: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/snapshots`, { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    resolveEditorDocumentConflict: (projectId, documentId, req, sessionId) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/resolve${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    syncEditorDocument: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/sync`, { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    leaveEditorDocument: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/leave`, { ...jsonRequest("POST", req), signal: AbortSignal.timeout(30_000) }),
    discardEditorDocument: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/discard`, jsonRequest("POST", req)),
    reloadEditorDocument: (projectId, documentId, req, sessionId) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/reload${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    observeEditorDocument: (projectId, documentId, req) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/observe`, jsonRequest("POST", req)),
    saveEditorDocument: (projectId, documentId, req, sessionId) =>
      j(`/v1/projects/${projectId}/editor-documents/${documentId}/save${formatQuery({ session_id: sessionId })}`, jsonRequest("POST", req)),
    previewEditorSecretMark: (projectId, documentId, range) =>
      j(
        `/v1/projects/${projectId}/editor-documents/${documentId}/secret-spans/preview`,
        jsonRequest("POST", range),
      ),
    markEditorSecret: (projectId, documentId, req) =>
      j(
        `/v1/projects/${projectId}/editor-documents/${documentId}/secret-spans/mark`,
        jsonRequest("POST", req),
      ),
  };
}
