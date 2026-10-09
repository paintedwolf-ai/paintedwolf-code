import { EditorView, ViewPlugin, type ViewUpdate } from "@codemirror/view";
import { attachThemedViewportScrollbar, updateThemedViewportScrollbar } from "../../../platform/scrolling/themed-scrollbars.ts";
import { scrollbarChrome } from "../../../platform/scrolling/scrollbar-chrome.ts";
import { bindEditorScrollPaint } from "./editor-scroll-paint.ts";

type EditorScrollbarLifetime = { detach: () => void; detachScrollPaint: () => void; plugin: object };
const editorScrollbars = new WeakMap<EditorView, EditorScrollbarLifetime>();
/** One placement request per editor measure pass. */
const chromePlacement = {};

export const denScrollbars = ViewPlugin.fromClass(
  class {
    private readonly view: EditorView;
    private detach: (() => void) | undefined;
    private detachScrollPaint: (() => void) | undefined;
    private revision = 0;
    private disposed = false;

    constructor(view: EditorView) {
      this.view = view;
      const lifetime = editorScrollbars.get(view);
      if (lifetime) {
        lifetime.plugin = this;
        this.detach = lifetime.detach;
        this.detachScrollPaint = lifetime.detachScrollPaint;
      }
      this.measure();
    }

    update(update: ViewUpdate) {
      if (!update.docChanged && !update.geometryChanged) return;
      if (update.docChanged) this.revision++;
      this.measure();
    }

    private measure() {
      // Scrollbar attachment follows the first document measurement.
      this.view.requestMeasure({
        key: this,
        read: (view) => {
          if (!this.detach) return null;
          const geometry = scrollbarChrome.read(view.dom);
          return {
            signature: `${this.revision}:${Math.round(view.contentHeight)}:${geometry.verticalPercent}:${geometry.horizontalPercent}`,
            geometry,
          };
        },
        write: (measurement) => {
          if (this.disposed) return;
          if (!this.detach) {
            const view = this.view;
            this.detach = attachThemedViewportScrollbar(view.dom, view.scrollDOM, {
              // Scroll-driven chrome placement reads and writes inside the editor's own measure pass.
              schedule: (read, write) => view.requestMeasure({ key: chromePlacement, read, write }),
            });
            this.detachScrollPaint = bindEditorScrollPaint(this.view);
            editorScrollbars.set(this.view, { detach: this.detach, detachScrollPaint: this.detachScrollPaint, plugin: this });
          } else if (measurement) {
            updateThemedViewportScrollbar(this.view.dom, measurement.signature, measurement.geometry);
          }
        },
      });
    }

    destroy() {
      this.disposed = true;
      // A replacement plugin retains the viewport’s scrollbar binding.
      queueMicrotask(() => {
        const lifetime = editorScrollbars.get(this.view);
        if (lifetime?.plugin !== this) return;
        lifetime.detachScrollPaint();
        lifetime.detach();
        editorScrollbars.delete(this.view);
      });
    }
  },
);
