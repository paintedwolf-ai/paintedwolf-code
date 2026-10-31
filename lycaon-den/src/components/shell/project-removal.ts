import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError, isApiErrorCode } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import type {
  ProjectBackgroundProcess,
  ProjectRemovalAssessment,
  ProjectRemovalRequest,
  ProjectRemovalProgress,
  ProjectRemovalResult,
} from "../../api/types.ts";
import type { ConfirmDestructivePresenter } from "../../platform/interaction/confirm-dialog.ts";
import {
  formatBackgroundCommandWithChat,
  sameRunningCommandList,
  type RunningCommandSummary,
} from "../../session/confirm-chat-delete.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { clientNoticeError } from "../../notices/client-notices.ts";
import type { NoticeInput } from "../../notices/notice-model.ts";

/** Failures after which the removal may still have settled on the host. */
const OUTCOME_UNKNOWN_CODES = ["internal_error", "host_fault"] as const;

/** Consecutive unanswered status reads before the outcome is reported as unconfirmed. */
const STATUS_TRANSPORT_RETRIES = 8;

const statusPause = (unanswered: number) =>
  new Promise<void>((resolve) => setTimeout(resolve, Math.min(15_000, 1000 * 2 ** Math.min(unanswered, 4))));

type RemovalClient = Pick<
  LycaonClient,
  "assessProjectRemoval" | "createProjectRemoval" | "getProjectRemoval"
> &
  Partial<Pick<LycaonClient, "listProjectBackgroundProcesses">>;

export function forceRemovalMessage(
  result: Pick<ProjectRemovalResult, "documents" | "sessions" | "workers" | "overlays"> & {
    background_processes?: readonly RunningCommandSummary[];
  },
  fallbackProcesses?: readonly RunningCommandSummary[],
): string {
  const bgProcesses = result.background_processes ?? fallbackProcesses ?? [];
  const parts = [
    [result.documents, "open or unsaved file"],
    [result.sessions, "active turn"],
    [result.workers, "worker"],
    [result.overlays, "unmerged worker result"],
    [bgProcesses.length, "background command"],
  ]
    .filter(([count]) => Number(count) > 0)
    .map(([count, label]) => `${count} ${label}${count === 1 ? "" : "s"}`);
  const summary = `${parts.join(", ") || "In-flight project work"} will be discarded or stopped. Any edits made before removal completes may also be lost. This cannot be undone.`;
  if (bgProcesses.length === 0) {
    return summary;
  }
  const lines = [
    summary,
    bgProcesses.length === 1
      ? "1 background command in this project will stop:"
      : `${bgProcesses.length} background commands in this project will stop:`,
    ...bgProcesses.map((proc) => `• ${formatBackgroundCommandWithChat(proc)}`),
  ];
  return lines.join("\n");
}

export type ProjectRemovalOptions = {
  confirmed?: boolean;
};

export function createProjectRemoval(options: {
  client: () => RemovalClient | null;
  confirm: ConfirmDestructivePresenter;
  review: (assessment: ProjectRemovalAssessment | null, projectId: string) => Promise<string[] | null>;
  prepare: (projectId: string) => Promise<void>;
  retire: (projectId: string) => Promise<void>;
  report: (error: unknown) => void;
  notify: (notice: NoticeInput) => void;
  extensionsChanged: () => void;
  progress?: (projectId: string, progress: ProjectRemovalProgress | null) => void;
  /** Waits before the next status read; `unanswered` counts consecutive transport gaps. */
  pause?: (unanswered: number) => Promise<void>;
  operationId?: () => string;
}) {
  const active = new Map<string, Promise<void>>();
  // An uncertain transport result keeps its identity for an exact retry.
  const pending = new Map<string, ProjectRemovalRequest>();
  const newID = options.operationId ?? (() => crypto.randomUUID());

  const review = async (
    client: RemovalClient,
    projectId: string,
    opts?: ProjectRemovalOptions,
  ): Promise<{ request: ProjectRemovalRequest; assessment: ProjectRemovalAssessment | null }> => {
    let assessment: ProjectRemovalAssessment | null = null;
    try {
      assessment = await client.assessProjectRemoval(projectId);
    } catch {
      // Deletion can proceed while keeping extensions.
    }
    for (;;) {
      const hasBackground = Boolean(
        assessment?.background_processes && assessment.background_processes.length > 0,
      );
      const skipReview = Boolean(
        opts?.confirmed && assessment && assessment.extensions.length === 0 && !hasBackground,
      );
      const selected = skipReview
        ? []
        : await options.review(assessment, projectId);
      if (selected === null) throw new RemovalCancelled();
      if (typeof client.listProjectBackgroundProcesses === "function") {
        let liveRunning: ProjectBackgroundProcess[] | null = null;
        try {
          const resp = await client.listProjectBackgroundProcesses(projectId);
          liveRunning = (resp.processes ?? []).filter((p) => p.running);
        } catch {
          liveRunning = null;
        }
        if (
          liveRunning !== null &&
          !sameRunningCommandList(assessment?.background_processes ?? [], liveRunning)
        ) {
          assessment = assessment
            ? { ...assessment, background_processes: liveRunning }
            : null;
          continue;
        }
      }
      return {
        request: {
          operation_id: newID(),
          assessment_token: assessment?.assessment_token ?? "",
          remove_extensions: selected,
          force: false,
        },
        assessment,
      };
    }
  };

  const readStatus = async (client: RemovalClient, projectId: string, operationId: string) => {
    let unanswered = 0;
    for (;;) {
      try {
        return await client.getProjectRemoval(projectId, operationId);
      } catch (cause) {
        if (cause instanceof BackendTransportError && ++unanswered < STATUS_TRANSPORT_RETRIES) {
          await (options.pause ?? statusPause)(unanswered);
          continue;
        }
        const error = clientNoticeError("project_removal_unconfirmed");
        error.cause = cause;
        throw error;
      }
    }
  };

  const execute = async (
    client: RemovalClient,
    projectId: string,
    req: ProjectRemovalRequest,
  ) => {
    pending.set(projectId, req);
    try {
      return await client.createProjectRemoval(projectId, req);
    } catch (error) {
      if (error instanceof LycaonApiError && error.code === "project_removal_assessment_changed") {
        pending.delete(projectId);
        throw error;
      }
      if (error instanceof LycaonApiError && !isApiErrorCode(error, OUTCOME_UNKNOWN_CODES)) {
        pending.delete(projectId);
        throw error;
      }
      // A lost acceptance response gets the same bounded status recovery as accepted work.
      return readStatus(client, projectId, req.operation_id);
    }
  };

  const settle = async (client: RemovalClient, projectId: string, req: ProjectRemovalRequest) => {
    let result = await execute(client, projectId, req);
    let lastProgress = "";
    while (result.progress) {
      const current = JSON.stringify(result.progress);
      if (current !== lastProgress) options.progress?.(projectId, result.progress);
      lastProgress = current;
      await (options.pause ?? statusPause)(0);
      result = await readStatus(client, projectId, req.operation_id);
    }
    return result;
  };

  const remove = async (
    projectId: string,
    opts?: ProjectRemovalOptions,
  ): Promise<void> => {
    const client = options.client();
    if (!client) {
      options.report(clientNoticeError("offline"));
      return;
    }
    try {
      let request = pending.get(projectId);
      let reviewedAssessment: ProjectRemovalAssessment | null = null;
      if (!request) {
        await options.prepare(projectId);
        const reviewed = await review(client, projectId, opts);
        request = reviewed.request;
        reviewedAssessment = reviewed.assessment;
      }
      let result = await settle(client, projectId, request);
      if (result.failure_code === "root_busy") {
        pending.delete(projectId);
        let fallbackBg: readonly RunningCommandSummary[] | undefined =
          result.background_processes ?? reviewedAssessment?.background_processes;
        for (;;) {
          const force = await options.confirm({
            title: "Delete project",
            message: forceRemovalMessage(result, fallbackBg),
            okLabel: "Delete anyway",
          });
          if (!force) return;
          if (typeof client.listProjectBackgroundProcesses === "function") {
            let liveRunning: ProjectBackgroundProcess[] | null = null;
            try {
              const resp = await client.listProjectBackgroundProcesses(projectId);
              liveRunning = (resp.processes ?? []).filter((p) => p.running);
            } catch {
              liveRunning = null;
            }
            if (
              liveRunning !== null &&
              !sameRunningCommandList(fallbackBg ?? [], liveRunning)
            ) {
              fallbackBg = liveRunning;
              result = { ...result, background_processes: liveRunning };
              continue;
            }
          }
          break;
        }
        request = { ...request, force: true, operation_id: newID() };
        result = await settle(client, projectId, request);
      }
      pending.delete(projectId);
      if (["removed", "interrupted", "failed"].includes(result.cleanup_state)) {
        options.extensionsChanged();
      }
      if (result.project_state === "deleted") {
        try {
          await options.retire(projectId);
        } catch (error) {
          options.report(error);
        }
      }
      if (result.reason || result.project_state !== "deleted") {
        const copy = CLIENT_NOTICES.project_removal_incomplete;
        options.notify({
          code: "project_removal_incomplete",
          title: copy.title,
          message: result.reason || copy.message,
          severity: result.project_state === "deleted" ? "warning" : "error",
        });
      }
    } catch (error) {
      if (!(error instanceof RemovalCancelled)) options.report(error);
    } finally {
      options.progress?.(projectId, null);
    }
  };

  let queue = Promise.resolve();
  return (projectId: string, opts?: ProjectRemovalOptions): Promise<void> => {
    const existing = active.get(projectId);
    if (existing) return existing;
    const task = queue.then(() => remove(projectId, opts)).finally(() => active.delete(projectId));
    active.set(projectId, task);
    queue = task.catch(() => undefined);
    return task;
  };
}

class RemovalCancelled extends Error {}
