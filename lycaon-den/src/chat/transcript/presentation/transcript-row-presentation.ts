import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import {
  transcriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "./transcript-disclosure-key.ts";

export const TRANSCRIPT_ROW_COLLAPSED_PRESENTATION = "collapsed";

type TranscriptDisclosureReader = (key: TranscriptDisclosureKey) => boolean;

function toolPresentationKeys(
  isOpen: TranscriptDisclosureReader,
  partId: string,
): TranscriptDisclosureKey[] {
  const toolKey = transcriptDisclosureKey.tool(partId);
  if (!isOpen(toolKey)) return [];
  const keys = [toolKey];
  const networkKey = transcriptDisclosureKey.toolNetwork(partId);
  if (isOpen(networkKey)) keys.push(networkKey);
  return keys;
}

function rowDisclosureKey(
  kind: "index_warming" | "workflow_feedback" | "workflow_explain" | "checkpoint" | "blueprint_card",
  rowKey: string,
): TranscriptDisclosureKey {
  switch (kind) {
    case "index_warming": return transcriptDisclosureKey.indexWarming(rowKey);
    case "workflow_feedback": return transcriptDisclosureKey.workflowFeedback(rowKey);
    case "workflow_explain": return transcriptDisclosureKey.workflowExplain(rowKey);
    case "checkpoint": return transcriptDisclosureKey.checkpoint(rowKey);
    case "blueprint_card": return transcriptDisclosureKey.blueprint(rowKey);
  }
}

export function transcriptRowPresentation(
  item: TranscriptItem,
  isOpen: TranscriptDisclosureReader,
): string {
  let keys: TranscriptDisclosureKey[] = [];
  switch (item.kind) {
    case "assistant": {
      const evidenceKey = transcriptDisclosureKey.groundingEvidence(item.key);
      if (isOpen(evidenceKey)) keys = [evidenceKey];
      break;
    }
    case "tool":
      keys = toolPresentationKeys(isOpen, item.part.id);
      break;
    case "worker_group":
      for (const part of item.parts) {
        keys.push(...toolPresentationKeys(isOpen, part.id));
      }
      break;
    case "activity_span": {
      const spanKey = transcriptDisclosureKey.activitySpan(item.key);
      if (!isOpen(spanKey)) break;
      keys.push(spanKey);
      for (const entry of item.entries) {
        if (entry.kind === "tool") {
          keys.push(...toolPresentationKeys(isOpen, entry.part.id));
        } else if (entry.kind === "turn_load") {
          // A decision row is a tool card keyed by the row.
          keys.push(...toolPresentationKeys(isOpen, entry.key));
        } else {
          const warmingKey = transcriptDisclosureKey.indexWarming(entry.key);
          if (isOpen(warmingKey)) keys.push(warmingKey);
        }
      }
      break;
    }
    case "index_warming":
    case "workflow_feedback":
    case "workflow_explain":
    case "checkpoint":
    case "blueprint_card": {
      const key = rowDisclosureKey(item.kind, item.key);
      if (isOpen(key)) keys = [key];
      break;
    }
    case "worker_file_edit":
    case "file_edit": {
      const groupKey = transcriptDisclosureKey.diffGroup(item.key);
      if (isOpen(groupKey)) {
        keys = [groupKey, ...item.folds.map(fold => transcriptDisclosureKey.diffFile(fold.key)).filter(isOpen)];
      }
      break;
    }
    case "pending_user":
    case "user":
    case "draft":
    case "progress_complete":
    case "progress_update":
    case "workflow_boundary":
    case "fallback":
    case "time_marker":
    case "unread_marker":
    case "turn_tail":
    case "turn_load":
      break;
    default: {
      const _exhaustive: never = item;
      return _exhaustive;
    }
  }
  return keys.length > 0
    ? JSON.stringify(keys)
    : TRANSCRIPT_ROW_COLLAPSED_PRESENTATION;
}

const ACTIVITY_SPAN_BASE_ESTIMATE_PX = 64;
const COLLAPSED_DIFF_ESTIMATE_PX = 38;

function diffRowEstimate(foldCount: number): number {
  if (foldCount <= 0) return 0;
  return COLLAPSED_DIFF_ESTIMATE_PX;
}

/** Estimate complete compound and expanded rows. */
export function transcriptRowPresentationEstimate(
  item: TranscriptItem,
  isOpen: TranscriptDisclosureReader,
): number | undefined {
  const presentation = transcriptRowPresentation(item, isOpen);

  if (item.kind === "activity_span") {
    if (presentation === TRANSCRIPT_ROW_COLLAPSED_PRESENTATION) {
      return ACTIVITY_SPAN_BASE_ESTIMATE_PX;
    }
    const openChild = item.entries.some((entry) =>
      entry.kind === "tool" ? isOpen(transcriptDisclosureKey.tool(entry.part.id))
        : entry.kind === "turn_load" && isOpen(transcriptDisclosureKey.tool(entry.key)),
    );
    return (
      ACTIVITY_SPAN_BASE_ESTIMATE_PX +
      item.entries.length * 42 +
      (openChild ? 360 : 0)
    );
  }
  if (item.kind === "worker_file_edit" || item.kind === "file_edit") {
    const estimate = diffRowEstimate(item.folds.length);
    if (estimate <= 0) return undefined;
    return presentation === TRANSCRIPT_ROW_COLLAPSED_PRESENTATION ? estimate
      : estimate + item.folds.length * 38 + (item.folds.some(fold => isOpen(transcriptDisclosureKey.diffFile(fold.key))) ? 360 : 0);
  }
  if (item.kind === "worker_group") {
    return 30 + item.parts.length * 53;
  }
  if (presentation === TRANSCRIPT_ROW_COLLAPSED_PRESENTATION) return undefined;
  if (
    item.kind === "tool" &&
    isOpen(transcriptDisclosureKey.toolNetwork(item.part.id))
  ) {
    return 660;
  }
  return 420;
}
