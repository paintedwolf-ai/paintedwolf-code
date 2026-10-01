import type { CitationGrounding, CitationGroundingCheck, CitationVerdict } from "../../api/types.ts";
import {
  citationVerdictLabel,
  GROUNDING_HEADLINE_GROUNDED,
  GROUNDING_HEADLINE_NO_WORK,
  GROUNDING_HEADLINE_PARTIAL,
  GROUNDING_SOURCE_AGENT,
  GROUNDING_SOURCE_HOST,
  groundingHeadlinePartialWithCheck,
} from "./grounding-copy.ts";
import {
  mapEvidenceRecord,
  type EvidenceRecordView,
} from "./evidence-shape-model.ts";

export type CitationGroundingOutcome = "traced" | "partial";

export type CitationGroundingCheckView = {
  id: string;
  label: string;
  status: CitationGroundingCheck["status"];
  kind?: "citation" | "lifecycle";
  vacuous?: boolean;
  summary?: string;
  matched: string[];
  failed: string[];
};

export type CitationGroundingView = {
  outcome: CitationGroundingOutcome;
  headline: string;
  hostAssembled: boolean;
  sourceLabel: string;
  hintCode?: string;
  retryCount?: number;
  observedPathCount?: number;
  observedPathsSample: string[];
  observedURLCount?: number;
  observedURLsSample: string[];
  findings: CitationGroundingFindingView[];
  citedURLs: string[];
  citedEvidence: CitationGroundingCitedEvidenceView[];
  proseLeakCount?: number;
  proseLeaksSample: string[];
  proseAdvisoryCount?: number;
  proseAdvisoriesSample: string[];
  checks: CitationGroundingCheckView[];
  hadTraceableWork: boolean;
  evidenceRecords: EvidenceRecordView[];
};

export type CitationGroundingCitedEvidenceView = {
  handle?: string;
  path?: string;
  line?: number;
  excerpt?: string;
  verdict?: CitationVerdict;
  verdictLabel?: string;
  openable?: boolean;
};

export type CitationGroundingFindingView = {
  handle?: string;
  path?: string;
  line?: number;
  excerpt?: string;
  verdict?: CitationVerdict;
  note?: string;
  verdictLabel?: string;
  openable?: boolean;
};

function hasTypedCitationFieldsOnWire(
  grounding: CitationGrounding,
): boolean {
  return (
    (grounding.findings?.length ?? 0) > 0 ||
    (grounding.cited_urls?.length ?? 0) > 0 ||
    (grounding.cited_evidence?.length ?? 0) > 0
  );
}

export function citationGroundingCheckHadTraceableWork(
  check: CitationGroundingCheck,
): boolean {
  // Lifecycle checks are outside citation audits.
  if (check.kind !== "citation") return false;
  if (check.status !== "passed") return true;
  if ((check.matched?.length ?? 0) > 0) return true;
  if ((check.failed?.length ?? 0) > 0) return true;
  return !check.vacuous;
}

export function citationGroundingHadTraceableWork(
  grounding?: CitationGrounding | null,
): boolean {
  if (!grounding) return false;
  if (grounding.hint_code?.trim()) return true;
  if ((grounding.prose_leak_count ?? 0) > 0) return true;
  if ((grounding.prose_advisory_count ?? 0) > 0) return true;
  if (hasTypedCitationFieldsOnWire(grounding)) return true;
  const checks = grounding.checks ?? [];
  if (checks.length === 0) return false;
  return checks.some(citationGroundingCheckHadTraceableWork);
}

export function citationGroundingPresent(
  grounding?: CitationGrounding | null,
): boolean {
  if (!grounding) return false;
  return (
    grounding.traced ||
    Boolean(grounding.host_assembled) ||
    (grounding.checks?.length ?? 0) > 0 ||
    Boolean(grounding.hint_code?.trim()) ||
    hasTypedCitationFieldsOnWire(grounding)
  );
}

/** Stable wire equality for SSE merge and transcript fingerprinting. */
export function citationGroundingWireEqual(
  left?: CitationGrounding | null,
  right?: CitationGrounding | null,
): boolean {
  if (left === right) return true;
  if (!left || !right) return !left && !right;
  return citationGroundingWireFingerprint(left) === citationGroundingWireFingerprint(right);
}

export function citationGroundingWireFingerprint(
  grounding?: CitationGrounding | null,
): string {
  return grounding ? JSON.stringify(grounding) : "";
}

function mapCheck(check: CitationGroundingCheck): CitationGroundingCheckView {
  return {
    id: check.id,
    label: check.label,
    status: check.status,
    kind: check.kind,
    vacuous: check.vacuous,
    summary: check.summary?.trim() || undefined,
    matched: check.matched ?? [],
    failed: check.failed ?? [],
  };
}

export function buildCitationGroundingView(
  grounding: CitationGrounding,
): CitationGroundingView {
  const checks = (grounding.checks ?? []).map(mapCheck);
  const hadTraceableWork = citationGroundingHadTraceableWork(grounding);
  const hostAssembled = Boolean(grounding.host_assembled);
  const failedCheck = checks.find((c) => c.status === "failed");
  const headline = !hadTraceableWork
    ? GROUNDING_HEADLINE_NO_WORK
    : grounding.traced || hostAssembled
      ? GROUNDING_HEADLINE_GROUNDED
      : failedCheck?.label
        ? groundingHeadlinePartialWithCheck(failedCheck.label)
        : GROUNDING_HEADLINE_PARTIAL;
  return {
    outcome: grounding.traced ? "traced" : "partial",
    headline,
    hostAssembled,
    sourceLabel: hostAssembled ? GROUNDING_SOURCE_HOST : GROUNDING_SOURCE_AGENT,
    hintCode: grounding.hint_code?.trim() || undefined,
    retryCount: grounding.retry_count,
    observedPathCount: grounding.observed_path_count,
    observedPathsSample: grounding.observed_paths_sample ?? [],
    observedURLCount: grounding.observed_url_count,
    observedURLsSample: grounding.observed_urls_sample ?? [],
    findings: (grounding.findings ?? []).map((f) => ({
      handle: f.handle?.trim() || undefined,
      path: f.path?.trim() || undefined,
      line: f.line,
      excerpt: f.excerpt?.trim() || undefined,
      verdict: f.verdict,
      verdictLabel: citationVerdictLabel(f.verdict),
      note: f.note?.trim() || undefined,
      openable: f.openable,
    })),
    citedURLs: grounding.cited_urls ?? [],
    citedEvidence: (grounding.cited_evidence ?? []).map((item) => ({
      handle: item.handle?.trim() || undefined,
      path: item.path?.trim() || undefined,
      line: item.line,
      excerpt: item.excerpt?.trim() || undefined,
      verdict: item.verdict,
      verdictLabel: citationVerdictLabel(item.verdict),
      openable: item.openable,
    })),
    proseLeakCount: grounding.prose_leak_count,
    proseLeaksSample: grounding.prose_leaks_sample ?? [],
    proseAdvisoryCount: grounding.prose_advisory_count,
    proseAdvisoriesSample: grounding.prose_advisories_sample ?? [],
    checks,
    hadTraceableWork,
    evidenceRecords: (grounding.evidence_records ?? []).map(mapEvidenceRecord),
  };
}
