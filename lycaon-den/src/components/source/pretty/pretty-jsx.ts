import { stepBracket } from "./pretty-bracket.ts";
import { ScriptLexer } from "./pretty-lexers.ts";
import { type BracketRule, type ScanCursor, targetColumns } from "./pretty-scan-state.ts";

const SPACE = 0x20;
const TAB = 0x09;
const LESS = 0x3c;
const GREATER = 0x3e;
const SLASH = 0x2f;
const QUOTE = 0x22;
const APOS = 0x27;
const OPEN_BRACE = 0x7b;
const CLOSE_BRACE = 0x7d;

export const JSX_SCRIPT_RULE: BracketRule = {
  kind: "bracket",
  data: false,
  statements: true,
  afterBlock: "script",
  records: false,
  parsed: false,
  lexer: () => new ScriptLexer(),
};

export interface JsxState {
  readonly depth: number;
  readonly inTag: boolean;
  readonly tagQuote: number;
  readonly tagClosing: boolean;
  readonly tagSelf: boolean;
  readonly braceDepthWhenEntered: number[];
}

export const INITIAL_JSX_STATE: JsxState = {
  depth: 0,
  inTag: false,
  tagQuote: 0,
  tagClosing: false,
  tagSelf: false,
  braceDepthWhenEntered: [],
};

export function jsxStatesEqual(a: JsxState, b: JsxState): boolean {
  if (
    a.depth !== b.depth ||
    a.inTag !== b.inTag ||
    a.tagQuote !== b.tagQuote ||
    a.tagClosing !== b.tagClosing ||
    a.tagSelf !== b.tagSelf ||
    a.braceDepthWhenEntered.length !== b.braceDepthWhenEntered.length
  ) {
    return false;
  }
  for (let i = 0; i < a.braceDepthWhenEntered.length; i++) {
    if (a.braceDepthWhenEntered[i] !== b.braceDepthWhenEntered[i]) return false;
  }
  return true;
}

function frameDepth(cursor: ScanCursor): number {
  let d = 0;
  for (let f = cursor.state.frames; f; f = f.parent) d += 1;
  return d;
}

function isJsxOpener(cursor: ScanCursor, pos: number): boolean {
  const { text, state } = cursor;
  if (pos + 1 >= state.lineEnd) return false;
  const next = text.at(pos + 1);
  // `<>` fragment, `</` closing tag, or `<Tag` identifier
  if (next === GREATER || next === SLASH) return true;
  const isIdent =
    (next >= 0x41 && next <= 0x5a) ||
    (next >= 0x61 && next <= 0x7a) ||
    next === 0x5f ||
    next === 0x24;
  if (!isIdent) return false;
  // Disambiguate `<` comparison from JSX element start by looking back on the line.
  let prev = pos - 1;
  while (prev >= 0 && (text.at(prev) === SPACE || text.at(prev) === TAB)) prev -= 1;
  if (prev < 0 || !state.hasContent) return true;
  const ch = text.at(prev);
  return (
    ch === 0x28 /* ( */ ||
    ch === 0x5b /* [ */ ||
    ch === 0x7b /* { */ ||
    ch === 0x2c /* , */ ||
    ch === 0x3a /* : */ ||
    ch === 0x3f /* ? */ ||
    ch === 0x3d /* = */ ||
    ch === 0x3e /* > */ ||
    ch === 0x26 /* & */ ||
    ch === 0x7c /* | */ ||
    ch === 0x3b /* ; */ ||
    text.slice(Math.max(0, prev - 5), prev + 1) === "return"
  );
}

export function stepJsx(cursor: ScanCursor, pos: number, end: number): number {
  const { state, text } = cursor;
  const j = state.jsxState;
  const curFrameDepth = frameDepth(cursor);

  // Inside a `{...}` JS expression embedded in JSX (prop or child), delegate to Script bracket layout.
  if (j.braceDepthWhenEntered.length > 0 && j.braceDepthWhenEntered[j.braceDepthWhenEntered.length - 1]! < curFrameDepth) {
    return stepBracket(cursor, JSX_SCRIPT_RULE, pos, end);
  }

  if (j.inTag) {
    const c = text.at(pos);
    state.col += c === TAB ? cursor.options.tabSize : 1;
    state.hasContent = true;
    if (j.tagQuote !== 0) {
      if (c === j.tagQuote) state.jsxState = { ...j, tagQuote: 0 };
      return pos + 1;
    }
    if (c === QUOTE || c === APOS) {
      state.jsxState = { ...j, tagQuote: c };
      return pos + 1;
    }
    if (c === OPEN_BRACE) {
      return stepBracket(cursor, JSX_SCRIPT_RULE, pos, end);
    }
    if (c === GREATER) {
      const nextDepth = j.tagClosing
        ? Math.max(0, j.depth - 1)
        : j.tagSelf
          ? j.depth
          : j.depth + 1;
      const nextBraces =
        nextDepth < j.depth ? j.braceDepthWhenEntered.slice(0, -1) : j.braceDepthWhenEntered;
      state.jsxState = {
        ...j,
        depth: nextDepth,
        inTag: false,
        tagClosing: false,
        tagSelf: false,
        braceDepthWhenEntered: nextBraces,
      };
      if (state.laidOut && nextDepth > 0) {
        const next = cursor.skipSpaces(pos + 1, state.lineEnd);
        if (next < state.lineEnd && text.at(next) === LESS) {
          const isNextClose = next + 1 < state.lineEnd && text.at(next + 1) === SLASH;
          const breakDepth = isNextClose ? Math.max(0, nextDepth - 1) : nextDepth;
          if (!(isNextClose && !j.tagSelf && !j.tagClosing && nextDepth === j.depth + 1)) {
            const baseIndent = state.frames?.childIndent ?? 0;
            state.pending = next;
            state.pendingColumns = cursor.columns(baseIndent + breakDepth);
          }
        }
      }
      return pos + 1;
    }
    if (c !== SPACE && c !== TAB) {
      const tagSelf = c === SLASH;
      if (tagSelf !== j.tagSelf) state.jsxState = { ...j, tagSelf };
    }
    return pos + 1;
  }

  if (!state.lexer!.inProtected() && text.at(pos) === LESS && isJsxOpener(cursor, pos)) {
    const isClose = pos + 1 < state.lineEnd && text.at(pos + 1) === SLASH;
    const nextBraces = isClose
      ? j.braceDepthWhenEntered
      : [...j.braceDepthWhenEntered, curFrameDepth];
    state.jsxState = {
      ...j,
      inTag: true,
      tagQuote: 0,
      tagClosing: isClose,
      tagSelf: false,
      braceDepthWhenEntered: nextBraces,
    };
    state.col += 1;
    state.hasContent = true;
    return pos + 1;
  }

  // Outside tags, if at JSX text depth > 0 and `<` is reached after text, break if wide.
  if (j.depth > 0 && text.at(pos) === CLOSE_BRACE) {
    return stepBracket(cursor, JSX_SCRIPT_RULE, pos, end);
  }
  if (j.depth > 0 && text.at(pos) !== LESS) {
    const c = text.at(pos);
    state.col += c === TAB ? cursor.options.tabSize : 1;
    if (c !== SPACE && c !== TAB) state.hasContent = true;
    void targetColumns;
    return pos + 1;
  }

  return stepBracket(cursor, JSX_SCRIPT_RULE, pos, end);
}
