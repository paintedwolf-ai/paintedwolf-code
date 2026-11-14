import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";
import { setEditorScrollTop } from "./editor-scroll-position.ts";
import { isSettlingScroll, positionRect, settleGeneration, topEdgePoint } from "./editor-settled-scroll.ts";

type Anchor = { pos: number; offset: number; left: number };
type Drift = { top: number; left: number };

/**
 * The text at the top-left edge and its distance from both edges, while the
 * line there is longer than the viewport: taller when it wraps, wider when it
 * does not. The editor anchors scrolling to whole lines and estimates the size
 * of text it has not drawn, so as it draws more of such a line, the text in
 * view moves.
 */
export function readReadingAnchor(view: EditorView): Anchor | null {
  const scroller = view.scrollDOM;
  if (scroller.clientHeight <= 0) return null;
  const block = view.lineBlockAtHeight(scroller.scrollTop);
  const tall = block.height > scroller.clientHeight;
  const wide = !view.lineWrapping && scroller.scrollWidth > scroller.clientWidth;
  if (!tall && !wide) return null;
  const port = scroller.getBoundingClientRect();
  const pos = view.posAtCoords(topEdgePoint(view), false);
  const at = view.coordsAtPos(pos);
  return at ? { pos, offset: at.top - port.top, left: at.left - port.left } : null;
}

/** Keeps the reader's text in place while the editor revises the size of a long line. */
export const editorReadingAnchor = ViewPlugin.fromClass(class {
  anchor: Anchor | null = null;
  // Where this anchor last scrolled to: that scroll is its own, every other one is the reader's place.
  wrote: { top: number; left: number } | null = null;
  readonly correction = {};
  constructor(readonly view: EditorView) { this.capture(); }

  capture(): void {
    this.view.requestMeasure({ key: this, read: (view) => { this.anchor = readReadingAnchor(view); } });
  }

  update(update: ViewUpdate): void {
    if (this.anchor && update.docChanged) this.anchor = { ...this.anchor, pos: update.changes.mapPos(this.anchor.pos) };
    if (!this.anchor || !(update.heightChanged || update.geometryChanged) || isSettlingScroll(update.view)) return;
    const anchor = this.anchor;
    // A layout change that starts a settle replaces this correction: the anchor is from before it.
    const generation = settleGeneration(update.view);
    update.view.requestMeasure<Drift | null>({
      key: this.correction,
      read: (view) => {
        const rect = positionRect(view, Math.min(anchor.pos, view.state.doc.length));
        if (!rect) return null;
        const port = view.scrollDOM.getBoundingClientRect();
        return { top: rect.top - port.top - anchor.offset, left: rect.left === null ? 0 : rect.left - port.left - anchor.left };
      },
      write: (drift, view) => {
        // The reader moved since this anchor was taken; their place is the new one.
        if (!drift || this.anchor !== anchor) return;
        if (isSettlingScroll(view) || settleGeneration(view) !== generation) return;
        const scroller = view.scrollDOM;
        if (Math.abs(drift.top) < 1 && Math.abs(drift.left) < 1) return;
        if (Math.abs(drift.top) >= 1) setEditorScrollTop(view, scroller.scrollTop + drift.top);
        if (Math.abs(drift.left) >= 1) scroller.scrollLeft += drift.left;
        this.wrote = { top: scroller.scrollTop, left: scroller.scrollLeft };
      },
    });
  }

  onScroll(): void {
    const wrote = this.wrote;
    const scroller = this.view.scrollDOM;
    if (wrote && Math.abs(scroller.scrollTop - wrote.top) < 1 && Math.abs(scroller.scrollLeft - wrote.left) < 1) return;
    this.wrote = null;
    this.capture();
  }
}, { eventHandlers: { scroll() { this.onScroll(); } } });
