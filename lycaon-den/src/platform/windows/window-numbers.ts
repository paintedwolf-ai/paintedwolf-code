import type { ItemWindowView } from "./item-windows.ts";
import type { WindowSubject } from "./window-subject.ts";

/** The native registry reserves Window 1 for the main window; peers count up from 2. */
export const MAIN_WINDOW_NUMBER = 1;
export const MAIN_WINDOW_LABEL = "main";

type NumberedWindow = Pick<ItemWindowView, "label" | "viewNumber">;

/** This window's native number: a peer's comes from its launch subject. */
export function currentWindowNumber(subject: WindowSubject | null): number | null {
  if (!subject) return MAIN_WINDOW_NUMBER;
  const number = Number(subject.viewId);
  return Number.isSafeInteger(number) ? number : null;
}

/** The label after `current` in window-number order, wrapping to the main window. */
export function nextWindowLabel(
  peers: readonly NumberedWindow[],
  current: number | null,
): string | null {
  if (peers.length === 0) return null;
  const ordered = [
    { label: MAIN_WINDOW_LABEL, viewNumber: MAIN_WINDOW_NUMBER },
    ...peers.filter((peer) => peer.viewNumber !== MAIN_WINDOW_NUMBER),
  ].sort((a, b) => a.viewNumber - b.viewNumber);
  const next = ordered.find((window) => current !== null && window.viewNumber > current);
  return (next ?? ordered[0]!).label;
}

/** The native label of window `number`, if that window is open. */
export function windowLabelForNumber(
  peers: readonly NumberedWindow[],
  number: number,
): string | null {
  if (number === MAIN_WINDOW_NUMBER) return MAIN_WINDOW_LABEL;
  return peers.find((peer) => peer.viewNumber === number)?.label ?? null;
}
