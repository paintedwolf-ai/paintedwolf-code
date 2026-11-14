import { createEffect, createRoot, createSignal } from "solid-js";
import { effectiveTextScale } from "../../../platform/desktop/accessibility-text-size.ts";

/** The rungs a vertical seam in the stream may take. */
export type TranscriptSeam = "row" | "section" | "turn";

/**
 * Shared CSS lengths and estimate inputs.
 *
 * Four rungs carry every vertical seam: `partGap` inside one row, then the
 * three `*Gap` placement rungs between rows. Placement never invalidates
 * interior heights. Lengths that separate or surround text are rem, so they
 * follow the accessibility text scale the root font size carries; content
 * maxima stay px.
 */
export const TRANSCRIPT_SPACING = {
  partGap: { value: 0.5714, unit: "rem", affects: "row" },
  rowGap: { value: 0.8571, unit: "rem", affects: "placement" },
  sectionGap: { value: 1.4286, unit: "rem", affects: "placement" },
  turnGap: { value: 5.1429, unit: "rem", affects: "placement" },
  userWidth: { value: 78, unit: "%", affects: "row" },
  userPaddingX: { value: 0.9286, unit: "rem", affects: "row" },
  userPaddingY: { value: 0.6429, unit: "rem", affects: "row" },
  userLineHeight: { value: 1.45, unit: "", affects: "row" },
  proseLineHeight: { value: 1.55, unit: "", affects: "row" },
  paragraphGap: { value: 0.65, unit: "em", affects: "row" },
  headingBefore: { value: 1, unit: "em", affects: "row" },
  headingAfter: { value: 0.4, unit: "em", affects: "row" },
  headingLineHeight: { value: 1.25, unit: "", affects: "row" },
  listIndent: { value: 1.35, unit: "em", affects: "row" },
  listItemGap: { value: 0.2, unit: "em", affects: "row" },
  quoteIndent: { value: 0.85, unit: "em", affects: "row" },
  codePaddingY: { value: 0.75, unit: "em", affects: "row" },
  codePaddingX: { value: 0.85, unit: "em", affects: "row" },
  codeGap: { value: 0.75, unit: "em", affects: "row" },
  codeLineHeight: { value: 1.45, unit: "", affects: "row" },
  cellPaddingY: { value: 0.45, unit: "em", affects: "row" },
  cellPaddingX: { value: 0.7, unit: "em", affects: "row" },
  tableGap: { value: 0.75, unit: "em", affects: "row" },
  ruleGap: { value: 0.85, unit: "em", affects: "row" },
  visualBefore: { value: 0.5, unit: "rem", affects: "row" },
  visualAfter: { value: 0.75, unit: "rem", affects: "row" },
  visualRight: { value: 0.4286, unit: "rem", affects: "row" },
  visualWidth: { value: 760, unit: "px", affects: "row" },
  visualPlaceholder: { value: 7.5, unit: "rem", affects: "row" },
  presentGap: { value: 0.75, unit: "rem", affects: "row" },
  presentAfter: { value: 0.75, unit: "rem", affects: "row" },
  stripGap: { value: 0.5, unit: "rem", affects: "row" },
  stripColumn: { value: 340, unit: "px", affects: "row" },
  stripWidth: { value: 840, unit: "px", affects: "row" },
  referenceGap: { value: 0.35, unit: "rem", affects: "row" },
  referenceMargin: { value: 0.25, unit: "rem", affects: "row" },
  referencePaddingY: { value: 0.3, unit: "rem", affects: "row" },
  referencePaddingX: { value: 0.55, unit: "rem", affects: "row" },
  referenceLineHeight: { value: 1.3, unit: "", affects: "row" },
  messageTimeGap: { value: 0.2857, unit: "rem", affects: "row" },
} as const;

const SEAM_RUNG = {
  row: "rowGap",
  section: "sectionGap",
  turn: "turnGap",
} as const satisfies Record<TranscriptSeam, keyof typeof TRANSCRIPT_SPACING>;

export type TranscriptSpacing = { readonly [K in keyof typeof TRANSCRIPT_SPACING]: number };
export const DEFAULT_TRANSCRIPT_SPACING: TranscriptSpacing = Object.freeze(Object.fromEntries(
  Object.entries(TRANSCRIPT_SPACING).map(([key, spec]) => [key, spec.value]),
) as TranscriptSpacing);
const [spacing, setSpacing] = createSignal(DEFAULT_TRANSCRIPT_SPACING);
export const transcriptSpacing = spacing;

/** The root font size Den declares at scale 1. */
export const TRANSCRIPT_ROOT_REM_PX = 14;
const [rootRemPx, setRootRemPx] = createSignal(TRANSCRIPT_ROOT_REM_PX);
/** Root font size in px; rem seams resolve against it. */
export const transcriptRootRemPx = rootRemPx;

/** One published root size keeps CSS seams and virtual geometry on the same number. */
export function publishTranscriptRootRemPx(px: number): void {
  if (Number.isFinite(px) && px > 0 && px !== rootRemPx()) setRootRemPx(px);
}

/** A seam's height in px, or zero for the first row, which has no seam above it. */
export function transcriptSeamPx(
  seam: TranscriptSeam | undefined,
  values: TranscriptSpacing = spacing(),
  remPx: number = rootRemPx(),
): number {
  return seam ? values[SEAM_RUNG[seam]] * remPx : 0;
}

export function transcriptSpacingProperty(key: keyof TranscriptSpacing): string {
  return `--transcript-${key.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
}

/** One publication updates CSS and every consumer's geometry in the same turn. */
export function setTranscriptSpacing(next: TranscriptSpacing): void {
  const keys = Object.keys(TRANSCRIPT_SPACING) as (keyof TranscriptSpacing)[];
  if (keys.some((key) => !Number.isFinite(next[key]) || next[key] < 0)) {
    throw new Error("Transcript spacing must contain finite nonnegative values.");
  }
  if (keys.every((key) => spacing()[key] === next[key])) return;
  setSpacing(Object.freeze({ ...next }));
}

export function transcriptSpacingStyles(values: TranscriptSpacing): Record<string, string> {
  return Object.fromEntries((Object.keys(TRANSCRIPT_SPACING) as (keyof TranscriptSpacing)[])
    .map((key) => [transcriptSpacingProperty(key), `${values[key]}${TRANSCRIPT_SPACING[key].unit}`]));
}

/** Installs defaults before the first render; later publications preserve DOM identity. */
export function installTranscriptSpacing(root: HTMLElement): () => void {
  return createRoot((dispose) => {
    createEffect(() => {
      for (const [key, value] of Object.entries(transcriptSpacingStyles(spacing()))) {
        if (root.style.getPropertyValue(key) !== value) root.style.setProperty(key, value);
      }
    });
    createEffect(() => {
      // The root font size carries the composed text scale that rem seams follow.
      effectiveTextScale();
      publishTranscriptRootRemPx(Number.parseFloat(getComputedStyle(root).fontSize));
    });
    return dispose;
  });
}

export function transcriptInteriorSignature(values: TranscriptSpacing): string {
  // The renderer digest already identifies defaults; retain only live overrides.
  return JSON.stringify((Object.keys(TRANSCRIPT_SPACING) as (keyof TranscriptSpacing)[])
    .flatMap((key, index) => TRANSCRIPT_SPACING[key].affects === "row" && values[key] !== DEFAULT_TRANSCRIPT_SPACING[key]
      ? [[index, values[key]]] : []));
}

export type TranscriptTypography = { uiFont: string; monoFont: string; scale: number; revision: string };

export function transcriptGeometryKey(metrics: { widthPx: number; remPx: number; bodyPx: number },
  interior: string, typography: TranscriptTypography): string {
  return JSON.stringify([typography.revision, interior, Math.round(metrics.widthPx * 64) / 64, metrics.remPx,
    metrics.bodyPx, typography.uiFont, typography.monoFont, typography.scale]);
}
