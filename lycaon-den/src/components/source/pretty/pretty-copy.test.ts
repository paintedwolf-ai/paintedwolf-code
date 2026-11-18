import { EditorState } from "@codemirror/state";
import { Decoration } from "@codemirror/view";
import { describe, expect, it } from "vitest";
import { copyAsShown } from "./pretty-copy.ts";
import { prettyBreakDecoration, prettyIndentDecoration, prettyLayoutDecorations } from "./pretty-view.ts";

describe("copyAsShown", () => {
  const state = EditorState.create({
    doc: "ab, \tcd, ef\n  gh",
    extensions: [prettyLayoutDecorations.of(Decoration.set([
      prettyBreakDecoration.range(5),
      prettyIndentDecoration(3).range(5, 9),
      prettyBreakDecoration.range(9),
    ], true))],
  });

  it.each([
    [0, 16, "ab,\n   cd,\nef\n  gh"],
    [1, 6, "b,\n   c"],
    [4, 6, "\n   c"],
    [5, 9, "cd, "],
    [2, 5, ", \t"],
    [9, 9, ""],
    [6, 12, "d,\nef\n"],
  ] as const)("copies the interval %i to %i", (from, to, expected) => {
    expect(copyAsShown(state, from, to)).toBe(expected);
  });
});
