// @vitest-environment jsdom
import { expect, it } from "vitest";
import { EditorView } from "@codemirror/view";
import { createSourceEditorState } from "../../components/source/editor/codemirror-theme.ts";
import { applyEditorScopeDiff } from "../../components/source/diff/scope-diff.ts";
import { navigateEditorChange } from "./editor-change-navigation.ts";

it("visits separated changes in both directions and wraps", () => {
  const view = new EditorView({ parent: document.body, state: createSourceEditorState({ surface: "files", doc: "one\nTWO\nthree\nfour\nFIVE\n", editable: true }) });
  try {
    applyEditorScopeDiff(view, { original: "one\ntwo\nthree\nfour\nfive\n" });
    expect(navigateEditorChange(view, 1)).toBe(true);
    expect(view.state.doc.lineAt(view.state.selection.main.head).number).toBe(2);
    navigateEditorChange(view, 1);
    expect(view.state.doc.lineAt(view.state.selection.main.head).number).toBe(5);
    navigateEditorChange(view, 1);
    expect(view.state.doc.lineAt(view.state.selection.main.head).number).toBe(2);
    navigateEditorChange(view, -1);
    expect(view.state.doc.lineAt(view.state.selection.main.head).number).toBe(5);
  } finally { view.destroy(); }
});
