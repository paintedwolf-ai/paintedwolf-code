import { createEffect, on, untrack } from "solid-js";
import { LycaonApiError } from "../api/http.ts";
import { ClientNoticeError } from "./client-notices.ts";
import { projectScope } from "./notice-scope.ts";
import { publishNotice, reportProjectNoticeError } from "./notice-store.ts";

/** Stable copy for one surface's failures; the code merges its repeats into one row. */
export type SurfaceFailureCopy = {
  code: string;
  title: string;
  suggestedAction: string;
};

function failureMessage(failure: unknown): string {
  if (typeof failure === "string") return failure.trim();
  if (failure instanceof Error) return failure.message.trim();
  return "";
}

/** Publishes a surface failure to its project's notices. A typed host error keeps the host's copy. */
export function reportSurfaceFailure(
  copy: SurfaceFailureCopy,
  failure: unknown,
  projectId: string,
): void {
  if ((failure as { name?: string } | null)?.name === "AbortError") return;
  const code = (failure as { code?: string } | null)?.code;
  if (code === "source_view_not_found" || code === "source_workspace_mismatch") return;
  if (failure instanceof LycaonApiError || failure instanceof ClientNoticeError) {
    reportProjectNoticeError(failure, projectId);
    return;
  }
  const message = failureMessage(failure);
  if (!message) return;
  publishNotice(
    { code: copy.code, title: copy.title, message, suggestedAction: copy.suggestedAction },
    projectScope(projectId),
  );
}

/** Reports each new failure a component holds as state; clearing it, or having no project, reports nothing. */
export function observeSurfaceFailure(
  copy: SurfaceFailureCopy,
  failure: () => unknown,
  projectId: () => string | undefined,
): void {
  createEffect(
    on(failure, (value) => {
      if (!value) return;
      untrack(() => {
        const project = projectId();
        if (project) reportSurfaceFailure(copy, value, project);
      });
    }),
  );
}
