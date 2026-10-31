import type { BackgroundProcessTerminationReason } from "../api/types.ts";

const TERMINATION_REASON_LABELS: Record<
  BackgroundProcessTerminationReason,
  string
> = {
  stopped_by_user: "Stopped by you",
  stopped_by_agent: "Stopped by the agent",
  root_detached: "Stopped when the folder was removed",
  chat_deleted: "Stopped when the chat was deleted",
  project_deleted: "Stopped when the project was deleted",
  app_quit: "Ended when Painted Wolf Code quit",
  worker_completed: "Stopped when the worker finished",
  worker_cancelled: "Stopped when the worker was cancelled",
  timed_out: "Timed out",
  exited: "Exited",
};

/**
 * Formats a host-supplied background command termination reason in plain words.
 * Never infers a stop reason from process output or exit codes.
 */
export function formatBackgroundTerminationReason(
  reason?: BackgroundProcessTerminationReason,
): string {
  if (!reason) return "Ended";
  return TERMINATION_REASON_LABELS[reason] ?? "Ended";
}
