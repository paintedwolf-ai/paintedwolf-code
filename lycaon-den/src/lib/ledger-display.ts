import type {
  FindingAbsence,
  FindingIgnore,
  FindingIgnoreEntry,
  FindingLedgerCounts,
  FindingLedgerEntry,
  FindingLedgerState,
  SecurityOverview,
  SecurityScannerState,
} from "../api/types.ts";
import { FINDING_LEDGER_STATE_LABELS } from "./finding-ledger-state.generated.ts";
import { relativeTimeLabel } from "../time/time-copy.ts";

/** Ledger presentation, built only from host-stamped facts. */

export function ledgerStateLabel(state: FindingLedgerState): string {
  return FINDING_LEDGER_STATE_LABELS[state] ?? state;
}

/** Absence states read differently from presence and are dimmed as a group. */
export function ledgerStateIsAbsent(state: FindingLedgerState): boolean {
  return state === "fixed" || state === "not_observed" || state === "unverified";
}

/** Literal class names, so the stylesheet contract can see each one. */
const LEDGER_STATE_MODIFIER: Record<FindingLedgerState, string> = {
  open: "den-ledger-state--open",
  reopened: "den-ledger-state--reopened",
  fixed: "den-ledger-state--fixed",
  not_observed: "den-ledger-state--not-observed",
  unverified: "den-ledger-state--unverified",
  ignored: "den-ledger-state--ignored",
};

export function ledgerStateChipClass(state: FindingLedgerState): string {
  return `den-ledger-state ${LEDGER_STATE_MODIFIER[state] ?? ""}`.trim();
}

/** Why a finding left. Only "fixed" is evidence of a change. */
export function absenceSentence(
  state: FindingLedgerState,
  absence: FindingAbsence | undefined,
): string {
  if (!ledgerStateIsAbsent(state)) return "";
  if (state === "fixed") {
    return "A scanner re-read this and no longer reports it. That is evidence of a change, not proof the code is correct.";
  }
  if (state === "unverified") {
    return absence?.execution_moved
      ? "The scanner's rules or engine changed after this stopped being reported, so the pass behind its absence no longer applies. The next full pass settles it."
      : "Nothing established what happened here. The next full pass settles it.";
  }
  const coverage = absence?.coverage_status;
  if (coverage === "bounded") {
    return "The pass that stopped reporting this was bounded — a source budget cut a directory before it was read. Its absence establishes nothing.";
  }
  if (coverage === "partial") {
    return "The pass that stopped reporting this could not analyze everything, so its absence establishes nothing.";
  }
  return "The pass that stopped reporting this did not cover the whole tree, so its absence establishes nothing.";
}

/** Counts cover the project across all result pages. */
export function ledgerCountsSummary(counts: FindingLedgerCounts | undefined): string {
  if (!counts) return "";
  const parts = [`${counts.open} open`];
  if (counts.reopened > 0) parts.push(`${counts.reopened} reopened`);
  parts.push(`${counts.fixed} fixed`);
  if (counts.unverified > 0) parts.push(`${counts.unverified} unverified`);
  if (counts.not_observed > 0) parts.push(`${counts.not_observed} not observed`);
  if (counts.ignored > 0) parts.push(`${counts.ignored} ignored`);
  return parts.join(" · ");
}

/** How many findings the Open tab holds. */
export function ledgerOpenCount(counts: FindingLedgerCounts | undefined): number {
  if (!counts) return 0;
  return counts.open + counts.reopened;
}

/** Every row the project has ever recorded. */
export function ledgerAllCount(counts: FindingLedgerCounts | undefined): number {
  if (!counts) return 0;
  return (
    counts.open +
    counts.reopened +
    counts.fixed +
    counts.not_observed +
    counts.unverified +
    counts.ignored
  );
}

/**
 * The empty-list line when no filter is set. Scans are change-driven, so
 * without a complete full pass the ledger speaks only for scanned files.
 */
export function ledgerEmptyLine(
  counts: FindingLedgerCounts | undefined,
  overview: SecurityOverview | null,
): string {
  const recorded =
    ledgerAllCount(counts) > 0 ? "No open findings reported." : "No findings recorded yet.";
  if (!overview) return recorded;
  const full = overview.last_full;
  if (!full?.completed_at) {
    return `${recorded} No full scan has run, so only changed files have been scanned.`;
  }
  if (full.coverage_status !== "complete") {
    return `${recorded} The last full scan did not cover the whole project.`;
  }
  return recorded;
}

/** Describes the decision covering a row, including a lapsed one. */
export function ignoreSentence(ignore: FindingIgnore | undefined): string {
  if (!ignore) return "";
  const reason = ignore.reason?.trim();
  const matched = ignore.matched_on?.trim();
  if (ignore.expired) {
    const until = ignore.expires_on ? ` on ${ignore.expires_on}` : "";
    return `This project's decision to ignore it lapsed${until}, so the finding is open again.${
      reason ? ` The reason given was: ${reason}` : ""
    }`;
  }
  const scope = matched ? ` Matched on ${matched}.` : "";
  const until = ignore.expires_on ? ` Lapses on ${ignore.expires_on}.` : "";
  // Punctuate the reason so the sentences after it don't run on.
  const stated = reason || "This project decided not to act on it.";
  const opening = /[.!?]$/u.test(stated) ? stated : `${stated}.`;
  return `${opening}${scope}${until}`;
}

/** The entry's predicates in the ignore file's syntax. */
export function ignoreEntrySummary(entry: FindingIgnoreEntry): string {
  const parts: string[] = [];
  const add = (key: string, value: string | undefined) => {
    if (value?.trim()) parts.push(`${key}: ${value.trim()}`);
  };
  add("path", entry.path);
  add("kind", entry.kind);
  add("scanner", entry.scanner);
  add("rule", entry.rule);
  add("advisory", entry.advisory);
  add("fingerprint", entry.fingerprint);
  return parts.join(" · ");
}

/** Reports observed scanner readiness and completed passes. */
export function scannerReadinessLine(scanner: SecurityScannerState): string {
  if (!scanner.available) {
    return scanner.unavailable_reason?.trim() || "Unavailable";
  }
  if (scanner.running) return "Scanning now";

  const parts: string[] = [scanner.watching ? "Watching for changes" : "No baseline yet"];
  const full = relativeTimeLabel(scanner.last_full_at);
  if (full) {
    parts.push(`full pass ${full}`);
    if (scanner.pass_superseded) parts.push("engine changed since that pass");
    return parts.join(" · ");
  }
  const last = relativeTimeLabel(scanner.last_completed_at);
  parts.push(last ? `last scan ${last}` : "not scanned yet");
  return parts.join(" · ");
}

/** True when an available scanner's engine changed after a full pass it completed. */
export function anyPassSuperseded(overview: SecurityOverview | null): boolean {
  return (overview?.scanners ?? []).some(
    (scanner) => scanner.available && scanner.pass_superseded && Boolean(scanner.last_full_at),
  );
}

export function ledgerEntryKey(entry: FindingLedgerEntry): string {
  return `${entry.scanner_id}:${entry.finding.fingerprints.primary}`;
}
