/** Row-continuity watch over the assembled list, which replay cannot see. */

import {
  denScrollDebugLog,
  isStreamScrollDebugEnabled,
} from "../../stream/den-scroll-debug.ts";
import { activitySpanEntryKey, type ActivitySpanEntry, type TranscriptItem } from "../projection/transcript-item-model.ts";

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

type ActivitySpanRow = { kind: ActivitySpanEntry["kind"]; source: string };
type ActivitySpanRowWithdrawal = RowWithdrawal & ActivitySpanRow;

const MAX_ACTIVITY_SPAN_SCOPES = 8;
const MAX_ACTIVITY_SPAN_ROWS = 4096;
const lastActivityRowsByScope = new Map<string, Map<string, ActivitySpanRow>>();

function visibleActivitySpanRows(
  entries: readonly ActivitySpanEntry[],
): Map<string, ActivitySpanRow> {
  return new Map(entries.map(entry => {
    const key = activitySpanEntryKey(entry);
    const source = entry.kind === "tool" ? entry.part.commandActivity?.calls[0]?.id ?? key : key;
    return [key, { kind: entry.kind, source }];
  }));
}

export function noteActivitySpanRowWithdrawals(
  scope: string,
  sources: readonly ActivitySpanEntry[],
  entries: readonly ActivitySpanEntry[],
): ActivitySpanRowWithdrawal[] {
  if (!isStreamScrollDebugEnabled()) {
    lastActivityRowsByScope.clear();
    return [];
  }
  const sourceKeys = new Set(sources.map(activitySpanEntryKey));
  const current = visibleActivitySpanRows(entries);
  const previous = lastActivityRowsByScope.get(scope) ?? new Map<string, ActivitySpanRow>();
  const gone: ActivitySpanRowWithdrawal[] = [];
  for (const [key, row] of previous) {
    if (current.has(key) || !sourceKeys.has(row.source)) continue;
    previous.delete(key);
    gone.push({ key, ...row });
    denScrollDebugLog("scroll", "row-withdrawn", {
      surface: "activity-span",
      row_key: key,
      kind: row.kind,
      source_key: row.source,
      rows: entries.length,
    });
  }
  for (const [key, row] of current) {
    previous.delete(key);
    previous.set(key, row);
  }
  while (previous.size > MAX_ACTIVITY_SPAN_ROWS) previous.delete(previous.keys().next().value!);
  lastActivityRowsByScope.delete(scope);
  lastActivityRowsByScope.set(scope, previous);
  while (lastActivityRowsByScope.size > MAX_ACTIVITY_SPAN_SCOPES) {
    lastActivityRowsByScope.delete(lastActivityRowsByScope.keys().next().value!);
  }
  return gone;
}
