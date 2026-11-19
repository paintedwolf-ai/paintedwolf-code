import type { EditorState } from "@codemirror/state";
import { copyAsShown } from "../../components/source/pretty/pretty-copy.ts";
import { prettyLayoutActive } from "../../components/source/pretty/pretty-view.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";

/** A menu keeps the document and selection from the view that opened it. */
export function prettyCopyAction(state: EditorState | undefined, scope: "document" | "selection") {
  if (!state?.facet(prettyLayoutActive)) return undefined;
  return () => {
    const ranges = state.selection.ranges.filter(range => !range.empty);
    const text = scope === "selection" && ranges.length
      ? ranges.map(range => copyAsShown(state, range.from, range.to)).join(state.lineBreak)
      : copyAsShown(state, 0, state.doc.length);
    return writeClipboardText(text);
  };
}
