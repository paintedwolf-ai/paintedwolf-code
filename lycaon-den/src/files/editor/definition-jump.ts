import type { SourceDefinitionCandidate } from "../../api/types.ts";

export type DefinitionJumpOutcome =
  | { kind: "noop" }
  | { kind: "jump"; candidate: SourceDefinitionCandidate }
  | {
      kind: "picker";
      symbol: string;
      candidates: readonly SourceDefinitionCandidate[];
      truncated: boolean;
    }
  | { kind: "miss"; symbol: string; truncated: boolean };

/** A single candidate is unambiguous only when the lookup is complete. */
export function definitionJumpOutcome(
  symbol: string,
  response: {
    candidates: readonly SourceDefinitionCandidate[];
    truncated: boolean;
  },
): DefinitionJumpOutcome {
  const sym = symbol.trim();
  if (!sym) return { kind: "noop" };
  const candidates = response.candidates;
  if (candidates.length === 0) {
    return { kind: "miss", symbol: sym, truncated: response.truncated };
  }
  if (candidates.length === 1 && !response.truncated) {
    return { kind: "jump", candidate: candidates[0]! };
  }
  return {
    kind: "picker",
    symbol: sym,
    candidates,
    truncated: response.truncated,
  };
}

export const DEFINITION_PICKER_TRUNCATION_FOOTER =
  "This lookup was incomplete. Other definitions may exist.";

export function definitionPickerHeader(symbol: string, count: number): string {
  return `${count} ${count === 1 ? "definition" : "definitions"} of ${symbol}`;
}

export function definitionPickerExplainer(truncated: boolean): string {
  return truncated
    ? "Choose from the definitions found so far."
    : "This name is declared in more than one place — pick the one you meant.";
}

/** Cached lookups finish before the waiting notice appears. */
export const DEFINITION_PENDING_NOTICE_DELAY_MS = 150;

export function definitionPendingCopy(symbol: string): string {
  return `Finding definition of ${symbol}…`;
}

export function definitionMissCopy(symbol: string, truncated: boolean): string {
  return truncated
    ? `No definition found for ${symbol} in the files searched; the lookup was incomplete`
    : `No definition found for ${symbol}`;
}
