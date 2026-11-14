import type { ScanCursor } from "./pretty-scan-state.ts";

const SPACE = 0x20;
const TAB = 0x09;
const LESS = 0x3c;
const GREATER = 0x3e;
const SLASH = 0x2f;
const QUOTE = 0x22;
const APOS = 0x27;
const BANG = 0x21;
const QUESTION = 0x3f;
const DASH = 0x2d;
const CLOSE_BRACKET = 0x5d;

const VOID_TAGS = new Set([
  "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr",
]);
const RAW_TAGS = new Set(["pre", "code", "textarea", "script", "style"]);

type TagKind = "open" | "close" | "self";

/** A tag being read: its kind and name are known at `<`, its end at `>`. */
interface OpenTag {
  readonly kind: TagKind;
  readonly name: string;
  readonly quote: number;
  readonly slash: boolean;
}

export interface MarkupState {
  readonly comment: boolean;
  readonly cdata: boolean;
  readonly rawTag: string | null;
  readonly tag: OpenTag | null;
  readonly previous: TagKind | null;
  readonly previousName: string;
  readonly adjacent: boolean;
  readonly depth: number;
}

export const INITIAL_MARKUP: MarkupState = {
  comment: false, cdata: false, rawTag: null, tag: null, previous: null, previousName: "", adjacent: false, depth: 0,
};

function tagsEqual(a: OpenTag | null, b: OpenTag | null): boolean {
  return a === b || (!!a && !!b && a.kind === b.kind && a.name === b.name && a.quote === b.quote && a.slash === b.slash);
}

export function markupEqual(a: MarkupState, b: MarkupState): boolean {
  return a.comment === b.comment && a.cdata === b.cdata && a.rawTag === b.rawTag && tagsEqual(a.tag, b.tag) &&
    a.previous === b.previous && a.previousName === b.previousName && a.adjacent === b.adjacent && a.depth === b.depth;
}

/** Tags on different source lines are never adjacent, and depth counts within a line. */
export function startMarkupLine(state: MarkupState): MarkupState {
  return { ...state, previous: null, adjacent: false, depth: 0 };
}

function isTagNameCode(code: number): boolean {
  return (code >= 0x61 && code <= 0x7a) || (code >= 0x41 && code <= 0x5a) || (code >= 0x30 && code <= 0x39) ||
    code === 0x5f || code === 0x24 || code === DASH || code === 0x3a || code === 0x2e;
}

function tagName(cursor: ScanCursor, from: number, limit: number): string {
  let end = from;
  while (end < limit && isTagNameCode(cursor.text.at(end))) end += 1;
  return cursor.text.slice(from, end).toLowerCase();
}

/**
 * Reads markup one character at a time. A break before a tag depends only
 * on the tag's kind and name, read at `<`; whether it closes itself is known
 * at its `>`, so no decision waits on text further on.
 */
export function stepMarkup(cursor: ScanCursor, pos: number): number {
  const { text, state } = cursor;
  const m = state.markup;
  const lineEnd = state.lineEnd;
  state.visits += 1;
  const c = text.at(pos);
  state.col += c === TAB ? cursor.options.tabSize : 1;
  if (m.comment) {
    if (c === DASH && pos + 2 < lineEnd && text.at(pos + 1) === DASH && text.at(pos + 2) === GREATER) {
      state.markup = { ...m, comment: false, previous: "self", previousName: "!comment", adjacent: true };
      state.col += 2;
      return pos + 3;
    }
    return pos + 1;
  }
  if (m.cdata) {
    if (c === CLOSE_BRACKET && pos + 2 < lineEnd && text.at(pos + 1) === CLOSE_BRACKET && text.at(pos + 2) === GREATER) {
      state.markup = { ...m, cdata: false, previous: null, adjacent: false };
      state.col += 2;
      return pos + 3;
    }
    return pos + 1;
  }
  if (m.tag) {
    const tag = m.tag;
    if (tag.quote !== 0) {
      if (c === tag.quote) state.markup = { ...m, tag: { ...tag, quote: 0 } };
    } else if (c === QUOTE || c === APOS) {
      state.markup = { ...m, tag: { ...tag, quote: c } };
    } else if (c === GREATER) {
      const kind: TagKind = tag.kind === "open" && (tag.slash || VOID_TAGS.has(tag.name)) ? "self" : tag.kind;
      state.markup = {
        ...m,
        tag: null,
        rawTag: kind === "open" && RAW_TAGS.has(tag.name) ? tag.name : null,
        previous: kind,
        previousName: tag.name,
        adjacent: true,
      };
    } else if (c !== SPACE && c !== TAB) {
      const slash = c === SLASH;
      if (slash !== tag.slash) state.markup = { ...m, tag: { ...tag, slash } };
    }
    return pos + 1;
  }
  if (m.rawTag !== null) {
    if (c === LESS && pos + 1 < lineEnd && text.at(pos + 1) === SLASH && tagName(cursor, pos + 2, lineEnd) === m.rawTag) {
      state.markup = { ...m, rawTag: null, tag: { kind: "close", name: m.rawTag, quote: 0, slash: false } };
    }
    return pos + 1;
  }
  if (c === SPACE || c === TAB) return pos + 1;
  state.hasContent = true;
  if (c !== LESS) {
    if (m.adjacent) state.markup = { ...m, adjacent: false };
    return pos + 1;
  }
  if (pos + 3 < lineEnd && text.at(pos + 1) === BANG && text.at(pos + 2) === DASH && text.at(pos + 3) === DASH) {
    let depth = m.depth;
    if (m.adjacent && m.previous !== null) {
      if (m.previous === "open") depth += 1;
      if (state.laidOut) cursor.emitBreak(pos, cursor.columns(Math.max(0, depth)));
    }
    state.markup = { ...m, comment: true, previous: null, adjacent: false, depth };
    state.col += 3;
    return pos + 4;
  }
  if (pos + 8 < lineEnd && text.slice(pos, pos + 9) === "<![CDATA[") {
    state.markup = { ...m, cdata: true, previous: null, adjacent: false };
    state.col += 8;
    return pos + 9;
  }
  const first = pos + 1 < lineEnd ? text.at(pos + 1) : 0;
  let kind: TagKind = "open";
  let name: string;
  if (first === SLASH) {
    kind = "close";
    name = tagName(cursor, pos + 2, lineEnd);
  } else if (first === BANG || first === QUESTION) {
    kind = "self";
    name = "!decl";
  } else {
    name = tagName(cursor, pos + 1, lineEnd);
  }
  let depth = m.depth;
  if (m.adjacent && m.previous !== null) {
    let breaks = true;
    if (m.previous === "open") {
      if (kind === "close" && name === m.previousName) breaks = false;
      else if (kind !== "close") depth += 1;
    } else if (kind === "close") {
      depth -= 1;
    }
    if (breaks && state.laidOut) cursor.emitBreak(pos, cursor.columns(Math.max(0, depth)));
  }
  state.markup = { ...m, tag: { kind, name, quote: 0, slash: false }, depth };
  return pos + 1;
}
