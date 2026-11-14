import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";
import { setEditorScrollTop } from "./editor-scroll-position.ts";
import { isSettlingScroll, positionRect } from "./editor-settled-scroll.ts";

type Anchor = { pos: number; offset: number };

/**
 * The text at the top edge and its distance below it, while the line there is
 * taller than the viewport. The editor anchors scrolling to whole lines, so
 * when its height estimates for such a line change, the text inside it moves.
 */
export function readReadingAnchor(view: EditorView): Anchor | null {
  const scroller = view.scrollDOM;
  if (scroller.clientHeight <= 0) return null;
  const block = view.lineBlockAtHeight(scroller.scrollTop);
  if (block.height <= scroller.clientHeight) return null;
  const port = scroller.getBoundingClientRect();
  const content = view.contentDOM.getBoundingClientRect();
  const pos = view.posAtCoords({ x: Math.max(content.left, port.left) + 1, y: port.top + 1 }, false);
  const at = view.coordsAtPos(pos);
  return at ? { pos, offset: at.top - port.top } : null;
}

/** Keeps the reader's text in place when height estimates of a tall line change. */
export const editorReadingAnchor = ViewPlugin.fromClass(class {
  anchor: Anchor | null = null;
  // Set when heights changed; a scroll the editor makes meanwhile is not the reader's.
  pending = false;
  readonly correction = {};
  constructor(readonly view: EditorView) { this.capture(); }

  capture(): void {
    this.view.requestMeasure({ key: this, read: (view) => { this.anchor = readReadingAnchor(view); } });
  }

  update(update: ViewUpdate): void {
    if (this.anchor && update.docChanged) this.anchor = { ...this.anchor, pos: update.changes.mapPos(this.anchor.pos) };
    if (!this.anchor || !update.heightChanged || isSettlingScroll(update.view)) return;
    this.pending = true;
    const anchor = this.anchor;
    update.view.requestMeasure<number | null>({
      key: this.correction,
      read: (view) => {
        const rect = positionRect(view, Math.min(anchor.pos, view.state.doc.length));
        return rect ? rect.top - view.scrollDOM.getBoundingClientRect().top - anchor.offset : null;
      },
      write: (delta, view) => {
        this.pending = false;
        if (delta !== null && Math.abs(delta) >= 1) setEditorScrollTop(view, view.scrollDOM.scrollTop + delta);
      },
    });
  }
}, { eventHandlers: { scroll() { if (!this.pending) this.capture(); } } });
