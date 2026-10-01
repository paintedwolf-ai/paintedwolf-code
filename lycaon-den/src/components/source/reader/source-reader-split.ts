import { setEditorScrollTop } from "../editor/editor-scroll-position.ts";
import type { ReaderEditor } from "./source-reader-editor.ts";

/** Both panes share row geometry, including wrapped lines and partial ranges. */
export class ReaderSplit {
  private disposed = false;
  private queued = false;
  private source: ReaderEditor;
  private readonly offsets = new Map<ReaderEditor, number>();
  private readonly reflected = new Map<ReaderEditor, number>();
  constructor(readonly before: ReaderEditor, readonly after: ReaderEditor) {
    this.source = after;
    for (const editor of [before, after]) this.offsets.set(editor, editor.view.scrollDOM.scrollTop);
  }

  viewportAnchor(): ReturnType<ReaderEditor["viewportAnchor"]> {
    if (this.source.viewportInputPending) return this.source.viewportAnchor();
    // Native scroll events may arrive after a page response starts materializing rows.
    const moved = [this.before, this.after].filter(editor => editor.view.scrollDOM.scrollTop !== this.offsets.get(editor));
    if (moved.length === 1) this.source = moved[0]!;
    return this.source.viewportAnchor();
  }

  restoreViewport(anchor: NonNullable<ReturnType<ReaderEditor["viewportAnchor"]>>): void {
    if (this.source.viewportInputPending) return;
    this.before.restoreViewport(anchor);
    this.after.restoreViewport(anchor);
  }

  input(source: ReaderEditor): void {
    this.source = source;
    this.before.interruptViewportRestoration();
    this.after.interruptViewportRestoration();
  }

  scroll(source: ReaderEditor): void {
    if (this.disposed) return;
    const target = source === this.before ? this.after : this.before;
    const top = source.view.scrollDOM.scrollTop;
    this.offsets.set(source, top);
    const reflected = this.reflected.get(source);
    this.reflected.delete(source);
    if (reflected === top) return;
    if (source !== this.source && this.source.viewportInputPending) return;
    this.source = source;
    if (Math.abs(target.view.scrollDOM.scrollTop - top) > 1) {
      setEditorScrollTop(target.view, top);
      const landed = target.view.scrollDOM.scrollTop;
      this.offsets.set(target, landed);
      this.reflected.set(target, landed);
    }
  }

  measure(): void {
    if (this.disposed || this.queued) return;
    this.queued = true;
    this.before.view.requestMeasure({ key: this,
      read: () => {
        if (this.disposed) return undefined;
        const left = new Map(this.before.alignment), right = new Map(this.after.alignment);
        const viewport = this.before.view.viewport;
        for (const entry of this.before.document.entriesBetween(viewport.from, viewport.to)) {
          if (entry.slot.kind === "gap") continue;
          const peer = this.after.document.entryForSlot(entry.section, entry.slot.index);
          if (!peer) continue;
          const a = this.before.view.lineBlockAt(entry.from), b = this.after.view.lineBlockAt(peer.from);
          const aHeight = a.height - (left.get(a.from) ?? 0), bHeight = b.height - (right.get(b.from) ?? 0);
          if (aHeight <= 0 || bHeight <= 0) continue;
          const height = Math.max(aHeight, bHeight);
          if (height > aHeight) left.set(a.from, height - aHeight); else left.delete(a.from);
          if (height > bHeight) right.set(b.from, height - bHeight); else right.delete(b.from);
        }
        return { left, right, before: this.before.document, after: this.after.document };
      },
      write: result => queueMicrotask(() => {
        this.queued = false;
        if (this.disposed || !result) return;
        if (result.before !== this.before.document || result.after !== this.after.document) { this.measure(); return; }
        this.before.align(result.left); this.after.align(result.right);
      }),
    });
  }
  destroy(): void { this.disposed = true; }
}
