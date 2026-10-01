import { formatQuery, type JsonRequester } from "../http.ts";
import type {
  SourcePinList,
  SourceWalkResponse,
  SourceWalkSummary,
  SourceFileVersionsResponse,
  SourceVersionRestoreRequest,
  SourceVersionRestoreResponse,
  SourceCommitRestoreRequest,
  SourceCommitRestoreResponse,
  FileBriefingRequest,
  FileBriefingResponse,
  SourcePresentationCompletion,
  SourceSeenList,
  SourceStorage,
  SourcePin,
  SourcePinCreate,
  SourcePinUpdate,
  SourceComparison,
  SourceAttributionResponse,
  WorkerJobChangesResponse,
  AgentPresenceSnapshot,
} from "../types.ts";

/** Path comparisons read working bytes; recorded scopes and steps use history. */
export type SourceComparisonTarget =
  /** markUserEdits false carries the person's own writes into the range start. */
  | { fileId: string; baseline: string; markUserEdits?: boolean; presentationAfterOrdinal?: number }
  | { fileId: string; reviewedThroughOrdinal: number }
  /** What one turn did to the file, from the state it found to the state it left. */
  | { fileId: string; sessionId: string; turn: number; markUserEdits?: boolean }
  | { path: string; rootId: string; baseline: "commit"; expectedHead?: string }
  | { effectId: string }
  | { versionId: string }
  | { blobOid: string; rootId: string; beforeBlobOid?: string; displayPath?: string };

/** The range endpoint has no turn form; a source view reads turns. */
export type SourceRangeTarget = Exclude<SourceComparisonTarget, { turn: number }>;

export interface SourceHistoryClient {
  getProjectSourceWalkSummary(projectId: string, sessionId: string, messageIds: string[]): Promise<import("../types.ts").SourceWalkTurnSummary[]>;
  getProjectSourceGitReview(projectId: string, changeId: string, opts?: {
    movement?: boolean; parent?: number; cursor?: string; limit?: number; sessionId?: string; signal?: AbortSignal;
  }): Promise<import("../types.ts").SourceGitReview>;
  /** Commits a typed commit, branch, or range names in each Git root; empty when it names nothing. */
  resolveProjectSourceRevisions(projectId: string, spec: string, opts?: {
    rootId?: string; sessionId?: string; signal?: AbortSignal;
  }): Promise<import("../types.ts").SourceRevisionsResponse>;
  /** One page of the files between two commits of a root's repository. */
  getProjectSourceRevisionReview(projectId: string, target: { rootId: string; beforeCommit?: string; afterCommit: string }, opts?: {
    cursor?: string; limit?: number; sessionId?: string; signal?: AbortSignal;
  }): Promise<import("../types.ts").SourceRevisionReview>;
  listProjectSourceWalk(
    projectId: string,
    opts?: {
      baseline?: string;
      cursor?: string;
      limit?: number;
      sessionId?: string;
      /** Session baseline includes unaffiliated outside changes within its span. */
      includeOutsideChanges?: boolean;
      /** False leaves the person's own writes out of the range. */
      markUserEdits?: boolean;
    },
  ): Promise<SourceWalkResponse>;
  listProjectSourceVersions(
    projectId: string,
    target: { fileId: string },
    opts?: {
      cursor?: string;
      limit?: number;
      lane?: "all" | "retained" | "git";
      sessionId?: string;
      signal?: AbortSignal;
    },
  ): Promise<SourceFileVersionsResponse>;
  restoreProjectSourceVersion(
    projectId: string,
    versionId: string,
    req: SourceVersionRestoreRequest,
    sessionId?: string,
  ): Promise<SourceVersionRestoreResponse>;
  restoreProjectSourceCommitState(
    projectId: string,
    commit: string,
    req: SourceCommitRestoreRequest,
    sessionId?: string,
  ): Promise<SourceCommitRestoreResponse>;
  getFileBriefing(
    projectId: string,
    opts: Omit<FileBriefingRequest, "trigger">,
  ): Promise<FileBriefingResponse>;
  requestFileBriefing(
    projectId: string,
    req: FileBriefingRequest,
  ): Promise<FileBriefingResponse>;
  completeProjectSourcePresentation(
    projectId: string,
    req: SourcePresentationCompletion,
  ): Promise<void>;
  /** Marks a file unseen by withdrawing the look the Seen list named. */
  withdrawProjectSourcePresentation(
    projectId: string,
    fileId: string,
    throughOrdinal: number,
  ): Promise<void>;
  listProjectSourceSeen(
    projectId: string,
    opts?: {
      cursor?: string;
      limit?: number;
      /** False ignores the person's own writes. */
      markUserEdits?: boolean;
      sessionId?: string;
    },
  ): Promise<SourceSeenList>;
  getProjectSourceStorage(projectId: string): Promise<SourceStorage>;
  listProjectSourcePins(
    projectId: string,
    opts?: { limit?: number; cursor?: string },
  ): Promise<SourcePinList>;
  createProjectSourcePin(
    projectId: string,
    req?: SourcePinCreate,
  ): Promise<SourcePin>;
  updateProjectSourcePin(
    projectId: string,
    pinId: string,
    req: SourcePinUpdate,
  ): Promise<SourcePin>;
  deleteProjectSourcePin(projectId: string, pinId: string): Promise<void>;
  getProjectSourceComparison(
    projectId: string,
    target: SourceRangeTarget,
    opts?: { sessionId?: string },
  ): Promise<SourceComparison>;
  getProjectSourceAttribution(
    projectId: string,
    opts: { path: string; rootId: string; sessionId?: string },
  ): Promise<SourceAttributionResponse>;
  getWorkerChanges(workerId: string): Promise<WorkerJobChangesResponse>;
  /** What each chat is reading and about to change in the project's files. */
  getAgentPresence(projectId: string): Promise<AgentPresenceSnapshot>;
}

function sourceComparisonQuery(target: SourceRangeTarget, sessionId?: string): string {
  return formatQuery("reviewedThroughOrdinal" in target
            ? { file_id: target.fileId, reviewed_through_ordinal: target.reviewedThroughOrdinal, session_id: sessionId }
            : "path" in target
            ? { path: target.path, root_id: target.rootId, baseline: "commit", expected_head: target.expectedHead, session_id: sessionId }
            : "effectId" in target
            ? { effect_id: target.effectId, session_id: sessionId }
            : "versionId" in target
              ? { version_id: target.versionId, session_id: sessionId }
            : "blobOid" in target
              ? {
                  blob_oid: target.blobOid,
                  before_blob_oid: target.beforeBlobOid,
                  display_path: target.displayPath,
                  root_id: target.rootId,
                  session_id: sessionId,
                }
            : {
                file_id: target.fileId,
                baseline: target.baseline,
                presentation_after_ordinal: target.presentationAfterOrdinal,
                mark_user_edits: target.markUserEdits === false ? "false" : undefined,
                session_id: sessionId,
              });
}

export function createSourceHistoryClient(j: JsonRequester): SourceHistoryClient {
  return {
    getProjectSourceWalkSummary: async (projectId, sessionId, messageIds) => (await j<SourceWalkSummary>(`/v1/projects/${projectId}/source/walk-summary${formatQuery({ session_id: sessionId, message_ids: messageIds.join(",") })}`)).turns,
    getProjectSourceGitReview: (projectId, changeId, opts) => j(
      `/v1/projects/${projectId}/source/git-changes/${encodeURIComponent(changeId)}/review${formatQuery({
        movement: opts?.movement == null ? undefined : String(opts.movement), parent: opts?.parent == null ? undefined : String(opts.parent),
        cursor: opts?.cursor, limit: opts?.limit == null ? undefined : String(opts.limit), session_id: opts?.sessionId,
      })}`, { signal: opts?.signal }),
    resolveProjectSourceRevisions: (projectId, spec, opts) => j(
      `/v1/projects/${projectId}/source/revisions${formatQuery({ spec, root_id: opts?.rootId, session_id: opts?.sessionId })}`,
      { signal: opts?.signal }),
    getProjectSourceRevisionReview: (projectId, target, opts) => j(
      `/v1/projects/${projectId}/source/revisions/review${formatQuery({
        root_id: target.rootId,
        from_revision: target.beforeCommit || undefined,
        to_revision: target.afterCommit,
        cursor: opts?.cursor, limit: opts?.limit == null ? undefined : String(opts.limit), session_id: opts?.sessionId,
      })}`, { signal: opts?.signal }),
    listProjectSourceWalk: (projectId, opts) =>
      j(
        `/v1/projects/${projectId}/source/walk${formatQuery({
          baseline: opts?.baseline,
          cursor: opts?.cursor,
          limit: opts?.limit != null ? String(opts.limit) : undefined,
          session_id: opts?.sessionId,
          include_outside_changes: opts?.includeOutsideChanges ? "true" : undefined,
          mark_user_edits: opts?.markUserEdits === false ? "false" : undefined,
        })}`,
      ),
    listProjectSourceVersions: (projectId, target, opts) =>
      j(
        `/v1/projects/${projectId}/source/versions${formatQuery({
          file_id: target.fileId,
          cursor: opts?.cursor,
          limit: opts?.limit != null ? String(opts.limit) : undefined,
          lane: opts?.lane,
          session_id: opts?.sessionId,
        })}`,
        { signal: opts?.signal },
      ),
    restoreProjectSourceVersion: (projectId, versionId, req, sessionId) =>
      j(
        `/v1/projects/${projectId}/source/versions/${encodeURIComponent(versionId)}/restore${formatQuery({ session_id: sessionId })}`,
        { method: "POST", body: JSON.stringify(req) },
      ),
    restoreProjectSourceCommitState: (projectId, commit, req, sessionId) =>
      j(
        `/v1/projects/${projectId}/source/commits/${encodeURIComponent(commit)}/restore${formatQuery({ session_id: sessionId })}`,
        { method: "POST", body: JSON.stringify(req) },
      ),
    getFileBriefing: (projectId, opts) =>
      j(
        `/v1/projects/${projectId}/source/file-briefings${formatQuery({
          root_id: opts.root_id,
          path: opts.path,
          presentation: opts.presentation,
          worker_id: opts.worker_id,
          document_id: opts.document_id,
          document_revision:
            opts.document_revision != null
              ? String(opts.document_revision)
              : undefined,
          version_id: opts.version_id,
        })}`,
      ),
    requestFileBriefing: (projectId, req) =>
      j(`/v1/projects/${projectId}/source/file-briefings`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    completeProjectSourcePresentation: (projectId, req) =>
      j(`/v1/projects/${projectId}/source/seen`, {
        method: "POST",
        body: JSON.stringify(req),
      }),
    withdrawProjectSourcePresentation: (projectId, fileId, throughOrdinal) =>
      j(
        `/v1/projects/${projectId}/source/seen/${encodeURIComponent(fileId)}${formatQuery({
          through_ordinal: String(throughOrdinal),
        })}`,
        { method: "DELETE" },
      ),
    listProjectSourceSeen: (projectId, opts) =>
      j(
        `/v1/projects/${projectId}/source/seen${formatQuery({
          cursor: opts?.cursor,
          limit: opts?.limit != null ? String(opts.limit) : undefined,
          mark_user_edits: opts?.markUserEdits === false ? "false" : undefined,
          session_id: opts?.sessionId,
        })}`,
      ),
    getProjectSourceStorage: (projectId) =>
      j(`/v1/projects/${projectId}/source/storage`),
    listProjectSourcePins: (projectId, opts) =>
      j<SourcePinList>(
        `/v1/projects/${projectId}/source/pins${formatQuery(opts ?? {})}`,
      ),
    createProjectSourcePin: (projectId, req) =>
      j(`/v1/projects/${projectId}/source/pins`, {
        method: "POST",
        body: JSON.stringify(req ?? {}),
      }),
    updateProjectSourcePin: (projectId, pinId, req) =>
      j(`/v1/projects/${projectId}/source/pins/${encodeURIComponent(pinId)}`, {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    deleteProjectSourcePin: (projectId, pinId) =>
      j(`/v1/projects/${projectId}/source/pins/${encodeURIComponent(pinId)}`, {
        method: "DELETE",
      }),
    getProjectSourceComparison: (projectId, target, opts) =>
      j(`/v1/projects/${projectId}/source/comparison${sourceComparisonQuery(target, opts?.sessionId)}`),
    getProjectSourceAttribution: (projectId, opts) =>
      j(
        `/v1/projects/${projectId}/source/attribution${formatQuery({
          path: opts.path,
          root_id: opts.rootId,
          session_id: opts.sessionId,
        })}`,
      ),
    getWorkerChanges: (workerId) => j(`/v1/workers/${encodeURIComponent(workerId)}/changes`),
    getAgentPresence: (projectId) =>
      j(`/v1/projects/${encodeURIComponent(projectId)}/agent-presence`),

  };
}
