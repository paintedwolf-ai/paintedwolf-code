// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { occurrenceHighlight } from "./occurrence-highlight.ts";

function markCount(view: EditorView): number {
  let n = 0;
  view.plugin(occurrenceHighlight)?.decorations.between(
    0,
    view.state.doc.length,
    () => {
      n++;
    },
  );
  return n;
}

describe("occurrence highlight", () => {
  it("highlights other occurrences of the word under the caret", () => {
    const doc = "alpha beta alpha\n";
    const view = new EditorView({
      state: EditorState.create({
        doc,
        extensions: [occurrenceHighlight],
        selection: { anchor: 0 },
      }),
    });
    try {
      expect(markCount(view)).toBe(1);
      view.dispatch({ selection: { anchor: 6 } });
      expect(markCount(view)).toBe(0);
    } finally {
      view.destroy();
    }
  });

  it("keeps every selected-text match decorated across the document", () => {
    const doc = [
      "needle",
      ...Array.from({ length: 200 }, () => "padding"),
      "needle",
      "needle",
    ].join("\n");
    const view = new EditorView({
      state: EditorState.create({
        doc,
        extensions: [occurrenceHighlight],
        selection: { anchor: 0, head: 6 },
      }),
    });
    try {
      expect(markCount(view)).toBe(2);
    } finally {
      view.destroy();
    }
  });

  it("paints each selected-text match once", () => {
    const view = new EditorView({
      state: EditorState.create({
        doc: "alpha alpha alpha",
        extensions: [occurrenceHighlight],
        selection: { anchor: 0, head: 5 },
      }),
    });
    try {
      expect(view.dom.querySelectorAll(".cm-selectionMatch")).toHaveLength(2);
    } finally {
      view.destroy();
    }
  });
});
