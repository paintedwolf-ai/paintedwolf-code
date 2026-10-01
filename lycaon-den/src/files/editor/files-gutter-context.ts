/** Adds gutter context-menu targeting without moving the caret. */

import { EditorView, ViewPlugin } from "@codemirror/view";

const gutterSurfaceMarker = ViewPlugin.fromClass(
  class {
    /** Cached because reconfiguring gutters (line-number toggle) rebuilds it. */
    private gutters: Element | null = null;

    constructor(private view: EditorView) {
      this.mark();
    }

    update() {
      this.mark();
    }

    mark() {
      if (this.gutters?.isConnected) return;
      const gutters = this.view.dom.querySelector(".cm-gutters");
      if (!gutters) return;
      gutters.setAttribute("data-files-ctx", "gutter");
      this.gutters = gutters;
    }
  },
);

export const filesGutterContextExtension = [
  gutterSurfaceMarker,
  EditorView.domEventHandlers({
    mousedown(e) {
      if (e.button !== 2) return false;
      if (!(e.target as Element).closest(".cm-gutters")) return false;
      e.preventDefault();
      return true;
    },
  }),
];
