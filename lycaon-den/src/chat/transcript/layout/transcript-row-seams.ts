import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";
import type { TranscriptSeam } from "./transcript-spacing.ts";

type Row = Pick<DisplayTranscriptItem, "kind" | "key">;
type SeamContext = {
  hasActions?: (row: Row) => boolean;
  closesWithCard?: (row: Row) => boolean;
};

function isMessage(row: Row | undefined): boolean {
  return row?.kind === "user" || row?.kind === "pending_user" ||
    row?.kind === "assistant" || row?.kind === "fallback";
}

function boundary(above: Row | undefined, below: Row | undefined, context: SeamContext): TranscriptSeam | undefined {
  if (!above) return undefined;
  if (isMessage(above) && !context.closesWithCard?.(above)) {
    return context.hasActions?.(above) ? "message-reserved" : "message";
  }
  return isMessage(below) ? "message" : "item";
}

export function transcriptSeamBelow(rows: readonly Row[], context: SeamContext = {}): TranscriptSeam | undefined {
  return boundary(rows.at(-1), undefined, context);
}

export function transcriptRowSeams(rows: readonly Row[], context: SeamContext = {}): Map<string, TranscriptSeam> {
  const seams = new Map<string, TranscriptSeam>();
  let above: Row | undefined;
  let firstMarker: string | undefined;
  for (const row of rows) {
    if (row.kind === "time_marker" || row.kind === "unread_marker") {
      if (firstMarker === undefined) firstMarker = row.key;
      else seams.set(row.key, "item");
      continue;
    }
    // The first marker inherits the seam of the row it dates.
    const seam = boundary(above, row, context);
    if (seam) seams.set(firstMarker ?? row.key, seam);
    if (firstMarker !== undefined) seams.set(row.key, "item");
    firstMarker = undefined;
    above = row;
  }
  const trailing = boundary(above, undefined, context);
  if (firstMarker !== undefined && trailing) seams.set(firstMarker, trailing);
  return seams;
}
