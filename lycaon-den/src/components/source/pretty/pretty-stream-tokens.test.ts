// @vitest-environment jsdom
import { NodeType, Tree } from "@lezer/common";
import { forceParsing, indentUnit, type StreamParser } from "@codemirror/language";
import { EditorState, Text } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { createSourceEditorState } from "../editor/codemirror-theme.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { TextWindow } from "./pretty-text-window.ts";
import { TreeLexer, TreeTokens } from "./pretty-tokens.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function breakPositions(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) out.push(from);
  });
  return out;
}

function buildState(path: string, doc: string): EditorState {
  return createSourceEditorState({
    surface: "files",
    doc,
    language: languageExtensionForPath(path),
    extensions: [indentUnit.of("  "), createPrettyExtension({ enabled: true, path })],
  });
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

  it("matches ^-anchored mode regexes past column 0 and deep-clones array state across checkpoints", () => {
    const parser: StreamParser<{ stack: string[] }> = {
      startState: () => ({ stack: [] }),
      token(stream, state) {
        if (state.stack.length > 0) {
          if (stream.match(/^\}/)) {
            state.stack.pop();
            return "string";
          }
          stream.next();
          return "string";
        }
        if (stream.match(/^STR\{/)) {
          state.stack.push("str");
          return "string";
        }
        stream.next();
        return null;
      },
    };

    const pad = "a".repeat(10_500);
    const line = `prefix = STR{${pad}} + tail`;
    const doc = Text.of([line]);
    const fullTree = new Tree(NodeType.none, [], [], doc.length);
    const treeTokens = new TreeTokens(fullTree, doc, parser as StreamParser<unknown>, 4, 2);
    const win = new TextWindow(doc, treeTokens);
    const lexer = new TreeLexer();

    const openIdx = line.indexOf("STR{");
    const closeIdx = line.indexOf("}") + 1;
    // Query past two 4,096-char windows first to populate checkpoints inside `stack: ["str"]`,
    // then advance past `}` (mutating the working state's `stack`), then re-query an earlier window.
    expect(lexer.skip(win, openIdx + 9_000, line.length)).toBe(closeIdx);
    lexer.endLine();
    expect(lexer.skip(win, closeIdx + 1, line.length)).toBe(closeIdx + 1);
    expect(lexer.skip(win, openIdx, line.length)).toBe(closeIdx);
    lexer.endLine();
    expect(lexer.skip(win, openIdx + 4_500, line.length)).toBe(closeIdx);
  });

  it("carries multi-line block comment state from earlier lines into >10,000-column lines", () => {
    const commentBody = `${"word ".repeat(2_500)}trap(a, b, c); { x: 1, y: 2 } */`;
    const codeTail = ` void after() { compute(${Array.from({ length: 30 }, (_, i) => `arg${i}`).join(", ")}); } }`;
    const line2 = `${commentBody}${codeTail}`;
    expect(line2.length).toBeGreaterThan(12_500);
    const doc = `class Multi { /* open comment on line 1\n${line2}\n`;

    const { view, parent, breaks } = forceAndRead("Multi.java", doc);
    try {
      const commentEnd = doc.indexOf("*/") + 2;
      expect(breaks.filter((pos) => pos < commentEnd)).toEqual([]);
      expect(breaks.filter((pos) => pos > commentEnd).length).toBeGreaterThan(0);
    } finally {
      view.destroy();
      parent.remove();
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
