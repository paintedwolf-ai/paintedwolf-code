import type { SourceComparisonSide } from "../../api/types.ts";

export type ComparisonEndpoints<T = SourceComparisonSide> = { before: T; after: T };

/** Both endpoints are absent when the selected lens contains no effect. */
export function comparisonEndpoints<T>(diff: { in_range: boolean; before?: T; after?: T; location_changed?: boolean } | null | undefined): ComparisonEndpoints<T> | null {
  if (!diff?.in_range || !diff.before || !diff.after) return null;
  return { before: diff.before, after: diff.after };
}
