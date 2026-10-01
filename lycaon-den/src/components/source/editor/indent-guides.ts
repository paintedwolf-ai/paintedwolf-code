/** Decorations at indent columns. */

import { RangeSetBuilder } from "@codemirror/state";
import {
  Decoration,
  ViewPlugin,
  type DecorationSet,
  type EditorView,
  type ViewUpdate,
} from "@codemirror/view";
import { getIndentUnit } from "@codemirror/language";

const guideMark = Decoration.mark({ class: "cm-den-indent-guide" });

function buildGuides(view: EditorView): DecorationSet {
  const builder = new RangeSetBuilder<Decoration>();
  const unit = Math.max(1, getIndentUnit(view.state));
  const tabSize = view.state.tabSize;
  for (const { from, to } of view.visibleRanges) {
    let pos = from;
    while (pos <= to) {
      const line = view.state.doc.lineAt(pos);
      const text = line.text;
      let col = 0;
      let indentEnd = 0;
      while (indentEnd < text.length) {
        const ch = text.charCodeAt(indentEnd);
        if (ch === 32) {
          col++;
          indentEnd++;
        } else if (ch === 9) {
          col += tabSize - (col % tabSize);
          indentEnd++;
        } else {
          break;
        }
      }
      let at = 0;
      let visual = 0;
      let nextMark = unit;
      while (at < indentEnd && nextMark < col) {
        const ch = text.charCodeAt(at);
        if (ch === 9) {
          visual += tabSize - (visual % tabSize);
        } else {
          visual++;
        }
        at++;
        if (visual === nextMark) {
          builder.add(line.from + at, line.from + at, guideMark);
          nextMark += unit;
        }
      }
      pos = line.to + 1;
      if (line.to >= view.state.doc.length) break;
    }
  }
  return builder.finish();
}

export const indentGuides = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = buildGuides(view);
    }
    update(update: ViewUpdate) {
      // Height-map discovery sets geometryChanged during scroll; indent
      // columns do not move with that.
      if (update.docChanged || update.viewportChanged) {
        this.decorations = buildGuides(update.view);
      }
    }
  },
  { decorations: (v) => v.decorations },
);
