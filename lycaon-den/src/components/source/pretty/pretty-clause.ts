import { stepBracket } from "./pretty-bracket.ts";
import { type BracketRule, PRETTY_TARGET_COLUMNS, type ScanCursor } from "./pretty-scan-state.ts";
import { TreeLexer } from "./pretty-tokens.ts";

const SPACE = 0x20;
const TAB = 0x09;
const COMMA = 0x2c;
const SEMICOLON = 0x3b;
const OPEN_PAREN = 0x28;
const CLOSE_PAREN = 0x29;
const DOT = 0x2e;

/** Words that start a clause. */
const CLAUSES = new Set([
  "select", "from", "where", "group", "order", "having", "limit", "offset", "union", "intersect", "except",
  "values", "set", "insert", "update", "delete", "returning", "with", "window", "join", "on", "do",
]);
/** Words that start a join clause when a `join` follows them. */
const JOIN_MODIFIERS = new Set(["left", "right", "inner", "outer", "cross", "full", "natural"]);
/** Words after which a clause word continues the same clause, as `FROM` does in `DELETE FROM`. */
const JOINING = new Set(["group", "order", "insert", "delete", "update", "join", "on", "left", "right", "inner", "outer", "cross", "full", "natural"]);
/** Clauses whose items break at commas when the clause does not fit. */
const LISTS = new Set(["select", "set", "returning", "group", "order", "values"]);
/** Words that complete a list clause's opening phrase. */
const PHRASE_TAILS = new Set(["by", "distinct", "all"]);
/** How far back a word's context is read. */
const LOOKBACK = 64;

function isLetter(code: number): boolean {
  return (code >= 0x61 && code <= 0x7a) || (code >= 0x41 && code <= 0x5a);
}

function isWordCode(code: number): boolean {
  return isLetter(code) || (code >= 0x30 && code <= 0x39) || code === 0x5f || code === 0x24;
}

/** The word starting at `pos`, lower case, when `pos` begins one. */
function wordAt(cursor: ScanCursor, pos: number): { end: number; word: string } | null {
  const { text, state } = cursor;
  if (!isLetter(text.at(pos))) return null;
  if (pos > 0) {
    const before = text.at(pos - 1);
    if (isWordCode(before) || before === DOT) return null;
  }
  let end = pos + 1;
  while (end < state.lineEnd && end - pos < 16 && isWordCode(text.at(end))) end += 1;
  return { end, word: text.slice(pos, end).toLowerCase() };
}

/** The code character and word before `pos`, across whitespace, within the line. */
function previousToken(cursor: ScanCursor, pos: number): { code: number; word: string } {
  const { text } = cursor;
  const lineStart = cursor.options.doc.lineAt(pos).from;
  const floor = Math.max(lineStart, pos - LOOKBACK);
  let at = pos - 1;
  while (at >= floor && (text.at(at) === SPACE || text.at(at) === TAB)) at -= 1;
  if (at < floor) return { code: 0, word: "" };
  const code = text.at(at);
  if (!isWordCode(code)) return { code, word: "" };
  let start = at;
  while (start > floor && isWordCode(text.at(start - 1))) start -= 1;
  return { code, word: text.slice(start, at + 1).toLowerCase() };
}

/** Whether the word at `pos` starts a clause, and where it ends. */
function clauseAt(cursor: ScanCursor, pos: number): { end: number; word: string } | null {
  const found = wordAt(cursor, pos);
  if (!found) return null;
  const { word, end } = found;
  if (!CLAUSES.has(word) && !JOIN_MODIFIERS.has(word)) return null;
  if ((cursor.text.tokens?.protectedEnd(pos) ?? -1) > pos) return null;
  const previous = previousToken(cursor, pos);
  if (JOINING.has(previous.word) && word !== "select") return null;
  let next = cursor.skipSpaces(end, cursor.state.lineEnd);
  if (JOIN_MODIFIERS.has(word)) {
    // A modifier starts the clause only when its `join` follows.
    for (let words = 0; words < 3; words++) {
      const after = wordAt(cursor, next);
      if (!after) return null;
      if (after.word === "join") return found;
      if (!JOIN_MODIFIERS.has(after.word)) return null;
      next = cursor.skipSpaces(after.end, cursor.state.lineEnd);
    }
    return null;
  }
  if (word === "on") return wordAt(cursor, next)?.word === "conflict" ? found : null;
  if (word === "set" && previous.word === "do") return null;
  if (word === "do" && previous.word !== "conflict" && previous.code !== CLOSE_PAREN) return null;
  return found;
}

/** Width of the clause starting at `pos` up to the next clause at its depth, or more than `limit`. */
function clauseWidth(cursor: ScanCursor, pos: number, limit: number): number {
  const { text, state } = cursor;
  const lineEnd = state.lineEnd;
  let width = 0;
  let depth = 0;
  let first = true;
  while (pos < lineEnd && width <= limit) {
    const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
    if (protectedEnd > pos) {
      const to = Math.min(protectedEnd, lineEnd);
      width += to - pos;
      pos = to;
      continue;
    }
    state.visits += 1;
    const clause = depth === 0 ? clauseAt(cursor, pos) : null;
    if (clause) {
      if (!first) return width;
      first = false;
      width += clause.end - pos;
      pos = clause.end;
      continue;
    }
    const c = text.at(pos);
    if (c === OPEN_PAREN) depth += 1;
    else if (c === CLOSE_PAREN) {
      if (depth === 0) return width;
      depth -= 1;
    } else if (c === SEMICOLON && depth === 0) return width;
    width += c === TAB ? cursor.options.tabSize : 1;
    pos += 1;
  }
  return width;
}

/** End of a list clause's opening phrase, such as `GROUP BY` or `SELECT DISTINCT`. */
function phraseEnd(cursor: ScanCursor, end: number): number {
  const next = wordAt(cursor, cursor.skipSpaces(end, cursor.state.lineEnd));
  return next && PHRASE_TAILS.has(next.word) ? next.end : end;
}

/** Parentheses lay out as groups; a clause inside one keeps it from staying flat, and one after it ends its line. */
export const SQL_GROUPS: BracketRule = {
  kind: "bracket", data: false, statements: false, afterBlock: null, records: false, parsed: true,
  lexer: () => new TreeLexer(),
  breaksBefore: (cursor, pos) => clauseAt(cursor, pos) !== null && previousToken(cursor, pos).code !== OPEN_PAREN,
};

/**
 * Scans one position of SQL. A clause starts a visual line at its group
 * depth, unless it opens a parenthesis or its source line; a list clause
 * wider than the target breaks after its opening phrase and at each comma at
 * its depth; parentheses fit or expand as groups.
 */
export function stepClause(cursor: ScanCursor, pos: number, end: number): number {
  const { state, text } = cursor;
  const depth = state.frames?.childIndent ?? 0;
  if (state.listDepth > depth) state.listDepth = -1;
  const clause = state.laidOut ? clauseAt(cursor, pos) : null;
  if (clause) {
    state.visits += 1;
    if (state.listDepth === depth) state.listDepth = -1;
    if (previousToken(cursor, pos).code !== OPEN_PAREN) {
      if (state.hasContent) cursor.emitBreak(pos, cursor.columns(depth));
      const remaining = PRETTY_TARGET_COLUMNS - state.col;
      if (LISTS.has(clause.word) && clauseWidth(cursor, pos, remaining) > remaining) {
        const next = cursor.skipSpaces(phraseEnd(cursor, clause.end), state.lineEnd);
        if (next < state.lineEnd) {
          state.pending = next;
          state.pendingColumns = cursor.columns(depth + 1);
          state.listDepth = depth;
        }
      }
    }
    // A word is read whole, even across a pause, so pauses never split one.
    state.col += clause.end - pos;
    state.hasContent = true;
    return clause.end;
  }
  if (state.laidOut && text.at(pos) === COMMA && state.listDepth === depth && (text.tokens?.protectedEnd(pos) ?? -1) <= pos) {
    state.visits += 1;
    state.col += 1;
    state.hasContent = true;
    const next = cursor.skipSpaces(pos + 1, state.lineEnd);
    if (next < state.lineEnd) {
      state.pending = next;
      state.pendingColumns = cursor.columns(depth + 1);
    }
    return pos + 1;
  }
  return stepBracket(cursor, SQL_GROUPS, pos, end);
}
