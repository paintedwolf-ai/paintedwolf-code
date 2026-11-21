import { forceParsing, language } from "@codemirror/language";
import type { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { expect } from "vitest";

export function finishFixtureParsing(state: EditorState, label?: string): EditorState {
  if (!state.facet(language)) return state;
  const view = new EditorView({ state });
  try {
    expect(forceParsing(view, state.doc.length, 5_000), label).toBe(true);
    return view.state;
  } finally {
    view.destroy();
  }
}
