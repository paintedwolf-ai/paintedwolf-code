import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError, isApiErrorCode } from "../../api/http.ts";
import type {
  ProjectRemovalAssessment,
  ProjectRemovalRequest,
  ProjectRemovalResult,
} from "../../api/types.ts";
import type { ConfirmDestructivePresenter } from "../../platform/interaction/confirm-dialog.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { clientNoticeError } from "../../notices/client-notices.ts";
import type { NoticeInput } from "../../notices/notice-model.ts";

/** Failures after which the removal may still have settled on the host. */
const OUTCOME_UNKNOWN_CODES = ["internal_error", "host_fault"] as const;

type RemovalClient = Pick<
  LycaonClient,
  "assessProjectRemoval" | "createProjectRemoval" | "getProjectRemoval"
>;

export function forceRemovalMessage(
  result: Pick<ProjectRemovalResult, "documents" | "sessions" | "workers" | "overlays">,
): string {
  const parts = [
    [result.documents, "open or unsaved file"],
    [result.sessions, "active turn"],
    [result.workers, "worker"],
    [result.overlays, "unmerged worker result"],
  ]
    .filter(([count]) => Number(count) > 0)
    .map(([count, label]) => `${count} ${label}${count === 1 ? "" : "s"}`);
  return `${parts.join(", ") || "In-flight project work"} will be discarded or stopped. Any edits made before removal completes may also be lost. This cannot be undone.`;
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
  ): Promise<ProjectRemovalRequest> => {
    let assessment: ProjectRemovalAssessment | null = null;
    try {
      assessment = await client.assessProjectRemoval(projectId);
    } catch {
      // Deletion can proceed while keeping extensions.
    }
    const skipReview = Boolean(opts?.confirmed && assessment && assessment.extensions.length === 0);
    const selected = skipReview
      ? []
      : await options.review(assessment, projectId);
    if (selected === null) throw new RemovalCancelled();
    return {
      operation_id: newID(),
      assessment_token: assessment?.assessment_token ?? "",
      remove_extensions: selected,
      force: false,
    };
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
      // The host may have settled after the response was lost.
      try {
        return await client.getProjectRemoval(projectId, req.operation_id);
      } catch {
        const notice = clientNoticeError("project_removal_unconfirmed");
        notice.cause = error;
        throw notice;
      }
    }
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
      if (!request) {
        await options.prepare(projectId);
        request = await review(client, projectId, opts);
      }
      let result = await execute(client, projectId, request);
      if (result.failure_code === "root_busy") {
        pending.delete(projectId);
        const force = await options.confirm({
          title: "Delete project",
          message: forceRemovalMessage(result),
          okLabel: "Delete anyway",
        });
        if (!force) return;
        request = { ...request, force: true, operation_id: newID() };
        result = await execute(client, projectId, request);
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
