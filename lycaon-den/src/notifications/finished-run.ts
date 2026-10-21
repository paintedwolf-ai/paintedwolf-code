import type { AttentionRow, SessionBootstrap } from "../api/types.ts";

/** Only a settled host clock identifies a meaningful completed user turn. */
export function finishedRun(snapshot: SessionBootstrap, row: AttentionRow): { activeMs: number; identity: string } | undefined {
  const sessionId = row.session_id;
  const session = snapshot.session;
  const clock = snapshot.turn_clock;
  if (session.id !== sessionId || session.status !== "idle" || session.prompt_pending || session.archived_at
    || clock.session_id !== sessionId || clock.running || !clock.opening_message_id || !clock.settled_at) return undefined;
  const completed = Date.parse(row.since_at);
  if (!Number.isFinite(completed) || session.seen_at && Date.parse(session.seen_at) >= completed) return undefined;
  return { activeMs: clock.active_ms, identity: `${clock.opening_message_id}:${clock.settled_at}` };
}
