import { stepBracket } from "./pretty-bracket.ts";
import type { ClauseState } from "./pretty-clause-state.ts";
import { type BracketRule, PRETTY_TARGET_COLUMNS, type ScanCursor } from "./pretty-scan-state.ts";
import { TreeLexer } from "./pretty-tokens.ts";

const SPACE = 0x20;
const TAB = 0x09;
const COMMA = 0x2c;
const SEMICOLON = 0x3b;
const OPEN_PAREN = 0x28;
const CLOSE_PAREN = 0x29;

/** Words that start a clause. */
const CLAUSES = new Set([
  "select", "from", "where", "group", "order", "having", "limit", "offset", "union", "intersect", "except",
  "values", "set", "insert", "update", "delete", "returning", "with", "window", "join", "left", "right",
  "inner", "outer", "cross", "full", "on", "do",
]);

/** Clause words that a following clause word never joins. */
const STANDALONE = new Set(["select", "from", "where", "set", "values", "returning", "with", "do", "union", "except", "intersect"]);

/** Clauses whose items break at commas when the clause does not fit. */
const LISTS = new Set(["select", "set", "returning", "group", "order", "values"]);
/** Words that complete a list clause's opening phrase. */
const PHRASE_TAILS = new Set(["by", "distinct", "all"]);

/** Parentheses lay out as groups; separators are commas. */
export const SQL_GROUPS: BracketRule = {
  kind: "bracket", data: false, statements: false, afterBlock: null, records: false, parsed: true,
  lexer: () => new TreeLexer(),
};

function wordAt(cursor: ScanCursor, pos: number): { end: number; word: string } | null {
  const end = cursor.text.tokens?.keywordEnd(pos) ?? -1;
  return end > pos ? { end, word: cursor.text.slice(pos, end).toLowerCase() } : null;
}

/** Whether a keyword after `previous` starts a clause, given what follows it. */
function startsClause(cursor: ScanCursor, clause: ClauseState, word: string, end: number): boolean {
  if (!CLAUSES.has(word)) return false;
  if (clause.adjacent && CLAUSES.has(clause.keyword) && !STANDALONE.has(clause.keyword) && word !== "select") return false;
  if (word === "on") return wordAt(cursor, cursor.skipSpaces(end, cursor.state.lineEnd))?.word === "conflict";
  if (word === "set" && clause.adjacent && clause.keyword === "do") return false;
  if (word === "do") return (clause.adjacent && clause.keyword === "conflict") || clause.afterClose;
  return true;
}

/** Width of the clause starting at `pos` up to the next clause at its depth, or more than `limit`. */
function clauseWidth(cursor: ScanCursor, pos: number, limit: number): number {
  const { text, state } = cursor;
  const lineEnd = state.lineEnd;
  let width = 0;
  let depth = 0;
  let previous: ClauseState = { ...state.clause, adjacent: false };
  let first = true;
  while (pos < lineEnd && width <= limit) {
    const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
    if (protectedEnd > pos) {
      const to = Math.min(protectedEnd, lineEnd);
      width += to - pos;
      pos = to;
      previous = { ...previous, adjacent: false };
      continue;
    }
    state.visits += 1;
    const keyword = wordAt(cursor, pos);
    if (keyword) {
      if (!first && depth === 0 && startsClause(cursor, previous, keyword.word, keyword.end)) return width;
      first = false;
      width += keyword.end - pos;
      previous = { ...previous, keyword: keyword.word, adjacent: true };
      pos = keyword.end;
      continue;
    }
    const c = text.at(pos);
    if (c === OPEN_PAREN) depth += 1;
    else if (c === CLOSE_PAREN) {
      if (depth === 0) return width;
      depth -= 1;
    } else if (c === SEMICOLON && depth === 0) return width;
    if (c !== SPACE && c !== TAB) previous = { ...previous, adjacent: false };
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

/**
 * Scans one position of SQL. A clause keyword starts a visual line at its
 * group depth, unless it opens a parenthesis or its source line; a list
 * clause wider than the target breaks after its opening phrase and at each
 * comma at its depth; parentheses fit or expand as groups.
 */
export function stepClause(cursor: ScanCursor, pos: number, end: number): number {
  const { state, text } = cursor;
  const clause = state.clause;
  const keyword = state.laidOut ? wordAt(cursor, pos) : null;
  if (keyword) {
    state.visits += 1;
    const depth = state.frames?.childIndent ?? 0;
    if (startsClause(cursor, clause, keyword.word, keyword.end)) {
      let listDepth = clause.listDepth > depth ? -1 : clause.listDepth === depth ? -1 : clause.listDepth;
      if (!clause.afterOpen) {
        if (state.hasContent) cursor.emitBreak(pos, cursor.columns(depth));
        if (LISTS.has(keyword.word) && clauseWidth(cursor, pos, PRETTY_TARGET_COLUMNS - state.col) > PRETTY_TARGET_COLUMNS - state.col) {
          const phrase = phraseEnd(cursor, keyword.end);
          const next = cursor.skipSpaces(phrase, state.lineEnd);
          if (next < state.lineEnd) {
            state.pending = next;
            state.pendingColumns = cursor.columns(depth + 1);
            listDepth = depth;
          }
        }
      }
      state.clause = { ...clause, keyword: keyword.word, adjacent: true, afterOpen: false, afterClose: false, listDepth };
    } else {
      state.clause = { ...clause, keyword: keyword.word, adjacent: true, afterOpen: false, afterClose: false };
    }
    // A keyword is read whole, even across a pause, so pauses never split one.
    state.col += keyword.end - pos;
    state.hasContent = true;
    return keyword.end;
  }
  const c = text.at(pos);
  if (state.laidOut && c === COMMA && clause.listDepth >= 0 && clause.listDepth === (state.frames?.childIndent ?? 0)) {
    state.visits += 1;
    state.col += 1;
    state.hasContent = true;
    state.clause = { ...clause, adjacent: false, afterOpen: false, afterClose: false };
    const next = cursor.skipSpaces(pos + 1, state.lineEnd);
    if (next < state.lineEnd) {
      state.pending = next;
      state.pendingColumns = cursor.columns(clause.listDepth + 1);
    }
    return pos + 1;
  }
  const next = stepBracket(cursor, SQL_GROUPS, pos, end);
  if (c === SPACE || c === TAB) return next;
  const depth = state.frames?.childIndent ?? 0;
  state.clause = {
    ...clause,
    adjacent: false,
    afterOpen: c === OPEN_PAREN,
    afterClose: c === CLOSE_PAREN,
    listDepth: clause.listDepth > depth ? -1 : clause.listDepth,
  };
  return next;
}
