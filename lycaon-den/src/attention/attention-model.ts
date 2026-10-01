import type { AttentionClass, AttentionReason, AttentionRow } from "../api/types.ts";

/** Ordering for every attention-aware surface. Lower sorts first. */
const ATTENTION_RANK: Record<AttentionClass, number> = {
  needs_you: 0,
  error: 1,
  finished: 2,
  running: 3,
};

/** Sessions the host did not list are idle. Rank them behind everything. */
const ATTENTION_RANK_IDLE = 4;

export function attentionRank(cls: AttentionClass | undefined): number {
  return cls ? ATTENTION_RANK[cls] : ATTENTION_RANK_IDLE;
}

/** Chip copy. Second person: the row is addressed to the reader. */
export const ATTENTION_CLASS_LABEL: Record<AttentionClass, string> = {
  needs_you: "Needs you",
  error: "Failed",
  finished: "Finished",
  running: "Running",
};

/** Reason copy for the one row slot that says why; each fits one line beside a title. */
const ATTENTION_REASON_LABEL: Record<AttentionReason, string> = {
  checkpoint: "Waiting on approval",
  ask: "Waiting on your answer",
  turn_running: "Working",
  turn_finished: "Finished while you were away",
  turn_error: "Last turn failed",
};

export function attentionReasonLabel(row: AttentionRow): string {
  return ATTENTION_REASON_LABEL[row.reason];
}

/** Only the blocked class is an obligation; the rest is status. */
export function isBlocking(cls: AttentionClass | undefined): boolean {
  return cls === "needs_you";
}

/**
 * Classes worth a dot on the control that reopens a hidden pane: an obligation,
 * or a result nobody has read. Running marks nothing.
 */
export function marksHiddenPane(cls: AttentionClass | undefined): boolean {
  return cls === "needs_you" || cls === "finished";
}

export type AttentionIndex = ReadonlyMap<string, AttentionRow>;

/** Index rows by session id so a surface can look up one chat in constant time. */
export function indexAttention(rows: readonly AttentionRow[]): AttentionIndex {
  const index = new Map<string, AttentionRow>();
  for (const row of rows) {
    if (!row.session_id) continue;
    index.set(row.session_id, row);
  }
  return index;
}

export function attentionFor(
  index: AttentionIndex | undefined,
  sessionId: string,
): AttentionRow | undefined {
  return index?.get(sessionId);
}

/** Suppress finished attention while its chat is readable; the host seen stamp may lag. */
export function withoutReadFinish(
  index: AttentionIndex,
  readableSessionId: string | null,
): AttentionIndex {
  if (!readableSessionId) return index;
  const row = index.get(readableSessionId);
  if (row?.class !== "finished") return index;
  const next = new Map(index);
  next.delete(readableSessionId);
  return next;
}

/** Rows that are actually asking something of the human. */
export function blockingRows(rows: readonly AttentionRow[]): AttentionRow[] {
  return rows.filter((r) => isBlocking(r.class));
}

function sinceMs(row: AttentionRow | undefined): number {
  if (!row?.since_at) return 0;
  const ms = Date.parse(row.since_at);
  return Number.isNaN(ms) ? 0 : ms;
}

/**
 * Compare two sessions by what they are asking of the reader: worst class
 * first, then longest-waiting first inside a class. Surfaces that mix host rows
 * with sessions the host never listed (idle recents) re-sort with this.
 */
export function compareAttention(
  a: AttentionRow | undefined,
  b: AttentionRow | undefined,
): number {
  const rank = attentionRank(a?.class) - attentionRank(b?.class);
  if (rank !== 0) return rank;
  if (!a || !b) return 0;
  return sinceMs(a) - sinceMs(b);
}

/** Coarse age for a row, e.g. "12m", with minute resolution. */
export function attentionAgeLabel(row: AttentionRow, nowMs: number): string {
  const started = sinceMs(row);
  if (!started) return "";
  const minutes = Math.floor((nowMs - started) / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}
