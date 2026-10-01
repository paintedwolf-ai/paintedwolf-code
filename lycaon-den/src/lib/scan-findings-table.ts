import type {
  FindingLevel,
  SecurityFinding,
} from "../api/types.ts";
import { type SortDirection } from "../list/list-sort.ts";

export const FINDINGS_PAGE_SIZE = 25;
export const HISTORY_PAGE_SIZE = 6;
export const FINDINGS_QUERY_LIMIT = 500;

export type RunSortKey = "date" | "engine" | "status";

const LEVEL_RANK: Record<FindingLevel, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
  info: 4,
  unknown: 5,
};

export function findingLevelRank(level?: FindingLevel): number {
  if (!level) return LEVEL_RANK.info;
  return LEVEL_RANK[level] ?? LEVEL_RANK.info;
}

/** Empty set means all levels (no filter). */
export function filterFindingsByLevels(
  findings: SecurityFinding[],
  levels: ReadonlySet<FindingLevel>,
): SecurityFinding[] {
  if (levels.size === 0) return findings;
  return findings.filter((f) => f.level != null && levels.has(f.level));
}

function defaultRunSortDirection(key: RunSortKey): SortDirection {
  return key === "date" ? "desc" : "asc";
}

export function nextRunSortDirection(
  currentKey: RunSortKey,
  nextKey: RunSortKey,
  currentDir: SortDirection,
): SortDirection {
  if (currentKey === nextKey) {
    return currentDir === "asc" ? "desc" : "asc";
  }
  return defaultRunSortDirection(nextKey);
}
