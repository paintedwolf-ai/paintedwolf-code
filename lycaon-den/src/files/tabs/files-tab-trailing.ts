/**
 * Tab / tree trailing adornment precedence: live agent state > dirty > scope change.
 * Exactly one indicator renders; suppressed states stay in aria/title copy.
 */

import {
  scopeChangeMarkSpec,
  type FileChange,
  type ScopeChangeKind,
} from "../components/files-scope-change-mark.ts";

export type TrailingTabState = "presence" | "dirty" | ScopeChangeKind;

export function trailingTabState(args: {
  presence: boolean;
  dirty: boolean;
  changeKind: ScopeChangeKind | null;
}): TrailingTabState | null {
  if (args.presence) return "presence";
  if (args.dirty) return "dirty";
  return args.changeKind;
}

/** Names every active trailing fact for title / aria-label (including suppressed). */
export function trailingTabStateLabels(args: {
  agentLabels: readonly string[];
  dirty: boolean;
  change: FileChange | null;
}): string[] {
  const out: string[] = [...args.agentLabels];
  if (args.dirty) out.push("Unsaved changes");
  if (args.change) out.push(scopeChangeMarkSpec(args.change).label);
  return out;
}
