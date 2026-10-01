import { StateEffect, StateField, type ChangeDesc, type EditorState } from "@codemirror/state";
import type { OverviewTick } from "./overview-ruler-model.ts";

export type OverviewWindowSelection = { clientId: string; anchor: number; head: number; color?: string };
const EMPTY_SELECTIONS: readonly OverviewWindowSelection[] = [];

function mapSelections(selections: readonly OverviewWindowSelection[], changes: ChangeDesc): readonly OverviewWindowSelection[] {
  return selections.map(selection => ({ ...selection,
    anchor: changes.mapPos(selection.anchor), head: changes.mapPos(selection.head),
  }));
}

export const setOverviewWindowSelections = StateEffect.define<readonly OverviewWindowSelection[]>({ map: mapSelections });

/** Presence positions remain available while scrollbar ticks are disabled. */
export const overviewWindowSelections = StateField.define<readonly OverviewWindowSelection[]>({
  create: () => EMPTY_SELECTIONS,
  update(value, transaction) {
    for (const effect of transaction.effects) if (effect.is(setOverviewWindowSelections)) return effect.value;
    return transaction.docChanged ? mapSelections(value, transaction.changes) : value;
  },
});

export function getOverviewWindowSelections(state: EditorState): readonly OverviewWindowSelection[] {
  return state.field(overviewWindowSelections, false) ?? EMPTY_SELECTIONS;
}

export function collectWindowSelectionTicks(state: EditorState): OverviewTick[] {
  return getOverviewWindowSelections(state).map(selection => {
    const from = Math.max(0, Math.min(state.doc.length, selection.anchor, selection.head));
    const to = Math.max(from, Math.min(state.doc.length, Math.max(selection.anchor, selection.head)));
    return { kind: "window", fromLine: state.doc.lineAt(from).number,
      toLine: state.doc.lineAt(to > from ? to - 1 : to).number,
      window: { clientId: selection.clientId, color: selection.color } };
  });
}
