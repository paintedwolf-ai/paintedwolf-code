import type {
  AdvisoryRef,
  CodeScanEvent,
  CodeScan,
  FindingLevel,
  ScanGuidanceSummary,
  SecurityFinding,
  SecurityFindingLocation,
} from "../api/types.ts";

const LEVEL_ORDER: FindingLevel[] = [
  "critical",
  "high",
  "medium",
  "low",
  "info",
  "unknown",
];

const LEVEL_LABEL: Record<FindingLevel, string> = {
  critical: "Critical",
  high: "High",
  medium: "Medium",
  low: "Low",
  info: "Info",
  unknown: "Unknown",
};

export function levelLabel(level?: FindingLevel): string {
  if (!level) return "Info";
  return LEVEL_LABEL[level];
}

export function levelChipClass(level?: FindingLevel): string {
  switch (level) {
    case "critical":
      return "den-scans-level--critical";
    case "high":
      return "den-scans-level--high";
    case "medium":
      return "den-scans-level--medium";
    case "low":
      return "den-scans-level--low";
    default:
      return "den-scans-level--info";
  }
}

export function primaryLocation(
  finding: SecurityFinding,
): SecurityFindingLocation | undefined {
  return finding.locations?.[0];
}

function normalizeScanUri(uri: string): string {
  let path = uri.trim();
  if (!path) return "";
  if (/^file:\/\//i.test(path)) {
    path = path.replace(/^file:\/\//i, "");
    if (/^localhost\//i.test(path)) {
      path = path.slice("localhost".length);
    } else if (!/^[A-Za-z]:\//.test(path) && !path.startsWith("/")) {
      path = `/${path}`;
    }
  }
  return path.replace(/\\/g, "/");
}

export function relativizeScanUri(uri: string, repoRoot?: string | null): string {
  const path = normalizeScanUri(uri);
  if (!path) return "";
  const root = normalizeScanUri(repoRoot ?? "").replace(/\/+$/, "");
  if (!root) return path;
  if (path === root) return ".";
  const prefix = `${root}/`;
  if (path.startsWith(prefix)) return path.slice(prefix.length);
  // Match case-insensitive filesystems.
  const pathLower = path.toLowerCase();
  const prefixLower = prefix.toLowerCase();
  if (pathLower.startsWith(prefixLower)) {
    return path.slice(prefix.length);
  }
  return path;
}

export function formatLocation(
  loc?: SecurityFindingLocation,
  repoRoot?: string | null,
): string {
  if (!loc?.uri) return "(no location)";
  const uri = relativizeScanUri(loc.uri, repoRoot);
  if (loc.start_line && loc.start_line > 0) {
    return `${uri}:${loc.start_line}`;
  }
  return uri;
}

/** Canonical id first, then every other id the vulnerability is published under. */
export function formatAdvisory(adv?: AdvisoryRef): string {
  if (!adv) return "";
  const parts: string[] = [];
  for (const raw of [
    adv.osv_id,
    ...(adv.cve_ids ?? []),
    ...(adv.ghsa_ids ?? []),
    ...(adv.aliases ?? []),
  ]) {
    const id = raw?.trim();
    if (id && !parts.includes(id)) parts.push(id);
  }
  return parts.join(" · ");
}

export function advisoryKindLabel(adv?: AdvisoryRef): string {
  return adv?.kind === "malicious_package" ? "Malicious package" : "";
}

/** Zero-count levels remain visible. */
export function kpiEntries(
  byLevel?: Record<string, number>,
): Array<{ level: FindingLevel; count: number; label: string }> {
  return LEVEL_ORDER.map((level) => ({
    level,
    count: byLevel?.[level] ?? 0,
    label: LEVEL_LABEL[level],
  }));
}

export function formatScannerId(
  id?: string,
  labels: Readonly<Record<string, string>> = {},
): string {
  const raw = id?.trim();
  if (!raw) return "Security scan";
  const mapped = labels[raw];
  if (mapped) return mapped;
  return raw;
}

export function formatScanEngine(
  scan: CodeScan,
  labels: Readonly<Record<string, string>> = {},
): string {
  if (scan.scanner_id?.trim()) {
    return formatScannerId(scan.scanner_id, labels);
  }
  const categories = scan.categories?.filter((c) => c.trim()) ?? [];
  if (categories.length > 0) {
    return categories.map((c) => c.toUpperCase()).join(" · ");
  }
  return "Security scan";
}

export function formatScanTimestamp(iso?: string): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

export function formatScanStatus(status?: CodeScan["status"], longRunning = false): string {
  switch (status) {
    case "complete":
      return "Completed";
    case "running":
      return longRunning ? "Running longer than usual" : "Running";
    case "pending":
      return "Pending";
    case "failed":
      return "Failed";
    case "timed_out":
      return "Timed out";
    case "canceled":
      return "Canceled";
    case "superseded":
      return "Superseded";
    default:
      return "Unknown";
  }
}

export function scanHasLimitations(scan: CodeScan): boolean {
  return scan.coverage_status === "partial" || scan.coverage_status === "bounded" || scan.coverage_status === "unavailable" ||
    (scan.warning_summary?.length ?? 0) > 0 || (scan.warnings?.length ?? 0) > 0;
}

export function scanEmptyState(scan: CodeScan | null): { title: string; description?: string } {
  if (!scan) return { title: "Select a scan to review findings" };
  if (scan.detail_pruned_at) return { title: "Scan details were pruned", description: "Retention removed this run’s detailed findings. Its summary remains; run a new scan to inspect current findings." };
  if (scan.status === "pending") {
    return { title: "Scan queued", description: "Waiting to start. Results will load automatically when the run finishes." };
  }
  if (scan.status === "running") {
    return { title: scan.long_running ? "Scan is taking longer than usual" : "Scan running",
      description: scan.progress
        ? `${scan.progress.completed} of ${scan.progress.chunks} chunks analyzed · ${scan.progress.files} files. Results will load when the run finishes.`
        : "Analysis is in progress. Results will load automatically when the run finishes." };
  }
  const stopped = {
    failed: "Scan failed", timed_out: "Scan timed out", canceled: "Scan canceled", superseded: "Scan superseded",
  } as const;
  if (scan.status in stopped) {
    const title = stopped[scan.status as keyof typeof stopped];
    return { title, description: scan.error || (scan.status === "superseded"
      ? "A newer run replaced this scan. Select another run to review its results."
      : "This run did not finish. Select another run or start a full scan.") };
  }
  if (scan.status !== "complete" || scan.coverage_status === "unavailable") {
    return { title: "No findings available", description: scan.error || "This run did not produce a complete analysis. Review the run status and any analysis limitations." };
  }
  if (scanHasLimitations(scan)) {
    return { title: "No findings in analyzed code", description: "Results cover the code the scanner could analyze." };
  }
  return { title: "No findings in this run." };
}

export function scanFindingsCounts(scan: CodeScan): string {
  if (scan.status === "pending" || scan.status === "running") return "Results pending";
  if (scan.status !== "complete") return "No completed results";
  const raw = scan.findings_count ?? 0;
  const stored = scan.findings_stored ?? scan.findings?.length ?? 0;
  if (raw > 0 && stored > 0 && raw !== stored) {
    return `${stored} of ${raw} findings`;
  }
  const n = stored || raw;
  return `${n} ${n === 1 ? "finding" : "findings"}`;
}

export function guidanceForFinding(
  finding: SecurityFinding,
  guidance: ScanGuidanceSummary[] | undefined,
): ScanGuidanceSummary | undefined {
  if (!guidance?.length) return undefined;
  const uri = primaryLocation(finding)?.uri ?? "";
  const line = primaryLocation(finding)?.start_line ?? 0;
  const matches = guidance.filter((g) => g.rule_id === finding.rule_id);
  if (matches.length === 0) return undefined;
  return (
    matches.find((g) => g.file === uri && (line === 0 || g.line === line)) ??
    matches[0]
  );
}


export function hintOverlaySnippet(finding: SecurityFinding): string {
  const rule = finding.rule_id?.trim() || "<rule-id>";
  const code = finding.properties?.lycaon?.hint_code?.trim() || "SCAN_FINDING_UNMAPPED";
  return [
    "# scan-hints.yaml (project overlay)",
    "rule_hints:",
    `  ${JSON.stringify(rule)}: ${code}`,
  ].join("\n");
}
const ACTIVE_SCAN_STATUSES = new Set<CodeScan["status"]>(["pending", "running"]);

export function scansWantLiveRefresh(
  history: CodeScan[],
  liveScan?: CodeScanEvent,
  selected?: CodeScan | null,
): boolean {
  const observed = selected ? [selected, ...history.filter((scan) => scan.id !== selected.id)] : history;
  if (observed.some((scan) => ACTIVE_SCAN_STATUSES.has(scan.status))) {
    return true;
  }
  return !!liveScan && !observed.some((scan) => scan.id === liveScan.scan_id) && ACTIVE_SCAN_STATUSES.has(liveScan.status);
}

export const SCANS_LIVE_REFRESH_MS = 3000;
export const SCANS_LIVE_REFRESH_MAX_MS = 30_000;

/** Patches a known row without listing again. */
export function applyLiveScanPatch(
  rows: CodeScan[],
  live: CodeScanEvent,
): { rows: CodeScan[]; known: boolean; statusChanged: boolean } {
  const idx = rows.findIndex((row) => row.id === live.scan_id);
  const cur = idx < 0 ? undefined : rows[idx];
  if (!cur) {
    return { rows, known: false, statusChanged: false };
  }
  const statusChanged = cur.status !== live.status;
  if (
    !statusChanged &&
    cur.findings_count === live.findings_count &&
    (live.coverage_status == null || cur.coverage_status === live.coverage_status) &&
    (live.error == null || cur.error === live.error) &&
    (live.progress == null || JSON.stringify(cur.progress) === JSON.stringify(live.progress)) &&
    cur.long_running === live.long_running &&
    (live.runtime == null ||
      JSON.stringify(cur.runtime) === JSON.stringify(live.runtime)) &&
    (live.started_at == null || cur.started_at === live.started_at) &&
    (live.long_running_at == null ||
      cur.long_running_at === live.long_running_at) &&
    (live.categories == null ||
      JSON.stringify(cur.categories) === JSON.stringify(live.categories))
  ) {
    return { rows, known: true, statusChanged: false };
  }
  const next = rows.slice();
  next[idx] = {
    ...cur,
    status: live.status,
    findings_count: live.findings_count,
    categories: live.categories ?? cur.categories,
    error: live.error ?? cur.error,
    coverage_status: live.coverage_status ?? cur.coverage_status,
    progress: live.progress ?? cur.progress,
    runtime: live.runtime ?? cur.runtime,
    started_at: live.started_at ?? cur.started_at,
    long_running_at: live.long_running_at ?? cur.long_running_at,
    long_running: live.long_running,
  };
  return { rows: next, known: true, statusChanged };
}

export function liveScanWantsListRefresh(
  known: boolean,
  statusChanged: boolean,
  status: CodeScanEvent["status"],
): boolean {
  if (!known) return true;
  if (!statusChanged) return false;
  return isTerminalScanStatus(status);
}

function isTerminalScanStatus(status: CodeScan["status"]): boolean {
  return (
    status === "complete" ||
    status === "failed" ||
    status === "timed_out" ||
    status === "canceled" ||
    status === "superseded"
  );
}

export function pickDefaultScanId(
  rows: CodeScan[],
  latestScanId?: string,
): string | null {
  const preferLatest = latestScanId?.trim();
  if (preferLatest && rows.some((row) => row.id === preferLatest)) {
    return preferLatest;
  }
  return (
    rows.find((row) => row.status === "complete")?.id ??
    rows[0]?.id ??
    null
  );
}
