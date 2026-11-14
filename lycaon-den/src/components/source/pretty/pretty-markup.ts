import { stepBracket } from "./pretty-bracket.ts";
import { INITIAL_JSX_STATE, stepJsx } from "./pretty-jsx.ts";
import { DataLexer, ScriptLexer, StyleLexer } from "./pretty-lexers.ts";
import { type BracketRule, type ScanCursor, targetColumns } from "./pretty-scan-state.ts";

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
const RAW_TAGS = new Set(["pre", "code", "textarea"]);
const JSON_SCRIPT_TYPES = new Set(["application/json", "application/ld+json", "importmap", "speculationrules"]);

const EMBEDDED_SCRIPT_RULE: BracketRule = {
  kind: "bracket", data: false, statements: true, afterBlock: "script", records: false, parsed: false,
  lexer: () => new ScriptLexer(),
};
const EMBEDDED_DATA_RULE: BracketRule = {
  kind: "bracket", data: true, statements: false, afterBlock: null, records: false, parsed: false,
  lexer: () => new DataLexer({ singleQuotes: false, comments: false }),
};
const EMBEDDED_STYLE_RULE: BracketRule = {
  kind: "bracket", data: false, statements: true, afterBlock: "style", records: false, parsed: false,
  lexer: () => new StyleLexer(false),
};
const EMBEDDED_SCSS_RULE: BracketRule = { ...EMBEDDED_STYLE_RULE, lexer: () => new StyleLexer(true) };

type TagKind = "open" | "close" | "self";

interface OpenTag {
  readonly kind: TagKind;
  readonly name: string;
  readonly start: number;
  readonly quote: number;
  readonly slash: boolean;
}

export interface EmbeddedRegion {
  readonly closeTag: string;
  readonly rule: BracketRule;
  readonly jsx: boolean;
}

export interface MarkupState {
  readonly comment: boolean;
  readonly cdata: boolean;
  readonly rawTag: string | null;
  readonly embedded: EmbeddedRegion | null;
  readonly tag: OpenTag | null;
  readonly previous: TagKind | null;
  readonly previousName: string;
  readonly adjacent: boolean;
  readonly depth: number;
}

export const INITIAL_MARKUP: MarkupState = {
  comment: false, cdata: false, rawTag: null, embedded: null, tag: null,
  previous: null, previousName: "", adjacent: false, depth: 0,
};

function tagsEqual(a: OpenTag | null, b: OpenTag | null): boolean {
  return a === b || (!!a && !!b && a.kind === b.kind && a.name === b.name && a.quote === b.quote && a.slash === b.slash);
}

function embeddedEqual(a: EmbeddedRegion | null, b: EmbeddedRegion | null): boolean {
  return a === b || (!!a && !!b && a.closeTag === b.closeTag && a.rule === b.rule && a.jsx === b.jsx);
}

export function markupEqual(a: MarkupState, b: MarkupState): boolean {
  return a.comment === b.comment && a.cdata === b.cdata && a.rawTag === b.rawTag && embeddedEqual(a.embedded, b.embedded) &&
    tagsEqual(a.tag, b.tag) && a.previous === b.previous && a.previousName === b.previousName &&
    a.adjacent === b.adjacent && a.depth === b.depth;
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

function attrValue(tagText: string, name: string): string {
  const match = new RegExp(`\\b${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, "i").exec(tagText);
  return (match?.[1] ?? match?.[2] ?? match?.[3] ?? "").trim().toLowerCase();
}

function embeddedForTag(cursor: ScanCursor, tag: OpenTag, tagEnd: number): EmbeddedRegion | null {
  if (tag.name !== "script" && tag.name !== "style") return null;
  const tagText = cursor.text.slice(tag.start, Math.min(tagEnd + 1, tag.start + 512));
  const lang = attrValue(tagText, "lang");
  if (tag.name === "script") {
    const type = attrValue(tagText, "type");
    if (JSON_SCRIPT_TYPES.has(type)) return { closeTag: "</script", rule: EMBEDDED_DATA_RULE, jsx: false };
    if (lang === "tsx" || lang === "jsx") return { closeTag: "</script", rule: EMBEDDED_SCRIPT_RULE, jsx: true };
    return { closeTag: "</script", rule: EMBEDDED_SCRIPT_RULE, jsx: false };
  }
  const rule = lang === "scss" || lang === "less" ? EMBEDDED_SCSS_RULE : EMBEDDED_STYLE_RULE;
  return { closeTag: "</style", rule, jsx: false };
}

function attrWidth(cursor: ScanCursor, from: number, lineEnd: number): number {
  const { text } = cursor;
  let pos = from;
  let quote = 0;
  let width = 0;
  while (pos < lineEnd) {
    const c = text.at(pos);
    if (quote !== 0) {
      if (c === quote) quote = 0;
    } else if (c === QUOTE || c === APOS) {
      quote = c;
    } else if (c === SPACE || c === TAB || c === GREATER) {
      break;
    }
    width += c === TAB ? cursor.options.tabSize : 1;
    pos += 1;
  }
  return width;
}

export function stepMarkup(cursor: ScanCursor, pos: number, end: number): number {
  const { text, state } = cursor;
  let m = state.markup;
  const lineEnd = state.lineEnd;

  // Astro leading `---` frontmatter at offset 0.
  if (pos === 0 && !m.embedded && lineEnd >= 3 && text.slice(0, 3) === "---" && text.slice(3, lineEnd).trim() === "") {
    state.lexer = EMBEDDED_SCRIPT_RULE.lexer();
    state.frames = null;
    state.containers = null;
    state.markup = { ...m, embedded: { closeTag: "---", rule: EMBEDDED_SCRIPT_RULE, jsx: false } };
    state.col += 3;
    state.hasContent = true;
    return 3;
  }

  if (m.embedded) {
    const emb = m.embedded;
    if (!(state.lexer?.inProtected() ?? false)) {
      if (emb.closeTag === "---") {
        if (!state.hasContent && lineEnd - pos >= 3 && text.slice(pos, lineEnd).trim() === "---") {
          state.lexer = null;
          state.frames = null;
          state.containers = null;
          state.markup = { ...m, embedded: null };
          state.col += lineEnd - pos;
          return lineEnd;
        }
      } else if (
        text.at(pos) === LESS &&
        pos + emb.closeTag.length <= lineEnd &&
        text.slice(pos, pos + emb.closeTag.length).toLowerCase() === emb.closeTag
      ) {
        if (state.laidOut && state.hasContent) cursor.emitBreak(pos, cursor.columns(Math.max(0, m.depth)));
        state.lexer = null;
        state.frames = null;
        state.containers = null;
        state.jsxState = INITIAL_JSX_STATE;
        state.markup = { ...m, embedded: null };
        m = state.markup;
      }
    }
    if (state.markup.embedded) {
      return emb.jsx ? stepJsx(cursor, pos, end) : stepBracket(cursor, emb.rule, pos, end);
    }
  }

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
      const embedded = kind === "open" ? embeddedForTag(cursor, tag, pos) : null;
      if (embedded) {
        state.lexer = embedded.rule.lexer();
        state.frames = null;
        state.containers = null;
        state.jsxState = INITIAL_JSX_STATE;
      }
      state.markup = {
        ...m,
        tag: null,
        rawTag: !embedded && kind === "open" && RAW_TAGS.has(tag.name) ? tag.name : null,
        embedded,
        previous: kind,
        previousName: tag.name,
        adjacent: true,
      };
    } else if (c === SPACE || c === TAB) {
      if (state.laidOut && tag.kind !== "close") {
        const next = cursor.skipSpaces(pos + 1, lineEnd);
        if (next < lineEnd && text.at(next) !== GREATER && !(text.at(next) === SLASH && text.at(next + 1) === GREATER)) {
          if (state.col + attrWidth(cursor, next, lineEnd) > targetColumns(cursor.options.lineWidth)) {
            state.pending = next;
            state.pendingColumns = cursor.columns(Math.max(0, m.depth + 1));
          }
        }
      }
    } else {
      const slash = c === SLASH;
      if (slash !== tag.slash) state.markup = { ...m, tag: { ...tag, slash } };
    }
    return pos + 1;
  }
  if (m.rawTag !== null) {
    if (c === LESS && pos + 1 < lineEnd && text.at(pos + 1) === SLASH && tagName(cursor, pos + 2, lineEnd) === m.rawTag) {
      state.markup = { ...m, rawTag: null, tag: { kind: "close", name: m.rawTag, start: pos, quote: 0, slash: false } };
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
  state.markup = { ...m, tag: { kind, name, start: pos, quote: 0, slash: false }, depth };
  return pos + 1;
}
