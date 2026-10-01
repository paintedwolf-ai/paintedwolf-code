/**
 * Contribution condition evaluation over the closed fact vocabulary.
 *
 * Den evaluates every plane for presentation only; the host re-evaluates
 * host facts from machine state on invocation. Semantics are conformance-
 * tested against the Go evaluator with shared fixtures.
 */
import type { ContributionCondition } from "../api/types.ts";
import {
  CONTRIBUTION_FACTS,
  type ContributionFactId,
} from "./contribution-facts.generated.ts";

export type FactLookup = (fact: ContributionFactId, operand: string) => boolean;

export function evaluateCondition(
  condition: ContributionCondition | null | undefined,
  lookup: FactLookup,
): boolean {
  if (!condition) return true;
  if (condition.all && condition.all.length > 0) {
    return condition.all.every((child) => evaluateCondition(child, lookup));
  }
  if (condition.any && condition.any.length > 0) {
    return condition.any.some((child) => evaluateCondition(child, lookup));
  }
  if (condition.not) return !evaluateCondition(condition.not, lookup);
  const fact = condition.fact ?? "";
  if (!(fact in CONTRIBUTION_FACTS)) return false;
  return lookup(fact as ContributionFactId, condition.is ?? "");
}
