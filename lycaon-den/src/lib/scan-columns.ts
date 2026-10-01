import type { FindingLedgerEntry, SecurityFinding } from "../api/types.ts";
import type { ColumnSpec } from "../list/list-columns.ts";
import { compareStrings, type SortDirection } from "../list/list-sort.ts";
import { formatLocation, primaryLocation } from "./scan-display.ts";
import { findingLevelRank } from "./scan-findings-table.ts";

/** Surface id for persisted pane size, column widths, and sort. */
export const SECURITY_LIST_SURFACE = "security";

function hintCode(finding: SecurityFinding): string {
  return finding.properties?.lycaon?.hint_code?.trim() ?? "";
}

/** Location sorting uses raw paths to avoid render context. */
export const SECURITY_COLUMNS: readonly ColumnSpec<SecurityFinding>[] = [
  {
    key: "severity",
    label: "Severity",
    width: { basis: 100, min: 72, max: 160 },
    firstDirection: "desc",
    compare: (left, right, direction) => {
      const cmp = findingLevelRank(left.level) - findingLevelRank(right.level);
      // Lower ranks represent higher severity.
      return direction === "desc" ? cmp : -cmp;
    },
  },
  {
    key: "message",
    label: "Finding",
    width: { basis: 240, min: 160, max: 640, grow: true },
    compare: (left, right, direction) =>
      compareStrings(
        left.message?.trim() ?? "",
        right.message?.trim() ?? "",
        direction,
      ),
  },
  {
    key: "location",
    label: "Location",
    width: { basis: 180, min: 80, max: 480 },
    compare: (left, right, direction) =>
      compareStrings(
        formatLocation(primaryLocation(left)),
        formatLocation(primaryLocation(right)),
        direction,
      ),
  },
  {
    key: "hint",
    label: "Hint",
    width: { basis: 130, min: 64, max: 280 },
    // Finding details repeat the hint.
    optional: true,
    compare: (left, right, direction: SortDirection) =>
      compareStrings(hintCode(left), hintCode(right), direction),
  },
];

/** Surface id for the ledger's persisted pane size, column widths, and sort. */
export const LEDGER_LIST_SURFACE = "security-ledger";

/** No comparators: the sidecar sorts the ledger. */
export const LEDGER_COLUMNS: readonly ColumnSpec<FindingLedgerEntry>[] = [
  {
    // Matches the lead-control width of other list rows.
    key: "select",
    label: "Select",
    headerHidden: true,
    width: { basis: 28, min: 28, max: 28 },
  },
  {
    key: "severity",
    label: "Severity",
    width: { basis: 100, min: 72, max: 160 },
    sortable: true,
    firstDirection: "desc",
  },
  {
    key: "finding",
    label: "Finding",
    width: { basis: 240, min: 160, max: 640, grow: true },
    sortable: true,
  },
  {
    key: "location",
    label: "Location",
    width: { basis: 200, min: 120, max: 480 },
    sortable: true,
  },
  {
    key: "state",
    label: "State",
    width: { basis: 128, min: 88, max: 240 },
    sortable: true,
    firstDirection: "desc",
  },
  {
    key: "last_seen",
    label: "Last seen",
    width: { basis: 108, min: 72, max: 200 },
    sortable: true,
    firstDirection: "desc",
    // The detail pane carries the exact times.
    optional: true,
  },
];

export function defaultSecurityOrder(
  rows: readonly SecurityFinding[],
): SecurityFinding[] {
  return rows
    .map((row, index) => ({ row, index }))
    .sort(
      (a, b) =>
        findingLevelRank(a.row.level) - findingLevelRank(b.row.level) ||
        a.index - b.index,
    )
    .map((entry) => entry.row);
}
