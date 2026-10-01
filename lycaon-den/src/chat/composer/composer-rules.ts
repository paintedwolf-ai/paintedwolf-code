import type { SidecarStatus } from "../../store/app-state-model.ts";
import type { SessionStatus, WorkerTask, WorkflowRun } from "../../api/types.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { isPendingSessionId } from "../session/session-scope.ts";

export type ComposerBlockReason =
  | "offline"
  | "no_session"
  | "session_preparing"
  | "no_provider"
  | "workflow_paused"
  | null;

/** Soft holds block Send while drafting and attachments remain available. */
export function isComposerSoftSendHold(
  reason: ComposerBlockReason,
): boolean {
  return reason === "session_preparing" || reason === "workflow_paused";
}

/** Drafting requires a backend, active session, and model provider. */
export function isComposerDisabled(
  sidecarStatus: SidecarStatus,
  sessionId?: string | null,
  activeWorkflow?: WorkflowRun | null,
  chatHydrationLock?: string | null,
  needsProvider = false,
  sessionStatus?: SessionStatus | null,
): boolean {
  return (
    composerComposeBlockReason(
      sidecarStatus,
      sessionId,
      activeWorkflow,
      chatHydrationLock,
      needsProvider,
      sessionStatus,
    ) != null
  );
}

/** Preparing and paused states hold Send without a hard rejection. */
export function isComposerSendBlocked(
  sidecarStatus: SidecarStatus,
  sessionId?: string | null,
  activeWorkflow?: WorkflowRun | null,
  chatHydrationLock?: string | null,
  needsProvider = false,
  sessionStatus?: SessionStatus | null,
): boolean {
  return (
    composerBlockReason(
      sidecarStatus,
      sessionId,
      activeWorkflow,
      chatHydrationLock,
      needsProvider,
      sessionStatus,
    ) != null
  );
}

/**
 * Hard blocks for drafting and attaching. Soft holds return null so chips and
 * draft text can land before the host is promptable.
 */
export function composerComposeBlockReason(
  sidecarStatus: SidecarStatus,
  sessionId?: string | null,
  activeWorkflow?: WorkflowRun | null,
  chatHydrationLock?: string | null,
  needsProvider = false,
  sessionStatus?: SessionStatus | null,
): ComposerBlockReason {
  const reason = composerBlockReason(
    sidecarStatus,
    sessionId,
    activeWorkflow,
    chatHydrationLock,
    needsProvider,
    sessionStatus,
  );
  if (isComposerSoftSendHold(reason)) return null;
  return reason;
}

export function composerBlockReason(
  sidecarStatus: SidecarStatus,
  sessionId?: string | null,
  activeWorkflow?: WorkflowRun | null,
  chatHydrationLock?: string | null,
  needsProvider = false,
  sessionStatus?: SessionStatus | null,
): ComposerBlockReason {
  if (!isBackendReachable(sidecarStatus)) return "offline";
  if (needsProvider) return "no_provider";
  if (
    chatHydrationLock?.trim() ||
    isPendingSessionId(sessionId) ||
    !sessionId?.trim()
  ) {
    return "no_session";
  }
  if (sessionStatus === "preparing") return "session_preparing";
  if (activeWorkflow?.status === "paused") return "workflow_paused";
  return null;
}

export function isExitSlashCommand(text: string): boolean {
  return /^\/exit\b/i.test(text.trim());
}

/** Hint when workflow is paused but child workers are still running. */
export function softPauseWorkerHint(
  activeWorkflow: WorkflowRun | null | undefined,
  workers: readonly WorkerTask[],
): string | null {
  if (activeWorkflow?.status !== "paused") return null;
  const running = workers.filter(
    (w) => w.status === "running" || w.status === "pending",
  );
  if (running.length === 0) return null;
  return `${running.length} worker${running.length === 1 ? "" : "s"} still running — wait or cancel from Workers.`;
}
