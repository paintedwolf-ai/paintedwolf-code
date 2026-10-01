import { formatScanStatus } from "./scan-display.ts";
import type {
  CodeScan,
  CodeScanStatus,
  ScanCoverageStatus,
  SecurityFinding,
  SecurityFullPass,
  SecurityFullPassMember,
  SecurityOverview,
} from "../api/types.ts";

export type CoverageChipState = ScanCoverageStatus | "incremental" | "unknown";

function parseTime(iso: string | undefined): number | undefined {
  if (!iso?.trim()) return undefined;
  const t = Date.parse(iso);
  return Number.isFinite(t) ? t : undefined;
}

/** Later of the baseline generation and the last full pass; undefined before either exists. */
export function securityBaselineTime(overview: SecurityOverview | null | undefined): string | undefined {
  if (!overview) return undefined;
  const baseline = overview.baseline?.created_at;
  const full = overview.last_full?.completed_at;
  const b = parseTime(baseline);
  const f = parseTime(full);
  if (b === undefined) return f === undefined ? undefined : full;
  if (f === undefined) return baseline;
  return f > b ? full : baseline;
}

const DAY_MS = 24 * 60 * 60 * 1000;

/** Clock time today, weekday + clock inside a week, otherwise month + day + clock. */
export function formatCoverageTime(iso: string | undefined, now = Date.now()): string {
  const t = parseTime(iso);
  if (t === undefined) return "—";
  const date = new Date(t);
  const clock = date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  const today = new Date(now);
  const sameDay =
    date.getFullYear() === today.getFullYear() &&
    date.getMonth() === today.getMonth() &&
    date.getDate() === today.getDate();
  if (sameDay) return clock;
  if (now - t < 7 * DAY_MS) {
    return `${date.toLocaleDateString(undefined, { weekday: "short" })} ${clock}`;
  }
  return `${date.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${clock}`;
}

export function coverageSentence(overview: SecurityOverview | null | undefined, now = Date.now()): string {
  if (!overview) return "Coverage has not loaded.";
  if (!overview.enabled) return "Automatic security scanning is off.";
  if (!overview.baseline && !overview.last_full) {
    return "Incremental scanning not started · no full scan yet";
  }
  const full = overview.last_full;
  if (!full) {
    return `Incremental since ${formatCoverageTime(overview.baseline?.created_at, now)} · no full scan yet`;
  }
  const at = formatCoverageTime(full.completed_at ?? full.started_at, now);
  switch (overview.coverage_status ?? full.coverage_status) {
    case "bounded":
      return `Bounded as of ${at} · some directories were not analyzed`;
    case "partial":
      return `Partial as of ${at} · some code or scanners could not be fully analyzed`;
    case "unavailable":
      return `Unavailable as of ${at} · no scanner established coverage`;
    case "complete":
      return `Complete as of ${at}`;
    default:
      return `Coverage unknown for the full scan at ${at}`;
  }
}

/** Coverage the chip reports: the overview's own status, else the last pass, else incremental. */
export function coverageChipState(overview: SecurityOverview | null | undefined): CoverageChipState {
  return overview?.coverage_status ?? overview?.last_full?.coverage_status ?? (overview?.last_full ? "unknown" : "incremental");
}

export function coverageChipLabel(state: CoverageChipState): string {
  switch (state) {
    case "complete":
      return "Complete";
    case "bounded":
      return "Bounded";
    case "partial":
      return "Partial";
    case "unavailable":
      return "Unavailable";
    case "unknown":
      return "Unknown";
    default:
      return "Incremental";
  }
}

export function coverageDotState(state: CoverageChipState): "seen" | "partial" | "unseen" | "unavailable" | "empty" {
  switch (state) {
    case "complete":
      return "seen";
    case "bounded":
      return "partial";
    case "partial":
      return "unseen";
    case "unavailable":
      return "unavailable";
    default:
      return "empty";
  }
}

/** Findings introduced at or after the baseline time; everything when no baseline exists. */
export function filterFindingsNewSince<T extends Pick<SecurityFinding, "history">>(
  findings: readonly T[],
  sinceIso: string | undefined,
): T[] {
  const since = parseTime(sinceIso);
  if (since === undefined) return [...findings];
  return findings.filter((finding) => {
    const at = parseTime(finding.history?.introduced_at);
    return at !== undefined && at >= since;
  });
}

/** Scanner ids checked when the full-scan panel opens: every available scanner. */
export function defaultFullScanSelection(overview: SecurityOverview | null | undefined): Set<string> {
  return new Set((overview?.scanners ?? []).filter((s) => s.available).map((s) => s.id));
}

export function fullScanScanStatus(scan: CodeScan): string {
  if (scan.status === "complete") return scan.coverage_status === "unavailable" ? "Unavailable" : "Complete";
  return formatScanStatus(scan.status, scan.long_running);
}

/** Progress measures completed chunks and reports the total file count. */
export function fullScanProgressReadout(scan: CodeScan): string {
  const progress = scan.progress;
  if (!progress) {
    return scan.status === "running" ? "Analyzing files" : fullScanScanStatus(scan);
  }
  return `${progress.completed.toLocaleString()} of ${progress.chunks.toLocaleString()} chunks · ${progress.files.toLocaleString()} files`;
}

/** Fraction done in [0, 1]; 1 once the scan completed without reporting chunks. */
export function fullScanProgressRatio(scan: CodeScan): number {
  const progress = scan.progress;
  if (!progress || progress.chunks <= 0) return scan.status === "complete" ? 1 : 0;
  return Math.min(1, Math.max(0, progress.completed / progress.chunks));
}

/** A full pass as the Security surfaces present it; `live` is false while a finished pass is held on screen. */
export type DisplayedFullPass = {
  pass: SecurityFullPass;
  live: boolean;
};

export function fullPassMemberStatus(member: SecurityFullPassMember): string {
  switch (member.phase) {
    case "waiting_for_scanner":
    case "waiting_for_pass":
      return "Waiting";
    case "not_started":
      return "Not started";
  }
  return member.scan ? fullScanScanStatus(member.scan) : "Pending";
}

const scannerList = new Intl.ListFormat("en", { style: "long", type: "conjunction" });

/** Joins scanner labels for a sentence: "A", "A and B", "A, B, and C". */
export function joinScannerLabels(labels: readonly string[]): string {
  return scannerList.format(labels);
}

/** Labels of the members still finishing earlier work, which the rest of the pass waits on. */
export function fullPassWaitingOn(
  pass: SecurityFullPass,
  labelOf: (scannerId: string) => string,
): string[] {
  return pass.members
    .filter((member) => member.phase === "waiting_for_scanner")
    .map((member) => labelOf(member.scanner_id));
}

/**
 * Says why a member that has no scan yet is waiting, or how far its scan has
 * come. A free member names the scanners it waits on.
 */
export function fullPassMemberReadout(
  member: SecurityFullPassMember,
  waitingOn: readonly string[] = [],
): string {
  switch (member.phase) {
    case "waiting_for_scanner":
      return "Finishing an earlier scan first";
    case "waiting_for_pass":
      return waitingOn.length > 0
        ? `Waiting for ${joinScannerLabels(waitingOn)}`
        : "Starts with the other scanners";
    case "not_started":
      return "Left the pass before it started";
  }
  return member.scan ? fullScanProgressReadout(member.scan) : "";
}

export function fullPassMemberRatio(member: SecurityFullPassMember): number {
  return member.scan ? fullScanProgressRatio(member.scan) : 0;
}

/** Whether the member's progress is known, so its bar may state a value. */
export function fullPassMemberMeasured(member: SecurityFullPassMember): boolean {
  return member.scan != null && (member.scan.progress != null || member.scan.status === "complete");
}

const TERMINAL_SCAN_STATUSES: ReadonlySet<CodeScanStatus> = new Set([
  "complete",
  "failed",
  "timed_out",
  "canceled",
  "superseded",
]);

/** Members with no work left in the pass: a run that ended, or a scanner that left before it started. */
export function fullPassDoneMembers(pass: SecurityFullPass): number {
  return pass.members.filter((member) =>
    member.phase === "not_started" ||
    (member.scan != null && TERMINAL_SCAN_STATUSES.has(member.scan.status)),
  ).length;
}

/** The coverage strip's full-scan control: it starts a pass, or discloses the one on screen. */
export function fullScanToggleLabel(displayed: DisplayedFullPass | null): string {
  if (!displayed) return "Run full scan";
  if (!displayed.live) return "Full scan finished";
  return `Full scan · ${fullPassDoneMembers(displayed.pass)} of ${displayed.pass.members.length} done`;
}
