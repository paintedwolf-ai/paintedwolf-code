import {
  Annotation,
  EditorSelection,
  StateEffect,
  StateField,
  Transaction,
  type Transaction as CmTransaction,
} from "@codemirror/state";
import type { StateCommand } from "@codemirror/state";

const COALESCE_MS = 300;
const CAP = 100;

/** Suppresses a push when a selection restore comes from undo. */
const fromUndoAnn = Annotation.define<boolean>();

const popSelectionHistory = StateEffect.define<null>();

type HistoryEntry = {
  ranges: readonly { anchor: number; head: number }[];
  mainIndex: number;
};

type HistoryState = {
  entries: HistoryEntry[];
  lastPushAt: number;
};

function snapshot(sel: EditorSelection): HistoryEntry {
  return {
    ranges: sel.ranges.map((r) => ({ anchor: r.anchor, head: r.head })),
    mainIndex: sel.mainIndex,
  };
}

function restore(entry: HistoryEntry): EditorSelection {
  return EditorSelection.create(
    entry.ranges.map((r) => EditorSelection.range(r.anchor, r.head)),
    entry.mainIndex,
  );
}

function empty(): HistoryState {
  return { entries: [], lastPushAt: 0 };
}

/** Selection undo; edits clear it and nearby changes coalesce. */
export const selectionHistoryField = StateField.define<HistoryState>({
  create: empty,
  update(value, tr: CmTransaction): HistoryState {
    if (tr.docChanged) return empty();
    for (const e of tr.effects) {
      if (e.is(popSelectionHistory)) {
        if (value.entries.length === 0) return value;
        return {
          entries: value.entries.slice(0, -1),
          lastPushAt: value.lastPushAt,
        };
      }
    }
    if (tr.annotation(fromUndoAnn)) return value;
    // Only explicit selection transactions enter the stack.
    if (tr.selection == null) return value;
    const prior = tr.startState.selection;
    if (prior.eq(tr.newSelection)) return value;
    const now = tr.annotation(Transaction.time) ?? 0;
    const entry = snapshot(prior);
    if (value.entries.length > 0 && now - value.lastPushAt < COALESCE_MS) {
      return { entries: value.entries, lastPushAt: now };
    }
    const entries = [...value.entries, entry];
    if (entries.length > CAP) entries.splice(0, entries.length - CAP);
    return { entries, lastPushAt: now };
  },
});

/** Restore the last selection. */
export const undoSelectionHistory: StateCommand = ({ state, dispatch }) => {
  const hist = state.field(selectionHistoryField, false);
  if (!hist || hist.entries.length === 0) return false;
  const entry = hist.entries[hist.entries.length - 1]!;
  dispatch(
    state.update({
      selection: restore(entry),
      effects: [popSelectionHistory.of(null)],
      annotations: fromUndoAnn.of(true),
      userEvent: "select",
    }),
  );
  return true;
};
