import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";
import { setEditorScrollTop } from "./editor-scroll-position.ts";
import { isSettlingScroll, positionRect, topEdgePoint } from "./editor-settled-scroll.ts";

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
  // Set when geometry changed; a scroll the editor makes meanwhile is not the reader's.
  pending = false;
  readonly correction = {};
  constructor(readonly view: EditorView) { this.capture(); }

  capture(): void {
    this.view.requestMeasure({ key: this, read: (view) => { this.anchor = readReadingAnchor(view); } });
  }

  update(update: ViewUpdate): void {
    if (this.anchor && update.docChanged) this.anchor = { ...this.anchor, pos: update.changes.mapPos(this.anchor.pos) };
    if (!this.anchor || !(update.heightChanged || update.geometryChanged) || isSettlingScroll(update.view)) return;
    this.pending = true;
    const anchor = this.anchor;
    update.view.requestMeasure<Drift | null>({
      key: this.correction,
      read: (view) => {
        const rect = positionRect(view, Math.min(anchor.pos, view.state.doc.length));
        if (!rect) return null;
        const port = view.scrollDOM.getBoundingClientRect();
        return { top: rect.top - port.top - anchor.offset, left: rect.left === null ? 0 : rect.left - port.left - anchor.left };
      },
      write: (drift, view) => {
        this.pending = false;
        if (!drift) return;
        if (Math.abs(drift.top) >= 1) setEditorScrollTop(view, view.scrollDOM.scrollTop + drift.top);
        if (Math.abs(drift.left) >= 1) view.scrollDOM.scrollLeft += drift.left;
      },
    });
  }
}, { eventHandlers: { scroll() { if (!this.pending) this.capture(); } } });
