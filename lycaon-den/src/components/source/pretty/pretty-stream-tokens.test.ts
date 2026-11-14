// @vitest-environment jsdom
import { forceParsing, indentUnit } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { createSourceEditorState } from "../editor/codemirror-theme.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function breakPositions(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) out.push(from);
  });
  return out;
}

function buildState(path: string, doc: string): EditorState {
  const state = createSourceEditorState({
    surface: "files",
    doc,
    language: languageExtensionForPath(path),
    extensions: [indentUnit.of("  "), createPrettyExtension({ enabled: true, path })],
  });
  return state;
}

function forceAndRead(path: string, doc: string): { view: EditorView; parent: HTMLDivElement; breaks: number[] } {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  const view = new EditorView({
    parent,
    state: buildState(path, doc),
  });
  forceParsing(view, doc.length, 5_000);
  return { view, parent, breaks: breakPositions(view.state) };
}

describe("simplified parser and stream tokens past column 10,000", () => {
  it("protects strings past column 10,000 in Go (Lezer), Java, Ruby, and HCL, and emits zero .tok-* highlights", () => {
    const pad = "x".repeat(12_000);

    const goDoc = `package p; func f() { a := "${pad}a, b; {c}"; b := compute(1, 2, 3, 4) }`;
    const javaDoc = `class A { String s = "${pad}a, b; {c}"; void f(int a, int b) { call(a, b); } }`;
    const rubyDoc = `def f(a, b); s = "${pad}a, b; {c}"; call(a, b); end`;
    const hclDoc = `resource "a" "b" { note = "${pad}a, b; {c}"; list = [1, 2, 3] }`;

    for (const [path, doc] of [
      ["main.go", goDoc],
      ["Main.java", javaDoc],
      ["app.rb", rubyDoc],
      ["main.tf", hclDoc],
    ] as const) {
      const { view, parent, breaks } = forceAndRead(path, doc);
      try {
        expect(breaks.length, `${path} should lay out in simplified mode`).toBeGreaterThan(0);
        const strStart = doc.indexOf(`"${pad}`);
        const strEnd = doc.indexOf('"', strStart + 1) + 1;
        const breaksInsideString = breaks.filter((pos) => pos > strStart && pos < strEnd);
        expect(breaksInsideString, `${path} must not break inside string past 10k`).toEqual([]);
        expect(breaks.some((pos) => pos >= strEnd), `${path} should lay out code after string`).toBe(true);
        expect(view.dom.querySelectorAll("[class*='tok-']").length).toBe(0);
      } finally {
        view.destroy();
        parent.remove();
      }
    }
  });

  it("matches a fresh build after an incremental edit at column 13,000", () => {
    const pad = "a".repeat(13_000);
    const before = `class A { String s = "${pad}"; void f(int x, int y) { call(x, y); } }`;
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const view = new EditorView({
      parent,
      state: buildState("Main.java", before),
    });
    try {
      forceParsing(view, before.length, 5_000);
      const editAt = before.indexOf('";');
      view.dispatch({ changes: { from: editAt, to: editAt, insert: ", z; {w}" } });
      const after = view.state.doc.toString();
      forceParsing(view, after.length, 5_000);
      const incrementalBreaks = breakPositions(view.state);

      const fresh = forceAndRead("Main.java", after);
      try {
        expect(incrementalBreaks).toEqual(fresh.breaks);
      } finally {
        fresh.view.destroy();
        fresh.parent.remove();
      }
    } finally {
      view.destroy();
      parent.remove();
    }
  });
});
