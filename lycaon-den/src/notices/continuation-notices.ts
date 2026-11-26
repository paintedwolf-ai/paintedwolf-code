import type { ContinuationNotice } from "../api/types.ts";
import { publishSessionHostError, withdrawNotice } from "./notice-store.ts";

const current = new Map<string, { revision: number; id?: string; project: string }>();

export function applyContinuationNotice(project: string, notice: ContinuationNotice): void {
  const session = notice.session_id;
  if ((current.get(session)?.revision ?? 0) >= notice.revision) return;
  current.set(session, { revision: notice.revision, id: notice.continuation_id, project });
  if (notice.host_error) {
    publishSessionHostError(notice.host_error, project, session);
  } else {
    withdrawNotice("coordinator_not_continued", { kind: "session", projectId: project, sessionId: session });
  }
}

export function currentContinuationId(session: string): string | undefined {
  return current.get(session)?.id;
}

export function resetContinuationNotices(): void {
  for (const [sessionId, { project: projectId, id }] of current) {
    if (id) withdrawNotice("coordinator_not_continued", { kind: "session", projectId, sessionId });
  }
  current.clear();
}
