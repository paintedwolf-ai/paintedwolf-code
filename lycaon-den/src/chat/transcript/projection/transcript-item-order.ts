import type { Message } from "../../../api/types.ts";
import { type TranscriptItem } from "./transcript-item-model.ts";

function messageOrdById(messages: readonly Message[]): Map<string, number> {
  const index = new Map<string, number>();
  for (const msg of messages) index.set(msg.id, msg.ord ?? 0);
  return index;
}

/** Maps progress messages to their sub-order. */

function progressSeqById(messages: readonly Message[]): Map<string, number> {
  const index = new Map<string, number>();
  for (const msg of messages) {
    if (msg.progress_update) index.set(msg.id, msg.progress_update.seq ?? 0);
  }
  return index;
}

type OrdIndex = {
  ordById: Map<string, number>;
  progressSeqById: Map<string, number>;
};

export function buildOrdIndex(messages: readonly Message[]): OrdIndex {
  return {
    ordById: messageOrdById(messages),
    progressSeqById: progressSeqById(messages),
  };
}

/** Tool rows follow prose at the same ordinal. */

const TOOL_SUB_ORDINAL_BASE = 10;
/** Inline checkpoints follow their tool row. */

const CHECKPOINT_SUB_ORDINAL = TOOL_SUB_ORDINAL_BASE + 1;
/** Diffs follow tools and checkpoints at their anchor. */

const FILE_EDIT_SUB_ORDINAL = 20;
/** A day or gap label precedes its row, and a new marker follows the label. */

const TIME_MARKER_SUB_ORDINAL = -2;

const UNREAD_MARKER_SUB_ORDINAL = -1;
/** A turn's tail follows everything anchored at its last message. */

const TURN_TAIL_SUB_ORDINAL = 1000;

/** Transcript order uses creation and structural ordinals. */

export type TranscriptOrdSortKey = {
  ord: number;
  sub: number;
};

function transcriptItemOrdKey(
  item: TranscriptItem,
  ordById: Map<string, number>,
  progressSeqById: Map<string, number>,
): TranscriptOrdSortKey {
  switch (item.kind) {
    case "pending_user":
      return { ord: Number.MAX_SAFE_INTEGER, sub: 0 };
    case "user":
    case "assistant":
    case "draft":
    case "progress_complete":
    case "workflow_feedback":
    case "workflow_explain":
    case "workflow_boundary":
    case "index_warming":
    case "blueprint_card":
    case "fallback":
      return { ord: ordById.get(item.key) ?? Number.MAX_SAFE_INTEGER, sub: 0 };
    case "progress_update":
      return {
        ord: ordById.get(item.key) ?? Number.MAX_SAFE_INTEGER,
        sub: progressSeqById.get(item.key) ?? 0,
      };
    case "tool":
      return {
        ord: ordById.get(item.part.messageId) ?? Number.MAX_SAFE_INTEGER,
        sub: TOOL_SUB_ORDINAL_BASE + (item.batchOrder ?? 0),
      };
    case "turn_load":
      return {
        ord: ordById.get(item.row.anchorMessageId) ?? Number.MAX_SAFE_INTEGER,
        sub: item.row.sub,
      };
    case "checkpoint":
      return {
        ord: ordById.get(item.parentMessageId) ?? Number.MAX_SAFE_INTEGER,
        sub: CHECKPOINT_SUB_ORDINAL,
      };
    case "activity_span": {
      let minOrd = Number.MAX_SAFE_INTEGER;
      for (const entry of item.entries) {
        const id = entry.kind === "tool" ? entry.part.messageId
          : entry.kind === "turn_load" ? entry.row.anchorMessageId : entry.key;
        const ord = ordById.get(id) ?? Number.MAX_SAFE_INTEGER;
        if (ord < minOrd) minOrd = ord;
      }
      return { ord: minOrd, sub: 0 };
    }
    case "worker_group": {
      let minOrd = Number.MAX_SAFE_INTEGER;
      for (const part of item.parts) {
        const ord = ordById.get(part.messageId) ?? Number.MAX_SAFE_INTEGER;
        if (ord < minOrd) minOrd = ord;
      }
      return { ord: minOrd, sub: TOOL_SUB_ORDINAL_BASE };
    }
    case "worker_file_edit":
    case "file_edit":
      return {
        ord: ordById.get(item.anchorMessageId) ?? Number.MAX_SAFE_INTEGER,
        sub: FILE_EDIT_SUB_ORDINAL,
      };
    case "time_marker":
      return {
        ord: ordById.get(item.anchorMessageId) ?? Number.MAX_SAFE_INTEGER,
        sub: TIME_MARKER_SUB_ORDINAL,
      };
    case "unread_marker":
      return {
        ord: ordById.get(item.anchorMessageId) ?? Number.MAX_SAFE_INTEGER,
        sub: UNREAD_MARKER_SUB_ORDINAL,
      };
    case "turn_tail":
      return {
        ord: ordById.get(item.anchorMessageId) ?? Number.MAX_SAFE_INTEGER,
        sub: TURN_TAIL_SUB_ORDINAL,
      };
    default: {
      const _exhaustive: never = item;
      return _exhaustive;
    }
  }
}

/** Canonical (ord, sub) for rendered transcript order checks. */

export function transcriptItemOrdSortKey(
  item: TranscriptItem,
  messages: readonly Message[],
): TranscriptOrdSortKey {
  const index = buildOrdIndex(messages);
  return transcriptItemOrdKey(
    item,
    index.ordById,
    index.progressSeqById,
  );
}

export function compareTranscriptOrdSortKeys(
  a: TranscriptOrdSortKey,
  b: TranscriptOrdSortKey,
): number {
  if (a.ord !== b.ord) return a.ord - b.ord;
  return a.sub - b.sub;
}

/** Sorts by creation order, then structural order. */

export function sortTranscriptItemsByOrd<T extends TranscriptItem>(
  items: readonly T[],
  messages: readonly Message[],
): T[] {
  return sortItemsWithIndex(items, buildOrdIndex(messages));
}

export function sortItemsWithIndex<T extends TranscriptItem>(items: readonly T[], index: OrdIndex): T[] {
  const indexed = items.map((item) => ({
    item,
    key: transcriptItemOrdKey(
      item,
      index.ordById,
      index.progressSeqById,
    ),
  }));
  indexed.sort((a, b) => compareTranscriptOrdSortKeys(a.key, b.key));
  return indexed.map((row) => row.item);
}
