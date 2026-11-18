import type { EditorState } from "@codemirror/state";
import { prettyBreakDecoration, prettyLayoutDecorations } from "./pretty-view.ts";

/** Writes the selected source with its interior Pretty breaks and logical indentation. */
export function copyAsShown(state: EditorState, from: number, to: number): string {
  const cursor = state.facet(prettyLayoutDecorations).iter(from);
  const parts: string[] = [];
  let start = from;
  while (cursor.value && cursor.from < to) {
    const pos = cursor.from;
    let hasBreak = false;
    let columns = 0;
    do {
      if (cursor.value === prettyBreakDecoration) hasBreak = true;
      else columns = cursor.value.spec.prettyIndent ?? 0;
      cursor.next();
    } while (cursor.value && cursor.from === pos);
    if (pos <= from || !hasBreak) continue;
    parts.push(state.sliceDoc(start, pos).replace(/[ \t]+$/, ""), state.lineBreak, " ".repeat(columns));
    start = pos;
  }
  parts.push(state.sliceDoc(start, to));
  return parts.join("");
}
