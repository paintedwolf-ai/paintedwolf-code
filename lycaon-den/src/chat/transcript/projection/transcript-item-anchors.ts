import { createComputed, untrack } from "solid-js";
import {
  activitySpanEntryKey,
  activitySpanToolParts,
} from "../../tool/activity-span-model.ts";
import {
  transcriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "../presentation/transcript-disclosure-key.ts";
import { ACTIVITY_SPAN_ENTRY_ESTIMATE_PX } from "../presentation/transcript-row-presentation.ts";
import type { TranscriptItem } from "./transcript-item-model.ts";
import { isTranscriptDisclosureOpen, isTranscriptDisclosureShell } from "../presentation/transcript-disclosure-shell.ts";

export type TranscriptSpanState = {
  entryToSpanKey: ReadonlyMap<string, string>;
  spanEntries: ReadonlyMap<string, readonly string[]>;
};

export const EMPTY_TRANSCRIPT_SPAN_STATE: TranscriptSpanState = {
  entryToSpanKey: new Map(),
  spanEntries: new Map(),
};

export type ResolvedTranscriptAnchor = {
  rowKey: string;
  offsetPx: number;
  measureOffsetPx?: () => number | undefined;
};

export function transcriptItemToolCallIds(
  item: TranscriptItem,
): string | undefined {
  if (item.kind === "tool") return item.part.toolCallId;
  if (item.kind === "worker_group") {
    return item.parts.map((part) => part.toolCallId).join(" ");
  }
  if (item.kind === "activity_span") {
    return activitySpanToolParts(item.entries)
      .map((part) => part.toolCallId)
      .join(" ");
  }
  if (item.kind === "file_edit" || item.kind === "worker_file_edit") {
    return [...new Set(item.folds.flatMap((fold) =>
      fold.steps.flatMap((step) => step.toolCallId ? [step.toolCallId] : []),
    ))].join(" ") || undefined;
  }
  return undefined;
}

/** Checks direct and grouped transcript rows. */
export function transcriptItemContainsMessage(
  item: TranscriptItem,
  messageId: string,
): boolean {
  const id = messageId.trim();
  if (!id || item.kind === "pending_user") return false;
  if (item.key === id) return true;
  if (item.kind === "file_edit" || item.kind === "worker_file_edit") {
    return item.anchorMessageId === id || item.folds.some((fold) =>
      fold.steps.some((step) => step.messageId === id),
    );
  }
  if (item.kind === "tool") return item.part.messageId === id;
  if (item.kind === "worker_group") {
    return item.parts.some((part) => part.messageId === id);
  }
  if (item.kind === "activity_span") {
    return activitySpanToolParts(item.entries).some((part) => part.messageId === id);
  }
  return false;
}

/** Carries open state when activity spans grow, shrink, or merge across page changes. */
export function reconcileRegroupedActivitySpans(
  items: readonly TranscriptItem[],
  previous: TranscriptSpanState,
  renameSpan: (fromItemKey: string, toItemKey: string) => void,
): TranscriptSpanState {
  const entryToSpanKey = new Map<string, string>();
  const spanEntries = new Map<string, readonly string[]>();
  for (const item of items) {
    if (item.kind !== "activity_span") continue;
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
  }
  return { entryToSpanKey, spanEntries };
}

function measureActivitySpanEntryOffsetPx(
  root: ParentNode | null | undefined,
  entryKey: string,
  relativeToFirstEntry: boolean,
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
  const baseEl = relativeToFirstEntry
    ? (spanEl ?? root).querySelector<HTMLElement>("[data-activity-entry-key]")
    : (targetEl.closest<HTMLElement>("[data-msg-id]") ?? spanEl);
  if (!baseEl || baseEl === targetEl) return undefined;
  const rectDelta = targetEl.getBoundingClientRect().top - baseEl.getBoundingClientRect().top;
  if (rectDelta > 0) return rectDelta;
  const offsetDelta = targetEl.offsetTop - (relativeToFirstEntry ? baseEl.offsetTop : 0);
  return offsetDelta > 0 ? offsetDelta : undefined;
}

/** Finds the row that absorbed or succeeded a vanished anchor key and the offset within it. */
export function resolveTranscriptItemAnchor(
  items: readonly TranscriptItem[],
  rowKey: string,
  isSpanOpen: (spanItemKey: string) => boolean,
  entryEstimatePx = ACTIVITY_SPAN_ENTRY_ESTIMATE_PX,
  options?: { previous?: TranscriptSpanState; root?: () => ParentNode | null },
): ResolvedTranscriptAnchor | undefined {
  for (const item of items) {
    if (item.kind === "activity_span") {
      const index = item.entries.findIndex(
        (entry) =>
          activitySpanEntryKey(entry) === rowKey ||
          (entry.kind === "tool" && entry.part.messageId === rowKey),
      );
      if (index < 0) continue;
      const entryKey = activitySpanEntryKey(item.entries[index]!);
      const wasSpan = options?.previous ? options.previous.spanEntries.has(rowKey) : true;
      const measureOffsetPx = () =>
        isSpanOpen(item.key)
          ? measureActivitySpanEntryOffsetPx(options?.root?.(), entryKey, wasSpan)
          : 0;
      return {
        rowKey: item.key,
        offsetPx: isSpanOpen(item.key) ? (measureOffsetPx() ?? index * entryEstimatePx) : 0,
        measureOffsetPx,
      };
    }
    if (item.key !== rowKey && transcriptItemContainsMessage(item, rowKey)) {
      return { rowKey: item.key, offsetPx: 0 };
    }
  }
  const previousEntries = options?.previous?.spanEntries.get(rowKey);
  if (previousEntries) {
    for (let removed = 1; removed < previousEntries.length; removed += 1) {
      const candidateKey = previousEntries[removed]!;
      const surviving = items.find(
        (item) => item.kind === "activity_span" && item.key === candidateKey,
      );
      if (surviving) {
        return {
          rowKey: surviving.key,
          offsetPx: isSpanOpen(surviving.key) ? -removed * entryEstimatePx : 0,
        };
      }
    }
  }
  return undefined;
}

/** Tracks activity-span regrouping for a transcript item stream and resolves vanished anchors. */
export function bindTranscriptSpanAnchors(options: {
  items: () => readonly TranscriptItem[];
  isOpen: (key: TranscriptDisclosureKey) => boolean;
  rename: (from: TranscriptDisclosureKey, to: TranscriptDisclosureKey) => void;
  root?: () => ParentNode | null;
}): (rowKey: string) => ResolvedTranscriptAnchor | undefined {
  let previous = EMPTY_TRANSCRIPT_SPAN_STATE;
  let currentState = EMPTY_TRANSCRIPT_SPAN_STATE;
  createComputed(() => {
    const current = options.items();
    untrack(() => {
      previous = currentState;
      currentState = reconcileRegroupedActivitySpans(current, currentState, (from, to) =>
        options.rename(
          transcriptDisclosureKey.activitySpan(from),
          transcriptDisclosureKey.activitySpan(to),
        ),
      );
    });
  });
  return (rowKey: string) =>
    resolveTranscriptItemAnchor(
      options.items(),
      rowKey,
      (spanKey) => options.isOpen(transcriptDisclosureKey.activitySpan(spanKey)),
      ACTIVITY_SPAN_ENTRY_ESTIMATE_PX,
      { previous, root: options.root },
    );
}

export function transcriptToolTarget(
  root: ParentNode,
  toolCallId: string,
): HTMLElement | null {
  let fallback: HTMLElement | null = null;
  for (const element of root.querySelectorAll<HTMLElement>(
    "[data-tool-call-id]",
  )) {
    if (element.dataset.toolCallId !== toolCallId) continue;
    const card = element.matches('.den-tool-part, [data-testid="task-card"]')
      ? element
      : element.closest<HTMLElement>(
          '.den-tool-part, [data-testid="task-card"]',
        );
    if (card) return card;
    fallback ??= element;
  }
  if (fallback) return fallback;
  for (const row of root.querySelectorAll<HTMLElement>("[data-tool-call-ids]")) {
    if (!row.dataset.toolCallIds?.split(" ").includes(toolCallId)) continue;
    return row.querySelector<HTMLElement>(".den-transcript-disclosure-card") ?? row;
  }
  return null;
}

/** The command itself, or the outermost disclosure currently hiding it. */
export function visibleTranscriptToolTarget(
  root: HTMLElement,
  toolCallId: string,
): HTMLElement | null {
  const command = transcriptToolTarget(root, toolCallId);
  if (!command) return null;
  let target = command;
  for (let parent = command.parentElement; parent && parent !== root; parent = parent.parentElement) {
    if (isTranscriptDisclosureShell(parent) && (
      !isTranscriptDisclosureOpen(parent) || parent.hasAttribute("data-closing") || parent.dataset.animating === "true"
    )) target = parent;
  }
  return target;
}
