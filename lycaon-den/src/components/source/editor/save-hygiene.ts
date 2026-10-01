/** Save-time whitespace and line-ending hygiene from EditorConfig properties. */

import type { ChangeSpec, Text } from "@codemirror/state";
import type { EditorConfigProps } from "./editorconfig.ts";
import type { EolKind } from "./eol.ts";

/** The exact edits save hygiene makes and the line ending it selects. */
export type SaveHygieneChanges = {
  /** Each trimmed run and the final newline on their own, never a whole-document replacement. */
  changes: ChangeSpec[];
  eol: EolKind;
};

const TRAILING_WHITESPACE = /[ \t]+$/u;

/** Hygiene as the smallest edits that produce it, so every other character keeps its author. */
export function saveHygieneChanges(doc: Text, eol: EolKind, props: EditorConfigProps | undefined): SaveHygieneChanges {
  const changes: ChangeSpec[] = [];
  if (props?.trimTrailingWhitespace === true) {
    for (let number = 1; number <= doc.lines; number++) {
      const line = doc.line(number);
      const match = TRAILING_WHITESPACE.exec(line.text);
      if (match) changes.push({ from: line.from + match.index, to: line.to });
    }
  }
  const length = doc.length;
  if (props?.insertFinalNewline === true) {
    // Empty files remain empty when final newlines are enabled.
    if (length > 0 && doc.sliceString(length - 1) !== "\n") changes.push({ from: length, insert: "\n" });
  } else if (props?.insertFinalNewline === false) {
    let end = length;
    while (end > 0 && doc.sliceString(end - 1, end) === "\n") end--;
    if (end < length) changes.push({ from: end, to: length });
  }
  return { changes, eol: props?.endOfLine ?? eol };
}
