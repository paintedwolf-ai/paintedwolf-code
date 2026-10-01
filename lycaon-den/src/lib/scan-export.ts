import type { SecurityFinding } from "../api/types.ts";
import { formatLocation, primaryLocation } from "./scan-display.ts";

export type ScanExportFormat = "jsonl" | "csv";

function csvEscape(value: string): string {
  if (/[",\n\r]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`;
  }
  return value;
}

function findingRow(
  finding: SecurityFinding,
  repoRoot?: string | null,
): Record<string, string> {
  const loc = primaryLocation(finding);
  return {
    level: finding.level ?? "",
    rule_id: finding.rule_id ?? "",
    message: finding.message ?? "",
    location: formatLocation(loc, repoRoot),
    uri: loc?.uri ?? "",
    start_line: loc?.start_line != null ? String(loc.start_line) : "",
    hint_code: finding.properties?.lycaon?.hint_code ?? "",
    fingerprint: finding.fingerprints?.primary ?? "",
    engine: finding.tool?.driver_id || finding.tool?.name || "",
    dataflow: finding.dataflow ? JSON.stringify(finding.dataflow) : "",
  };
}

const CSV_COLUMNS = [
  "level",
  "rule_id",
  "message",
  "location",
  "uri",
  "start_line",
  "hint_code",
  "fingerprint",
  "engine",
  "dataflow",
] as const;

export function findingsToJsonl(
  findings: SecurityFinding[],
  repoRoot?: string | null,
): string {
  return (
    findings.map((f) => JSON.stringify({ ...f, location: formatLocation(primaryLocation(f), repoRoot) })).join("\n") +
    (findings.length ? "\n" : "")
  );
}

export function findingsToCsv(
  findings: SecurityFinding[],
  repoRoot?: string | null,
): string {
  const header = CSV_COLUMNS.join(",");
  const lines = findings.map((f) => {
    const row = findingRow(f, repoRoot);
    return CSV_COLUMNS.map((col) => csvEscape(row[col] ?? "")).join(",");
  });
  return [header, ...lines].join("\n") + "\n";
}

export function buildFindingsExportBlob(
  findings: SecurityFinding[],
  format: ScanExportFormat,
  repoRoot?: string | null,
): Blob {
  if (format === "csv") {
    return new Blob([findingsToCsv(findings, repoRoot)], {
      type: "text/csv;charset=utf-8",
    });
  }
  return new Blob([findingsToJsonl(findings, repoRoot)], {
    type: "application/x-ndjson;charset=utf-8",
  });
}

export function findingsExportFilename(
  scanId: string,
  format: ScanExportFormat,
  scope: "run" | "visible",
): string {
  const suffix = scope === "visible" ? "visible" : "run";
  return `scan-${scanId}-${suffix}.${format}`;
}
