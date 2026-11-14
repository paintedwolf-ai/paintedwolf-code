import type { EditorState, StateEffect } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { setEditorScrollTop } from "./editor-scroll-position.ts";

type ScrollStrategy = "nearest" | "start" | "end" | "center";

/** Scroll corrections made after the first request, while estimated heights settle. */
export const SETTLED_SCROLL_REPEATS = 40;
/** Frames checked before giving up. */
const SETTLED_SCROLL_CHECKS = 96;
/**
 * Frames checked while the editor is still measuring: in a long line each
 * drawn window changes the size estimates, moving the target as it settles.
 */
const SETTLED_SCROLL_MEASURING_CHECKS = 900;
/** Consecutive frames the target must stay visible. */
const SETTLED_SCROLL_STABLE = 2;

// The settle loop each view is running; starting another ends it.
const activeSettles = new WeakMap<EditorView, object>();
// The reader's place a running restore holds; a change made meanwhile keeps it.
const heldAnchors = new WeakMap<EditorView, ScrollAnchor>();
/** Reader input that ends automatic corrections. */
const READER_INPUT = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

// Counts settle loops started per view, so work measured before one began can tell.
const settleStarts = new WeakMap<EditorView, number>();

/** Whether a settle loop is correcting this view's scroll offset. */
export function isSettlingScroll(view: EditorView): boolean {
  return activeSettles.has(view);
}

/** How many settle loops this view has started; changes whenever one begins. */
export function settleGeneration(view: EditorView): number {
  return settleStarts.get(view) ?? 0;
}

/**
 * Corrects the scroll offset each frame by `read`'s distance until it reads 0.
 * The editor estimates the height of text it has not drawn, and moving the
 * drawn window changes the estimates, so a correction that overshoots is halved
 * on the way back and grows again while corrections keep the same direction.
 * A null reading means the target has no coordinates yet; `fallback` asks the
 * editor to scroll to it.
 */
/** Distance to correct; `exact` is false while the target is not drawn and its place is estimated. */
type SettleOffset = { top: number; left: number; exact: boolean };

type PositionRect = { top: number; left: number | null; exact: boolean };

/**
 * Where a position is drawn, or, while it is not, where its line's height
 * estimate puts it. The editor reports text it has not drawn at the top of
 * its line block, which for a long wrapped line can be far from the text, so
 * that report is replaced with a place interpolated through the block. The
 * horizontal place of undrawn text is unknown.
 */
export function positionRect(view: EditorView, pos: number): PositionRect | null {
  const { from, to } = view.viewport;
  if (pos >= from && pos <= to) {
    const rect = view.coordsAtPos(pos);
    return rect ? { top: rect.top, left: rect.left, exact: true } : null;
  }
  const block = view.lineBlockAt(pos);
  const share = block.length > 0 ? (pos - block.from) / block.length : 0;
  return { top: view.documentTop + block.top + block.height * share, left: null, exact: false };
}

function settleScroll(
  view: EditorView,
  read: (view: EditorView) => SettleOffset | null,
  fallback: (view: EditorView) => StateEffect<unknown>,
  onRelease?: () => void,
): void {
  const token = {};
  activeSettles.set(view, token);
  settleStarts.set(view, settleGeneration(view) + 1);
  const release = (): void => {
    for (const type of READER_INPUT) view.scrollDOM.removeEventListener(type, release, true);
    if (activeSettles.get(view) !== token) return;
    activeSettles.delete(view);
    onRelease?.();
  };
  for (const type of READER_INPUT) view.scrollDOM.addEventListener(type, release, { capture: true, passive: true });
  let checks = 0;
  let repeats = 0;
  let stable = 0;
  let scale = 1;
  let lastSign = 0;
  let lastSize = `${view.scrollDOM.scrollWidth}x${view.scrollDOM.scrollHeight}`;
  const check = (): void => {
    if (activeSettles.get(view) !== token) return;
    if (!view.dom.isConnected) return release();
    view.requestMeasure<SettleOffset | null>({
      read,
      write: (offset, v) => {
        if (activeSettles.get(v) !== token) return;
        checks += 1;
        // Wrapped lines revise their height as they are drawn; unwrapped ones, their width.
        const size = `${v.scrollDOM.scrollWidth}x${v.scrollDOM.scrollHeight}`;
        const measuring = size !== lastSize;
        lastSize = size;
        if (offset !== null && offset.exact && offset.top === 0 && offset.left === 0) {
          stable += 1;
          if (stable >= SETTLED_SCROLL_STABLE) return release();
        } else {
          // Corrections spent chasing estimates the editor is still revising do not count.
          if (repeats >= SETTLED_SCROLL_REPEATS && !measuring) return release();
          if (!measuring) repeats += 1;
          if (offset === null) {
            stable = 0;
            // A measure's write phase may not dispatch.
            queueMicrotask(() => {
              if (v.dom.isConnected) v.dispatch({ effects: fallback(v) });
            });
          } else {
            const before = { top: v.scrollDOM.scrollTop, left: v.scrollDOM.scrollLeft };
            if (offset.top !== 0) {
              const sign = Math.sign(offset.top);
              if (lastSign !== 0) scale = sign !== lastSign ? scale / 2 : Math.min(1, scale * 2);
              lastSign = sign;
              setEditorScrollTop(v, before.top + offset.top * scale);
            }
            if (offset.left !== 0) v.scrollDOM.scrollLeft = before.left + offset.left;
            // At a scroll bound the offset cannot shrink further; that is as close as it gets.
            if (offset.exact && v.scrollDOM.scrollTop === before.top && v.scrollDOM.scrollLeft === before.left) {
              stable += 1;
              if (stable >= SETTLED_SCROLL_STABLE) return release();
            } else {
              stable = 0;
            }
          }
        }
        if (checks < SETTLED_SCROLL_CHECKS || (measuring && checks < SETTLED_SCROLL_MEASURING_CHECKS)) requestAnimationFrame(check);
        else release();
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
      const rect = positionRect(v, target(v.state));
      if (!rect) return null;
      const port = v.scrollDOM.getBoundingClientRect();
      if (rect.exact && rect.top + v.defaultLineHeight > port.top && rect.top < port.bottom) return { top: 0, left: 0, exact: true };
      const anchor = port.top + port.height * (y === "center" ? 0.5 : y === "end" ? 0.8 : 0.25);
      return { top: rect.top - anchor, left: 0, exact: rect.exact };
    },
    (v) => EditorView.scrollIntoView(target(v.state), { y }),
  );
}

/**
 * The point where the text at the viewport's top-left edge is drawn. The first
 * line can start below the top edge (the content's padding at the start of the
 * document), and content scrolled sideways starts left of the viewport.
 */
export function topEdgePoint(view: EditorView): { x: number; y: number } {
  const port = view.scrollDOM.getBoundingClientRect();
  const content = view.contentDOM.getBoundingClientRect();
  const block = view.lineBlockAtHeight(port.top - view.documentTop);
  return { x: Math.max(content.left, port.left) + 1, y: Math.max(port.top, view.documentTop + block.top) + 1 };
}

/** A position and its distance from the viewport's top and left edges. */
export type ScrollAnchor = { pos: number; offset: number; left: number };

/**
 * The position a reader is looking at and its distance from the viewport top:
 * the selection head when any of it is on screen, otherwise the text at the
 * top edge. A view at the start of the document with no head on screen has
 * no anchor and stays there.
 */
export function captureScrollAnchor(view: EditorView): ScrollAnchor | null {
  if (!view.dom.isConnected) return null;
  const port = view.scrollDOM.getBoundingClientRect();
  if (port.height <= 0) return null;
  const head = view.state.selection.main.head;
  const caret = view.coordsAtPos(head);
  if (caret && caret.bottom > port.top && caret.top < port.bottom && caret.right >= port.left && caret.left <= port.right) {
    return { pos: head, offset: caret.top - port.top, left: caret.left - port.left };
  }
  // Only the document start is "at the top"; a long unwrapped line may be scrolled sideways.
  if (view.scrollDOM.scrollTop <= 0 && view.scrollDOM.scrollLeft <= 0) return null;
  const pos = view.posAtCoords(topEdgePoint(view), false);
  const at = view.coordsAtPos(pos);
  return at ? { pos, offset: at.top - port.top, left: at.left - port.left } : null;
}

/**
 * Holds the anchored position at its captured distance from the viewport's
 * top and left edges. Sideways matters in a long unwrapped line, where the
 * editor estimates the width of text it has not drawn and a scroll request
 * can land far from the position.
 */
export function restoreScrollAnchor(view: EditorView, anchor: ScrollAnchor): void {
  const pos = (v: EditorView) => Math.min(anchor.pos, v.state.doc.length);
  heldAnchors.set(view, anchor);
  settleScroll(
    view,
    (v) => {
      const rect = positionRect(v, pos(v));
      if (!rect) return null;
      const port = v.scrollDOM.getBoundingClientRect();
      const top = rect.top - port.top - anchor.offset;
      const left = rect.left === null ? 0 : rect.left - port.left - anchor.left;
      return { top: Math.abs(top) < 1 ? 0 : top, left: Math.abs(left) < 1 ? 0 : left, exact: rect.exact };
    },
    (v) => EditorView.scrollIntoView(pos(v), { y: "start", yMargin: Math.max(0, anchor.offset) }),
    () => { if (heldAnchors.get(view) === anchor) heldAnchors.delete(view); },
  );
}

/** Applies a reflowing change without moving the reader's place in the document. */
export function preserveScrollAnchor(view: EditorView, apply: () => void): void {
  // One choice can arrive as several changes (wrapping, then Pretty). Until the
  // first settles, the geometry is half-updated, so later ones keep its anchor.
  const anchor = (isSettlingScroll(view) ? heldAnchors.get(view) : undefined) ?? captureScrollAnchor(view);
  apply();
  if (!anchor) return;
  // The editor's own scroll anchoring would hold the top line block, which in
  // a long wrapped line is its start; a scroll request takes precedence.
  view.dispatch({ effects: EditorView.scrollIntoView(anchor.pos, { y: "start", yMargin: Math.max(0, anchor.offset) }) });
  restoreScrollAnchor(view, anchor);
}
