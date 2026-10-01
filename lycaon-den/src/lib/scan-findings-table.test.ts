import { describe, expect, it } from "vitest";
import { at } from "../test/at.ts";
import type { SecurityFinding } from "../api/types.ts";
import {
  filterFindingsByLevels,
} from "./scan-findings-table.ts";
import { SECURITY_COLUMNS } from "./scan-columns.ts";
import { sortRows } from "../list/list-columns.ts";
import type { SortDirection } from "../list/list-sort.ts";

function sortFindings(
  rows: SecurityFinding[],
  key: string,
  dir: SortDirection,
): SecurityFinding[] {
  return sortRows(rows, SECURITY_COLUMNS, { key, dir });
}

const findings: SecurityFinding[] = [
  {
    rule_id: "a",
    level: "medium",
    message: "Beta issue",
    locations: [{ uri: "b.go", start_line: 2 }],
    fingerprints: { primary: "fp-b" },
    tool: { driver_id: "x", name: "X" },
    properties: { lycaon: { hint_code: "H_B" } },
  },
  {
    rule_id: "b",
    level: "critical",
    message: "Alpha issue",
    locations: [{ uri: "a.go", start_line: 1 }],
    fingerprints: { primary: "fp-a" },
    tool: { driver_id: "x", name: "X" },
    properties: { lycaon: { hint_code: "H_A" } },
  },
];

describe("scan-findings-table", () => {
  it("sorts findings by severity descending", () => {
    const sorted = sortFindings(findings, "severity", "desc");
    expect(sorted.map((f) => f.fingerprints.primary)).toEqual(["fp-a", "fp-b"]);
  });

  it("sorts findings by location ascending", () => {
    const sorted = sortFindings(findings, "location", "asc");
    expect(at(at(sorted, 0).locations, 0).uri).toBe("a.go");
  });

  it("sorts findings by hint", () => {
    expect(at(sortFindings(findings, "hint", "asc"), 0).fingerprints.primary).toBe(
      "fp-a",
    );
  });

  it("filters by severity; empty set means all", () => {
    expect(filterFindingsByLevels(findings, new Set())).toEqual(findings);
    const filtered = filterFindingsByLevels(findings, new Set(["critical"]));
    expect(filtered).toHaveLength(1);
    expect(at(filtered, 0).fingerprints.primary).toBe("fp-a");
  });
});
