import type { LycaonClient } from "../../api/client.ts";
import type { SourceRevisionComparison } from "../../api/types.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import type { GitDiffsAddress } from "./diffs-address.ts";

/** Longest revision text the host accepts. */
const MAX_SPEC_LENGTH = 256;
const OBJECT_ID = /^[0-9a-f]{7,64}$/i;
/** Ancestry steps after a name: `~`, `~3`, `^`, `^2`. */
const ANCESTRY = /(?:[~^]\d*)+$/;

/** A branch or tag name by git check-ref-format's rules; "/" separates components. */
function refNameShape(name: string): boolean {
  if (!name || name === "@" || name.startsWith("-") || name.startsWith("/") || name.endsWith("/") || name.endsWith(".")) return false;
  if (name.includes("..") || name.includes("//") || name.includes("@{") || /[~^:?*[\\\s]/.test(name)) return false;
  for (let at = 0; at < name.length; at++) {
    const code = name.charCodeAt(at);
    if (code < 0x20 || code === 0x7f) return false;
  }
  return name.split("/").every((part) => !part.startsWith(".") && !part.endsWith(".lock"));
}

/** One side of a range, or a whole single revision: a name or id with optional ancestry steps. */
function revisionShape(text: string): boolean {
  const base = text.replace(ANCESTRY, "");
  return OBJECT_ID.test(base) || refNameShape(base);
}

/**
 * A cheap gate before asking the host: one token that reads as a commit id,
 * a ref name, or an A..B / A...B range. Text with inner whitespace never passes.
 */
export function looksLikeRevisionSpec(text: string): boolean {
  const spec = text.trim();
  if (!spec || spec.length > MAX_SPEC_LENGTH || /\s/.test(spec)) return false;
  const separator = spec.includes("...") ? "..." : spec.includes("..") ? ".." : null;
  if (!separator) return revisionShape(spec);
  const at = spec.indexOf(separator);
  const sides = [spec.slice(0, at), spec.slice(at + separator.length)];
  if (sides.every((side) => side === "")) return false;
  return sides.every((side) => side === "" || revisionShape(side));
}

/** Comparisons the revision names, one per Git root where it resolves, primary root first. */
export async function resolveRevisionComparisons(
  client: LycaonClient,
  projectId: string,
  spec: string,
  opts?: { sessionId?: string; signal?: AbortSignal },
): Promise<SourceRevisionComparison[]> {
  if (!looksLikeRevisionSpec(spec)) return [];
  const response = await client.resolveProjectSourceRevisions(projectId, spec.trim(), {
    sessionId: opts?.sessionId,
    signal: opts?.signal,
  });
  return response.comparisons;
}

export function revisionDiffsAddress(comparison: SourceRevisionComparison): GitDiffsAddress {
  return {
    kind: "git",
    rootId: comparison.root_id,
    spec: comparison.spec,
    beforeCommit: comparison.before_commit,
    afterCommit: comparison.after_commit,
    label: comparison.label,
  };
}

/** Opens the diffs page on the two commits a resolved revision names. */
export function openRevisionDiffs(projectId: string, comparison: SourceRevisionComparison): void {
  openFilesSurface({ kind: "diffs", projectId, address: revisionDiffsAddress(comparison) });
}
