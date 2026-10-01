import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";
import type { TranscriptSeam } from "./transcript-spacing.ts";
import { transcriptRowOpensTurn } from "../projection/transcript-turns.ts";

/**
 * One classifier decides every vertical seam in the stream.
 *
 * A seam belongs to the row beneath it, so exactly one rung applies and two
 * claims on the same seam resolve by precedence instead of adding up. The
 * first row of a transcript has no seam at all.
 */

/** Reading units inside one turn; a change of unit earns the section rung. */
type RowSection = "prompt" | "prose" | "activity" | "diff" | "card" | "tail";

export type TranscriptSeamContext = {
  /** Identity of the catalog span a row sits in; undefined outside one. */
  spanKeyOf?: (itemKey: string) => string | undefined;
  /** A prompt the host recorded inside the open turn rather than opening one. */
  continuation?: (itemKey: string) => boolean;
};

function isMarker(item: DisplayTranscriptItem): boolean {
  return item.kind === "time_marker" || item.kind === "unread_marker";
}

function sectionOf(item: DisplayTranscriptItem): RowSection {
  switch (item.kind) {
    case "user":
    case "pending_user":
      return "prompt";
    case "assistant":
    case "draft":
    case "fallback":
      return "prose";
    case "tool":
    case "activity_span":
    case "worker_group":
      return "activity";
    case "file_edit":
    case "worker_file_edit":
      return "diff";
    case "turn_tail":
      return "tail";
    default:
      return "card";
  }
}

function seamFor(
  item: DisplayTranscriptItem,
  section: RowSection,
  previous: { key: string; section: RowSection } | undefined,
  ctx: TranscriptSeamContext,
): TranscriptSeam | undefined {
  if (!previous) return undefined;
  if (transcriptRowOpensTurn(item, ctx.continuation)) return "turn";
  // The tail reports on the turn above it, so it stays with that turn.
  if (item.kind === "turn_tail") return "row";
  const spanKeyOf = ctx.spanKeyOf;
  if (spanKeyOf && spanKeyOf(previous.key) !== spanKeyOf(item.key)) return "section";
  return section === previous.section ? "row" : "section";
}

/** Maps each row key to the seam above it; rows absent from the map carry none. */
export function transcriptRowSeams(
  items: readonly DisplayTranscriptItem[],
  ctx: TranscriptSeamContext = {},
): Map<string, TranscriptSeam> {
  const seams = new Map<string, TranscriptSeam>();
  let previous: { key: string; section: RowSection } | undefined;
  let markers: string[] = [];

  const settle = (seam: TranscriptSeam | undefined, key: string): void => {
    if (markers.length === 0) {
      if (seam) seams.set(key, seam);
      return;
    }
    // A marker dates the row beneath it: it takes that row's seam, and the row sits tight.
    const [first, ...rest] = markers;
    if (seam && first) seams.set(first, seam);
    for (const marker of rest) seams.set(marker, "row");
    seams.set(key, "row");
    markers = [];
  };

  for (const item of items) {
    if (isMarker(item)) {
      markers.push(item.key);
      continue;
    }
    const section = sectionOf(item);
    settle(seamFor(item, section, previous, ctx), item.key);
    previous = { key: item.key, section };
  }

  // A marker with nothing beneath it still separates from the rows above.
  const [first, ...rest] = markers;
  if (first && previous) seams.set(first, "section");
  for (const marker of rest) seams.set(marker, "row");

  return seams;
}
