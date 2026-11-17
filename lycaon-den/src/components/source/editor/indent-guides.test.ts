// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView, type ViewUpdate } from "@codemirror/view";
import { indentUnit } from "@codemirror/language";
import { indentGuides } from "./indent-guides.ts";

function guideCount(view: EditorView): number {
  const plugin = view.plugin(indentGuides);
  let n = 0;
  plugin?.decorations.between(0, view.state.doc.length, () => {
    n++;
  });
  return n;
}

describe("indent guides", () => {
  it("marks indent boundaries before content", () => {
    const view = new EditorView({
      state: EditorState.create({
        doc: "    if (true) {\n        return;\n}\n",
        extensions: [indentUnit.of("    "), indentGuides],
      }),
    });
    try {
      // Unit 4: only the 8-space line has an interior mark.
      expect(guideCount(view)).toBe(1);
      view.dispatch({ changes: { from: 0, insert: " " } });
      expect(guideCount(view)).toBe(2);
    } finally {
      view.destroy();
    }
  });

  it("keeps its marks through an update that changes only geometry", () => {
    const view = new EditorView({
      state: EditorState.create({ doc: "        return;\n", extensions: [indentUnit.of("    "), indentGuides] }),
    });
    try {
      const plugin = view.plugin(indentGuides)!;
      const marks = plugin.decorations;
      plugin.update({ docChanged: false, viewportChanged: false, geometryChanged: true, view, state: view.state } as unknown as ViewUpdate);
      expect(plugin.decorations).toBe(marks);
    } finally {
      view.destroy();
    }
  });
});
