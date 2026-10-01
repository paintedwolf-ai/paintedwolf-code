import {
  formatQuery,
  lycaonBlob,
  lycaonDownload,
  lycaonFetch,
  type JsonRequester,
  jsonRequest,
} from "../http.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";
import type {
  BoardView,
  CostSummary,
  SessionTranscriptPage,
  CheckpointEvent,
  CheckpointListResponse,
  CheckpointResponse,
  ResolveCheckpointRequest,
  BackgroundProcess,
  BackgroundProcessListResponse,
  BackgroundProcessOutput,
  AttachmentUploadResponse,
  CreateComposerSecretRequest,
  ManagedSecret,
  PromptRequest,
  PromptAcceptedResponse,
  AbortSessionRequest,
  RewindSessionRequest,
  RewindSessionResponse,
  RewindPreviewRequest,
  RewindPreviewResponse,
  Session,
  SessionBootstrap,
  SessionListOrder,
  SessionListPage,
  SessionListSort,
  SessionCompactResponse,
  SessionContextResponse,
  FindingsDigest,
  DraftVersionsResponse,
  ProgressDigest,
  ArtifactListResponse,
  InvocationReceiptList,
  QueueDraft,
  QueueMutateRequest,
  BackgroundProcessStopResult,
  PreviewWatchRequest,
  PreviewWatchResult,
  PreviewAttachment,
  PreviewListResponse,
  CoordinatorRunContext,
  CreateSessionRequest,
  UpdateSessionRequest,
  WorkerTask,
  WorkerListResponse,
} from "../types.ts";

export interface SessionsClient {
  createSession(req: CreateSessionRequest): Promise<Session>;
  /** Aborting a caller leaves shared reads running. */
  getSessionBootstrap(id: string, opts?: { signal?: AbortSignal }): Promise<SessionBootstrap>;
  getSession(id: string): Promise<Session>;
  /** Each call changes exactly one of title, archived, or pinned. */
  updateSession(id: string, req: UpdateSessionRequest): Promise<Session>;
  /** Refused with session_not_idle while a turn is running. */
  deleteSession(id: string): Promise<void>;
  /** Pages a project's chats, worker children excluded; `pinned` narrows to either group. */
  listProjectSessions(
    projectId: string,
    opts?: {
      archived?: boolean;
      pinned?: boolean;
      q?: string;
      sort?: SessionListSort;
      order?: SessionListOrder;
      limit?: number;
      cursor?: string;
    },
  ): Promise<SessionListPage>;
  resolveMessageNavigation(sessionId: string, req: import("../types.ts").ResolveMessageNavigationRequest, signal?: AbortSignal): Promise<import("../types.ts").MessageNavigationResponse>;
  listSessionMessages(
    sessionId: string,
    opts?: {
      limit?: number;
      before?: string;
      after?: string;
      beforeMessageId?: string;
      afterMessageId?: string;
      from?: "newest" | "oldest";
      workerId?: string;
    },
  ): Promise<SessionTranscriptPage>;
  /** Reads a byte range, or the rows at or around one row or byte offset. */
  getChatContent(
    sessionId: string,
    messageId: string,
    reference: import("../types.ts").ChatContentReference,
    position: { offset: number; limit: number } | { row: number } | { locate: number },
    signal?: AbortSignal,
  ): Promise<import("../types.ts").ChatContentPage>;
  searchChatContent(sessionId: string, messageId: string, reference: import("../types.ts").ChatContentReference, query: string, cursor: string | number | undefined, caseSensitive: boolean, signal?: AbortSignal): Promise<import("../types.ts").ChatContentSearchPage>;
  sendPrompt(sessionId: string, req: PromptRequest): Promise<PromptAcceptedResponse>;
  createComposerSecret(sessionId: string, req: CreateComposerSecretRequest): Promise<ManagedSecret>;
  abortSession(sessionId: string, req?: AbortSessionRequest): Promise<Session>;
  /** Stamps the session read as of now, clearing its finished attention row. */
  markSessionSeen(sessionId: string): Promise<Session>;
  previewSessionRewind(sessionId: string, req: RewindPreviewRequest): Promise<RewindPreviewResponse>;
  /** Reverses the reviewed source suffix and removes its transcript rows. */
  rewindSession(
    sessionId: string,
    req: RewindSessionRequest,
  ): Promise<RewindSessionResponse>;
  streamSession(
    sessionId: string,
    opts?: { messageId?: string; signal?: AbortSignal },
  ): Promise<Response>;
  compactSession(sessionId: string): Promise<SessionCompactResponse>;
  getSessionContext(sessionId: string): Promise<SessionContextResponse>;
  getSessionFindings(sessionId: string): Promise<FindingsDigest>;
  listDraftVersions(sessionId: string, slotId: string): Promise<DraftVersionsResponse>;
  getSessionProgress(sessionId: string): Promise<ProgressDigest>;
  getSessionArtifact(sessionId: string, artifactId: string): Promise<Blob>;
  /** Every artifact of the session tree, following pages. */
  listSessionArtifacts(sessionId: string): Promise<ArtifactListResponse>;
  listSessionInvocations(sessionId: string, opts?: { limit?: number; cursor?: string }): Promise<InvocationReceiptList>;
  createSessionArtifact(
    sessionId: string,
    recording: Blob,
    meta: {
      pageId: string;
      assistantMessageId: string;
      toolCallId: string;
      recordedAt: string;
      durationMs: number;
      /** Idempotency key for one upload. */
      operationId: string;
    },
  ): Promise<import("../types.ts").VisualArtifact>;
  listSessionPreviews(sessionId: string): Promise<PreviewAttachment[]>;
  /** Toggle CDP screencast for the live preview pane (read-only mirror). */
  watchPreview(
    sessionId: string,
    req: PreviewWatchRequest,
  ): Promise<PreviewWatchResult>;
  getSessionQueue(sessionId: string): Promise<QueueDraft>;
  updateSessionQueue(
    sessionId: string,
    req: QueueMutateRequest,
  ): Promise<QueueDraft>;
  getCoordinatorContext(sessionId: string): Promise<CoordinatorRunContext>;
  listCheckpoints(sessionId: string, opts?: { status?: string; kind?: string; includeChildren?: boolean }): Promise<CheckpointEvent[]>;
  resolveCheckpoint(
    sessionId: string,
    checkpointId: string,
    req: ResolveCheckpointRequest,
  ): Promise<CheckpointResponse>;
  stopBackgroundProcess(
    sessionId: string,
    processId: string,
  ): Promise<BackgroundProcessStopResult>;
  /** Stream one attachment body to project host data; returns the handle a prompt references. */
  uploadAttachment(
    projectId: string,
    filename: string,
    mime: string,
    body: Blob,
  ): Promise<AttachmentUploadResponse>;
  listWorkers(
    projectId: string,
    query?: { sessionId?: string },
  ): Promise<WorkerTask[]>;
  cancelWorker(id: string): Promise<void>;
  getBoard(projectId: string, sessionId: string): Promise<BoardView>;
  listSessionBackgroundProcesses(sessionId: string): Promise<BackgroundProcess[]>;
  getSessionBackgroundProcessOutput(
    sessionId: string,
    processId: string,
  ): Promise<BackgroundProcessOutput>;
  getCostSummary(session?: string, project?: string): Promise<CostSummary>;
  /** Download one session transcript as Markdown or JSON. */
  exportSessionTranscript(
    sessionId: string,
    format: "md" | "json",
  ): Promise<{ blob: Blob; filename: string }>;
}

export function createSessionsClient(j: JsonRequester, connection: BackendConnection): SessionsClient {
  return {
    createSession: (req) =>
      j("/v1/sessions", { method: "POST", body: JSON.stringify(req) }),
    getSessionBootstrap: (id, opts) =>
      j(`/v1/sessions/${id}/bootstrap`, opts?.signal ? { signal: opts.signal } : undefined),
    getSession: (id) => j(`/v1/sessions/${id}`),
    updateSession: (id, req) =>
      j(`/v1/sessions/${id}`, jsonRequest("PATCH", req)),
    deleteSession: (id) => j(`/v1/sessions/${id}`, { method: "DELETE" }),
    listProjectSessions: (projectId, opts) =>
      j(
        `/v1/projects/${projectId}/sessions${formatQuery({
          archived: opts?.archived,
          pinned: opts?.pinned,
          q: opts?.q,
          sort: opts?.sort,
          order: opts?.order,
          limit: opts?.limit,
          cursor: opts?.cursor,
        })}`,
      ),
    resolveMessageNavigation: (sessionId, req, signal) =>
      j(`/v1/sessions/${sessionId}/navigation`, { ...jsonRequest("POST", req), signal }),
    listSessionMessages: (sessionId, opts) =>
      j(
        `/v1/sessions/${sessionId}/messages${formatQuery({
          limit: opts?.limit,
          before: opts?.before,
          after: opts?.after,
          before_message_id: opts?.beforeMessageId,
          after_message_id: opts?.afterMessageId,
          from: opts?.from,
          worker_id: opts?.workerId,
        })}`,
      ),
    getChatContent: (sessionId, messageId, reference, position, signal) =>
      j(`/v1/sessions/${sessionId}/messages/${messageId}/content${formatQuery({ field: reference.field, tool_call_id: reference.tool_call_id, sha256: reference.sha256, ...position })}`, { signal }),
    searchChatContent: (sessionId, messageId, reference, query, cursor, caseSensitive, signal) =>
      j(`/v1/sessions/${sessionId}/messages/${messageId}/content/search${formatQuery({ field: reference.field, tool_call_id: reference.tool_call_id, sha256: reference.sha256, q: query, cursor, case_sensitive: caseSensitive })}`, { signal }),
    sendPrompt: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/prompts`, jsonRequest("POST", req)),
    createComposerSecret: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/composer-secrets`, jsonRequest("POST", req)),
    abortSession: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/abort`, jsonRequest("POST", req ?? {})),
    previewSessionRewind: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/rewind/preview`, jsonRequest("POST", req)),
    rewindSession: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/rewind`, jsonRequest("POST", req)),
    markSessionSeen: (sessionId) =>
      j(`/v1/sessions/${sessionId}/seen`, { method: "POST" }),
    streamSession: (sessionId, opts) =>
      lycaonFetch(
        connection,
        `/v1/sessions/${sessionId}/stream${formatQuery({ message: opts?.messageId })}`,
        { signal: opts?.signal },
      ),
    compactSession: (sessionId) =>
      j(`/v1/sessions/${sessionId}/compact`, { method: "POST" }),
    getSessionContext: (sessionId) => j(`/v1/sessions/${sessionId}/context`),
    getSessionFindings: (sessionId) => j(`/v1/sessions/${sessionId}/findings`),
    listDraftVersions: (sessionId, slotId) =>
      j(`/v1/sessions/${sessionId}/drafts/${slotId}/versions`),
    getSessionProgress: (sessionId) => j(`/v1/sessions/${sessionId}/progress`),
    getSessionArtifact: (sessionId, artifactId) =>
      lycaonBlob(
        connection,
        `/v1/sessions/${sessionId}/artifacts/${artifactId}`,
        { cache: "no-store" },
      ),
    listSessionArtifacts: async (sessionId) => {
      const artifacts: ArtifactListResponse["artifacts"] = [];
      let cursor: string | undefined;
      do {
        const page = await j<ArtifactListResponse>(
          `/v1/sessions/${sessionId}/artifacts${formatQuery({ cursor, limit: 500 })}`,
        );
        artifacts.push(...page.artifacts);
        cursor = page.next_cursor || undefined;
      } while (cursor);
      return { artifacts };
    },
    listSessionInvocations: (sessionId, opts) =>
      j(
        `/v1/sessions/${sessionId}/invocations${formatQuery({
          limit: opts?.limit,
          cursor: opts?.cursor,
        })}`,
      ),
    createSessionArtifact: (sessionId, recording, meta) =>
      j(
        `/v1/sessions/${sessionId}/artifacts${formatQuery({
          page_id: meta.pageId,
          assistant_message_id: meta.assistantMessageId,
          tool_call_id: meta.toolCallId,
          operation_id: meta.operationId,
          recorded_at: meta.recordedAt,
          duration_ms: Math.max(0, Math.round(meta.durationMs)),
        })}`,
        {
          method: "POST",
          headers: { "Content-Type": recording.type || "video/mp4" },
          body: recording,
        },
      ),
    getSessionQueue: (sessionId) => j(`/v1/sessions/${sessionId}/queue`),
    updateSessionQueue: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/queue`, {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    getCoordinatorContext: (sessionId) =>
      j(`/v1/sessions/${sessionId}/coordinator-context`),
    listCheckpoints: async (sessionId, opts) => {
      const res = await j<CheckpointListResponse>(
        `/v1/sessions/${sessionId}/checkpoints${formatQuery({
          status: opts?.status,
          kind: opts?.kind,
          include_children: opts?.includeChildren,
        })}`,
      );
      return res.checkpoints;
    },
    resolveCheckpoint: (sessionId, checkpointId, req) =>
      j(
        `/v1/sessions/${sessionId}/checkpoints/${checkpointId}`,
        jsonRequest("POST", req),
      ),

    stopBackgroundProcess: (sessionId, processId) =>
      j(`/v1/sessions/${sessionId}/background/${encodeURIComponent(processId)}/stop`, {
        method: "POST",
      }),
    listSessionBackgroundProcesses: async (sessionId) => {
      const res = await j<BackgroundProcessListResponse>(`/v1/sessions/${sessionId}/background`);
      return res.processes;
    },
    getSessionBackgroundProcessOutput: (sessionId, processId) =>
      j(
        `/v1/sessions/${sessionId}/background/${encodeURIComponent(processId)}/output`,
      ),
    listSessionPreviews: async (sessionId) => {
      const res = await j<PreviewListResponse>(`/v1/sessions/${sessionId}/previews`);
      return res.previews;
    },

    watchPreview: (sessionId, req) =>
      j(`/v1/sessions/${sessionId}/previews/watch`, jsonRequest("POST", req)),

    uploadAttachment: (projectId, filename, mime, body) =>
      j(`/v1/projects/${projectId}/attachments${formatQuery({ filename, mime: mime || undefined })}`, {
        method: "POST",
        headers: { "Content-Type": "application/octet-stream" },
        body,
      }),
    listWorkers: async (projectId, query) => {
      const workers: WorkerTask[] = [];
      let cursor: string | undefined;
      do {
        const page = await j<WorkerListResponse>(
          `/v1/workers${formatQuery({ project_id: projectId, session_id: query?.sessionId, cursor, limit: 500 })}`,
        );
        workers.push(...page.workers);
        cursor = page.next_cursor || undefined;
      } while (cursor);
      return workers;
    },
    cancelWorker: (id) => j(`/v1/workers/${id}/cancel`, { method: "POST" }),

    getBoard: (projectId, sessionId) =>
      j(
        `/v1/projects/${projectId}/board${formatQuery({
          session_id: sessionId,
        })}`,
      ),

    getCostSummary: (session, project) =>
      j(
        `/v1/cost/summary${formatQuery({
          session_id: session,
          project_id: project,
        })}`,
      ),
    exportSessionTranscript: async (sessionId, format) => {
      const download = await lycaonDownload(
        connection,
        `/v1/sessions/${sessionId}/export${formatQuery({ format })}`,
        `conversation.${format === "json" ? "json" : "md"}`,
      );
      return { blob: download.blob, filename: download.filename };
    },
  };
}
