import { queryFilterOccurrences } from "./search-query-model.ts";

/**
 * Whether the active DSL query is scoped for SARIF export. Mirrors the host's
 * PlanSARIFScoped polarity rule: a negated kind:scan scopes nothing.
 */
export function queryScanScoped(query: string): boolean {
  const q = query.trim();
  if (!q) return false;
  let scoped = false;
  for (const occurrence of queryFilterOccurrences(q)) {
    if (occurrence.negated) continue;
    const value = occurrence.value.toLowerCase();
    if (
      (occurrence.field === "kind" && value === "scan") ||
      (occurrence.field === "shape" && value === "artifact")
    ) {
      scoped = true;
    } else if (occurrence.field === "kind") {
      return false;
    }
  }
  return scoped;
}
