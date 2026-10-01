import type { WorkflowRunObligation, WorkflowTopologyLeg } from "../api/types.ts";

/** Host-declared phase activity label. */
export function workflowPhaseLabel(hostLabel?: string): string {
  const declared = hostLabel?.trim();
  if (declared) return declared;
  return "—";
}

/** Returns a label for pending host obligations. */
export function workflowObligationSuffix(
  obligations: readonly WorkflowRunObligation[] | undefined,
): string {
  if (!obligations?.length) return "";
  for (const ob of obligations) {
    if (ob.status !== "pending") continue;
    if (ob.kind === "scan") {
      const detail = ob.detail ?? {};
      if (detail["warming"] === true) return " · warming source snapshot";
      const engines = detail["engines_pending"];
      if (Array.isArray(engines) && engines.length > 0) {
        return ` · running ${engines.filter((e) => typeof e === "string").join(", ")}`;
      }
      const total = detail["scans_total"];
      const terminal = detail["scans_terminal"];
      if (typeof total === "number" && typeof terminal === "number" && total > 0) {
        return ` · ${terminal}/${total} scans done`;
      }
      return " · waiting on scanners";
    }
    return ` · waiting on ${ob.kind}`;
  }
  return "";
}

/** Returns how many of a topology phase's workers have finished. */
export function workflowTopologySuffix(
  legs: readonly WorkflowTopologyLeg[] | undefined,
  phaseId: string,
): string {
  const phaseLegs = (legs ?? []).filter((leg) => leg.phase_id === phaseId);
  if (phaseLegs.length === 0) return "";
  const done = phaseLegs.filter((leg) => leg.status === "complete" || leg.status === "failed").length;
  return ` · ${done} of ${phaseLegs.length} workers done`;
}
