import { describe, expect, it } from "vitest";
import { EditorSelection, EditorState, Transaction } from "@codemirror/state";
import {
  selectionHistoryField,
  undoSelectionHistory,
} from "./selection-history.ts";

describe("selection history", () => {
  it("undoes a coalesced burst to its initial selection", () => {
    let state = EditorState.create({
      doc: "abcd",
      selection: EditorSelection.cursor(0),
      extensions: selectionHistoryField,
    });
    state = state.update({
      selection: EditorSelection.cursor(1),
      annotations: Transaction.time.of(100),
    }).state;
    state = state.update({
      selection: EditorSelection.cursor(2),
      annotations: Transaction.time.of(200),
    }).state;

    expect(undoSelectionHistory({
      state,
      dispatch: (transaction) => {
        state = transaction.state;
      },
    })).toBe(true);
    expect(state.selection.main.head).toBe(0);
  });
});
