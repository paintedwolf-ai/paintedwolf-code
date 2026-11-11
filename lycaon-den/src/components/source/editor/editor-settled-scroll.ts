import type { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { setEditorScrollTop } from "./editor-scroll-position.ts";

type ScrollStrategy = "nearest" | "start" | "end" | "center";

/** Scroll corrections made after the first request, while estimated heights settle. */
export const SETTLED_SCROLL_REPEATS = 24;
/** Frames checked before giving up. */
const SETTLED_SCROLL_CHECKS = 64;
/** Consecutive frames the target must stay visible. */
const SETTLED_SCROLL_STABLE = 2;

/**
 * Keeps a position in view after a layout change. The editor estimates the
 * height of text it has not drawn and scrolls to a position in a long wrapped
 * line by that line's whole block, so each frame checks the position's own
 * coordinates and corrects the scroll offset. Moving the drawn window changes
 * the estimates, so a correction that overshoots is halved on the way back.
 */
export function keepPositionInView(
  view: EditorView,
  target: (state: EditorState) => number,
  y: ScrollStrategy = "nearest",
): void {
  let checks = 0;
  let repeats = 0;
  let stable = 0;
  let scale = 1;
  let lastSign = 0;
  const check = (): void => {
    if (!view.dom.isConnected) return;
    view.requestMeasure<number | null>({
      read: (v) => {
        const rect = v.coordsAtPos(target(v.state));
        if (!rect) return null;
        const port = v.scrollDOM.getBoundingClientRect();
        if (rect.bottom > port.top && rect.top < port.bottom) return 0;
        const anchor = port.top + port.height * (y === "center" ? 0.5 : y === "end" ? 0.8 : 0.25);
        return rect.top - anchor;
      },
      write: (offset, v) => {
        checks += 1;
        if (offset === 0) {
          stable += 1;
          if (stable >= SETTLED_SCROLL_STABLE) return;
        } else {
          stable = 0;
          if (repeats >= SETTLED_SCROLL_REPEATS) return;
          repeats += 1;
          if (offset === null) {
            // A measure's write phase may not dispatch.
            queueMicrotask(() => {
              if (v.dom.isConnected) v.dispatch({ effects: EditorView.scrollIntoView(target(v.state), { y }) });
            });
          } else {
            const sign = Math.sign(offset);
            if (lastSign !== 0 && sign !== lastSign) scale /= 2;
            lastSign = sign;
            setEditorScrollTop(v, v.scrollDOM.scrollTop + offset * scale);
          }
        }
        if (checks < SETTLED_SCROLL_CHECKS) requestAnimationFrame(check);
      },
    });
  };
  requestAnimationFrame(check);
}
