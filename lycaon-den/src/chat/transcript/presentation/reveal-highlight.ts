import {
  applyFindHighlights,
  clearFindHighlights,
  type FindMatch,
} from "../../../find/find-match.ts";

let painted: HTMLElement | null = null;
let releaseRevealState: (() => void) | null = null;

export function bindRevealHighlightCleanup(release: () => void): void {
  releaseRevealState?.();
  releaseRevealState = release;
}

export function paintRevealHighlight(
  root: HTMLElement,
  matches: FindMatch[],
  activeIndex = 0,
): HTMLElement | null {
  clearRevealHighlight();
  const active = applyFindHighlights(root, matches, activeIndex);
  if (active) painted = root;
  return active;
}

/** A detached highlighted row is inactive. */
export function revealHighlightActive(): boolean {
  if (!painted) return false;
  if (!painted.isConnected) {
    painted = null;
    return false;
  }
  return true;
}

/** Remove the reveal highlight. Reports whether there was one. */
export function clearRevealHighlight(): boolean {
  releaseRevealState?.();
  releaseRevealState = null;
  const root = painted;
  painted = null;
  if (!root) return false;
  clearFindHighlights(root);
  return true;
}

export function resetRevealHighlightForTests(): void {
  painted = null;
  releaseRevealState = null;
}
