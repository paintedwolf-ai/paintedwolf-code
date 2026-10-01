import type { SourceGitChange } from "../../api/types.ts";

/** Rail-style label for a movement: `git checkout`. */
export function gitChangeAction(change: SourceGitChange): string {
  return `git ${change.kind.replaceAll("_", " ")}`;
}

/** Title-style label for a movement: `Git checkout`. */
export function gitChangeTitle(change: SourceGitChange): string {
  return `Git ${change.kind.replaceAll("_", " ")}`;
}

/** Where the movement went: `main → work`, `on main`, or empty. */
export function gitChangeRefs(change: SourceGitChange): string {
  const fromRef = change.from_ref?.trim() ?? "";
  const toRef = change.to_ref?.trim() ?? "";
  if (fromRef && toRef && fromRef !== toRef) return `${fromRef} → ${toRef}`;
  if (toRef) return `on ${toRef}`;
  return "";
}

/** One line for a recorded movement: kind, refs, and git's own subject. */
export function gitChangeLine(change: SourceGitChange): string {
  const parts = [change.kind.replaceAll("_", " ")];
  const refs = gitChangeRefs(change);
  if (refs) parts.push(refs);
  const detail = change.detail?.trim() ?? "";
  if (detail) parts.push(`— ${detail}`);
  return parts.join(" ");
}
