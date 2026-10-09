import { projectUserMessageParts } from "../content/user-message-parts.ts";
import { attachmentRailHeight } from "./user-message-height-estimate.ts";
import { DEFAULT_TRANSCRIPT_SPACING, type TranscriptSpacing } from "./transcript-spacing.ts";
import type { TranscriptItem } from "../projection/transcript-item-model.ts";
import { markdownHeightEstimate } from "./markdown-height-estimate.ts";
import { progressChangesToSteps, progressUpdateSummaryLabel } from "../../progress/progress-model.ts";

export type TranscriptRowMetrics = {
  widthPx: number;
  /** Root font size, which rem lengths resolve against. */
  remPx: number;
  bodyPx: number;
  spacing?: TranscriptSpacing;
};

/** How one presented artifact renders in a row. */
export type PresentedVisual =
  | { kind: "image"; width: number; height: number }
  | { kind: "unsized" }
  | { kind: "reference" };

/** Average glyph width remains an estimate; box dimensions come from layout. */
const AVERAGE_GLYPH_EM = 0.5;
const MIN_CHARS_PER_LINE = 16;
const PARAGRAPH_GAP_LINES = 0.5;
const VISUAL_FRAME_BORDER_PX = 2;
const REFERENCE_CHIP_PX = 28;

/** Height of body prose wrapped to `widthPx`. */
export function proseHeight(text: string, widthPx: number, bodyPx: number, lineHeight = DEFAULT_TRANSCRIPT_SPACING.proseLineHeight): number {
  const trimmed = text.trim();
  if (!trimmed || widthPx <= 0 || bodyPx <= 0) return 0;
  const perLine = Math.max(
    MIN_CHARS_PER_LINE,
    Math.floor(widthPx / (bodyPx * AVERAGE_GLYPH_EM)),
  );
  let lines = 0;
  for (const paragraph of trimmed.split("\n")) {
    const length = paragraph.trim().length;
    lines += length > 0 ? Math.ceil(length / perLine) : PARAGRAPH_GAP_LINES;
  }
  return lines * bodyPx * lineHeight;
}

function visualHeight(visual: PresentedVisual, widthPx: number, remPx: number, spacing: TranscriptSpacing): number {
  if (visual.kind === "reference") return REFERENCE_CHIP_PX;
  const frameWidth = Math.max(
    0,
    Math.min(widthPx - spacing.visualRight * remPx, spacing.visualWidth),
  );
  // The host stamps pixel size, so a sized frame reserves its final box.
  const media =
    visual.kind === "image" && visual.width > 0
      ? frameWidth * (visual.height / visual.width)
      : spacing.visualPlaceholder * remPx;
  return media + VISUAL_FRAME_BORDER_PX + (spacing.visualBefore + spacing.visualAfter) * remPx;
}

/** Height of a row's present block: one prominent visual, or a strip. */
export function presentedVisualsHeight(
  visuals: readonly PresentedVisual[],
  widthPx: number,
  remPx: number,
  spacing: TranscriptSpacing = DEFAULT_TRANSCRIPT_SPACING,
): number {
  const first = visuals[0];
  if (!first) return 0;
  const marginBottom = spacing.presentAfter * remPx;
  if (visuals.length === 1) return visualHeight(first, widthPx, remPx, spacing) + marginBottom;
  const stripWidth = Math.min(widthPx, spacing.stripWidth);
  const gap = spacing.stripGap * remPx;
  const columns = Math.max(
    1,
    Math.floor((stripWidth + gap) / Math.max(1, Math.min(stripWidth, spacing.stripColumn) + gap)),
  );
  const columnWidth = (stripWidth - (columns - 1) * gap) / columns;
  let rowsHeight = 0;
  let rows = 0;
  for (let start = 0; start < visuals.length; start += columns) {
    const row = visuals.slice(start, start + columns);
    rowsHeight += Math.max(...row.map((visual) => visualHeight(visual, columnWidth, remPx, spacing)));
    rows += 1;
  }
  return rowsHeight + (rows - 1) * gap + marginBottom;
}

/** Content height for prose rows, or undefined when content does not set it. */
export function transcriptRowContentEstimate(
  item: TranscriptItem,
  metrics: TranscriptRowMetrics,
  visuals: readonly PresentedVisual[],
): number | undefined {
  const { widthPx, remPx, bodyPx, spacing = DEFAULT_TRANSCRIPT_SPACING } = metrics;
  if (widthPx <= 0) return undefined;
  switch (item.kind) {
    case "assistant":
    case "draft": {
      const height =
        presentedVisualsHeight(visuals, widthPx, remPx, spacing) +
        markdownHeightEstimate(item.text, widthPx, bodyPx, spacing);
      if (height <= 0) return undefined;
      return Math.round(height);
    }
    case "pending_user":
    case "user": {
      const bubbleWidth = widthPx * spacing.userWidth / 100 - 2 * spacing.userPaddingX * remPx;
      const projection = projectUserMessageParts(item.text, item.kind === "user" ? item.contentParts : undefined);
      const labels = projection.chips.map((chip) => `${chip.label} ${chip.detail}`.trim());
      const pendingLabels = item.kind === "pending_user" ? item.pending.attachmentLabels ?? [] : [];
      return Math.round(
        attachmentRailHeight(labels, bubbleWidth, remPx, bodyPx) +
        attachmentRailHeight(pendingLabels, bubbleWidth, remPx, bodyPx) +
        2 * spacing.userPaddingY * remPx +
          presentedVisualsHeight(visuals, bubbleWidth, remPx, spacing) +
          proseHeight(projection.prose, bubbleWidth, bodyPx, spacing.userLineHeight),
      );
    }
    case "progress_complete":
    case "progress_update": {
      const labels = item.kind === "progress_update" && item.summary
        ? [progressUpdateSummaryLabel(item.summary, item.initial)]
        : (item.kind === "progress_update" && !item.initial
          ? progressChangesToSteps(item.changes) : item.steps).map((step) => step.label);
      const font = bodyPx * 0.9;
      const cardWidth = Math.min(widthPx, remPx * 32);
      const labelWidth = Math.max(font, cardWidth - remPx * 3);
      const body = labels.reduce((height, label) => height + markdownHeightEstimate(label, labelWidth, font, spacing), 0);
      return Math.round(font * 1.35 + remPx * 1.25 + 3 + body + Math.max(0, labels.length - 1) * remPx * 0.3);
    }
    default:
      return undefined;
  }
}
