import { stepBracket } from "./pretty-bracket.ts";
import { ScriptLexer } from "./pretty-lexers.ts";
import type { BracketRule, ScanCursor } from "./pretty-scan-state.ts";

const LESS = 0x3c;
const SLASH = 0x2f;

export const JSX_SCRIPT_RULE: BracketRule = {
  kind: "bracket",
  data: false,
  statements: true,
  afterBlock: "script",
  records: false,
  parsed: false,
  lexer: () => new ScriptLexer(true),
};

/**
 * Lays out JSX and TSX using `ScriptLexer(true)` for tag, children and `{}`
 * expression frames, adding breaks between adjacent tags on laid-out lines.
 */
export function stepJsx(cursor: ScanCursor, pos: number, end: number): number {
  const { state, text } = cursor;
  const lexer = state.lexer as ScriptLexer;
  const nextPos = stepBracket(cursor, JSX_SCRIPT_RULE, pos, end);
  if (state.laidOut && lexer.lastTagClosed !== null && lexer.jsxDepth > 0) {
    const next = cursor.skipSpaces(nextPos, state.lineEnd);
    if (next < state.lineEnd && text.at(next) === LESS) {
      const isClose = next + 1 < state.lineEnd && text.at(next + 1) === SLASH;
      if (!(isClose && lexer.lastTagClosed === "open")) {
        const baseIndent = state.frames?.childIndent ?? 0;
        const breakDepth = isClose ? lexer.jsxDepth - 1 : lexer.jsxDepth;
        state.pending = next;
        state.pendingColumns = cursor.columns(baseIndent + breakDepth);
      }
    }
  }
  return nextPos;
}
