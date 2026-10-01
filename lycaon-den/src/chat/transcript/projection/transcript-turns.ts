import type { DisplayTranscriptItem } from "./transcript-item-model.ts";

/** Shared turn boundary for seams and footer placement. */
export function transcriptRowOpensTurn(
  item: DisplayTranscriptItem,
  continuation?: (itemKey: string) => boolean,
): boolean {
  if (item.kind === "pending_user") return item.pending.kind === "prompt";
  return item.kind === "user" && !(continuation?.(item.key) ?? false);
}
