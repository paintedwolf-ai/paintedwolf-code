import { stepBracket } from "./pretty-bracket.ts";
import { PRETTY_MAX_DEPTH, type BracketRule, type ScanCursor, targetColumns } from "./pretty-scan-state.ts";
import { TreeLexer } from "./pretty-tokens.ts";

const SPACE = 0x20;
const TAB = 0x09;
const COMMA = 0x2c;
const SEMICOLON = 0x3b;
const OPEN_PAREN = 0x28;
const CLOSE_PAREN = 0x29;
const DOT = 0x2e;

export interface CaseFrame {
  readonly depth: number;
  readonly expanded: boolean;
  readonly stackDepth: number;
  readonly parent: CaseFrame | null;
}

export interface ClauseState {
  readonly listDepth: number;
  readonly predicateDepth: number;
  readonly betweenPending: boolean;
  readonly cases: CaseFrame | null;
}

export const INITIAL_CLAUSE_STATE: ClauseState = {
  listDepth: -1,
  predicateDepth: -1,
  betweenPending: false,
  cases: null,
};

function caseFramesEqual(a: CaseFrame | null, b: CaseFrame | null): boolean {
  while (a && b) {
    if (a === b) return true;
    if (a.depth !== b.depth || a.expanded !== b.expanded) return false;
    a = a.parent;
    b = b.parent;
  }
  return a === b;
}

export function clauseStatesEqual(a: ClauseState, b: ClauseState): boolean {
  return (
    a.listDepth === b.listDepth &&
    a.predicateDepth === b.predicateDepth &&
    a.betweenPending === b.betweenPending &&
    caseFramesEqual(a.cases, b.cases)
  );
}

/** Words that start a clause. */
const CLAUSES = new Set([
  "select", "from", "where", "group", "order", "having", "limit", "offset", "union", "intersect", "except",
  "values", "set", "insert", "update", "delete", "returning", "with", "window", "join", "on", "do",
]);
/** Words that start a join clause when a `join` follows them. */
const JOIN_MODIFIERS = new Set(["left", "right", "inner", "outer", "cross", "full", "natural"]);
/** Words after which a clause word continues the same clause, as `FROM` does in `DELETE FROM`. */
const JOINING = new Set([
  "group", "order", "insert", "delete", "update", "join", "on", "left", "right", "inner", "outer", "cross", "full", "natural",
]);
/** Clauses whose items break at commas when the clause does not fit. */
const LISTS = new Set(["select", "set", "returning", "group", "order", "values"]);
/** Clauses whose predicates split at AND / OR when the clause does not fit. */
const PREDICATES = new Set(["where", "having", "on"]);
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
  if (word === "on") {
    const after = wordAt(cursor, next);
    if (after && (after.word === "delete" || after.word === "update")) return null;
    return found;
  }
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

/** Width of a CASE ... END expression from `pos` up to its matching `END`, or more than `limit`. */
function caseWidth(cursor: ScanCursor, pos: number, limit: number): number {
  const { text, state } = cursor;
  const lineEnd = state.lineEnd;
  let width = 0;
  let parenDepth = 0;
  let caseDepth = 0;
  while (pos < lineEnd && width <= limit) {
    const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
    if (protectedEnd > pos) {
      const to = Math.min(protectedEnd, lineEnd);
      width += to - pos;
      pos = to;
      continue;
    }
    state.visits += 1;
    const w = wordAt(cursor, pos);
    if (w) {
      if (w.word === "case") caseDepth += 1;
      else if (w.word === "end") {
        width += w.end - pos;
        caseDepth -= 1;
        if (caseDepth <= 0) return width;
        pos = w.end;
        continue;
      }
      width += w.end - pos;
      pos = w.end;
      continue;
    }
    const c = text.at(pos);
    if (c === OPEN_PAREN) parenDepth += 1;
    else if (c === CLOSE_PAREN) {
      if (parenDepth === 0) return width;
      parenDepth -= 1;
    } else if (c === SEMICOLON && parenDepth === 0) return width;
    width += c === TAB ? cursor.options.tabSize : 1;
    pos += 1;
  }
  return width;
}

/** Width of the next predicate up to the next AND/OR/clause/closer at depth 0. */
function nextPredicateWidth(cursor: ScanCursor, pos: number, limit: number): number {
  const { text, state } = cursor;
  const lineEnd = state.lineEnd;
  let width = 0;
  let depth = 0;
  let first = true;
  let between = false;
  while (pos < lineEnd && width <= limit) {
    const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
    if (protectedEnd > pos) {
      const to = Math.min(protectedEnd, lineEnd);
      width += to - pos;
      pos = to;
      continue;
    }
    state.visits += 1;
    if (depth === 0) {
      if (!first && clauseAt(cursor, pos)) return width;
      const w = wordAt(cursor, pos);
      if (w) {
        if (w.word === "between") between = true;
        else if (!first && (w.word === "and" || w.word === "or")) {
          if (w.word === "and" && between) between = false;
          else return width;
        }
        first = false;
        width += w.end - pos;
        pos = w.end;
        continue;
      }
    }
    first = false;
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

/**
 * Scans one position of SQL. A clause starts a visual line at its group
 * depth, unless it opens a parenthesis or its source line; a list clause
 * wider than the target breaks after its opening phrase and at each comma at
 * its depth; predicates break before top-level AND/OR; CASE breaks before WHEN/ELSE/END.
 */
export function stepClause(cursor: ScanCursor, pos: number, end: number): number {
  const { state, text } = cursor;
  const depth = state.frames?.childIndent ?? 0;
  let cs = state.clauseState;
  if (cs.listDepth > depth || cs.predicateDepth > depth) {
    cs = {
      listDepth: cs.listDepth > depth ? -1 : cs.listDepth,
      predicateDepth: cs.predicateDepth > depth ? -1 : cs.predicateDepth,
      betweenPending: cs.predicateDepth > depth ? false : cs.betweenPending,
      cases: cs.cases,
    };
    state.clauseState = cs;
  }
  const clause = state.laidOut ? clauseAt(cursor, pos) : null;
  if (clause) {
    state.visits += 1;
    let listDepth = cs.listDepth === depth ? -1 : cs.listDepth;
    let predicateDepth = cs.predicateDepth === depth ? -1 : cs.predicateDepth;
    let betweenPending = cs.predicateDepth === depth ? false : cs.betweenPending;
    const isOnConflict =
      clause.word === "on" &&
      wordAt(cursor, cursor.skipSpaces(clause.end, state.lineEnd))?.word === "conflict";
    const clauseIndent = clause.word === "on" && !isOnConflict ? depth + 1 : depth;
    if (previousToken(cursor, pos).code !== OPEN_PAREN) {
      if (state.hasContent) cursor.emitBreak(pos, cursor.columns(clauseIndent));
      const remaining = targetColumns(cursor.options.lineWidth) - state.col;
      if (LISTS.has(clause.word) && clauseWidth(cursor, pos, remaining) > remaining) {
        const next = cursor.skipSpaces(phraseEnd(cursor, clause.end), state.lineEnd);
        if (next < state.lineEnd) {
          state.pending = next;
          state.pendingColumns = cursor.columns(clauseIndent + 1);
          listDepth = depth;
        }
      } else if (PREDICATES.has(clause.word) && !isOnConflict) {
        predicateDepth = depth;
        betweenPending = false;
      }
    }
    state.clauseState = { listDepth, predicateDepth, betweenPending, cases: cs.cases };
    // A word is read whole, even across a pause, so pauses never split one.
    state.col += clause.end - pos;
    state.hasContent = true;
    return clause.end;
  }
  if (state.laidOut && (text.tokens?.protectedEnd(pos) ?? -1) <= pos) {
    const word = wordAt(cursor, pos);
    if (word) {
      const target = targetColumns(cursor.options.lineWidth);
      if (word.word === "case") {
        state.visits += 1;
        const stackDepth = (cs.cases?.stackDepth ?? 0) + 1;
        if (stackDepth > PRETTY_MAX_DEPTH) {
          state.failure = "depth";
          return word.end;
        }
        const remaining = target - state.col;
        const expanded = caseWidth(cursor, pos, remaining) > remaining;
        state.clauseState = {
          ...cs,
          cases: { depth, expanded, stackDepth, parent: cs.cases },
        };
        state.col += word.end - pos;
        state.hasContent = true;
        return word.end;
      }
      const topCase = cs.cases;
      if (topCase && topCase.depth === depth) {
        if (word.word === "when" || word.word === "else") {
          state.visits += 1;
          if (topCase.expanded && state.hasContent) cursor.emitBreak(pos, cursor.columns(depth + 1));
          state.col += word.end - pos;
          state.hasContent = true;
          return word.end;
        }
        if (word.word === "end") {
          state.visits += 1;
          if (topCase.expanded && state.hasContent) cursor.emitBreak(pos, cursor.columns(depth));
          state.clauseState = { ...cs, cases: topCase.parent };
          state.col += word.end - pos;
          state.hasContent = true;
          return word.end;
        }
      }
      if (cs.predicateDepth === depth) {
        if (word.word === "between") {
          state.clauseState = { ...cs, betweenPending: true };
        } else if (word.word === "and" || word.word === "or") {
          state.visits += 1;
          if (word.word === "and" && cs.betweenPending) {
            state.clauseState = { ...cs, betweenPending: false };
          } else if (state.col + nextPredicateWidth(cursor, pos, target - state.col) > target && state.hasContent) {
            cursor.emitBreak(pos, cursor.columns(depth + 1));
          }
          state.col += word.end - pos;
          state.hasContent = true;
          return word.end;
        }
      }
    }
  }
  if (state.laidOut && text.at(pos) === COMMA && cs.listDepth === depth && (text.tokens?.protectedEnd(pos) ?? -1) <= pos) {
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
