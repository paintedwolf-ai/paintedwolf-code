/** Resize value that hides a pane instead of sizing it. */
export const PANE_HIDDEN_PX = 0;

/** A pane dragged below half its floor hides; dragging back above restores it. */
export function dragRequestsHide(requestedPx: number, minPx: number): boolean {
  return Number.isFinite(requestedPx) && requestedPx < minPx / 2;
}
