// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
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

  it("does not rebuild marks on height-map geometryChanged", () => {
    const src = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), "indent-guides.ts"),
      "utf8",
    );
    expect(src).toMatch(/update\.docChanged \|\| update\.viewportChanged/);
    expect(src).not.toMatch(/update\.geometryChanged/);
  });
});
