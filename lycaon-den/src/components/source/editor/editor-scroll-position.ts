import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";

export type EditorScrollPosition = { top: number; left: number };

/** Update and teardown read cached scroll positions without measuring layout. */
export const editorScrollPosition = ViewPlugin.fromClass(class {
  position: EditorScrollPosition = { top: 0, left: 0 };
  constructor(readonly view: EditorView) { this.schedule(); }
  capture(): void {
    this.position = { top: this.view.scrollDOM.scrollTop, left: this.view.scrollDOM.scrollLeft };
  }
  update(update: ViewUpdate): void {
    if (update.docChanged || update.geometryChanged) this.schedule();
  }
  schedule(): void {
    this.view.requestMeasure({ key: this, read: () => this.capture() });
  }
}, { eventHandlers: { scroll() { this.capture(); } } });

export function readEditorScrollPosition(view: EditorView): EditorScrollPosition {
  return view.plugin(editorScrollPosition)?.position ?? { top: 0, left: 0 };
}

/** A coordinated scroll commit already has a clamped destination. */
export function recordEditorScrollPosition(view: EditorView, next: EditorScrollPosition): void {
  const position = view.plugin(editorScrollPosition);
  if (position) position.position = next;
}

/** Restoration updates the snapshot before the browser scroll event. */
export function setEditorScrollTop(view: EditorView, top: number): void {
  const position = view.plugin(editorScrollPosition);
  if (position) position.position = { ...position.position, top };
  view.scrollDOM.scrollTop = top;
  position?.schedule();
}
