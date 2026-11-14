import { PRETTY_MAX_DEPTH, type ScanCursor, type ScanState, targetColumns } from "./pretty-scan-state.ts";

const SPACE = 0x20;
const TAB = 0x09;
const COMMA = 0x2c;
const OPEN_PAREN = 0x28;
const CLOSE_PAREN = 0x29;
const OPEN_BRACKET = 0x5b;
const CLOSE_BRACKET = 0x5d;
const OPEN_BRACE = 0x7b;
const CLOSE_BRACE = 0x7d;
const BACKSLASH = 0x5c;
const HASH = 0x23;
const QUOTE = 0x27;
const BACKTICK = 0x60;
const TILDE = 0x7e;
const AT = 0x40;
const CARET = 0x5e;

const HEADER_FORMS = /^(?:def\S*|ns|fn|let\S*|define\S*|lambda)$/;

export class SexpFrame {
  constructor(
    readonly kind: number,
    readonly expanded: boolean,
    readonly isMap: boolean,
    readonly childIndent: number,
    readonly depth: number,
    readonly formCount: number,
    readonly keepSecond: boolean,
    readonly parent: SexpFrame | null,
  ) {}
}

export function sexpFramesEqual(a: SexpFrame | null, b: SexpFrame | null): boolean {
  while (a && b) {
    if (a === b) return true;
    if (
      a.kind !== b.kind ||
      a.expanded !== b.expanded ||
      a.isMap !== b.isMap ||
      a.childIndent !== b.childIndent ||
      a.formCount !== b.formCount ||
      a.keepSecond !== b.keepSecond
    ) {
      return false;
    }
    a = a.parent;
    b = b.parent;
  }
  return a === b;
}

export function closeSexpLine(state: ScanState): void {
  state.sexpFrames = null;
}

function isDelimiter(c: number): boolean {
  return (
    c === OPEN_PAREN ||
    c === CLOSE_PAREN ||
    c === OPEN_BRACKET ||
    c === CLOSE_BRACKET ||
    c === OPEN_BRACE ||
    c === CLOSE_BRACE
  );
}

function isCloser(c: number): boolean {
  return c === CLOSE_PAREN || c === CLOSE_BRACKET || c === CLOSE_BRACE;
}

function isWhitespace(c: number): boolean {
  return c === SPACE || c === TAB || c === COMMA;
}

function openerOf(closer: number): number {
  return closer === CLOSE_PAREN ? OPEN_PAREN : closer === CLOSE_BRACKET ? OPEN_BRACKET : OPEN_BRACE;
}

function closerOf(opener: number): number {
  return opener === OPEN_PAREN ? CLOSE_PAREN : opener === OPEN_BRACKET ? CLOSE_BRACKET : CLOSE_BRACE;
}

function isPrefixChar(c: number): boolean {
  return c === QUOTE || c === BACKTICK || c === TILDE || c === AT || c === CARET || c === HASH;
}

/** Whether a S-expression starting at `openPos` fits in the remaining columns on one line. */
function sexpFits(cursor: ScanCursor, openPos: number): boolean {
  const { text, state } = cursor;
  const lineEnd = state.lineEnd;
  const remaining = targetColumns(cursor.options.lineWidth) - state.col;
  if (remaining <= 0) return false;
  const stack: number[] = [];
  let width = 0;
  let pos = openPos;
  while (pos < lineEnd && width <= remaining) {
    const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
    if (protectedEnd > pos) {
      const to = Math.min(protectedEnd, lineEnd);
      state.visits += to - pos;
      width += to - pos;
      pos = to;
      continue;
    }
    state.visits += 1;
    const c = text.at(pos);
    if (c === BACKSLASH && pos + 1 < lineEnd) {
      width += 2;
      pos += 2;
      continue;
    }
    if (c === OPEN_PAREN || c === OPEN_BRACKET || c === OPEN_BRACE) {
      stack.push(closerOf(c));
    } else if (c === CLOSE_PAREN || c === CLOSE_BRACKET || c === CLOSE_BRACE) {
      if (stack.pop() !== c) return false;
      if (stack.length === 0) return width + 1 <= remaining;
    }
    width += c === TAB ? cursor.options.tabSize : 1;
    pos += 1;
  }
  return false;
}

function skipProtectedOrAtom(cursor: ScanCursor, pos: number, end: number): { next: number; text: string } {
  const { text, state } = cursor;
  const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
  if (protectedEnd > pos) {
    const next = Math.min(protectedEnd, end);
    state.visits += next - pos;
    return { next, text: "" };
  }
  const c = text.at(pos);
  if (c === BACKSLASH && pos + 1 < end) {
    state.visits += 2;
    return { next: pos + 2, text: "" };
  }
  let p = pos;
  while (p < end) {
    const ch = text.at(p);
    if (isWhitespace(ch) || isDelimiter(ch)) break;
    if ((text.tokens?.protectedEnd(p) ?? -1) > p) break;
    p += 1;
  }
  const next = Math.max(pos + 1, p);
  state.visits += next - pos;
  return { next, text: text.slice(pos, p) };
}

function maybeBreakBeforeForm(cursor: ScanCursor, pos: number, atomText: string): void {
  const { state } = cursor;
  const top = state.sexpFrames;
  if (!top || !top.expanded || !state.hasContent) return;
  const index = top.formCount;
  const keepSecond =
    index === 0 ? top.kind === OPEN_PAREN && HEADER_FORMS.test(atomText) : top.keepSecond;
  state.sexpFrames = new SexpFrame(
    top.kind,
    top.expanded,
    top.isMap,
    top.childIndent,
    top.depth,
    index + 1,
    keepSecond,
    top.parent,
  );
  if (index === 0) return;
  if (top.isMap) {
    if (index % 2 === 0) cursor.emitBreak(pos, cursor.columns(top.childIndent));
    return;
  }
  if (top.kind === OPEN_PAREN) {
    if (index === 1 && keepSecond) return;
    cursor.emitBreak(pos, cursor.columns(top.childIndent));
    return;
  }
  cursor.emitBreak(pos, cursor.columns(top.childIndent));
}

export function stepSexp(cursor: ScanCursor, dataMode: boolean, pos: number, end: number): number {
  const { state, text } = cursor;
  const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
  if (protectedEnd > pos) {
    const to = Math.min(protectedEnd, end);
    state.visits += to - pos;
    if (state.laidOut && pos === cursor.skipSpaces(pos, end)) {
      maybeBreakBeforeForm(cursor, pos, "");
    }
    state.col += to - pos;
    state.hasContent = true;
    return to;
  }

  state.visits += 1;
  const c = text.at(pos);
  if (isWhitespace(c)) {
    state.col += c === TAB ? cursor.options.tabSize : 1;
    return pos + 1;
  }

  if (c === BACKSLASH && pos + 1 < end) {
    if (state.laidOut) maybeBreakBeforeForm(cursor, pos, "");
    state.col += 2;
    state.hasContent = true;
    return pos + 2;
  }

  // Reader prefixes bind to their following form or delimiter without breaking.
  if (isPrefixChar(c)) {
    if (state.laidOut) maybeBreakBeforeForm(cursor, pos, "");
    let p = pos + 1;
    if (c === HASH && p < end) {
      const next = text.at(p);
      if (next === QUOTE || next === 0x5f || next === 0x3f) p += 1;
      if (next === OPEN_BRACE || next === OPEN_PAREN) {
        const isSet = next === OPEN_BRACE;
        state.col += p - pos;
        state.hasContent = true;
        return openSexpGroup(cursor, dataMode, p, isSet);
      }
    } else if (c === TILDE && p < end && text.at(p) === AT) {
      p += 1;
    }
    state.col += p - pos;
    state.hasContent = true;
    return p;
  }

  if (c === OPEN_PAREN || c === OPEN_BRACKET || c === OPEN_BRACE) {
    if (state.laidOut) maybeBreakBeforeForm(cursor, pos, "");
    return openSexpGroup(cursor, dataMode, pos, false);
  }

  if (isCloser(c)) {
    const top = state.sexpFrames;
    if (top && top.kind === openerOf(c)) {
      state.sexpFrames = top.parent;
    }
    state.col += 1;
    state.hasContent = true;
    return pos + 1;
  }

  const atom = skipProtectedOrAtom(cursor, pos, end);
  if (state.laidOut) maybeBreakBeforeForm(cursor, pos, atom.text);
  state.col += atom.next - pos;
  state.hasContent = true;
  return atom.next;
}

function openSexpGroup(cursor: ScanCursor, dataMode: boolean, openPos: number, isSet: boolean): number {
  const { state, text } = cursor;
  const depth = (state.sexpFrames?.depth ?? 0) + 1;
  if (depth > PRETTY_MAX_DEPTH) {
    state.failure = "depth";
    return openPos + 1;
  }
  const kind = text.at(openPos);
  const nextPos = cursor.skipSpaces(openPos + 1, state.lineEnd);
  const nonEmpty = nextPos < state.lineEnd && text.at(nextPos) !== closerOf(kind);
  const isMap = kind === OPEN_BRACE && !isSet;
  const expanded =
    state.laidOut &&
    nonEmpty &&
    ((dataMode && isMap) || !sexpFits(cursor, openPos));
  const parentIndent = state.sexpFrames?.childIndent ?? 0;
  state.sexpFrames = new SexpFrame(kind, expanded, isMap, parentIndent + 1, depth, 0, false, state.sexpFrames);
  state.col += 1;
  state.hasContent = true;
  return openPos + 1;
}
