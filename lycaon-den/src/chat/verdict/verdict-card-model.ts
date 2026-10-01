/** Only accepted submissions carry the host verdict projection. */

import type { VerdictOutcome } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

/** Chicklet name slot for a verdict row. */
export const VERDICT_CHICKLET_NAME = "verdict";

/** Rows carrying a recorded verdict projection. */
export function isVerdictToolPart(part: ToolPartView): boolean {
  return !!verdictOutcomeFromPart(part);
}

/** Host projection for a recorded verdict; null while in flight or rejected. */
export function verdictOutcomeFromPart(part: ToolPartView): VerdictOutcome | null {
  const verdict = part.verdict;
  if (!verdict) return null;
  return verdict.verdict.trim() ? verdict : null;
}

/** One-line lifecycle summary: terminal advances, non-terminal re-loops. */
export function verdictStatusLine(outcome: VerdictOutcome): string {
  if (outcome.terminal) {
    return outcome.evidence_key
      ? `Terminal — evidence_passed:${outcome.evidence_key} satisfied`
      : "Terminal — the host is advancing the phase";
  }
  const attempt = outcome.attempt ?? 0;
  const cap = outcome.iteration_cap ?? 0;
  if (attempt > 0 && cap > 0) {
    return `Non-terminal — attempt ${attempt} of ${cap}`;
  }
  return "Non-terminal — the review re-loops";
}
