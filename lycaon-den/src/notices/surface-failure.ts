import { createEffect, on, untrack } from "solid-js";
import { LycaonApiError } from "../api/http.ts";
import { engineDown } from "../platform/connection/engine-supervision.ts";
import { ClientNoticeError } from "./client-notices.ts";
import { projectScope } from "./notice-scope.ts";
import { publishNotice, reportProjectNoticeError } from "./notice-store.ts";
import { shouldReportToNoticeRail } from "./report-policy.ts";

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

/**
 * Publishes a surface failure to its project's notices. A typed host error
 * keeps the host's copy unless the surface supplies `message`. Lost
 * connectivity and an engine the shell is restarting are reported by the
 * app, not by each surface that tried to reach it.
 */
export function reportSurfaceFailure(
  copy: SurfaceFailureCopy,
  failure: unknown,
  projectId: string,
  message?: string,
): void {
  if ((failure as { name?: string } | null)?.name === "AbortError") return;
  if (engineDown()) return;
  const code = (failure as { code?: string } | null)?.code;
  if (code === "source_view_not_found" || code === "source_workspace_mismatch") return;
  if (failure instanceof Error && !shouldReportToNoticeRail(failure)) return;
  if (message === undefined && (failure instanceof LycaonApiError || failure instanceof ClientNoticeError)) {
    reportProjectNoticeError(failure, projectId);
    return;
  }
  const text = message?.trim() || failureMessage(failure);
  if (!text) return;
  publishNotice(
    { code: copy.code, title: copy.title, message: text, suggestedAction: copy.suggestedAction },
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
