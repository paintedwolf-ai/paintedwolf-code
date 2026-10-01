import type { Message } from "../../../api/types.ts";
import { type TranscriptItem, type RawTranscriptItem, type DisplayTranscriptItem, type TranscriptBuildOptions } from "./transcript-item-model.ts";
import { buildOrdIndex, sortItemsWithIndex } from "./transcript-item-order.ts";
import { finalizeTranscriptItems, messagesToTranscriptItems } from "./transcript-items.ts";
import { sameWireValue } from "./wire-equal.ts";

/** Rows are plain data over shared wire objects, so an unchanged row compares without serializing. */
export function sameTranscriptItem(previous: TranscriptItem, next: TranscriptItem): boolean {
  return previous.kind === next.kind && previous.key === next.key && sameWireValue(previous, next);
}

export function transcriptItemsByKey<T extends TranscriptItem>(items: readonly T[]): Map<string, T> {
  const byKey = new Map<string, T>();
  for (const item of items) byKey.set(item.key, item);
  return byKey;
}

/**
 * Keeps the earlier object for every row whose rendered data is unchanged.
 *
 * A row's identity is what its mounted component, memos, and estimate caches key on, so a
 * rebuilt transcript must hand back the same object wherever nothing the row renders moved.
 */
export function retainTranscriptItemIdentity<T extends TranscriptItem>(
  previous: ReadonlyMap<string, T>,
  next: readonly T[],
): T[] {
  if (previous.size === 0) return [...next];
  return next.map((item) => {
    const prior = previous.get(item.key);
    return prior && (prior === item || sameTranscriptItem(prior, item)) ? prior : item;
  });
}

function reconcileProse(
  items: readonly RawTranscriptItem[],
  freshProse: ReadonlyMap<string, RawTranscriptItem>,
): RawTranscriptItem[] {
  if (freshProse.size === 0) return [...items];

  const coordinatorSlotKeysInSpan = (rows: readonly RawTranscriptItem[]): Set<string> => {
    const keys = new Set<string>();
    for (const item of rows) {
      if (item.kind === "assistant" || item.kind === "draft") {
        keys.add(item.key);
      }
      if (item.kind === "tool") {
        const parentId = item.part.messageId?.trim();
        if (parentId) keys.add(parentId);
      }
    }
    return keys;
  };

  // Prose follows the newest build; a full row includes late-arriving grounding and card metadata.
  const reconciled = items.map((item) => {
    if (item.kind !== "assistant" && item.kind !== "draft") return item;
    const fresh = freshProse.get(item.key);
    if (!fresh || (fresh.kind !== "draft" && fresh.kind !== "assistant")) return item;
    return sameWireValue(item, fresh) ? item : fresh;
  });

  // Restore drafts still referenced by a tool.
  const spanSlotKeys = coordinatorSlotKeysInSpan(items);
  const reconciledProseKeys = coordinatorSlotKeysInSpan(reconciled);
  const injected: RawTranscriptItem[] = [];
  for (const key of spanSlotKeys) {
    if (reconciledProseKeys.has(key)) continue;
    const fresh = freshProse.get(key);
    if (!fresh || fresh.kind !== "draft") continue;
    injected.push(fresh);
  }
  if (injected.length === 0) return reconciled;

  const out = [...reconciled];
  for (const prose of injected) {
    const toolIdx = out.findIndex(
      (item) => item.kind === "tool" && item.part.messageId === prose.key,
    );
    out.splice(toolIdx >= 0 ? toolIdx : out.length, 0, prose);
  }
  return out;
}

type SpanProjection = {
  verboseMode: boolean;
  layout: string;
  raw: readonly RawTranscriptItem[];
  items: DisplayTranscriptItem[];
};

/**
 * One history parse per update. An unchanged span retains its rendered projection whole; inside
 * a span that did change, every row whose data is unchanged retains its identity.
 */
export function createTranscriptDisplayProjector() {
  let previous: SpanProjection[] = [];
  let previousByKey = new Map<string, DisplayTranscriptItem>();
  return (
    messages: readonly Message[],
    spans: readonly (readonly RawTranscriptItem[])[] | undefined,
    options: TranscriptBuildOptions = {},
  ): DisplayTranscriptItem[][] => {
    const buildOpts = { verboseMode: options.verboseMode ?? false, layout: options.layout ?? "chat" } as const;
    const freshItems = messagesToTranscriptItems(messages, buildOpts);
    const fresh = new Map(freshItems.map((item) => [item.key, item]));
    const order = buildOrdIndex(messages);
    const next = (spans ?? [freshItems]).map((span, index): SpanProjection => {
      let raw = spans ? reconcileProse(span, fresh) : [...span];
      if (spans) raw = raw.map((item) => {
        const current = fresh.get(item.key);
        if (current?.kind === item.kind && (item.kind === "tool" || item.kind === "workflow_feedback")) {
          return current;
        }
        return item;
      });
      raw = sortItemsWithIndex(raw, order);
      // Visibility options change rendering without changing rows.
      const prior = previous[index];
      if (prior && prior.verboseMode === buildOpts.verboseMode && prior.layout === buildOpts.layout &&
        sameWireValue(prior.raw, raw)) return prior;
      const items = retainTranscriptItemIdentity(previousByKey, finalizeTranscriptItems(raw, buildOpts));
      return { ...buildOpts, raw, items };
    });
    previous = next;
    previousByKey = new Map();
    for (const span of next) {
      for (const item of span.items) previousByKey.set(item.key, item);
    }
    return next.map((span) => span.items);
  };
}
