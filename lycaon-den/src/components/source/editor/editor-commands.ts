import {
  EditorSelection,
  type ChangeSpec,
  type EditorState,
  type StateCommand,
  type Transaction,
  type TransactionSpec,
} from "@codemirror/state";
import { copyLineDown } from "@codemirror/commands";
import {
  SearchCursor,
  selectNextOccurrence,
  selectSelectionMatches,
} from "@codemirror/search";
import { undoSelectionHistory } from "./selection-history.ts";

function apply(
  state: EditorState,
  dispatch: (tr: Transaction) => void,
  spec: TransactionSpec,
): void {
  dispatch(state.update(spec));
}

export const EDITOR_EDITING_COMMAND_IDS = [
  "editor.addCursorAbove",
  "editor.addCursorBelow",
  "editor.splitSelectionIntoLines",
  "editor.selectNextOccurrence",
  "editor.skipAndSelectNextOccurrence",
  "editor.selectAllOccurrences",
  "editor.undoSelection",
  "editor.joinLines",
  "editor.sortSelectedLines",
  "editor.duplicateSelection",
  "editor.toUpperCase",
  "editor.toLowerCase",
] as const;

type EditorEditingCommandId = (typeof EDITOR_EDITING_COMMAND_IDS)[number];

function goalColumnFor(state: import("@codemirror/state").EditorState): number {
  const main = state.selection.main;
  if (main.goalColumn != null) return main.goalColumn;
  const line = state.doc.lineAt(main.head);
  return main.head - line.from;
}

function addCursorVertical(forward: boolean): StateCommand {
  return ({ state, dispatch }) => {
    const main = state.selection.main;
    const line = state.doc.lineAt(main.head);
    if (forward) {
      if (line.number >= state.doc.lines) return false;
    } else if (line.number <= 1) {
      return false;
    }
    const goal = goalColumnFor(state);
    const nextLine = state.doc.line(line.number + (forward ? 1 : -1));
    const col = Math.min(goal, nextLine.length);
    const cursor = EditorSelection.cursor(
      nextLine.from + col,
      undefined,
      undefined,
      goal,
    );
    const ranges = [...state.selection.ranges, cursor];
    apply(state, dispatch, {
      selection: EditorSelection.create(ranges, ranges.length - 1),
      userEvent: "select",
    });
    return true;
  };
}

export const addCursorAbove = addCursorVertical(false);
export const addCursorBelow = addCursorVertical(true);

export const splitSelectionIntoLines: StateCommand = ({ state, dispatch }) => {
  const next: import("@codemirror/state").SelectionRange[] = [];
  let changed = false;
  for (const range of state.selection.ranges) {
    if (range.empty) {
      next.push(range);
      continue;
    }
    const fromLine = state.doc.lineAt(range.from);
    const toPos = range.to > range.from ? range.to - 1 : range.to;
    const toLine = state.doc.lineAt(toPos);
    if (toLine.number - fromLine.number < 1) {
      next.push(range);
      continue;
    }
    changed = true;
    for (let n = fromLine.number; n <= toLine.number; n++) {
      const line = state.doc.line(n);
      next.push(EditorSelection.cursor(line.to));
    }
  }
  if (!changed) return false;
  apply(state, dispatch, {
    selection: EditorSelection.create(next, next.length - 1),
    userEvent: "select",
  });
  return true;
};

export const skipAndSelectNextOccurrence: StateCommand = (target) => {
  const { state, dispatch } = target;
  let main = state.selection.main;
  if (main.empty) {
    const word = state.wordAt(main.head);
    if (!word) return false;
    main = word;
  }
  const query = state.doc.sliceString(main.from, main.to);
  if (!query) return false;
  const others = state.selection.ranges.filter(
    (_, i) => i !== state.selection.mainIndex,
  );
  let nextRange: import("@codemirror/state").SelectionRange | null = null;
  for (const cursor of [
    new SearchCursor(state.doc, query, main.to, state.doc.length),
    new SearchCursor(state.doc, query, 0, main.from),
  ]) {
    for (let match = cursor.next(); !match.done; match = cursor.next()) {
      const candidate = EditorSelection.range(match.value.from, match.value.to);
      if (!others.some((range) =>
        range.from === candidate.from && range.to === candidate.to
      )) {
        nextRange = candidate;
        break;
      }
    }
    if (nextRange) break;
  }
  if (!nextRange) {
    if (others.length === 0) return false;
    apply(state, dispatch, {
      selection: EditorSelection.create(others, Math.min(others.length - 1, 0)),
      userEvent: "select",
    });
    return true;
  }
  const ranges = [...others, nextRange];
  apply(state, dispatch, {
    selection: EditorSelection.create(ranges, ranges.length - 1),
    userEvent: "select",
  });
  return true;
};

export const selectAllOccurrences: StateCommand = selectSelectionMatches;

export const undoSelection: StateCommand = undoSelectionHistory;

function collapseWs(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

export const joinLines: StateCommand = ({ state, dispatch }) => {
  if (state.readOnly) return false;
  const changes: ChangeSpec[] = [];
  const selections: import("@codemirror/state").SelectionRange[] = [];
  let any = false;
  let docOffset = 0;
  for (const range of state.selection.ranges) {
    const startLine = state.doc.lineAt(range.from);
    let endLine = state.doc.lineAt(
      range.empty ? range.head : Math.max(range.from, range.to - 1),
    );
    if (range.empty) {
      if (startLine.number >= state.doc.lines) {
        selections.push(EditorSelection.cursor(range.head + docOffset));
        continue;
      }
      endLine = state.doc.line(startLine.number + 1);
    } else if (endLine.number === startLine.number) {
      if (startLine.number >= state.doc.lines) {
        selections.push(EditorSelection.cursor(range.head + docOffset));
        continue;
      }
      endLine = state.doc.line(startLine.number + 1);
    }
    const from = startLine.from;
    const to = endLine.to;
    const parts: string[] = [];
    for (let n = startLine.number; n <= endLine.number; n++) {
      parts.push(collapseWs(state.doc.line(n).text));
    }
    const joined = parts.filter((p) => p.length > 0).join(" ");
    const firstLen = parts[0]?.length ?? 0;
    const joinAt =
      from +
      docOffset +
      firstLen +
      (parts.length > 1 && firstLen > 0 ? 1 : 0);
    changes.push({ from, to, insert: joined });
    selections.push(EditorSelection.cursor(joinAt));
    docOffset += joined.length - (to - from);
    any = true;
  }
  if (!any) return false;
  apply(state, dispatch, {
    changes,
    selection: EditorSelection.create(selections, selections.length - 1),
    userEvent: "input",
  });
  return true;
};

export const sortSelectedLines: StateCommand = ({ state, dispatch }) => {
  if (state.readOnly) return false;
  const changes: ChangeSpec[] = [];
  let any = false;
  for (const range of state.selection.ranges) {
    if (range.empty) continue;
    const fromLine = state.doc.lineAt(range.from);
    const toLine = state.doc.lineAt(Math.max(range.from, range.to - 1));
    if (toLine.number - fromLine.number < 1) continue;
    const lines: string[] = [];
    for (let n = fromLine.number; n <= toLine.number; n++) {
      lines.push(state.doc.line(n).text);
    }
    const sorted = lines.slice().sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
    if (sorted.every((l, i) => l === lines[i])) continue;
    const insert = sorted.join(state.lineBreak);
    changes.push({ from: fromLine.from, to: toLine.to, insert });
    any = true;
  }
  if (!any) return false;
  apply(state, dispatch, { changes, userEvent: "input" });
  return true;
};

export const duplicateSelection: StateCommand = (target) => {
  const { state, dispatch } = target;
  if (state.readOnly) return false;
  const ranges = state.selection.ranges;
  if (ranges.every((r) => r.empty)) {
    return copyLineDown(target);
  }
  const changes: ChangeSpec[] = [];
  const nextRanges: import("@codemirror/state").SelectionRange[] = [];
  let offset = 0;
  for (const range of ranges) {
    if (range.empty) {
      nextRanges.push(
        EditorSelection.cursor(range.head + offset),
      );
      continue;
    }
    const text = state.doc.sliceString(range.from, range.to);
    const insertAt = range.to + offset;
    changes.push({ from: range.to, to: range.to, insert: text });
    nextRanges.push(
      EditorSelection.range(insertAt, insertAt + text.length),
    );
    offset += text.length;
  }
  if (changes.length === 0) return false;
  apply(state, dispatch, {
    changes,
    selection: EditorSelection.create(nextRanges, nextRanges.length - 1),
    userEvent: "input",
  });
  return true;
};

function wordAround(
  state: import("@codemirror/state").EditorState,
  pos: number,
): { from: number; to: number } | null {
  const line = state.doc.lineAt(pos);
  const text = line.text;
  const offset = pos - line.from;
  // Scan both sides so a caret after a word still selects that word.
  let start = offset;
  let end = offset;
  while (start > 0 && /\w/.test(text[start - 1]!)) start--;
  while (end < text.length && /\w/.test(text[end]!)) end++;
  if (start === end) return null;
  return { from: line.from + start, to: line.from + end };
}

function mapCase(upper: boolean): StateCommand {
  return ({ state, dispatch }) => {
    if (state.readOnly) return false;
    const changes: ChangeSpec[] = [];
    const nextRanges: import("@codemirror/state").SelectionRange[] = [];
    let any = false;
    let offset = 0;
    for (const range of state.selection.ranges) {
      let from = range.from;
      let to = range.to;
      if (range.empty) {
        const word = wordAround(state, range.head);
        if (!word) {
          nextRanges.push(range);
          continue;
        }
        from = word.from;
        to = word.to;
      }
      const text = state.doc.sliceString(from, to);
      const mapped = upper ? text.toUpperCase() : text.toLowerCase();
      const mappedFrom = from + offset;
      const mappedTo = mappedFrom + mapped.length;
      if (mapped === text) {
        nextRanges.push(
          range.anchor > range.head
            ? EditorSelection.range(mappedTo, mappedFrom)
            : EditorSelection.range(mappedFrom, mappedTo),
        );
        continue;
      }
      changes.push({ from, to, insert: mapped });
      nextRanges.push(
        range.anchor > range.head
          ? EditorSelection.range(mappedTo, mappedFrom)
          : EditorSelection.range(mappedFrom, mappedTo),
      );
      offset += mapped.length - (to - from);
      any = true;
    }
    if (!any) return false;
    apply(state, dispatch, {
      changes,
      selection: EditorSelection.create(nextRanges, nextRanges.length - 1),
      userEvent: "input",
    });
    return true;
  };
}

export const toUpperCase = mapCase(true);
export const toLowerCase = mapCase(false);

export const EDITOR_COMMAND_IMPL: Record<
  EditorEditingCommandId,
  StateCommand
> = {
  "editor.addCursorAbove": addCursorAbove,
  "editor.addCursorBelow": addCursorBelow,
  "editor.splitSelectionIntoLines": splitSelectionIntoLines,
  "editor.selectNextOccurrence": selectNextOccurrence,
  "editor.skipAndSelectNextOccurrence": skipAndSelectNextOccurrence,
  "editor.selectAllOccurrences": selectAllOccurrences,
  "editor.undoSelection": undoSelection,
  "editor.joinLines": joinLines,
  "editor.sortSelectedLines": sortSelectedLines,
  "editor.duplicateSelection": duplicateSelection,
  "editor.toUpperCase": toUpperCase,
  "editor.toLowerCase": toLowerCase,
};
