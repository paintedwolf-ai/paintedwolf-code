import { describe, expect, it } from "vitest";
import type { SecurityFinding } from "../api/types.ts";
import {
  buildFindingsExportBlob,
  findingsExportFilename,
  findingsToCsv,
  findingsToJsonl,
} from "./scan-export.ts";

const highFinding: SecurityFinding = {
  rule_id: "r-high",
  level: "high",
  message: "High issue",
  locations: [{ uri: "a.go", start_line: 1 }],
  fingerprints: { primary: "fp-h" },
  tool: { driver_id: "opengrep", name: "OpenGrep" },
  properties: { lycaon: { hint_code: "H1" } },
};

const findings: SecurityFinding[] = [
  highFinding,
  {
    rule_id: "r-low",
    level: "low",
    message: "Low issue",
    locations: [{ uri: "b.go", start_line: 2 }],
    fingerprints: { primary: "fp-l" },
    tool: { driver_id: "opengrep", name: "OpenGrep" },
  },
];

describe("scan-export", () => {
  it("retains complete evidence in JSONL and CSV", () => {
    const finding: SecurityFinding = {
      ...highFinding,
      dataflow: {
        source: { location: { uri: "app.py", start_line: 7 }, callee: { location: { uri: "app.py", start_line: 2 } } },
        intermediates: [{ uri: "app.py", start_line: 8 }],
        sink: { location: { uri: "app.py", start_line: 9 } },
      },
    };
    expect(JSON.parse(findingsToJsonl([finding])).dataflow).toEqual(finding.dataflow);
    expect(findingsToCsv([finding])).toContain('""callee""');
  });
  it("builds jsonl and csv blobs", () => {
    const jsonl = findingsToJsonl(findings);
    expect(jsonl).toContain('"level":"high"');
    expect(jsonl.trim().split("\n")).toHaveLength(2);

    const csv = findingsToCsv(findings);
    expect(csv.split("\n")[0]).toContain("level,rule_id");
    expect(csv).toContain("High issue");

    const blob = buildFindingsExportBlob(findings, "csv");
    expect(blob.type).toContain("text/csv");
    expect(blob.size).toBeGreaterThan(10);
  });

  it("names export files by scope", () => {
    expect(findingsExportFilename("abc", "jsonl", "run")).toBe(
      "scan-abc-run.jsonl",
    );
    expect(findingsExportFilename("abc", "csv", "visible")).toBe(
      "scan-abc-visible.csv",
    );
  });
});
