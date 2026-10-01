/** Indentation folding for buffers without syntax trees. */

import { foldService } from "@codemirror/language";
import type { EditorState, Extension, Text } from "@codemirror/state";

type FoldRange = { from: number; to: number } | null;

/** Tabs advance to tab stops; blank lines have no indentation level. */
function indentColumns(text: string, tabSize: number): number | null {
  let indent = 0;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (ch === " ") indent += 1;
    else if (ch === "\t") indent += tabSize - (indent % tabSize);
    else return indent;
  }
  return null;
}

/** Mixed indentation depends on tab width as well as document identity. */
const cache = new WeakMap<Text, Map<number, Map<number, FoldRange>>>();

function scanBlock(state: EditorState, lineNumber: number): FoldRange {
  const doc = state.doc;
  const line = doc.line(lineNumber);
  const base = indentColumns(line.text, state.tabSize);
  if (base == null) return null;
  if (lineNumber >= doc.lines) return null;

  // Interior blank lines fold with the block; trailing blank lines stay visible.
  let end = 0;
  let n = lineNumber;
  const iter = doc.iterLines(lineNumber + 1, doc.lines + 1);
  for (const text of iter) {
    n += 1;
    const indent = indentColumns(text, state.tabSize);
    if (indent == null) continue;
    if (indent <= base) break;
    end = n;
  }
  if (end === 0) return null;
  const to = doc.line(end).to;
  return to > line.to ? { from: line.to, to } : null;
}

function indentFoldRange(state: EditorState, lineStart: number): FoldRange {
  const lineNumber = state.doc.lineAt(lineStart).number;
  let byTabSize = cache.get(state.doc);
  if (!byTabSize) {
    byTabSize = new Map();
    cache.set(state.doc, byTabSize);
  }
  let byLine = byTabSize.get(state.tabSize);
  if (!byLine) {
    byLine = new Map();
    byTabSize.set(state.tabSize, byLine);
  }
  const hit = byLine.get(lineNumber);
  if (hit !== undefined) return hit;
  const range = scanBlock(state, lineNumber);
  byLine.set(lineNumber, range);
  return range;
}

/** Fold a line whose following lines are indented deeper than it is. */
export const indentFoldService: Extension = foldService.of((state, lineStart) =>
  indentFoldRange(state, lineStart),
);
