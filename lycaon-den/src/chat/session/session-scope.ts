/** Shell foreground binding — project id + session id. */
export type SessionScope = {
  projectId: string;
  projectDir?: string;
  sessionId: string;
  /** Session originated from a submitted Home draft. */
  initialPrompt?: true;
};

/** Foreground placeholder while POST /v1/sessions is in flight — not a real session id. */
export const SESSION_CREATE_PENDING_ID = "__session_create_pending__";

export function isPendingSessionId(sessionId: string | null | undefined): boolean {
  return sessionId?.trim() === SESSION_CREATE_PENDING_ID;
}
