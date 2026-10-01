import type { TranscriptItem } from "../projection/transcript-item-model.ts";

/** Last painted transcript line. */
export type TranscriptDisplayTail = {
  key: string;
  proseText: string | null;
  hasReceivedProse: boolean;
  kind: TranscriptItem["kind"];
  itemCount: number;
};

export type TranscriptDeliveryKind =
  | "prose"
  | "structural";

export function transcriptDisplayTail(
  blocks: readonly { readonly items: readonly TranscriptItem[] }[],
  previous?: TranscriptDisplayTail | null,
): TranscriptDisplayTail | null {
  let itemCount = 0;
  let last: TranscriptItem | undefined;
  for (const block of blocks) {
    const { items } = block;
    itemCount += items.length;
    if (items.length > 0) {
      last = items[items.length - 1];
    }
  }
  if (!last) return null;
  const proseText =
    last.kind === "assistant" || last.kind === "draft"
      ? last.text
      : null;
  return {
    key: last.key,
    proseText,
    hasReceivedProse: Boolean(proseText?.trim()) ||
      (previous?.key === last.key && previous.hasReceivedProse),
    kind: last.kind,
    itemCount,
  };
}

/** Classify a painted tail mutation by the viewport behavior it permits. */
export function transcriptTailDelivery(
  prev: TranscriptDisplayTail | null,
  next: TranscriptDisplayTail | null,
): TranscriptDeliveryKind | null {
  if (!next) return null;
  if (prev) {
    if (next.itemCount < prev.itemCount) return null;
    if (next.itemCount === prev.itemCount) {
      if (
        next.key !== prev.key ||
        next.proseText === null ||
        next.proseText === prev.proseText
      ) {
        return null;
      }
      // Only prose rows are delivered in place.
      return "prose";
    }
  }

  const appended = !prev || (
    next.key !== prev.key && next.itemCount > prev.itemCount
  );
  if (!appended) return "structural";
  if (next.kind === "assistant" || next.kind === "draft") return "prose";
  return "structural";
}

/** A delivery is fresh only at the first nonempty text of its tail row. */
export function isFirstProseDelivery(
  prev: TranscriptDisplayTail | null,
  next: TranscriptDisplayTail | null,
): boolean {
  return Boolean(next?.proseText?.trim()) &&
    (prev?.key !== next?.key || !prev?.hasReceivedProse);
}
