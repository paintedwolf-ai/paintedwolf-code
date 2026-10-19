import { webkitAtspiWorkaroundActive } from "./webkit-atspi-workaround.ts";

/** A retained editor keeps its logical selection; its inaccessible DOM range must leave WebKit. */
export function releaseInaccessibleWebKitSelection(surface: HTMLElement): void {
  if (!webkitAtspiWorkaroundActive()) return;
  const selection = surface.ownerDocument.getSelection();
  if (!selection || selection.rangeCount === 0) return;
  if (surface.contains(selection.anchorNode) || surface.contains(selection.focusNode)) {
    selection.removeAllRanges();
  }
}
