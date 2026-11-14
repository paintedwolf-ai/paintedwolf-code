import { indentUnit } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function open(doc: string, path: string): EditorState {
  return EditorState.create({
    doc,
    extensions: [languageExtensionForPath(path)!, indentUnit.of("  "), createPrettyExtension({ enabled: true, path })],
  });
}

function breaks(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) out.push(from);
  });
  return out;
}

describe("JSX and TSX adjacent-tag layout", () => {
  it("breaks between adjacent JSX tags and inside expression props while leaving formatted JSX untouched", () => {
    const pad = "x".repeat(200);
    const tsx = `export function App() { return (<Layout data={{ a: 1, b: "${pad}" }}><Sidebar /><Main /></Layout>); }\n`;
    const state = open(tsx, "App.tsx");
    const pos = breaks(state);
    expect(pos).toContain(tsx.indexOf("<Sidebar />"));
    expect(pos).toContain(tsx.indexOf("<Main />"));
    expect(pos).toContain(tsx.indexOf("</Layout>"));

    const formatted = [
      "export function App() {",
      "  return (",
      "    <Layout>",
      "      <Sidebar />",
      "      <Main />",
      "    </Layout>",
      "  );",
      "}",
    ].join("\n");
    expect(breaks(open(formatted, "App.tsx"))).toEqual([]);
  });
});
