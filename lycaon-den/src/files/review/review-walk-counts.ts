import type { ReviewWalkDetails } from "./review-walk-details.ts";

export function lineCountLabel(before: number, after: number): string {
  const unit = after === 1 ? "line" : "lines";
  return before === after ? `${after} ${unit}` : `${before} → ${after} ${unit}`;
}

export function lineCounts(
  details: ReviewWalkDetails,
): { before: number; after: number } | null {
  const before = details.beforeLines;
  const after = details.afterLines;
  return before === null || after === null ? null : { before, after };
}
