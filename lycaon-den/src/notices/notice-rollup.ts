import type { NoticeIndex } from "./notice-store.ts";
import type { AppNotice } from "./notice-model.ts";

/** Shared cap for notice lists. */
export const NOTICE_LIST_CAP = 4;

/** Overflow line for a capped list. */
export function andNMore(hidden: number): string {
  return `and ${hidden} more`;
}

/** Badge text: exact while it is worth reading, then clamped. */
export function countLabel(count: number): string {
  if (count <= 0) return "";
  return count > 9 ? "9+" : String(count);
}

function allNotices(index: NoticeIndex): AppNotice[] {
  const out: AppNotice[] = [];
  for (const rows of index.values()) out.push(...rows);
  return out;
}

/** Notices belonging to one chat. */
export function countForSession(index: NoticeIndex, sessionId: string): number {
  return allNotices(index).filter(
    (n) => n.scope.kind === "session" && n.scope.sessionId === sessionId,
  ).length;
}
