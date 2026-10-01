import type { DraftStatus, Message } from "../../../api/types.ts";

/** The host stamps coordinator draft rows with kind=draft. */
export function isCoordinatorDraftMessage(msg: { kind?: string }): boolean {
  return msg.kind === "draft";
}

/** Identifies uncommitted coordinator answers from host-stamped fields. */
export function isCoordinatorDraftWire(
  msg: Pick<Message, "role" | "draft_status" | "kind" | "visibility">,
): boolean {
  if (msg.role !== "assistant") return false;
  if (
    msg.draft_status === "committed" ||
    msg.draft_status === "withdrawn" ||
    msg.draft_status === "rejected"
  ) {
    return false;
  }
  if (msg.draft_status === "live") return true;
  if (isCoordinatorDraftMessage(msg)) return true;
  // Guarded turns stream on internal visibility before commit-after-guard.
  if (msg.visibility === "internal") return true;
  return false;
}

/** True when a committed mid-run step is a host-stamped draft rail row (prose + tools). */
export function isCoordinatorIntermediateStep(
  msg: Pick<Message, "kind" | "draft_status" | "tool_calls">,
): boolean {
  if (msg.draft_status !== "committed") return false;
  if ((msg.tool_calls?.length ?? 0) === 0) return false;
  return msg.kind === "draft";
}

/** First-line plain-text preview for a collapsed draft. */
export function draftSummaryLine(text: string, maxLen = 120): string {
  const line = (text.split("\n")[0] ?? "").trim().replace(/\s+/g, " ");
  if (line.length <= maxLen) return line;
  return `${line.slice(0, maxLen - 1)}…`;
}

/** Wire-backed draft version metadata for transcript rows. */
export function draftVersionCount(msg: Pick<Message, "draft_version_count">): number {
  const count = msg.draft_version_count ?? 1;
  return count > 0 ? count : 1;
}

/** Prior reject count: `draft_version_count − 1`. */
export function coordinatorDraftPriorCount(
  msg: Pick<Message, "draft_version_count">,
): number {
  return Math.max(0, draftVersionCount(msg) - 1);
}

/** A draft with rejected prior versions; its closed rail shows `Draft versions (X)`. */
export function isCoordinatorDraftVariantB(
  msg: Pick<Message, "draft_version_count">,
): boolean {
  return coordinatorDraftPriorCount(msg) > 0;
}

export function draftWireFields(
  msg: Pick<Message, "draft_version_count" | "draft_status">,
): { draftVersionCount: number; draftStatus?: DraftStatus } {
  return {
    draftVersionCount: draftVersionCount(msg),
    draftStatus: msg.draft_status,
  };
}
