import type { WorkerTask } from "../../api/types.ts";
import type { AppStore, LlmTurnActivity, SessionActivity } from "../../store/app-state-model.ts";

const ACTIVE_WORKER_STATUSES = new Set(["pending", "running"]);

export function isWorkerTaskActive(
  status: WorkerTask["status"] | undefined,
): boolean {
  return status != null && ACTIVE_WORKER_STATUSES.has(status);
}

export function sessionActivityFor(
  appStore: AppStore,
  sessionId: string,
): SessionActivity {
  return appStore.state.sessionActivity[sessionId] ?? {};
}

export function llmTurnFor(
  appStore: AppStore,
  sessionId: string,
): LlmTurnActivity | undefined {
  return sessionActivityFor(appStore, sessionId).llmTurn;
}

/** True while a prompt this client sent is not yet reflected in session state. */
export function hasPromptInFlight(activity: SessionActivity | undefined): boolean {
  return (activity?.promptSubmissions?.length ?? 0) > 0;
}

/** True while no turn runs and a prompt from any client waits to start. */
export function isPromptStarting(appStore: AppStore, sessionId: string): boolean {
  const session = appStore.state.currentSession;
  const current = session?.id === sessionId;
  if (current && session.status === "busy") return false;
  return hasPromptInFlight(sessionActivityFor(appStore, sessionId)) ||
    (current && session.prompt_pending === true);
}

/** True while this session's turn, prompts, activities, or workers are live. */
export function isChatActivityLive(appStore: AppStore, sessionId: string): boolean {
  const state = appStore.state;
  const activity = sessionActivityFor(appStore, sessionId);
  return (
    activity.stopping === true ||
    hasPromptInFlight(activity) ||
    activity.llmTurn?.status === "active" ||
    Object.keys(activity.activities ?? {}).length > 0 ||
    (state.currentSession?.id === sessionId &&
      (state.currentSession.status === "busy" || state.currentSession.prompt_pending === true)) ||
    state.workers.some(
      (w) => w.parent_session_id === sessionId && isWorkerTaskActive(w.status),
    )
  );
}

/** Silent wake leases leave notice-rail announcements enabled. */
export function isAnySessionActivityLive(appStore: AppStore): boolean {
  return Object.values(appStore.state.sessionActivity).some(
    (activity) =>
      activity.stopping === true ||
      hasPromptInFlight(activity) ||
      activity.llmTurn?.status === "active" ||
      Object.values(activity.activities ?? {}).some(
        (lease) => lease.kind !== "awaiting_wake",
      ),
  );
}
