import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { editorSymbolTarget, wordAtPos } from "./editor-symbol-target.ts";

describe("editor symbol target", () => {
  it("finds the identifier under a position", () => {
    const state = EditorState.create({
      doc: "package main\n\nfunc use() { ResolveX() }\n",
    });
    const line = state.doc.line(3);
    const got = wordAtPos(state, line.from + "func use() { Res".length);
    expect(got).toMatchObject({ symbol: "ResolveX", line: 3 });
  });

  it("targets one identifier from the pointer, caret, or exact selection", () => {
    const doc = "const resolveProjectRoot = 1;";
    const start = doc.indexOf("resolveProjectRoot");
    const end = start + "resolveProjectRoot".length;
    expect(
      editorSymbolTarget(EditorState.create({ doc }), {
        source: "pointer",
        position: start + 4,
      }),
    ).toEqual({
      kind: "symbol",
      symbol: "resolveProjectRoot",
      from: start,
      to: end,
      source: "pointer",
    });

    const caretState = EditorState.create({
      doc,
      selection: { anchor: start + 2 },
    });
    expect(editorSymbolTarget(caretState, { source: "caret" })).toMatchObject({
      kind: "symbol",
      symbol: "resolveProjectRoot",
      source: "caret",
    });

    const selectedState = EditorState.create({
      doc,
      selection: { anchor: start, head: end },
    });
    expect(editorSymbolTarget(selectedState, { source: "caret" })).toMatchObject({
      kind: "symbol",
      symbol: "resolveProjectRoot",
      source: "selection",
    });
  });

  it("rejects broad selections and missing pointer positions", () => {
    const doc = "const resolveProjectRoot = 1;";
    const start = doc.indexOf("resolveProjectRoot");
    const rangeState = EditorState.create({
      doc,
      selection: { anchor: start, head: doc.length },
    });
    expect(editorSymbolTarget(rangeState, { source: "caret" })).toEqual({
      kind: "missing",
      reason: "range",
    });
    expect(
      editorSymbolTarget(EditorState.create({ doc }), {
        source: "pointer",
        position: null,
      }),
    ).toEqual({ kind: "missing", reason: "whitespace" });
  });

  it("rejects whitespace and out-of-range positions", () => {
    const doc = "const value = 1;";
    const state = EditorState.create({ doc });
    expect(
      editorSymbolTarget(state, {
        source: "pointer",
        position: doc.indexOf(" = ") + 1,
      }),
    ).toEqual({ kind: "missing", reason: "whitespace" });
    expect(wordAtPos(state, -1)).toBeNull();
    expect(wordAtPos(state, doc.length + 1)).toBeNull();
  });
});
