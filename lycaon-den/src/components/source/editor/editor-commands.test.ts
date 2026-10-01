import { describe, expect, it } from "vitest";
import { EditorSelection, EditorState, type StateCommand } from "@codemirror/state";
import {
  skipAndSelectNextOccurrence,
  toLowerCase,
  toUpperCase,
} from "./editor-commands.ts";

function run(
  command: StateCommand,
  doc: string,
  selection: EditorSelection,
): EditorState {
  let state = EditorState.create({
    doc,
    selection,
    extensions: EditorState.allowMultipleSelections.of(true),
  });
  expect(command({
    state,
    dispatch: (transaction) => {
      state = transaction.state;
    },
  })).toBe(true);
  return state;
}

describe("skipAndSelectNextOccurrence", () => {
  it("wraps from the final occurrence to the first", () => {
    const state = run(
      skipAndSelectNextOccurrence,
      "one x one x one",
      EditorSelection.single(12, 15),
    );
    expect(state.selection.main.from).toBe(0);
    expect(state.selection.main.to).toBe(3);
  });

  it("expands an empty selection and skips its current word", () => {
    const state = run(
      skipAndSelectNextOccurrence,
      "one x one",
      EditorSelection.single(1),
    );
    expect(state.selection.main.from).toBe(6);
    expect(state.selection.main.to).toBe(9);
  });
});

describe("case conversion selections", () => {
  it("selects the full replacement when uppercase changes UTF-16 length", () => {
    const state = run(
      toUpperCase,
      "straße",
      EditorSelection.single(0, 6),
    );
    expect(state.doc.toString()).toBe("STRASSE");
    expect(state.selection.main.from).toBe(0);
    expect(state.selection.main.to).toBe(7);
  });

  it("maps later multi-selections across earlier length changes", () => {
    const state = run(
      toUpperCase,
      "ß x ß",
      EditorSelection.create([
        EditorSelection.range(0, 1),
        EditorSelection.range(4, 5),
      ], 1),
    );
    expect(state.doc.toString()).toBe("SS x SS");
    expect(state.selection.ranges.map(({ from, to }) => [from, to])).toEqual([
      [0, 2],
      [5, 7],
    ]);
  });

  it("maps lowercase expansions", () => {
    const state = run(
      toLowerCase,
      "İ",
      EditorSelection.single(0, 1),
    );
    expect(state.doc.toString()).toBe("i̇");
    expect(state.selection.main.to).toBe(2);
  });
});
