import { foldEffect } from "@codemirror/language";
import type { EditorState } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { editorScopeDiffChunks } from "../../components/source/diff/scope-diff.ts";

/** Reuses the editor's incremental comparison; folding never computes another diff. */
export function condensedVersionLineRanges(state: EditorState, contextLines = 3): { fromLine: number; toLine: number }[] {
  const chunks = editorScopeDiffChunks(state);
  if (!chunks?.length) return [];
  const doc = state.doc;
  const context = Math.max(0, Math.floor(contextLines));
  const ranges: { fromLine: number; toLine: number }[] = [];
  let fromLine = 0;
  for (const chunk of chunks) {
    const toLine = Math.max(0, doc.lineAt(Math.min(doc.length, chunk.fromB)).number - 1 - context);
    if (toLine - fromLine >= 4) ranges.push({ fromLine, toLine });
    fromLine = Math.min(doc.lines, doc.lineAt(Math.min(doc.length, chunk.toB)).number - 1 + context);
  }
  if (doc.lines - fromLine >= 4) ranges.push({ fromLine, toLine: doc.lines });
  return ranges;
}

export function foldCondensedContext(view: EditorView): boolean {
  const doc = view.state.doc;
  const effects = condensedVersionLineRanges(view.state).flatMap(range => {
    const from = doc.line(Math.min(range.fromLine + 1, doc.lines)).from;
    const to = doc.line(Math.min(range.toLine, doc.lines)).to;
    return to > from ? [foldEffect.of({ from, to })] : [];
  });
  if (effects.length) view.dispatch({ effects });
  return effects.length > 0;
}
