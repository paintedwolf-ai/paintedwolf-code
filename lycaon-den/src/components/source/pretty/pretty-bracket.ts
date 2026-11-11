import {
  type BracketRule,
  PRETTY_MAX_DEPTH,
  PRETTY_TARGET_COLUMNS,
  type ScanCursor,
  type ScanState,
} from "./pretty-scan-state.ts";

const SPACE = 0x20;
const TAB = 0x09;
const COMMA = 0x2c;
const SEMICOLON = 0x3b;
const OPEN_BRACE = 0x7b;
const CLOSE_BRACE = 0x7d;
const OPEN_BRACKET = 0x5b;
const CLOSE_BRACKET = 0x5d;
const OPEN_PAREN = 0x28;
const CLOSE_PAREN = 0x29;
const SLASH = 0x2f;
const QUOTE = 0x22;
const APOS = 0x27;
const BACKTICK = 0x60;

/** Words after `}` that continue a statement rather than start one. */
const CONTINUATIONS = new Set([
  "else", "catch", "finally", "while", "from", "as", "satisfies", "of", "in", "instanceof",
]);

function closerOf(opener: number): number {
  return opener === OPEN_BRACE ? CLOSE_BRACE : opener === OPEN_BRACKET ? CLOSE_BRACKET : CLOSE_PAREN;
}

function openerOf(closer: number): number {
  return closer === CLOSE_BRACE ? OPEN_BRACE : closer === CLOSE_BRACKET ? OPEN_BRACKET : OPEN_PAREN;
}

function isOpener(code: number): boolean {
  return code === OPEN_BRACE || code === OPEN_BRACKET || code === OPEN_PAREN;
}

function isCloser(code: number): boolean {
  return code === CLOSE_BRACE || code === CLOSE_BRACKET || code === CLOSE_PAREN;
}

function isWordStart(code: number): boolean {
  return (code >= 0x61 && code <= 0x7a) || (code >= 0x41 && code <= 0x5a) || code === 0x5f || code === 0x24;
}

function isWordCode(code: number): boolean {
  return isWordStart(code) || (code >= 0x30 && code <= 0x39);
}

/** A group opened on the current source line. */
export class Frame {
  constructor(
    readonly kind: number,
    readonly expanded: boolean,
    readonly block: boolean,
    readonly childIndent: number,
    readonly depth: number,
    readonly parent: Frame | null,
  ) {}
}

/** A group left open by an earlier source line. */
export class Container {
  constructor(readonly kind: number, readonly depth: number, readonly parent: Container | null) {}
}

export function framesEqual(a: Frame | null, b: Frame | null): boolean {
  while (a && b) {
    if (a === b) return true;
    if (a.kind !== b.kind || a.expanded !== b.expanded || a.block !== b.block || a.childIndent !== b.childIndent) return false;
    a = a.parent;
    b = b.parent;
  }
  return a === b;
}

export function containersEqual(a: Container | null, b: Container | null): boolean {
  while (a && b) {
    if (a === b) return true;
    if (a.kind !== b.kind) return false;
    a = a.parent;
    b = b.parent;
  }
  return a === b;
}

/** Groups still open at a line end become containers for the lines after it. */
export function closeBracketLine(state: ScanState): void {
  const open: Frame[] = [];
  for (let frame = state.frames; frame; frame = frame.parent) open.push(frame);
  for (let i = open.length - 1; i >= 0; i--) {
    state.containers = new Container(open[i]!.kind, (state.containers?.depth ?? 0) + 1, state.containers);
  }
  state.frames = null;
  state.lexer!.endLine();
}

/** Scans one position under a bracket rule; returns the next position. */
export function stepBracket(cursor: ScanCursor, rule: BracketRule, pos: number, end: number): number {
  const { text, state } = cursor;
  const lexer = state.lexer!;
  const skipped = lexer.skip(text, pos, end, state.lineEnd);
  if (skipped > pos) {
    state.visits += skipped - pos;
    state.col += skipped - pos;
    state.hasContent = true;
    return skipped;
  }
  state.visits += 1;
  const c = text.at(pos);
  lexer.noteCode(text, pos);
  if (c === SPACE) {
    state.col += 1;
    return pos + 1;
  }
  if (c === TAB) {
    state.col += cursor.options.tabSize;
    return pos + 1;
  }
  if (isOpener(c)) open(cursor, rule, c, pos);
  else if (isCloser(c)) close(cursor, rule, c, pos);
  else if (c === COMMA || (c === SEMICOLON && rule.statements)) separate(cursor, c, pos);
  else state.col += 1;
  state.hasContent = true;
  return pos + 1;
}

function open(cursor: ScanCursor, rule: BracketRule, c: number, pos: number): void {
  const { state, text } = cursor;
  const depth = (state.frames?.depth ?? state.containers?.depth ?? 0) + 1;
  if (depth > PRETTY_MAX_DEPTH) {
    state.failure = "depth";
    return;
  }
  const childIndent = (state.frames?.childIndent ?? 0) + 1;
  let expanded = false;
  let block = false;
  if (state.laidOut) {
    const next = cursor.skipSpaces(pos + 1, state.lineEnd);
    const nonEmpty = next < state.lineEnd && text.at(next) !== closerOf(c);
    if (nonEmpty) {
      expanded = rule.data && c !== OPEN_PAREN ? true : !fits(cursor, rule, pos);
      if (expanded && c === OPEN_BRACE && rule.statements) block = blockFact(cursor, pos);
      if (expanded) {
        state.pending = next;
        state.pendingColumns = cursor.columns(childIndent);
      }
    }
  }
  state.frames = new Frame(c, expanded, block, childIndent, depth, state.frames);
  state.col += 1;
}

function close(cursor: ScanCursor, rule: BracketRule, c: number, pos: number): void {
  const { state } = cursor;
  const top = state.frames;
  if (top && top.kind === openerOf(c)) {
    if (state.laidOut && top.expanded) cursor.emitBreak(pos, cursor.columns(top.childIndent - 1));
    state.frames = top.parent;
  } else if (!top && state.containers && state.containers.kind === openerOf(c)) {
    if (state.laidOut && state.hasContent) cursor.emitBreak(pos, state.leadColumns);
    state.containers = state.containers.parent;
  } else {
    state.col += 1;
    return;
  }
  state.col += 1;
  if (state.laidOut && c === CLOSE_BRACE && breaksAfterBlock(cursor, rule, pos)) {
    state.pending = cursor.skipSpaces(pos + 1, state.lineEnd);
    state.pendingColumns = cursor.columns(state.frames?.childIndent ?? 0);
  }
}

function separate(cursor: ScanCursor, c: number, pos: number): void {
  const { state, text } = cursor;
  state.col += 1;
  if (!state.laidOut) return;
  const group = state.frames;
  const next = cursor.skipSpaces(pos + 1, state.lineEnd);
  if (next >= state.lineEnd || isCloser(text.at(next))) return;
  if (!separatorBreaks(state, c, group)) return;
  state.pending = next;
  state.pendingColumns = cursor.columns(group?.childIndent ?? 0);
}

function separatorBreaks(state: ScanState, c: number, group: Frame | null): boolean {
  if (group) {
    if (!group.expanded) return false;
    return c !== COMMA || group.kind !== OPEN_BRACE || !group.block;
  }
  return c === SEMICOLON || (state.containers?.kind ?? 0) !== 0;
}

/** Whether the `}` at `pos` is followed by a token that starts a new visual line. */
function breaksAfterBlock(cursor: ScanCursor, rule: BracketRule, pos: number): boolean {
  if (!rule.afterBlock) return false;
  const { text, state } = cursor;
  const next = cursor.skipSpaces(pos + 1, state.lineEnd);
  if (next >= state.lineEnd) return false;
  const c = text.at(next);
  if (c === QUOTE || c === APOS || c === BACKTICK || c === SLASH) return false;
  if (rule.afterBlock === "style") return c !== CLOSE_BRACE && c !== COMMA && c !== SEMICOLON;
  if (!isWordStart(c)) return false;
  let wordEnd = next + 1;
  while (wordEnd < state.lineEnd && wordEnd - next < 12 && isWordCode(text.at(wordEnd))) wordEnd += 1;
  return !CONTINUATIONS.has(text.slice(next, wordEnd));
}

function nonEmptyAt(cursor: ScanCursor, pos: number): boolean {
  const next = cursor.skipSpaces(pos + 1, cursor.state.lineEnd);
  return next < cursor.state.lineEnd && cursor.text.at(next) !== closerOf(cursor.text.at(pos));
}

/**
 * Whether the group opening at `open` stays flat: its visual line, up to the
 * next place the enclosing layout breaks, fits the target width, and nothing
 * inside it breaks on its own (a data container, or a break after a block).
 */
function fits(cursor: ScanCursor, rule: BracketRule, openPos: number): boolean {
  const { text, state } = cursor;
  const lineEnd = state.lineEnd;
  const tabSize = cursor.options.tabSize;
  const remaining = PRETTY_TARGET_COLUMNS - state.col;
  if (remaining <= 0) return false;
  const lexer = state.lexer!.copy();
  const stack: number[] = [];
  let width = 0;
  let pos = openPos;
  for (;;) {
    if (pos >= lineEnd) return false;
    if (pos > openPos && rule.breaksBefore?.(cursor, pos)) return false;
    const skipped = lexer.skip(text, pos, Math.min(lineEnd, pos + remaining - width + 1), lineEnd);
    if (skipped > pos) {
      state.visits += skipped - pos;
      width += skipped - pos;
      if (width > remaining) return false;
      pos = skipped;
      continue;
    }
    state.visits += 1;
    const c = text.at(pos);
    lexer.noteCode(text, pos);
    width += c === TAB ? tabSize : 1;
    if (width > remaining) return false;
    if (isOpener(c)) {
      stack.push(c);
      if (rule.data && c !== OPEN_PAREN && stack.length > 1 && nonEmptyAt(cursor, pos)) return false;
    } else if (isCloser(c) && stack[stack.length - 1] === openerOf(c)) {
      stack.pop();
      if (stack.length === 0) {
        pos += 1;
        break;
      }
      if (c === CLOSE_BRACE && breaksAfterBlock(cursor, rule, pos)) return false;
    }
    pos += 1;
  }
  if (text.at(openPos) === OPEN_BRACE && breaksAfterBlock(cursor, rule, pos - 1)) return true;
  const parent = state.frames;
  for (;;) {
    if (pos >= lineEnd) return true;
    if (rule.breaksBefore?.(cursor, pos)) return true;
    const skipped = lexer.skip(text, pos, Math.min(lineEnd, pos + remaining - width + 1), lineEnd);
    if (skipped > pos) {
      state.visits += skipped - pos;
      width += skipped - pos;
      if (width > remaining) return false;
      pos = skipped;
      continue;
    }
    state.visits += 1;
    const c = text.at(pos);
    lexer.noteCode(text, pos);
    if (c === COMMA || (c === SEMICOLON && rule.statements)) {
      const next = cursor.skipSpaces(pos + 1, lineEnd);
      if (next >= lineEnd) return width + 1 <= remaining;
      if (!isCloser(text.at(next)) && separatorBreaks(state, c, parent)) return width + (next - pos) <= remaining;
    } else if (isOpener(c)) {
      if (width + 1 > remaining) return false;
      if (nonEmptyAt(cursor, pos)) return true;
    } else if (isCloser(c)) {
      const closesParent = parent ? parent.kind === openerOf(c) : state.containers?.kind === openerOf(c);
      if (closesParent) return true;
    }
    width += c === TAB ? tabSize : 1;
    if (width > remaining) return false;
    pos += 1;
  }
}

/** Whether the `{}` opening at `open` directly holds a `;`, from memory or by reading to its end. */
function blockFact(cursor: ScanCursor, openPos: number): boolean {
  const { text, state } = cursor;
  const facts = cursor.options.facts;
  const known = facts.get(openPos);
  if (known !== undefined) return known;
  const lineEnd = state.lineEnd;
  const lexer = state.lexer!.copy();
  const stack: Array<{ kind: number; open: number; semicolon: boolean }> = [{ kind: OPEN_BRACE, open: openPos, semicolon: false }];
  let pos = openPos + 1;
  while (pos < lineEnd && stack.length > 0) {
    const skipped = lexer.skip(text, pos, lineEnd, lineEnd);
    if (skipped > pos) {
      state.visits += skipped - pos;
      pos = skipped;
      continue;
    }
    state.visits += 1;
    const c = text.at(pos);
    lexer.noteCode(text, pos);
    if (isOpener(c)) {
      stack.push({ kind: c, open: pos, semicolon: false });
    } else if (isCloser(c) && stack[stack.length - 1]!.kind === openerOf(c)) {
      const frame = stack.pop();
      if (frame?.kind === OPEN_BRACE) facts.set(frame.open, pos + 1, frame.semicolon);
    } else if (c === SEMICOLON) {
      stack[stack.length - 1]!.semicolon = true;
    }
    pos += 1;
  }
  // A group still open at the line end lays out as a container would.
  for (const frame of stack) if (frame.kind === OPEN_BRACE) facts.set(frame.open, lineEnd, false);
  return facts.get(openPos) ?? false;
}
