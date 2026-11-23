import { createComputed, untrack } from "solid-js";
import { fileEditPathKey } from "../../file-edit/file-edit-fold.ts";
import { activitySpanEntryKey, activitySpanToolParts } from "../../tool/activity-span-model.ts";
import { transcriptDisclosureKey, type TranscriptDisclosureKey } from "../presentation/transcript-disclosure-key.ts";
import { ACTIVITY_SPAN_ENTRY_ESTIMATE_PX } from "../presentation/transcript-row-presentation.ts";
import {
  ACTIVITY_GROUP_KEY_PREFIX,
  activityGroupContainsMessage,
  activityGroupMembers,
  measureActivityGroupMemberOffsetPx,
  transcriptItemActionToolCallIds,
  transcriptToolTarget,
  visibleTranscriptToolTarget,
} from "./activity-group-anchors.ts";
import type { ActivityGroupItem, TranscriptItem } from "./transcript-item-model.ts";

export { transcriptItemActionToolCallIds, transcriptToolTarget, visibleTranscriptToolTarget };

export type TranscriptSpanState = {
  entryToSpanKey: ReadonlyMap<string, string>;
  spanEntries: ReadonlyMap<string, readonly string[]>;
  memberToGroupKey: ReadonlyMap<string, string>;
  groupEntries: ReadonlyMap<string, readonly string[]>;
  groupFoldPaths: ReadonlyMap<string, readonly string[]>;
  firstRowKey?: string;
};

export const EMPTY_TRANSCRIPT_SPAN_STATE: TranscriptSpanState = {
  entryToSpanKey: new Map(),
  spanEntries: new Map(),
  memberToGroupKey: new Map(),
  groupEntries: new Map(),
  groupFoldPaths: new Map(),
};

export type ResolvedTranscriptAnchor = {
  rowKey: string;
  offsetPx: number;
  measureOffsetPx?: () => number | undefined;
};

export function transcriptItemToolCallIds(item: TranscriptItem): string | undefined {
  if (item.kind === "activity_group") {
    const ids = new Set<string>();
    for (const part of activitySpanToolParts(item.entries)) if (part.toolCallId) ids.add(part.toolCallId);
    for (const approval of item.approvals) {
      const id = approval.toolCallId ?? approval.call?.toolCallId;
      if (id) ids.add(id);
    }
    for (const fold of item.folds) {
      for (const step of fold.steps) if (step.toolCallId) ids.add(step.toolCallId);
    }
    return ids.size > 0 ? [...ids].join(" ") : undefined;
  }
  if (item.kind === "file_edit" || item.kind === "worker_file_edit") {
    return [...new Set(item.folds.flatMap((fold) =>
      fold.steps.flatMap((step) => step.toolCallId ? [step.toolCallId] : []),
    ))].join(" ") || undefined;
  }
  return transcriptItemActionToolCallIds(item);
}

/** Checks direct and grouped transcript rows. */
export function transcriptItemContainsMessage(item: TranscriptItem, messageId: string): boolean {
  const id = messageId.trim();
  if (!id || item.kind === "pending_user") return false;
  if (item.key === id) return true;
  if (item.kind === "activity_group") return activityGroupContainsMessage(item, id);
  if (item.kind === "file_edit" || item.kind === "worker_file_edit") {
    return item.anchorMessageId === id || item.folds.some((fold) =>
      fold.steps.some((step) => step.messageId === id),
    );
  }
  if (item.kind === "tool") return item.part.messageId === id;
  if (item.kind === "worker_group") return item.parts.some((part) => part.messageId === id);
  if (item.kind === "activity_span") {
    return activitySpanToolParts(item.entries).some((part) => part.messageId === id);
  }
  return false;
}

export type ReconcileGroupRename = (
  fromGroupKey: string,
  toGroupKey: string,
  foldPaths: readonly string[],
) => void;

/** Carries open state when activity spans or groups grow, shrink, or merge across page changes. */
export function reconcileRegroupedActivitySpans(
  items: readonly TranscriptItem[],
  previous: TranscriptSpanState,
  renameSpan: (fromItemKey: string, toItemKey: string) => void,
  renameGroup?: ReconcileGroupRename,
): TranscriptSpanState {
  const entryToSpanKey = new Map<string, string>();
  const spanEntries = new Map<string, readonly string[]>();
  const memberToGroupKey = new Map<string, string>();
  const groupEntries = new Map<string, readonly string[]>();
  const groupFoldPaths = new Map<string, readonly string[]>();
  const firstRowKey = items.find((i) => i.kind !== "time_marker" && i.kind !== "unread_marker")?.key;
  for (const item of items) {
    if (item.kind === "activity_span") {
      const keys = item.entries.map(activitySpanEntryKey);
      spanEntries.set(item.key, keys);
      const transferred = new Set<string>();
      for (const entryKey of keys) {
        entryToSpanKey.set(entryKey, item.key);
        const prevSpanKey = previous.entryToSpanKey.get(entryKey);
        if (prevSpanKey && prevSpanKey !== item.key && !transferred.has(prevSpanKey)) {
          transferred.add(prevSpanKey);
          renameSpan(prevSpanKey, item.key);
        }
      }
      continue;
    }
    if (item.kind === "activity_group") {
      const entries = item.entries.map(activitySpanEntryKey);
      const members = activityGroupMembers(item);
      groupEntries.set(item.key, entries.length > 0 ? entries : members);
      groupFoldPaths.set(item.key, item.folds.map((fold) => fileEditPathKey(fold.net)));
      const transferred = new Set<string>();
      for (const member of members) {
        memberToGroupKey.set(member, item.key);
        const prevGroupKey = previous.memberToGroupKey.get(member);
        if (prevGroupKey && prevGroupKey !== item.key && !transferred.has(prevGroupKey)) {
          transferred.add(prevGroupKey);
          renameGroup?.(prevGroupKey, item.key, previous.groupFoldPaths.get(prevGroupKey) ?? []);
          renameSpan(prevGroupKey, item.key);
        }
      }
    }
  }
  return { entryToSpanKey, spanEntries, memberToGroupKey, groupEntries, groupFoldPaths, firstRowKey };
}

function measureActivitySpanEntryOffsetPx(
  root: ParentNode | null | undefined,
  entryKey: string,
  relativeToFirstEntry: boolean,
  includeRowSeam = false,
): number | undefined {
  if (!root) return undefined;
  let targetEl: HTMLElement | null = null;
  for (const el of root.querySelectorAll<HTMLElement>("[data-activity-entry-key]")) {
    if (el.dataset.activityEntryKey === entryKey) {
      targetEl = el;
      break;
    }
  }
  if (!targetEl) return undefined;
  const spanEl = targetEl.closest<HTMLElement>(".den-activity-span");
  if (spanEl instanceof HTMLDetailsElement && !spanEl.open) return 0;
  const rowEl = targetEl.closest<HTMLElement>("[data-msg-id]");
  const baseEl = relativeToFirstEntry
    ? (spanEl ?? root).querySelector<HTMLElement>("[data-activity-entry-key]")
    : (rowEl ?? spanEl);
  if (!baseEl || baseEl === targetEl) return undefined;
  const seamPx = includeRowSeam && relativeToFirstEntry && spanEl && rowEl
    ? Math.max(0, spanEl.getBoundingClientRect().top - rowEl.getBoundingClientRect().top)
    : 0;
  const rectDelta = targetEl.getBoundingClientRect().top - baseEl.getBoundingClientRect().top;
  if (rectDelta > 0) return rectDelta + seamPx;
  const offsetDelta = targetEl.offsetTop - (relativeToFirstEntry ? baseEl.offsetTop : 0);
  return offsetDelta > 0 ? offsetDelta + seamPx : undefined;
}

/** Finds the row that absorbed or succeeded a vanished anchor key and the offset within it. */
export function resolveTranscriptItemAnchor(
  items: readonly TranscriptItem[],
  rowKey: string,
  isSpanOpen: (spanItemKey: string) => boolean,
  entryEstimatePx = ACTIVITY_SPAN_ENTRY_ESTIMATE_PX,
  options?: { previous?: TranscriptSpanState; root?: () => ParentNode | null },
): ResolvedTranscriptAnchor | undefined {
  const wasGroupKey = rowKey.startsWith(ACTIVITY_GROUP_KEY_PREFIX);
  const needle = wasGroupKey ? rowKey.slice(ACTIVITY_GROUP_KEY_PREFIX.length) : rowKey;

  for (const item of items) {
    if (item.kind === "activity_span") {
      const index = item.entries.findIndex(
        (e) => activitySpanEntryKey(e) === rowKey || (e.kind === "tool" && e.part.messageId === rowKey),
      );
      if (index < 0) continue;
      const entryKey = activitySpanEntryKey(item.entries[index]!);
      const wasSpan = options?.previous ? options.previous.spanEntries.has(rowKey) : true;
      const wasFirstRow = options?.previous?.firstRowKey === rowKey;
      const measureOffsetPx = () =>
        isSpanOpen(item.key) ? measureActivitySpanEntryOffsetPx(options?.root?.(), entryKey, wasSpan, wasFirstRow) : 0;
      return {
        rowKey: item.key,
        offsetPx: isSpanOpen(item.key) ? (measureOffsetPx() ?? index * entryEstimatePx) : 0,
        measureOffsetPx,
      };
    }
    if (item.kind === "activity_group" && item.key !== rowKey) {
      const entryIndex = item.entries.findIndex(
        (e) => activitySpanEntryKey(e) === needle || (e.kind === "tool" && e.part.messageId === needle),
      );
      const inApprovals = entryIndex < 0 && item.approvals.some((a) => a.key === needle);
      const inFolds = entryIndex < 0 && !inApprovals && item.folds.some((f) => f.steps.some((s) => s.key === needle || s.messageId === needle));
      if (entryIndex < 0 && !inApprovals && !inFolds && !transcriptItemContainsMessage(item, needle)) continue;
      const memberKey = entryIndex >= 0 ? activitySpanEntryKey(item.entries[entryIndex]!) : needle;
      const wasFirstRow = options?.previous?.firstRowKey === rowKey;
      const measureOffsetPx = () =>
        measureActivityGroupMemberOffsetPx(options?.root?.(), item.key, memberKey, isSpanOpen(item.key), wasGroupKey, wasFirstRow);
      const fallbackOffset = entryIndex >= 0 && isSpanOpen(item.key) ? entryIndex * entryEstimatePx : 0;
      return { rowKey: item.key, offsetPx: measureOffsetPx() ?? fallbackOffset, measureOffsetPx };
    }
    if (item.key !== rowKey && transcriptItemContainsMessage(item, rowKey)) {
      return { rowKey: item.key, offsetPx: 0 };
    }
  }

  const previousEntries = options?.previous?.spanEntries.get(rowKey);
  if (previousEntries) {
    for (let removed = 1; removed < previousEntries.length; removed += 1) {
      const surviving = items.find((item) => item.kind === "activity_span" && item.key === previousEntries[removed]);
      if (surviving) return { rowKey: surviving.key, offsetPx: isSpanOpen(surviving.key) ? -removed * entryEstimatePx : 0 };
    }
  }

  const previousGroupEntries = options?.previous?.groupEntries.get(rowKey);
  if (previousGroupEntries) {
    for (let removed = 1; removed < previousGroupEntries.length; removed += 1) {
      const candidateMember = previousGroupEntries[removed]!;
      const surviving = items.find(
        (item): item is ActivityGroupItem =>
          item.kind === "activity_group" &&
          (item.key === `${ACTIVITY_GROUP_KEY_PREFIX}${candidateMember}` || activityGroupMembers(item).includes(candidateMember)),
      );
      if (surviving) return { rowKey: surviving.key, offsetPx: isSpanOpen(surviving.key) ? -removed * entryEstimatePx : 0 };
    }
  }
  return undefined;
}

/** Tracks activity-span and activity-group regrouping for a transcript item stream. */
export function bindTranscriptSpanAnchors(options: {
  items: () => readonly TranscriptItem[];
  isOpen: (key: TranscriptDisclosureKey) => boolean;
  rename: (from: TranscriptDisclosureKey, to: TranscriptDisclosureKey) => void;
  root?: () => ParentNode | null;
}): (rowKey: string) => ResolvedTranscriptAnchor | undefined {
  let previous = EMPTY_TRANSCRIPT_SPAN_STATE;
  let currentState = EMPTY_TRANSCRIPT_SPAN_STATE;
  let syncedItems: readonly TranscriptItem[] | undefined;
  const sync = () => {
    const current = options.items();
    if (current === syncedItems) return current;
    syncedItems = current;
    untrack(() => {
      previous = currentState;
      currentState = reconcileRegroupedActivitySpans(
        current,
        currentState,
        (from, to) => {
          if (!from.startsWith(ACTIVITY_GROUP_KEY_PREFIX) && !to.startsWith(ACTIVITY_GROUP_KEY_PREFIX)) {
            options.rename(transcriptDisclosureKey.activitySpan(from), transcriptDisclosureKey.activitySpan(to));
          }
        },
        (fromGroup, toGroup, foldPaths) => {
          options.rename(transcriptDisclosureKey.activityGroupApprovals(fromGroup), transcriptDisclosureKey.activityGroupApprovals(toGroup));
          options.rename(transcriptDisclosureKey.activityGroupFiles(fromGroup), transcriptDisclosureKey.activityGroupFiles(toGroup));
          options.rename(transcriptDisclosureKey.activityGroupActivities(fromGroup), transcriptDisclosureKey.activityGroupActivities(toGroup));
          for (const pathKey of foldPaths) {
            options.rename(transcriptDisclosureKey.diffFile(`${fromGroup}\0${pathKey}`), transcriptDisclosureKey.diffFile(`${toGroup}\0${pathKey}`));
          }
        },
      );
    });
    return current;
  };
  createComputed(sync);
  return (rowKey: string) =>
    resolveTranscriptItemAnchor(
      sync(),
      rowKey,
      (key) => key.startsWith(ACTIVITY_GROUP_KEY_PREFIX)
        ? options.isOpen(transcriptDisclosureKey.activityGroupActivities(key))
        : options.isOpen(transcriptDisclosureKey.activitySpan(key)),
      ACTIVITY_SPAN_ENTRY_ESTIMATE_PX,
      { previous, root: options.root },
    );
}
