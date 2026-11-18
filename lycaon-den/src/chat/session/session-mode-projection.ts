import type { Session } from "../../api/types.ts";

export type SessionModeProjection = Partial<Pick<Session, "mode" | "started">>;

export function mergeSessionMode(session: Session, projection: SessionModeProjection): Session {
  // A snapshot can predate first admission or a successful upgrade.
  if (session.started && (projection.started === false || session.mode === "agent")) return session;
  const mode = projection.mode ?? session.mode;
  const started = projection.started ?? session.started;
  return mode === session.mode && started === session.started ? session : { ...session, mode, started };
}

export function sessionReceivesMode(session: Session, sessionId: string): boolean {
  return session.id === sessionId || session.root_session_id === sessionId;
}

export function replaceSessionWithMode(current: Session | undefined, snapshot: Session): Session;
export function replaceSessionWithMode(current: Session | undefined, snapshot: Session | undefined): Session | undefined;
export function replaceSessionWithMode(current: Session | undefined, snapshot: Session | undefined): Session | undefined {
  if (!current || !snapshot || current.id !== snapshot.id) return snapshot;
  const { mode, started } = mergeSessionMode(current, snapshot);
  return { ...snapshot, mode, started };
}
