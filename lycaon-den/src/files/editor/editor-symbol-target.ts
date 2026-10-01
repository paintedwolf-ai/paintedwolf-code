import type { EditorState } from "@codemirror/state";

type EditorWord = {
  symbol: string;
  line: number;
  from: number;
  to: number;
};

export type EditorSymbolTarget =
  | {
      kind: "symbol";
      symbol: string;
      from: number;
      to: number;
      source: "pointer" | "caret" | "selection";
    }
  | { kind: "missing"; reason: "range" | "whitespace" };

type EditorSymbolTargetInput =
  | { source: "pointer"; position: number | null }
  | { source: "caret" };

export function wordAtPos(
  state: EditorState,
  pos: number,
): EditorWord | null {
  if (pos < 0 || pos > state.doc.length) return null;
  const range = state.wordAt(pos);
  if (!range) return null;
  const symbol = state.doc.sliceString(range.from, range.to).trim();
  if (!symbol) return null;
  return {
    symbol,
    line: state.doc.lineAt(Math.min(pos, state.doc.length)).number,
    from: range.from,
    to: range.to,
  };
}

export function editorSymbolTarget(
  state: EditorState,
  input: EditorSymbolTargetInput,
): EditorSymbolTarget {
  const selection = state.selection.main;
  if (!selection.empty) {
    const startWord = state.wordAt(selection.from);
    const endWord = state.wordAt(Math.max(selection.from, selection.to - 1));
    if (
      startWord &&
      endWord &&
      startWord.from === endWord.from &&
      startWord.to === endWord.to &&
      selection.from === startWord.from &&
      selection.to === startWord.to
    ) {
      const symbol = state.doc.sliceString(startWord.from, startWord.to).trim();
      if (symbol) {
        return {
          kind: "symbol",
          symbol,
          from: startWord.from,
          to: startWord.to,
          source: "selection",
        };
      }
    }
    return { kind: "missing", reason: "range" };
  }

  const position = input.source === "pointer" ? input.position : selection.head;
  if (position == null) return { kind: "missing", reason: "whitespace" };
  const word = wordAtPos(state, position);
  if (!word) return { kind: "missing", reason: "whitespace" };
  return {
    kind: "symbol",
    symbol: word.symbol,
    from: word.from,
    to: word.to,
    source: input.source,
  };
}
