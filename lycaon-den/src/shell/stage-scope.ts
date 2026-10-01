import { isPendingSessionId } from "../chat/session/session-scope.ts";
import type { AppState } from "../store/app-state-model.ts";
import type { ShellStoreState } from "../store/shell-store.ts";

export type StagePhase =
  | "home"
  | "opening"
  | "switching"
  | "ready"
  | "project-empty";

export type StageScope = {
  project_id: string | null;
  session_id: string | null;
  phase: StagePhase;
};

export type ActiveChat = {
  projectId: string;
  sessionId: string;
};

/** Reuse the object when ids are unchanged so keyed Show keeps the same node. */
export function stabilizeActiveChat(
  prev: ActiveChat | null | undefined,
  projectId: string | null,
  sessionId: string | null,
  ready: boolean,
): ActiveChat | null {
  if (!ready || !projectId || !sessionId) return null;
  if (prev && prev.projectId === projectId && prev.sessionId === sessionId) {
    return prev;
  }
  return { projectId, sessionId };
}

export type ChatPresence = {
  projectId: string | null;
  present: boolean;
};

/** Hold presence through a same-project switch. A project change starts over. */
export function holdChatPresence(
  prev: ChatPresence | undefined,
  scope: StageScope,
): ChatPresence {
  if (scope.phase === "ready") {
    return { projectId: scope.project_id, present: true };
  }
  if (
    scope.phase === "switching" &&
    prev?.present === true &&
    prev.projectId === scope.project_id
  ) {
    return prev;
  }
  return { projectId: scope.project_id, present: false };
}

export type PresentedChatFacts = {
  currentSessionId: string | undefined;
  transcriptSessionId: string | undefined;
  hydrationLock: string | undefined;
  projectId: string | null;
};

/** Keep the outgoing chat mounted until the store names the incoming session. */
export function holdPresentedChat(
  previous: ActiveChat | null | undefined,
  next: ActiveChat | null,
  facts: PresentedChatFacts,
): ActiveChat | null {
  const current = facts.currentSessionId?.trim() || undefined;
  const transcript = facts.transcriptSessionId?.trim() || undefined;
  const incomingBound =
    next != null &&
    current === next.sessionId &&
    (transcript == null || transcript === next.sessionId);
  if (incomingBound) return next;

  const projectId = facts.projectId?.trim() || undefined;
  const lock = facts.hydrationLock?.trim();
  const sameProject =
    previous != null && projectId != null && previous.projectId === projectId;
  if (!sameProject) return next;

  // Same-project hydrate and create hold until the incoming session is bound.
  if (lock || next == null || current !== next.sessionId) return previous;
  return next;
}

/** One derived navigation tuple for Shell render gates — see docs/project-space.md. */
export function deriveStageScope(
  shell: Pick<ShellStoreState, "activeProjectId" | "foreground" | "opening">,
  appStore: Pick<AppState, "currentSession" | "chatHydrationLock">,
): StageScope {
  const project_id = shell.activeProjectId;

  if (!project_id) {
    // A workspace on its way to a project id is neither Home nor a project.
    const phase = shell.opening ? "opening" : "home";
    return { project_id: null, session_id: null, phase };
  }

  const fg = shell.foreground;
  const lock = appStore.chatHydrationLock?.trim();

  if (lock === "*") {
    return { project_id, session_id: null, phase: "switching" };
  }

  if (fg?.projectId && fg.projectId !== project_id) {
    return { project_id, session_id: null, phase: "switching" };
  }

  if (isPendingSessionId(fg?.sessionId)) {
    return { project_id, session_id: null, phase: "switching" };
  }

  if (!fg?.projectId || !fg.sessionId) {
    return { project_id, session_id: null, phase: "project-empty" };
  }

  const session_id = fg.sessionId;

  // Session-scoped lock: hydrate in place.
  if (lock && lock !== "*") {
    return { project_id, session_id, phase: "ready" };
  }

  // SSE apply gates on currentSession.id; ready only when it matches foreground.
  const currentId = appStore.currentSession?.id?.trim();
  if (currentId !== session_id) {
    return { project_id, session_id: null, phase: "switching" };
  }

  return { project_id, session_id, phase: "ready" };
}
