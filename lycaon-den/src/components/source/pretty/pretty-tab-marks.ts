import { type EditorState, RangeSetBuilder } from "@codemirror/state";
import {
  Decoration,
  type DecorationSet,
  type EditorView,
  ViewPlugin,
  type ViewUpdate,
} from "@codemirror/view";
import { MARKED_TAB_CLASS } from "../editor/editor-theme-rules.ts";
import { prettyLayoutActive } from "./pretty-view.ts";

const tabMark = Decoration.mark({ class: MARKED_TAB_CLASS });

const TAB = 9;
const SPACE = 32;
const CHUNK = 256;

type Range = { from: number; to: number };

/**
 * Marks each tab before a line's first non-blank character. A blank line has
 * no indentation to mark; its whitespace is trailing.
 */
export function leadingTabMarks(state: EditorState, ranges: readonly Range[]): DecorationSet {
  const builder = new RangeSetBuilder<Decoration>();
  const doc = state.doc;
  let lastLine = 0;
  for (const range of ranges) {
    for (let pos = range.from; pos <= range.to;) {
      const line = doc.lineAt(pos);
      pos = line.to + 1;
      if (line.number <= lastLine) continue;
      lastLine = line.number;
      const tabs: number[] = [];
      let blank = true;
      scan: for (let from = line.from; from < line.to; from += CHUNK) {
        const text = doc.sliceString(from, Math.min(line.to, from + CHUNK));
        for (let i = 0; i < text.length; i++) {
          const code = text.charCodeAt(i);
          if (code === TAB) tabs.push(from + i);
          else if (code !== SPACE) {
            blank = false;
            break scan;
          }
        }
      }
      if (blank) continue;
      for (const at of tabs) builder.add(at, at + 1, tabMark);
    }
  }
  return builder.finish();
}

function marksFor(view: EditorView): DecorationSet {
  return view.state.facet(prettyLayoutActive) ? leadingTabMarks(view.state, view.visibleRanges) : Decoration.none;
}

/** While Pretty lays the document out, tabs in indentation are always marked. */
export const prettyTabMarks = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;

    constructor(view: EditorView) {
      this.decorations = marksFor(view);
    }

    update(update: ViewUpdate) {
      if (
        update.docChanged ||
        update.viewportChanged ||
        update.startState.facet(prettyLayoutActive) !== update.state.facet(prettyLayoutActive)
      ) {
        this.decorations = marksFor(update.view);
      }
    }
  },
  { decorations: (plugin) => plugin.decorations },
);
