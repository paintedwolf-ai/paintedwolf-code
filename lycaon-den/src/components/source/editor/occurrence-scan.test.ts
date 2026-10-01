import { describe, expect, it } from "vitest";
import { EditorState, Text } from "@codemirror/state";
import {
  collectOccurrenceHits,
  collectOccurrenceLines,
  OCCURRENCE_MAX_MARKS,
  occurrenceTargetAt,
} from "./occurrence-scan.ts";

function stateAt(doc: string, head: number): EditorState {
  return EditorState.create({ doc, selection: { anchor: head } });
}

describe("occurrence scan", () => {
  it("uses the word under an empty caret or the selected text", () => {
    const doc = "alpha beta alpha\n";
    expect(occurrenceTargetAt(stateAt(doc, 0))).toEqual({
      text: "alpha",
      from: 0,
      to: 5,
      wholeWord: true,
    });
    expect(occurrenceTargetAt(stateAt(doc, 6))?.text).toBe("beta");
    const selected = EditorState.create({
      doc,
      selection: { anchor: 0, head: 5 },
    });
    expect(occurrenceTargetAt(selected)).toEqual({
      text: "alpha",
      from: 0,
      to: 5,
      wholeWord: false,
    });
  });

  it("finds selected text throughout the document with substring semantics", () => {
    const state = EditorState.create({
      doc: "cat\nconcatenate\ncat\n",
      selection: { anchor: 0, head: 3 },
    });
    const target = occurrenceTargetAt(state)!;
    expect(collectOccurrenceHits(state.doc, target)).toEqual([
      { from: 7, to: 10 },
      { from: 16, to: 19 },
    ]);
  });

  it("finds multi-line selections in the full document", () => {
    const state = EditorState.create({
      doc: "one\ntwo\ngap\none\ntwo\n",
      selection: { anchor: 0, head: 7 },
    });
    expect(
      collectOccurrenceHits(state.doc, occurrenceTargetAt(state)!),
    ).toEqual([{ from: 12, to: 19 }]);
  });

  it("ignores single-letter tokens", () => {
    expect(occurrenceTargetAt(stateAt("a bc\n", 0))).toBeNull();
  });

  it("skips the caret's own hit and substring matches", () => {
    const state = stateAt("alpha alphabet alpha\n", 0);
    expect(
      collectOccurrenceHits(state.doc, occurrenceTargetAt(state)!),
    ).toEqual([{ from: 15, to: 20 }]);
  });

  it("collects unique lines across the whole document and caps them", () => {
    const lines = ["alpha", ...Array.from({ length: 8 }, () => "x alpha x"), "end"];
    const doc = Text.of(lines);
    const target = { text: "alpha", from: 0, to: 5, wholeWord: true };
    expect(collectOccurrenceLines(doc, target)).toEqual([
      2, 3, 4, 5, 6, 7, 8, 9,
    ]);
    expect(collectOccurrenceLines(doc, target, 3)).toHaveLength(3);
  });

  it("stops at the mark cap", () => {
    const target = "ab";
    const text = Array.from({ length: OCCURRENCE_MAX_MARKS + 20 }, () => target).join(
      " ",
    );
    const doc = Text.of([text]);
    expect(
      collectOccurrenceHits(
        doc,
        { text: target, from: -1, to: -1, wholeWord: true },
        { cap: OCCURRENCE_MAX_MARKS },
      ),
    ).toHaveLength(OCCURRENCE_MAX_MARKS);
  });
});
