import { createSignal } from "solid-js";
import { EditorView } from "@codemirror/view";

export type GotoLineTarget = { lineCount: number; currentLine: number; focus(): void; goTo(line: number, column: number): void };
type GotoLineState = { target: GotoLineTarget };

const [state, setState] = createSignal<GotoLineState | null>(null);

export const gotoLineController = {
  state,
  isOpen: () => state() != null,
};

export function openGotoLine(view: EditorView | GotoLineTarget): void {
  const target: GotoLineTarget = view instanceof EditorView ? {
    lineCount: view.state.doc.lines, currentLine: view.state.doc.lineAt(view.state.selection.main.head).number,
    focus: () => view.focus(), goTo: (line, column) => {
      const docLine = view.state.doc.line(Math.min(view.state.doc.lines, line));
      const position = docLine.from + Math.min(column - 1, docLine.length);
      view.dispatch({ selection: { anchor: position }, effects: EditorView.scrollIntoView(position, { y: "center" }), userEvent: "select.gotoLine" });
    },
  } : view;
  setState({ target });
}

export function closeGotoLine(focusEditor = true): void {
  const active = state();
  if (!active) return;
  setState(null);
  if (focusEditor) {
    queueMicrotask(() => active.target.focus());
  }
}

export function submitGotoLine(value: string): boolean {
  const active = state();
  if (!active) return false;
  const match = /^(\d+)(?::(\d+))?$/.exec(value.trim());
  if (!match) return false;
  const line = Number(match[1]), column = Number(match[2] ?? 1);
  if (!Number.isSafeInteger(line) || !Number.isSafeInteger(column) || line < 1 || column < 1) return false;
  active.target.goTo(Math.min(active.target.lineCount, line), column);
  closeGotoLine();
  return true;
}

export function resetGotoLineForTests(): void {
  setState(null);
}
