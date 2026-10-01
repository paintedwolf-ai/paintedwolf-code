import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { collectWindowSelectionTicks, overviewWindowSelections, setOverviewWindowSelections } from "./overview-window-selections.ts";
import { layoutOverviewTicks } from "./overview-ruler-model.ts";

describe("window selection ticks", () => {
  it("covers touched lines for forward and backward selections and single carets", () => {
    const state = EditorState.create({ doc: "a\nb\n\nc\n", extensions: overviewWindowSelections }).update({
      effects: setOverviewWindowSelections.of([
        { clientId: "one", anchor: 0, head: 5, color: "#aa6633" },
        { clientId: "two", anchor: 5, head: 2, color: "#3377aa" },
        { clientId: "two", anchor: 7, head: 7, color: "#3377aa" },
      ]),
    }).state;
    expect(collectWindowSelectionTicks(state)).toEqual([
      { kind: "window", fromLine: 1, toLine: 3, window: { clientId: "one", color: "#aa6633" } },
      { kind: "window", fromLine: 2, toLine: 3, window: { clientId: "two", color: "#3377aa" } },
      { kind: "window", fromLine: 5, toLine: 5, window: { clientId: "two", color: "#3377aa" } },
    ]);
  });

  it("maps selections through text edits and preserves overlapping windows in the layout", () => {
    let state = EditorState.create({ doc: "a\nb\nc", extensions: overviewWindowSelections }).update({
      effects: setOverviewWindowSelections.of([
        { clientId: "one", anchor: 2, head: 5, color: "#aa6633" },
        { clientId: "two", anchor: 2, head: 5, color: "#3377aa" },
      ]),
    }).state;
    state = state.update({ changes: { from: 0, insert: "new\n" } }).state;
    const ticks = collectWindowSelectionTicks(state);
    expect(ticks.map(tick => [tick.fromLine, tick.toLine])).toEqual([[3, 4], [3, 4]]);
    const ops = layoutOverviewTicks(ticks, { height: 100, spanForLine: line => ({ top: line * 5, bottom: line * 5 + 5 }) });
    expect(ops).toHaveLength(2);
    expect(ops.map(op => op.window?.color)).toEqual(["#aa6633", "#3377aa"]);
    expect(ops[0]?.y).toBe(ops[1]?.y);
    state = state.update({ effects: setOverviewWindowSelections.of([]) }).state;
    expect(collectWindowSelectionTicks(state)).toEqual([]);
  });
});
