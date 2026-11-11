import { SQL_GROUPS } from "./pretty-clause.ts";
import type { PrettyLexer } from "./pretty-lexers.ts";
import { PRETTY_TRIGGER_COLUMNS, type PrettyRule } from "./pretty-scan-state.ts";
import type { TextWindow } from "./pretty-text-window.ts";

const TAB = 0x09;

function rawWidthExceeds(text: TextWindow, from: number, to: number, tabSize: number, limit: number): boolean {
  if (to - from > limit) return true;
  if ((to - from) * tabSize <= limit) return false;
  let width = 0;
  for (let pos = from; pos < to; pos++) {
    width += text.at(pos) === TAB ? tabSize : 1;
    if (width > limit) return true;
  }
  return false;
}

/** Whether a line is wider than the trigger used when no width is declared. */
export function exceedsTrigger(text: TextWindow, from: number, to: number, tabSize: number): boolean {
  return rawWidthExceeds(text, from, to, tabSize, PRETTY_TRIGGER_COLUMNS);
}

function lexerFor(rule: PrettyRule): PrettyLexer | null {
  if (rule.kind === "bracket") return rule.lexer();
  if (rule.kind === "clause") return SQL_GROUPS.lexer();
  return null;
}

/**
 * Whether a source line is laid out. Without a declared width, a line wider
 * than the trigger is. With one, a line is when its code alone is wider:
 * formatters leave a line long only for strings and comments they cannot
 * break. Each line reads from a fresh lexer, so the answer depends on the
 * line's text alone; breaks still follow the scanner's own lexical state.
 */
export function exceedsLineWidth(
  text: TextWindow, from: number, to: number, tabSize: number, rule: PrettyRule, lineWidth: number | undefined,
): boolean {
  if (lineWidth === undefined) return exceedsTrigger(text, from, to, tabSize);
  if (!rawWidthExceeds(text, from, to, tabSize, lineWidth)) return false;
  const lexer = lexerFor(rule);
  if (!lexer) return true;
  let width = 0;
  for (let pos = from; pos < to;) {
    const skipped = lexer.skip(text, pos, to, to);
    if (skipped > pos) {
      pos = skipped;
      continue;
    }
    lexer.noteCode(text, pos);
    width += text.at(pos) === TAB ? tabSize : 1;
    if (width > lineWidth) return true;
    pos += 1;
  }
  return false;
}
