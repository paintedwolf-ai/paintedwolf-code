import { createMemo, createUniqueId } from "solid-js";
import type { ConsequenceBand, ConsequenceCode, DetectionMatch, ToolApprovalPayload } from "../../api/types.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";

/** Payload fields override the plan when both are present. */
export function effectiveConsequence(
  payload: ToolApprovalPayload | undefined,
): {
  consequence_band?: ConsequenceBand;
  consequence_code?: ConsequenceCode;
  detection?: DetectionMatch;
} {
  const presentation = payload?.plan.presentation;
  return {
    consequence_band: payload?.consequence_band ?? presentation?.consequence_band,
    consequence_code: payload?.consequence_code ?? presentation?.consequence_code,
    detection: presentation?.detection,
  };
}

export function isHighRiskBand(band?: ConsequenceBand | null): boolean {
  return band === "high_risk";
}

function highRiskConsequenceLine(
  code: ConsequenceCode | undefined,
): string {
  const copy = APPROVALS_COPY.card.highRisk.consequence;
  switch (code) {
    // Secret and detection impacts come from the host; rule citations are details.
    case "secret":
    case "detection":
      return "";
    case "write_root":
      return copy.write_root;
    case "local_socket":
      return copy.local_socket;
    case "direct_ip":
      return copy.direct_ip;
    default:
      return "";
  }
}

export function useHighRisk(
  payload: () => {
    consequence_band?: ConsequenceBand;
    consequence_code?: ConsequenceCode;
    detection?: DetectionMatch;
  } | undefined,
  hostImpact: () => string,
) {
  const highRisk = createMemo(() => isHighRiskBand(payload()?.consequence_band));
  const consequenceText = createMemo(() =>
    highRisk()
      ? highRiskConsequenceLine(payload()?.consequence_code)
      : "",
  );
  const impactText = createMemo(() => consequenceText() || hostImpact());
  const labelId = createUniqueId();
  const impactId = createUniqueId();
  const describedBy = createMemo(() => {
    if (!highRisk()) return undefined;
    const parts = [labelId];
    if (impactText()) parts.push(impactId);
    return parts.join(" ");
  });
  return { highRisk, consequenceText, impactText, labelId, impactId, describedBy };
}
