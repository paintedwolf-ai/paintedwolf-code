import { StateEffect, StateField, type ChangeDesc, type EditorState } from "@codemirror/state";
import type { OverviewTick } from "./overview-ruler-model.ts";

/** An agent mark in document positions. A whole file has no position, so it has no tick. */
export type OverviewAgentMark = {
  kind: "agent" | "agent-pending";
  from: number;
  to: number;
  color?: string;
};

const EMPTY_MARKS: readonly OverviewAgentMark[] = [];

function mapMarks(marks: readonly OverviewAgentMark[], changes: ChangeDesc): readonly OverviewAgentMark[] {
  return marks.map(mark => ({ ...mark, from: changes.mapPos(mark.from, -1), to: changes.mapPos(mark.to, 1) }));
}

export const setOverviewAgentMarks = StateEffect.define<readonly OverviewAgentMark[]>({ map: mapMarks });

/** Agent marks remain available while scrollbar ticks are disabled. */
export const overviewAgentMarks = StateField.define<readonly OverviewAgentMark[]>({
  create: () => EMPTY_MARKS,
  update(value, transaction) {
    for (const effect of transaction.effects) if (effect.is(setOverviewAgentMarks)) return effect.value;
    return transaction.docChanged ? mapMarks(value, transaction.changes) : value;
  },
});

export function getOverviewAgentMarks(state: EditorState): readonly OverviewAgentMark[] {
  return state.field(overviewAgentMarks, false) ?? EMPTY_MARKS;
}

export function collectAgentTicks(state: EditorState): OverviewTick[] {
  const { doc } = state;
  return getOverviewAgentMarks(state).map(mark => {
    const from = Math.max(0, Math.min(doc.length, mark.from));
    const to = Math.max(from, Math.min(doc.length, mark.to));
    return { kind: mark.kind, fromLine: doc.lineAt(from).number,
      toLine: doc.lineAt(to > from ? to - 1 : to).number, color: mark.color };
  });
}
