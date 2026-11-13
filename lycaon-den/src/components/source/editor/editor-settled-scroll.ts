import type { EditorState, StateEffect } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { setEditorScrollTop } from "./editor-scroll-position.ts";

type ScrollStrategy = "nearest" | "start" | "end" | "center";

/** Scroll corrections made after the first request, while estimated heights settle. */
export const SETTLED_SCROLL_REPEATS = 40;
/** Frames checked before giving up. */
const SETTLED_SCROLL_CHECKS = 96;
/** Consecutive frames the target must stay visible. */
const SETTLED_SCROLL_STABLE = 2;

/**
 * Corrects the scroll offset each frame by `read`'s distance until it reads 0.
 * The editor estimates the height of text it has not drawn, and moving the
 * drawn window changes the estimates, so a correction that overshoots is halved
 * on the way back and grows again while corrections keep the same direction.
 * A null reading means the target has no coordinates yet; `fallback` asks the
 * editor to scroll to it.
 */
function settleScroll(
  view: EditorView,
  read: (view: EditorView) => number | null,
  fallback: (view: EditorView) => StateEffect<unknown>,
): void {
  let checks = 0;
  let repeats = 0;
  let stable = 0;
  let scale = 1;
  let lastSign = 0;
  const check = (): void => {
    if (!view.dom.isConnected) return;
    view.requestMeasure<number | null>({
      read,
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
              if (v.dom.isConnected) v.dispatch({ effects: fallback(v) });
            });
          } else {
            const sign = Math.sign(offset);
            if (lastSign !== 0) scale = sign !== lastSign ? scale / 2 : Math.min(1, scale * 2);
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

/**
 * Keeps a position in view after a layout change. The editor scrolls to a
 * position in a long wrapped line by that line's whole block, so each frame
 * checks the position's own coordinates.
 */
export function keepPositionInView(
  view: EditorView,
  target: (state: EditorState) => number,
  y: ScrollStrategy = "nearest",
): void {
  settleScroll(
    view,
    (v) => {
      const rect = v.coordsAtPos(target(v.state));
      if (!rect) return null;
      const port = v.scrollDOM.getBoundingClientRect();
      if (rect.bottom > port.top && rect.top < port.bottom) return 0;
      const anchor = port.top + port.height * (y === "center" ? 0.5 : y === "end" ? 0.8 : 0.25);
      return rect.top - anchor;
    },
    (v) => EditorView.scrollIntoView(target(v.state), { y }),
  );
}

export type ScrollAnchor = { pos: number; offset: number };

/**
 * The position a reader is looking at and its distance from the viewport top:
 * the selection head when it is on screen, otherwise the text at the top edge.
 * A view scrolled to the top has no anchor and stays there.
 */
export function captureScrollAnchor(view: EditorView): ScrollAnchor | null {
  if (!view.dom.isConnected || view.scrollDOM.scrollTop <= 0) return null;
  const port = view.scrollDOM.getBoundingClientRect();
  if (port.height <= 0) return null;
  const head = view.state.selection.main.head;
  const caret = view.coordsAtPos(head);
  if (caret && caret.top >= port.top && caret.bottom <= port.bottom) {
    return { pos: head, offset: caret.top - port.top };
  }
  const content = view.contentDOM.getBoundingClientRect();
  const pos = view.posAtCoords({ x: content.left + 1, y: port.top + 1 }, false);
  const at = view.coordsAtPos(pos);
  return at ? { pos, offset: at.top - port.top } : null;
}

/** Holds the anchored position at its captured distance from the viewport top. */
export function restoreScrollAnchor(view: EditorView, anchor: ScrollAnchor): void {
  const pos = (v: EditorView) => Math.min(anchor.pos, v.state.doc.length);
  settleScroll(
    view,
    (v) => {
      const rect = v.coordsAtPos(pos(v));
      if (!rect) return null;
      const offset = rect.top - v.scrollDOM.getBoundingClientRect().top - anchor.offset;
      return Math.abs(offset) < 1 ? 0 : offset;
    },
    (v) => EditorView.scrollIntoView(pos(v), { y: "start", yMargin: Math.max(0, anchor.offset) }),
  );
}

/** Applies a reflowing change without moving the reader's place in the document. */
export function preserveScrollAnchor(view: EditorView, apply: () => void): void {
  const anchor = captureScrollAnchor(view);
  apply();
  if (anchor) restoreScrollAnchor(view, anchor);
}
