// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import { lineGutterExtension } from "../annotations/line-gutter.ts";
import {
  applyEditorScopeDiff,
  editorScopeDiffFolds,
  scopeDiffExtension,
  setScopeDeletedLines,
  setScopeDeletionPeek,
  type ScopeComparisonInput,
} from "./scope-diff.ts";

describe("a folded removal's preview", () => {
  it("keeps the mark under the pointer while the preview opens and closes", () => {
    const view = mount("a\nb\n", { original: "a\nx\nb\n" });
    setScopeDeletedLines(view, "folded");
    const cut = view.dom.querySelector<HTMLElement>(".files-line-gutter__cut");
    const [fold] = editorScopeDiffFolds(view.state);
    expect(cut?.getAttribute("aria-expanded")).toBe("false");

    setScopeDeletionPeek(view, fold!.at);
    expect(view.dom.querySelector(".files-line-gutter__cut")).toBe(cut);
    expect(cut?.getAttribute("aria-expanded")).toBe("true");

    setScopeDeletionPeek(view, null);
    expect(view.dom.querySelector(".files-line-gutter__cut")).toBe(cut);
    expect(cut?.getAttribute("aria-expanded")).toBe("false");
  });
});

const views: EditorView[] = [];

afterEach(() => {
  for (const view of views.splice(0)) {
    view.dom.parentElement?.remove();
    view.destroy();
  }
});

function mount(doc: string, input: ScopeComparisonInput): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  const view = new EditorView({
    parent,
    state: EditorState.create({ doc, extensions: [lineGutterExtension, scopeDiffExtension] }),
  });
  views.push(view);
  applyEditorScopeDiff(view, input);
  return view;
}

describe("review comparison lifecycle", () => {
  it("clears both text and gutter marks when the acknowledged file reopens", () => {
    const view = mount("a\nB\n", { original: "a\nb\n" });
    expect(view.dom.querySelectorAll(".cm-changedLine")).toHaveLength(1);
    applyEditorScopeDiff(view, null);
    expect(view.state.doc.toString()).toBe("a\nB\n");
    expect(view.dom.querySelectorAll(".cm-changedLine, .cm-deletedChunk, .files-line-gutter__cell--add")).toHaveLength(0);
    applyEditorScopeDiff(view, { original: "a\nb\n" });
    expect(view.dom.querySelectorAll(".cm-changedLine")).toHaveLength(1);
  });

  it("keeps later human typing outside saved attribution", () => {
    const view = mount("a\nB\n", {
      original: "a\nb\n", tip: "a\nB\n",
      attribution: { before: [{ index: 2, length: 1, visible: true, selected: true, contributors: [] }],
        after: [{ index: 2, length: 1, visible: true, selected: true, contributors: [] }] },
    });
    view.dispatch({ changes: { from: view.state.doc.length, insert: "typed\n" }, userEvent: "input.type" });
    expect(view.dom.querySelectorAll(".cm-changedLine")).toHaveLength(1);
  });
});
