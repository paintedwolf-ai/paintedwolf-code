import type { Message } from "../../../api/types.ts";
import { activitySpanToolParts } from "../../tool/activity-span-model.ts";
import { toolDrawsWhileRunning } from "../../tool/tool-part-model.ts";
import type { ToolPartView } from "../../tool/tool-part-model.ts";
import type { RawTranscriptItem, TranscriptBuildOptions, TranscriptItem } from "./transcript-item-model.ts";

/**
 * Where rows sit. A row keeps the place it is first drawn at: the ordinal of the message that
 * first shows it, then its tier within that ordinal.
 */
type TranscriptOrder = {
  ordById: ReadonlyMap<string, number>;
  /** Notes read above the calls of the assistant row they follow. */
  noteIds: ReadonlySet<string>;
  /** Whether a tool's row is drawn before its result arrives. */
  drawsWhileRunning: (tool: string) => boolean;
};

export function buildTranscriptOrder(
  messages: readonly Message[],
  opts: TranscriptBuildOptions = {},
): TranscriptOrder {
  const ordById = new Map<string, number>();
  const noteIds = new Set<string>();
  for (const msg of messages) ordById.set(msg.id, msg.ord ?? 0);
  if (messages.some((msg) => msg.kind === "agent_note")) {
    const ordered = [...messages].sort((a, b) => (a.ord ?? 0) - (b.ord ?? 0));
    ordered.forEach((msg, at) => {
      const carrier = ordered[at - 1];
      if (msg.kind === "agent_note" && carrier?.role === "assistant" && carrier.tool_calls?.length) {
        ordById.set(msg.id, carrier.ord ?? 0);
        noteIds.add(msg.id);
      }
    });
  }
  const view = { verboseMode: opts.verboseMode ?? false, layout: opts.layout ?? "chat" } as const;
  return { ordById, noteIds, drawsWhileRunning: (tool) => toolDrawsWhileRunning(tool, view) };
}

/** Order within one ordinal. */
const TIER = {
  /** The message's own row. */
  row: 0,
  note: 1,
  /** Decision rows precede the calls they decided for. */
  decision: 2,
  /** Calls in batch order, each followed by its approval. */
  call: 3,
  diff: 4,
} as const;

export type TranscriptOrdSortKey = {
  ord: number;
  tier: number;
  /** Position within the tier. */
  index: number;
};

const UNORDERED = Number.MAX_SAFE_INTEGER;

/** A call's row sits at its assistant row when drawn while running, otherwise at its result. */
function callOrd(part: ToolPartView, order: TranscriptOrder): number {
  const id = part.shownInFlight || order.drawsWhileRunning(part.tool) ? part.assistantMessageId : part.messageId;
  return order.ordById.get(id) ?? UNORDERED;
}

function transcriptItemOrdKey(item: RawTranscriptItem, order: TranscriptOrder): TranscriptOrdSortKey {
  const at = (id: string, tier: number, index = 0): TranscriptOrdSortKey => ({
    ord: order.ordById.get(id) ?? UNORDERED,
    tier,
    index,
  });
  switch (item.kind) {
    case "user":
    case "assistant":
    case "draft":
    case "progress_complete":
    case "progress_update":
    case "workflow_feedback":
    case "workflow_explain":
    case "workflow_boundary":
    case "index_warming":
    case "blueprint_card":
    case "fallback":
      return at(item.key, order.noteIds.has(item.key) ? TIER.note : TIER.row);
    case "tool":
      return { ord: callOrd(item.part, order), tier: TIER.call, index: 2 * (item.batchOrder ?? 0) };
    case "checkpoint": {
      const call = item.call;
      if (!call) return at(item.parentMessageId, TIER.call, 1);
      const atResult = call.resultMessageId !== undefined && !call.shownInFlight && !order.drawsWhileRunning(call.tool);
      return at(atResult ? call.resultMessageId! : item.parentMessageId, TIER.call, 2 * call.index + 1);
    }
    case "turn_load":
      return at(item.row.anchorMessageId, TIER.decision);
    case "worker_file_edit":
      return at(item.anchorMessageId, TIER.diff);
    default: {
      const _exhaustive: never = item;
      return _exhaustive;
    }
  }
}

/** Every message that places a row. */
export function transcriptRowMessageIds(item: TranscriptItem): string[] {
  switch (item.kind) {
    case "tool":
      return [item.part.messageId];
    case "checkpoint":
      return [item.parentMessageId];
    case "activity_span":
      return activitySpanToolParts(item.entries).map((part) => part.messageId);
    case "worker_group":
      return item.parts.map((part) => part.messageId);
    case "turn_load":
      return [item.row.anchorMessageId];
    case "file_edit":
    case "worker_file_edit":
    case "time_marker":
    case "unread_marker":
    case "turn_tail":
      return [item.anchorMessageId];
    default:
      return [item.key];
  }
}

/** The first message ordinal a presented row holds; undefined while it has no durable ordinal. */
export function transcriptItemOrd(
  item: TranscriptItem,
  ordById: ReadonlyMap<string, number>,
): number | undefined {
  let first: number | undefined;
  for (const id of transcriptRowMessageIds(item)) {
    const ord = ordById.get(id);
    if (ord !== undefined && ord > 0 && (first === undefined || ord < first)) first = ord;
  }
  return first;
}

/** A row's place, for rendered transcript order checks. */
export function transcriptItemOrdSortKey(
  item: RawTranscriptItem,
  messages: readonly Message[],
  opts?: TranscriptBuildOptions,
): TranscriptOrdSortKey {
  return transcriptItemOrdKey(item, buildTranscriptOrder(messages, opts));
}

export function compareTranscriptOrdSortKeys(
  a: TranscriptOrdSortKey,
  b: TranscriptOrdSortKey,
): number {
  return a.ord - b.ord || a.tier - b.tier || a.index - b.index;
}

export function sortTranscriptItemsByOrd<T extends RawTranscriptItem>(
  items: readonly T[],
  messages: readonly Message[],
  opts?: TranscriptBuildOptions,
): T[] {
  return sortItemsInOrder(items, buildTranscriptOrder(messages, opts));
}

/**
 * A row whose message is outside the order shares its neighbor's key, so the stable sort keeps
 * it where the span placed it instead of sending it to the tail.
 */
function holdUnorderedRowsInPlace(rows: { key: TranscriptOrdSortKey }[]): void {
  let previous: TranscriptOrdSortKey | undefined;
  for (const row of rows) {
    if (row.key.ord !== UNORDERED) previous = row.key;
    else if (previous) row.key = previous;
  }
  const leading = rows.find((row) => row.key.ord !== UNORDERED)?.key;
  if (!leading) return;
  for (const row of rows) {
    if (row.key.ord !== UNORDERED) break;
    row.key = leading;
  }
}

export function sortItemsInOrder<T extends RawTranscriptItem>(items: readonly T[], order: TranscriptOrder): T[] {
  const indexed = items.map((item) => ({ item, key: transcriptItemOrdKey(item, order) }));
  holdUnorderedRowsInPlace(indexed);
  indexed.sort((a, b) => compareTranscriptOrdSortKeys(a.key, b.key));
  return indexed.map((row) => row.item);
}
