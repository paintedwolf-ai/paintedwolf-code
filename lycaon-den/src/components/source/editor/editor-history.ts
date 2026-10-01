import { Facet } from "@codemirror/state";
import { undo, redo } from "@codemirror/commands";
import type { EditorView } from "@codemirror/view";

/** Collaborative history lives outside the editor view. */
export const editorHistory = Facet.define<(direction: "undo" | "redo") => void, ((direction: "undo" | "redo") => void) | undefined>({
  combine: values => values[0],
});

export function stepEditorHistory(view: EditorView, direction: "undo" | "redo"): boolean {
  if (view.state.readOnly) return false;
  const step = view.state.facet(editorHistory);
  if (step) { step(direction); return true; }
  return (direction === "undo" ? undo : redo)(view);
}
