import { EditorView } from "@codemirror/view";
import { editorScopeDiffChunks } from "../../components/source/diff/scope-diff.ts";

/** Navigate the displayed comparison, including deletion-only changes. */
export function navigateEditorChange(view: EditorView, direction: 1 | -1): boolean {
  const chunks = editorScopeDiffChunks(view.state);
  if (!chunks?.length) return false;
  const head = view.state.selection.main.head;
  const positions = [...new Set(chunks.map(chunk => Math.min(chunk.fromB, view.state.doc.length)))];
  const target = direction === 1
    ? positions.find(position => position > head) ?? positions[0]!
    : [...positions].reverse().find(position => position < head) ?? positions[positions.length - 1]!;
  view.dispatch({ selection: { anchor: target }, effects: EditorView.scrollIntoView(target, { y: "center" }), userEvent: "select" });
  view.focus();
  return true;
}
