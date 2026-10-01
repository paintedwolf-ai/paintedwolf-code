import { describe, expect, it } from "vitest";
import { Compartment, EditorState } from "@codemirror/state";
import { ensureSyntaxTree, foldable } from "@codemirror/language";
import { indentFoldService } from "./indent-fold.ts";
import { languageExtensionForPath } from "./codemirror-lang.ts";

function stateOf(doc: string, extensions: unknown[] = []) {
  return EditorState.create({
    doc,
    extensions: extensions as never,
  });
}

/** Returns a fold's 1-based line range. */
function foldLines(doc: string, line: number): [number, number] | null {
  const state = stateOf(doc, [indentFoldService]);
  const at = state.doc.line(line);
  const range = foldable(state, at.from, at.to);
  if (!range) return null;
  return [
    state.doc.lineAt(range.from).number,
    state.doc.lineAt(range.to).number,
  ];
}

describe("indent-fold", () => {
  it("folds a block from its header to the last deeper line", () => {
    const doc = ["def a():", "    x = 1", "    y = 2", "z = 3"].join("\n");
    expect(foldLines(doc, 1)).toEqual([1, 3]);
  });

  it("offers nothing on a line that opens no block", () => {
    const doc = ["a = 1", "b = 2"].join("\n");
    expect(foldLines(doc, 1)).toBeNull();
    expect(foldLines(doc, 2)).toBeNull();
  });

  it("offers nothing on a blank line or the last line", () => {
    const doc = ["a:", "  b", "", "c"].join("\n");
    expect(foldLines(doc, 3)).toBeNull();
    expect(foldLines(doc, 4)).toBeNull();
  });

  it("keeps an interior blank line but not a trailing one", () => {
    const inner = ["a:", "  b", "", "  c", "d"].join("\n");
    expect(foldLines(inner, 1)).toEqual([1, 4]);
    const trailing = ["a:", "  b", "", "d"].join("\n");
    expect(foldLines(trailing, 1)).toEqual([1, 2]);
  });

  it("nests — an inner block folds independently of its parent", () => {
    const doc = [
      "outer:",
      "  middle:",
      "    leaf",
      "  sibling",
      "after",
    ].join("\n");
    expect(foldLines(doc, 1)).toEqual([1, 4]);
    expect(foldLines(doc, 2)).toEqual([2, 3]);
  });

  it("measures a tab as a tab stop, so mixed indentation still nests", () => {
    const doc = ["a:", "\tb", "        c", "d"].join("\n");
    // The tab lands on column 4.
    expect(foldLines(doc, 1)).toEqual([1, 3]);
    expect(foldLines(doc, 2)).toEqual([2, 3]);
  });

  it("recomputes mixed-indentation folds when tab width changes without a text edit", () => {
    const tabWidth = new Compartment();
    const initial = stateOf("\theader\n    child\nend", [
      indentFoldService,
      tabWidth.of(EditorState.tabSize.of(2)),
    ]);
    const line = initial.doc.line(1);
    const expected = { from: line.to, to: initial.doc.line(2).to };
    expect(foldable(initial, line.from, line.to)).toEqual(expected);

    const wider = initial.update({ effects: tabWidth.reconfigure(EditorState.tabSize.of(8)) }).state;
    expect(wider.doc).toBe(initial.doc);
    expect(foldable(wider, line.from, line.to)).toBeNull();
    expect(foldable(initial, line.from, line.to)).toEqual(expected);
  });

  it("starts the fold at the end of the header line, so it stays on screen", () => {
    const doc = ["a:", "  b", "c"].join("\n");
    const state = stateOf(doc, [indentFoldService]);
    const line = state.doc.line(1);
    // Keep the header outside the folded range.
    expect(foldable(state, line.from, line.to)).toEqual({
      from: line.to,
      to: state.doc.line(2).to,
    });
  });

  it("is attached to stream-mode languages, which carry no fold ranges", () => {
    const doc = ["server {", "  listen 80;", "}"].join("\n");
    // Stream parsers use indentation folding.
    const state = stateOf(doc, [languageExtensionForPath("deploy.sh")!]);
    const line = state.doc.line(1);
    expect(foldable(state, line.from, line.to)).toEqual({
      from: line.to,
      to: state.doc.line(2).to,
    });
  });

  it("leaves parsed languages to their own tree folding", () => {
    const doc = ["const a = {", "  b: 1,", "};"].join("\n");
    let state = stateOf(doc, [languageExtensionForPath("a.ts")!]);
    // Complete parsing, then publish the tree through a transaction.
    ensureSyntaxTree(state, state.doc.length, Infinity);
    state = state.update({}).state;
    const line = state.doc.line(1);
    // Object folding starts inside the header.
    const range = foldable(state, line.from, line.to);
    expect(range).not.toBeNull();
    expect(range!.from).toBeGreaterThan(line.from);
  });
});
