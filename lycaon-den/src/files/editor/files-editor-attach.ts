import { beginScrollMeasureQuiet } from "../../platform/scrolling/themed-scrollbars.ts";
import { afterPaint } from "../../ui/surface-reveal.ts";

export function holdFilesEditorAttach(host: HTMLElement): () => void {
  const release = beginScrollMeasureQuiet(host);
  let ended = false;
  return () => {
    if (ended) return;
    ended = true;
    release();
  };
}

/** The measurement hold lasts through the next paint. */
export function scheduleEditorChrome(
  apply: () => void,
  host: HTMLElement,
): () => void {
  const endAttach = holdFilesEditorAttach(host);
  apply();
  const cancelPaint = afterPaint(endAttach);
  return () => {
    cancelPaint();
    endAttach();
  };
}
