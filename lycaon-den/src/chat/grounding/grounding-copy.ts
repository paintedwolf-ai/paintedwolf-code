import type { CitationVerdict } from "../../api/types.ts";

export const GROUNDING_STATUS_GROUNDED = "Grounded";
export const GROUNDING_STATUS_PARTIAL = "Partial";

export const GROUNDING_SOURCE_PREFIX = "Source:";
export const GROUNDING_SOURCE_AGENT = "Agent citations";
export const GROUNDING_SOURCE_HOST = "Host-added (observed)";
export function groundingHostAddedNote(attemptCount: number): string {
  if (attemptCount > 0) {
    return `Answer kept as-is after ${attemptCount} attempt${attemptCount === 1 ? "" : "s"}; supporting files and pages it looked at were attached automatically.`;
  }
  return "Answer kept as-is; supporting files and pages it looked at were attached automatically.";
}

export const GROUNDING_PROVENANCE_MARK = "↳";

export const GROUNDING_BADGE_ARIA_GROUNDED =
  "Citations grounded in captured evidence — open evidence in worker panel";
export const GROUNDING_BADGE_ARIA_PARTIAL =
  "Some citations couldn't be traced — open evidence in worker panel";
export const GROUNDING_BADGE_TITLE_GROUNDED =
  "All citations grounded in captured tool output";
export const GROUNDING_BADGE_TITLE_PARTIAL =
  "Some citations not found in captured output";

export const GROUNDING_HEADLINE_NO_WORK = "No citations to trace";
export const GROUNDING_HEADLINE_GROUNDED =
  "All citations grounded in captured evidence";
export const GROUNDING_HEADLINE_PARTIAL =
  "Some citations couldn't be traced to evidence";

export function groundingHeadlinePartialWithCheck(checkLabel: string): string {
  return `Some citations couldn't be traced — ${checkLabel}`;
}

export const GROUNDING_PANEL_SCOPE =
  "Grounding traces references to captured evidence. Exact excerpt matches and references that need review are labeled below.";

export const GROUNDING_CITATIONS_SECTION = "Cited evidence";

export const GROUNDING_ADVISORY_NOTE_SUFFIX = "surfaced for review";

export const GROUNDING_VERDICT_MATCHED = "Traced — exact";
export const GROUNDING_VERDICT_TRACED = "Traced — confirm";
export const GROUNDING_VERDICT_UNVERIFIABLE = "Not traced";

export function citationVerdictLabel(
  verdict?: CitationVerdict,
): string | undefined {
  switch (verdict) {
    case "matched":
      return GROUNDING_VERDICT_MATCHED;
    case "traced":
      return GROUNDING_VERDICT_TRACED;
    case "unverifiable":
      return GROUNDING_VERDICT_UNVERIFIABLE;
    default:
      return undefined;
  }
}
