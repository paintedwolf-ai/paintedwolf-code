/** Convert leading indentation between spaces and tabs, across the file or the selected lines. */

import {
  EditorSelection,
  type ChangeSpec,
  type StateCommand,
} from "@codemirror/state";
import type { IndentInfo } from "./indent-detect.ts";

export type IndentConvertRange = {
  /** 1-based inclusive. */
  fromLine: number;
  toLine: number;
};

/** Expand leading tabs to spaces using tabWidth, then leave spaces as-is. */
export function leadingWhitespaceToSpaces(
  line: string,
  tabWidth: number,
): string {
  let i = 0;
  let cols = 0;
  let out = "";
  while (i < line.length) {
    const ch = line[i]!;
    if (ch === " ") {
      out += " ";
      cols += 1;
      i += 1;
      continue;
    }
    if (ch === "\t") {
      const add = tabWidth - (cols % tabWidth);
      out += " ".repeat(add);
      cols += add;
      i += 1;
      continue;
    }
    break;
  }
  return out + line.slice(i);
}

/** Collapse leading spaces into tabs of `indentWidth`, leaving a space remainder. */
export function leadingWhitespaceToTabs(
  line: string,
  indentWidth: number,
  tabWidth: number = indentWidth,
): string {
  // First expand any leading tabs so mixed indent is well-defined.
  const expanded = leadingWhitespaceToSpaces(line, tabWidth);
  let spaces = 0;
  while (spaces < expanded.length && expanded[spaces] === " ") spaces++;
  const tabs = Math.floor(spaces / indentWidth);
  const rem = spaces % indentWidth;
  return "\t".repeat(tabs) + " ".repeat(rem) + expanded.slice(spaces);
}

export function convertIndentationText(
  text: string,
  to: IndentInfo,
  fromTabWidth: number,
  range?: IndentConvertRange,
): string {
  const lines = text.split("\n");
  const from = range?.fromLine ?? 1;
  const toLine = range?.toLine ?? lines.length;
  for (let n = from; n <= toLine && n <= lines.length; n++) {
    const idx = n - 1;
    const line = lines[idx]!;
    lines[idx] =
      to.style === "spaces"
        ? leadingWhitespaceToSpaces(line, fromTabWidth)
        : leadingWhitespaceToTabs(line, to.width, fromTabWidth);
  }
  return lines.join("\n");
}

/** Convert indentation in the selection or document. */
export function convertIndentationCommand(
  to: IndentInfo,
  fromTabWidth: number,
): StateCommand {
  return ({ state, dispatch }) => {
    if (state.readOnly) return false;
    const changes: ChangeSpec[] = [];
    const ranges = state.selection.ranges;
    const wholeDoc = ranges.length === 1 && ranges[0]!.empty;

    if (wholeDoc) {
      const text = state.doc.toString();
      const next = convertIndentationText(text, to, fromTabWidth);
      if (next === text) return false;
      dispatch(
        state.update({
          changes: { from: 0, to: state.doc.length, insert: next },
          userEvent: "input",
        }),
      );
      return true;
    }

    for (const range of ranges) {
      const fromLine = state.doc.lineAt(range.from);
      const toPos = range.empty ? range.head : Math.max(range.from, range.to - 1);
      const toLine = state.doc.lineAt(toPos);
      for (let n = fromLine.number; n <= toLine.number; n++) {
        const line = state.doc.line(n);
        const next =
          to.style === "spaces"
            ? leadingWhitespaceToSpaces(line.text, fromTabWidth)
            : leadingWhitespaceToTabs(line.text, to.width, fromTabWidth);
        if (next !== line.text) {
          changes.push({ from: line.from, to: line.to, insert: next });
        }
      }
    }
    if (changes.length === 0) return false;
    dispatch(
      state.update({
        changes,
        selection: EditorSelection.create(ranges),
        userEvent: "input",
      }),
    );
    return true;
  };
}
