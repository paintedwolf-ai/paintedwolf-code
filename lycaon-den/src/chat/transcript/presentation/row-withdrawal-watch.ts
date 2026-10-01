/** Row-continuity watch over the assembled list, which replay cannot see. */

import {
  denScrollDebugLog,
  isStreamScrollDebugEnabled,
} from "../../stream/den-scroll-debug.ts";
import type { ActivitySpanEntry, TranscriptItem } from "../projection/transcript-item-model.ts";

/** Tool rows from one message share a continuity key. */
function toolRowContinuityKey(partId: string): string {
  const value = String(partId);
  const cut = value.indexOf(":");
  return cut < 0 ? value : value.slice(0, cut);
}

function continuityKeyOfItem(item: TranscriptItem | ActivitySpanEntry): string {
  if (item.kind === "tool") return toolRowContinuityKey(item.part.id);
  return String(item.key);
}

/** Visible row kinds keyed by continuity unit. */
export function visibleRowContinuity(
  items: readonly TranscriptItem[],
): Map<string, string> {
  const rows = new Map<string, string>();
  for (const item of items) {
    if (item.kind === "activity_span") {
      for (const entry of item.entries) {
        rows.set(continuityKeyOfItem(entry), entry.kind);
      }
      continue;
    }
    if (item.kind === "worker_group") {
      for (const part of item.parts) {
        rows.set(toolRowContinuityKey(part.id), "tool");
      }
      continue;
    }
    rows.set(continuityKeyOfItem(item), item.kind);
  }
  return rows;
}

export type RowWithdrawal = { key: string; kind: string };

/** Return retained continuity units whose final row disappeared. */
export function withdrawnRows(
  previous: ReadonlyMap<string, string>,
  current: ReadonlyMap<string, string>,
  retained: ReadonlySet<string>,
): RowWithdrawal[] {
  const gone: RowWithdrawal[] = [];
  for (const [key, kind] of previous) {
    if (current.has(key) || !retained.has(key)) continue;
    gone.push({ key, kind });
  }
  return gone;
}

const lastRowsByScope = new Map<string, Map<string, string>>();

/** Tracks retained row withdrawals only during scroll debugging. */
export function noteTranscriptRowWithdrawals(
  scope: string,
  items: readonly TranscriptItem[],
  retainedIds: ReadonlySet<string>,
): RowWithdrawal[] {
  if (!isStreamScrollDebugEnabled()) {
    if (lastRowsByScope.size > 0) lastRowsByScope.clear();
    return [];
  }
  const current = visibleRowContinuity(items);
  const previous = lastRowsByScope.get(scope);
  lastRowsByScope.set(scope, current);
  if (!previous) return [];
  const gone = withdrawnRows(previous, current, retainedIds);
  for (const row of gone) {
    denScrollDebugLog("scroll", "row-withdrawn", {
      row_key: row.key,
      kind: row.kind,
      rows: items.length,
    });
  }
  return gone;
}
