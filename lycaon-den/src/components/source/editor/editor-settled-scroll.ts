import type { EditorState } from "@codemirror/state";
import { EditorView, type ScrollStrategy } from "@codemirror/view";

/** Scroll requests made after the first, while estimated heights settle. */
export const SETTLED_SCROLL_REPEATS = 8;

/**
 * Keeps a position in view after a layout change. Heights of long lines are
 * estimates until measured, so one scroll can land short; each measure checks
 * the target and scrolls again, up to a fixed count.
 */
export function keepPositionInView(
  view: EditorView,
  target: (state: EditorState) => number,
  y: ScrollStrategy = "nearest",
): void {
  let repeats = 0;
  const check = (): void => {
    view.requestMeasure<boolean>({
      read: (v) => {
        const pos = target(v.state);
        const rect = v.coordsAtPos(pos);
        if (!rect) return false;
        const port = v.scrollDOM.getBoundingClientRect();
        return rect.bottom > port.top && rect.top < port.bottom;
      },
      write: (visible, v) => {
        if (visible || repeats >= SETTLED_SCROLL_REPEATS) return;
        repeats += 1;
        v.dispatch({ effects: EditorView.scrollIntoView(target(v.state), { y }) });
        check();
      },
    });
  };
  check();
}
