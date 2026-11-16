import type { TextWindow } from "./pretty-text-window.ts";

/**
 * Lexical state for one layout rule. A lexer passes over strings, comments,
 * regular expressions and templates, which are never broken, and leaves code
 * characters to the scanner. Every protected token can be resumed at any
 * position, so scanning stops at checkpoints even inside a long string.
 */
export interface PrettyLexer {
  /**
   * Consumes protected text at `pos`, stopping at `end`, and returns where it
   * stopped. Returns `pos` when the character there is code. Tokens are
   * recognised by looking as far as `lineEnd`, so where scanning pauses never
   * changes how text reads.
   */
  skip(text: TextWindow, pos: number, end: number, lineEnd: number): number;
  /** Records a code character the scanner consumed. */
  noteCode(text: TextWindow, pos: number): void;
  /** Whether a protected token is open. */
  inProtected(): boolean;
  /** Called at each line end. */
  endLine(): void;
  copy(): PrettyLexer;
  equals(other: PrettyLexer): boolean;
}

const QUOTE = 0x22;
const APOS = 0x27;
const BACKTICK = 0x60;
const SLASH = 0x2f;
const STAR = 0x2a;
const BACKSLASH = 0x5c;
const OPEN_BRACE = 0x7b;
const CLOSE_BRACE = 0x7d;
const DOLLAR = 0x24;
const OPEN_BRACKET = 0x5b;
const CLOSE_BRACKET = 0x5d;
const CLOSE_PAREN = 0x29;
const LESS = 0x3c;
const GREATER = 0x3e;

const enum Mode {
  Code,
  Single,
  Double,
  BlockComment,
  LineComment,
  Regex,
  Url,
}

function isSpace(code: number): boolean {
  return code === 0x20 || code === 0x09 || code === 0x0d;
}

function isJsxIdentStart(code: number): boolean {
  return (
    (code >= 0x61 && code <= 0x7a) ||
    (code >= 0x41 && code <= 0x5a) ||
    code === 0x5f ||
    code === DOLLAR ||
    code > 0x7f
  );
}

function isIdentifierCode(code: number): boolean {
  return isJsxIdentStart(code) || (code >= 0x30 && code <= 0x39);
}

/** Shared resumable scanning of quoted strings and block and line comments. */
abstract class LexerBase implements PrettyLexer {
  protected mode = Mode.Code;
  protected escape = false;

  abstract skip(text: TextWindow, pos: number, end: number, lineEnd: number): number;
  abstract copy(): PrettyLexer;

  noteCode(_text: TextWindow, _pos: number): void {}

  inProtected(): boolean {
    return this.mode !== Mode.Code;
  }

  endLine(): void {
    if (this.mode === Mode.LineComment) this.mode = Mode.Code;
    this.escape = false;
  }

  protected continueQuoted(text: TextWindow, pos: number, end: number, quote: number): number {
    let i = pos;
    while (i < end) {
      const c = text.at(i);
      i += 1;
      if (this.escape) {
        this.escape = false;
      } else if (c === BACKSLASH) {
        this.escape = true;
      } else if (c === quote) {
        this.closed();
        return i;
      }
    }
    return i;
  }

  protected continueBlockComment(text: TextWindow, pos: number, end: number): number {
    let i = pos;
    while (i < end) {
      const c = text.at(i);
      i += 1;
      if (this.escape) {
        // A star ended the text read so far; a slash now closes the comment.
        this.escape = false;
        if (c === SLASH) {
          this.closed();
          return i;
        }
      }
      if (c === STAR) {
        if (i >= end) {
          this.escape = true;
          return i;
        }
        if (text.at(i) === SLASH) {
          this.closed();
          return i + 1;
        }
      }
    }
    return i;
  }

  protected closed(): void {
    this.mode = Mode.Code;
    this.escape = false;
  }

  protected sameBase(other: LexerBase): boolean {
    return other.mode === this.mode && other.escape === this.escape;
  }

  abstract equals(other: PrettyLexer): boolean;
}

export interface DataLexerOptions {
  readonly singleQuotes: boolean;
  readonly comments: boolean;
}

/** JSON and its relatives: strings, and comments where the format has them. */
export class DataLexer extends LexerBase {
  constructor(private readonly options: DataLexerOptions) {
    super();
  }

  skip(text: TextWindow, pos: number, end: number, lineEnd: number): number {
    switch (this.mode) {
      case Mode.Double: return this.continueQuoted(text, pos, end, QUOTE);
      case Mode.Single: return this.continueQuoted(text, pos, end, APOS);
      case Mode.BlockComment: return this.continueBlockComment(text, pos, end);
      case Mode.LineComment: return end;
    }
    if (pos >= end) return pos;
    const c = text.at(pos);
    if (c === QUOTE) {
      this.mode = Mode.Double;
      return this.continueQuoted(text, pos + 1, end, QUOTE);
    }
    if (c === APOS && this.options.singleQuotes) {
      this.mode = Mode.Single;
      return this.continueQuoted(text, pos + 1, end, APOS);
    }
    if (this.options.comments && c === SLASH && pos + 1 < lineEnd) {
      const next = text.at(pos + 1);
      if (next === SLASH) {
        this.mode = Mode.LineComment;
        return end;
      }
      if (next === STAR) {
        this.mode = Mode.BlockComment;
        return this.continueBlockComment(text, pos + 2, end);
      }
    }
    return pos;
  }

  override endLine(): void {
    // Data strings never span lines.
    if (this.mode === Mode.BlockComment) this.escape = false;
    else this.closed();
  }

  copy(): DataLexer {
    const out = new DataLexer(this.options);
    out.mode = this.mode;
    out.escape = this.escape;
    return out;
  }

  equals(other: PrettyLexer): boolean {
    return other instanceof DataLexer && other.options === this.options && this.sameBase(other);
  }
}

const REGEX_AFTER_WORDS = new Set([
  "return", "case", "default", "throw", "delete", "void", "typeof", "instanceof", "in",
  "of", "yield", "await", "else", "do", "new",
]);

type ScriptFrame =
  | { readonly kind: "template" }
  | { readonly kind: "expr"; readonly owner: "template" | "jsx"; braceDepth: number }
  | { readonly kind: "jsx-tag"; readonly closing: boolean; quote: number; slash: boolean }
  | { readonly kind: "jsx-children" };

function copyFrame(frame: ScriptFrame): ScriptFrame {
  switch (frame.kind) {
    case "template":
    case "jsx-children":
      return frame;
    case "expr":
      return { kind: "expr", owner: frame.owner, braceDepth: frame.braceDepth };
    case "jsx-tag":
      return { kind: "jsx-tag", closing: frame.closing, quote: frame.quote, slash: frame.slash };
  }
}

function framesMatch(a: ScriptFrame, b: ScriptFrame): boolean {
  if (a.kind !== b.kind) return false;
  if (a.kind === "expr" && b.kind === "expr") {
    return a.owner === b.owner && a.braceDepth === b.braceDepth;
  }
  if (a.kind === "jsx-tag" && b.kind === "jsx-tag") {
    return a.closing === b.closing && a.quote === b.quote && a.slash === b.slash;
  }
  return true;
}

function startsWithExtendsConstraint(text: TextWindow, pos: number, lineEnd: number): boolean {
  if (pos + 7 > lineEnd || text.slice(pos, pos + 7) !== "extends") return false;
  if (pos + 7 < lineEnd && (isIdentifierCode(text.at(pos + 7)) || text.at(pos + 7) === 0x2d)) return false;
  let k = pos + 7;
  while (k < lineEnd && (text.at(k) === 0x20 || text.at(k) === 0x09)) k += 1;
  return k >= lineEnd || text.at(k) !== 0x3d;
}

/**
 * JavaScript, TypeScript, JSX and TSX: strings, comments, regular expressions,
 * template literal chunks with script lexing inside `${}`, and JSX tag and
 * children contexts when `jsx` is enabled.
 */
export class ScriptLexer extends LexerBase {
  private frames: ScriptFrame[] = [];
  private regexClass = false;
  private expressionEnd = false;
  private postfixSign = 0;
  private afterDot = false;
  private inWord = false;
  private inNumber = false;
  private lastWord = "";

  lastTagClosed: "open" | "close" | "self" | null = null;
  lastTagCodeWidth = 0;

  constructor(readonly jsx = false) {
    super();
  }

  /** Open JSX element depth above the innermost `{}` expression frame. */
  get jsxDepth(): number {
    let depth = 0;
    for (let i = this.frames.length - 1; i >= 0; i--) {
      const f = this.frames[i]!;
      if (f.kind === "expr") break;
      if (f.kind === "jsx-children") depth += 1;
    }
    return depth;
  }

  override inProtected(): boolean {
    if (this.mode !== Mode.Code) return true;
    const top = this.frames[this.frames.length - 1];
    return top?.kind === "template" || top?.kind === "jsx-tag" || top?.kind === "jsx-children";
  }

  skip(text: TextWindow, pos: number, end: number, lineEnd: number): number {
    this.lastTagClosed = null;
    this.lastTagCodeWidth = 0;
    switch (this.mode) {
      case Mode.Double: return this.continueQuoted(text, pos, end, QUOTE);
      case Mode.Single: return this.continueQuoted(text, pos, end, APOS);
      case Mode.BlockComment: return this.continueBlockComment(text, pos, end);
      case Mode.LineComment: return end;
      case Mode.Regex: return this.continueRegex(text, pos, end, lineEnd);
    }
    const top = this.frames[this.frames.length - 1];
    if (top?.kind === "template") return this.continueTemplateText(text, pos, end, lineEnd);
    if (top?.kind === "jsx-tag") return this.continueJsxTag(text, pos, end, lineEnd);
    if (top?.kind === "jsx-children") return this.continueJsxChildren(text, pos, end, lineEnd);
    if (pos >= end) return pos;
    const c = text.at(pos);
    if (top?.kind === "expr" && top.owner === "template" && top.braceDepth === 0 && c === CLOSE_BRACE) {
      this.frames.pop();
      return this.continueTemplateText(text, pos + 1, end, lineEnd);
    }
    if (c === QUOTE || c === APOS) {
      this.mode = c === QUOTE ? Mode.Double : Mode.Single;
      return this.continueQuoted(text, pos + 1, end, c);
    }
    if (c === BACKTICK) {
      this.frames.push({ kind: "template" });
      return this.continueTemplateText(text, pos + 1, end, lineEnd);
    }
    if (c === SLASH && pos + 1 < lineEnd) {
      const next = text.at(pos + 1);
      if (next === SLASH) {
        this.clearPartialToken();
        this.mode = Mode.LineComment;
        return end;
      }
      if (next === STAR) {
        this.clearPartialToken();
        this.mode = Mode.BlockComment;
        return this.continueBlockComment(text, pos + 2, end);
      }
    }
    if (c === SLASH && !this.expressionEnd) {
      this.mode = Mode.Regex;
      this.regexClass = false;
      return this.continueRegex(text, pos + 1, end, lineEnd);
    }
    if (c === LESS && this.jsx && !this.expressionEnd && this.opensJsxFromCode(text, pos, lineEnd)) {
      this.frames.push({ kind: "jsx-tag", closing: text.at(pos + 1) === SLASH, quote: 0, slash: false });
      this.lastTagCodeWidth += 1;
      return this.continueJsxTag(text, pos + 1, end, lineEnd);
    }
    return pos;
  }

  private opensJsxFromCode(text: TextWindow, pos: number, lineEnd: number): boolean {
    if (pos + 1 >= lineEnd) return false;
    const next = text.at(pos + 1);
    if (next === GREATER) return true;
    if (next === SLASH) return pos + 2 < lineEnd && text.at(pos + 2) !== SLASH && text.at(pos + 2) !== STAR;
    if (!isJsxIdentStart(next)) return false;
    let i = pos + 1;
    while (i < lineEnd && isIdentifierCode(text.at(i))) i += 1;
    if (i < lineEnd && (text.at(i) === 0x2d || text.at(i) === 0x3a || text.at(i) === 0x2e)) return true;
    const word = text.slice(pos + 1, i);
    while (i < lineEnd && (text.at(i) === 0x20 || text.at(i) === 0x09)) i += 1;
    if ((word === "const" || word === "in" || word === "out") && i < lineEnd && isJsxIdentStart(text.at(i))) {
      let j = i;
      while (j < lineEnd && isIdentifierCode(text.at(j))) j += 1;
      while (j < lineEnd && (text.at(j) === 0x20 || text.at(j) === 0x09)) j += 1;
      if (
        j < lineEnd &&
        (text.at(j) === 0x2c ||
          (text.at(j) === 0x3d && (j + 1 >= lineEnd || text.at(j + 1) !== GREATER)) ||
          startsWithExtendsConstraint(text, j, lineEnd))
      ) {
        return false;
      }
    }
    if (i >= lineEnd) return true;
    const after = text.at(i);
    if (after === 0x2c) return false;
    if (after === 0x3d && (i + 1 >= lineEnd || text.at(i + 1) !== GREATER)) return false;
    if (startsWithExtendsConstraint(text, i, lineEnd)) return false;
    return true;
  }

  private continueJsxTag(text: TextWindow, pos: number, end: number, lineEnd: number): number {
    const tag = this.frames[this.frames.length - 1];
    if (!tag || tag.kind !== "jsx-tag") return pos;
    let i = pos;
    while (i < end) {
      const c = text.at(i);
      if (tag.quote !== 0) {
        if (c === tag.quote) tag.quote = 0;
        i += 1;
        continue;
      }
      if (c === OPEN_BRACE) return i;
      if (c === SLASH && i + 1 < lineEnd) {
        const next = text.at(i + 1);
        if (next === SLASH) {
          tag.slash = false;
          this.mode = Mode.LineComment;
          return end;
        }
        if (next === STAR) {
          tag.slash = false;
          this.mode = Mode.BlockComment;
          return this.continueBlockComment(text, i + 2, end);
        }
      }
      i += 1;
      this.lastTagCodeWidth += 1;
      if (c === QUOTE || c === APOS) {
        tag.quote = c;
        tag.slash = false;
      } else if (c === GREATER) {
        this.frames.pop();
        if (tag.closing) {
          if (this.frames[this.frames.length - 1]?.kind === "jsx-children") this.frames.pop();
          this.lastTagClosed = "close";
        } else if (tag.slash) {
          this.lastTagClosed = "self";
        } else {
          this.frames.push({ kind: "jsx-children" });
          this.lastTagClosed = "open";
        }
        const top = this.frames[this.frames.length - 1];
        if (top?.kind !== "jsx-tag" && top?.kind !== "jsx-children") {
          this.resetTokenState(true);
        }
        return i;
      } else {
        tag.slash = c === SLASH;
      }
    }
    return i;
  }

  private continueJsxChildren(text: TextWindow, pos: number, end: number, lineEnd: number): number {
    let i = pos;
    while (i < end) {
      const c = text.at(i);
      if (c === OPEN_BRACE) return i;
      if (c === LESS && i + 1 < lineEnd) {
        const next = text.at(i + 1);
        if (next === SLASH || next === GREATER || isJsxIdentStart(next)) {
          if (i > pos) return i;
          this.frames.push({ kind: "jsx-tag", closing: next === SLASH, quote: 0, slash: false });
          this.lastTagCodeWidth += 1;
          return this.continueJsxTag(text, i + 1, end, lineEnd);
        }
      }
      i += 1;
    }
    return i;
  }

  private continueRegex(text: TextWindow, pos: number, end: number, lineEnd: number): number {
    let i = pos;
    while (i < end) {
      const c = text.at(i);
      i += 1;
      if (this.escape) {
        this.escape = false;
      } else if (c === BACKSLASH) {
        this.escape = true;
      } else if (c === OPEN_BRACKET) {
        this.regexClass = true;
      } else if (c === CLOSE_BRACKET) {
        this.regexClass = false;
      } else if (c === SLASH && !this.regexClass) {
        while (i < lineEnd && isIdentifierCode(text.at(i))) i += 1;
        this.closed();
        this.regexClass = false;
        this.resetTokenState(true);
        return i;
      }
    }
    return i;
  }

  private continueTemplateText(text: TextWindow, pos: number, end: number, lineEnd: number): number {
    let i = pos;
    while (i < end) {
      const c = text.at(i);
      i += 1;
      if (this.escape) {
        this.escape = false;
      } else if (c === BACKSLASH) {
        this.escape = true;
      } else if (c === BACKTICK) {
        this.frames.pop();
        this.resetTokenState(true);
        return i;
      } else if (c === DOLLAR && i < lineEnd && text.at(i) === OPEN_BRACE) {
        this.frames.push({ kind: "expr", owner: "template", braceDepth: 0 });
        this.resetTokenState(false);
        return i + 1;
      }
    }
    return i;
  }

  private clearPartialToken(): void {
    this.inWord = false;
    this.inNumber = false;
    this.postfixSign = 0;
  }

  private resetTokenState(expressionEnd: boolean): void {
    this.expressionEnd = expressionEnd;
    this.postfixSign = 0;
    this.afterDot = false;
    this.inWord = false;
    this.inNumber = false;
    this.lastWord = "";
  }

  protected override closed(): void {
    const closedString = this.mode === Mode.Single || this.mode === Mode.Double;
    super.closed();
    if (closedString) this.resetTokenState(true);
  }

  override noteCode(text: TextWindow, pos: number): void {
    const c = text.at(pos);
    const top = this.frames[this.frames.length - 1];
    if ((top?.kind === "jsx-tag" || top?.kind === "jsx-children") && c === OPEN_BRACE) {
      this.frames.push({ kind: "expr", owner: "jsx", braceDepth: 0 });
      this.resetTokenState(false);
      return;
    }
    if (top?.kind === "expr") {
      if (c === OPEN_BRACE) {
        top.braceDepth += 1;
        this.resetTokenState(false);
        return;
      }
      if (c === CLOSE_BRACE) {
        if (top.braceDepth === 0) this.frames.pop();
        else {
          top.braceDepth -= 1;
          this.resetTokenState(false);
        }
        return;
      }
    }
    this.noteCodeChar(c);
  }

  private noteCodeChar(c: number): void {
    if (isSpace(c)) {
      this.clearPartialToken();
      return;
    }
    if (this.inNumber) {
      if (isIdentifierCode(c)) return;
      if (c === 0x2e) {
        this.inNumber = false;
        this.afterDot = false;
        this.expressionEnd = true;
        return;
      }
      this.inNumber = false;
    }
    if (!this.inWord && c >= 0x30 && c <= 0x39) {
      this.inNumber = true;
      this.afterDot = false;
      this.postfixSign = 0;
      this.lastWord = "";
      this.expressionEnd = true;
      return;
    }
    if (isIdentifierCode(c)) {
      this.postfixSign = 0;
      const ch = c <= 0x7f ? String.fromCharCode(c) : "_";
      if (this.inWord) {
        if (this.lastWord.length <= 12) this.lastWord += ch;
      } else {
        this.inWord = true;
        this.lastWord = ch;
      }
      this.expressionEnd = this.afterDot || !REGEX_AFTER_WORDS.has(this.lastWord);
      return;
    }
    this.inWord = false;
    this.afterDot = false;
    if (c === 0x2e) {
      this.afterDot = true;
      this.postfixSign = 0;
      this.expressionEnd = false;
      return;
    }
    if (c === CLOSE_PAREN || c === CLOSE_BRACKET) {
      this.postfixSign = 0;
      this.expressionEnd = true;
      return;
    }
    if (c === 0x21) {
      this.postfixSign = 0;
      return;
    }
    if (c === 0x2b || c === 0x2d) {
      if (this.postfixSign === c) {
        this.expressionEnd = true;
        this.postfixSign = 0;
      } else if (this.expressionEnd) {
        this.expressionEnd = false;
        this.postfixSign = c;
      } else {
        this.expressionEnd = false;
        this.postfixSign = 0;
      }
      return;
    }
    this.postfixSign = 0;
    this.expressionEnd = false;
  }

  override endLine(): void {
    // Quoted strings end at the line unless a backslash continues them.
    if ((this.mode === Mode.Single || this.mode === Mode.Double) && !this.escape) {
      this.closed();
    } else if (this.mode === Mode.Regex) {
      this.closed();
      this.regexClass = false;
      this.resetTokenState(true);
    } else if (this.mode === Mode.LineComment) {
      this.mode = Mode.Code;
    }
    this.escape = false;
    this.clearPartialToken();
  }

  copy(): ScriptLexer {
    const out = new ScriptLexer(this.jsx);
    out.mode = this.mode;
    out.escape = this.escape;
    out.frames = this.frames.map(copyFrame);
    out.regexClass = this.regexClass;
    out.expressionEnd = this.expressionEnd;
    out.postfixSign = this.postfixSign;
    out.afterDot = this.afterDot;
    out.inWord = this.inWord;
    out.inNumber = this.inNumber;
    out.lastWord = this.lastWord;
    return out;
  }

  equals(other: PrettyLexer): boolean {
    if (!(other instanceof ScriptLexer) || other.jsx !== this.jsx || !this.sameBase(other)) return false;
    if (
      other.regexClass !== this.regexClass ||
      other.expressionEnd !== this.expressionEnd ||
      other.postfixSign !== this.postfixSign ||
      other.afterDot !== this.afterDot ||
      other.inWord !== this.inWord ||
      other.inNumber !== this.inNumber ||
      other.lastWord !== this.lastWord ||
      other.frames.length !== this.frames.length
    ) {
      return false;
    }
    for (let i = 0; i < this.frames.length; i++) {
      if (!framesMatch(this.frames[i]!, other.frames[i]!)) return false;
    }
    return true;
  }
}

/** CSS and its preprocessors: strings, comments, and unquoted `url()`. */
export class StyleLexer extends LexerBase {
  constructor(private readonly lineComments: boolean) {
    super();
  }

  skip(text: TextWindow, pos: number, end: number, lineEnd: number): number {
    switch (this.mode) {
      case Mode.Double: return this.continueQuoted(text, pos, end, QUOTE);
      case Mode.Single: return this.continueQuoted(text, pos, end, APOS);
      case Mode.BlockComment: return this.continueBlockComment(text, pos, end);
      case Mode.LineComment: return end;
      case Mode.Url: return this.continueUrl(text, pos, end);
    }
    if (pos >= end) return pos;
    const c = text.at(pos);
    if (c === QUOTE || c === APOS) {
      this.mode = c === QUOTE ? Mode.Double : Mode.Single;
      return this.continueQuoted(text, pos + 1, end, c);
    }
    if (c === SLASH && pos + 1 < lineEnd) {
      const next = text.at(pos + 1);
      if (next === STAR) {
        this.mode = Mode.BlockComment;
        return this.continueBlockComment(text, pos + 2, end);
      }
      if (next === SLASH && this.lineComments) {
        this.mode = Mode.LineComment;
        return end;
      }
    }
    if ((c === 0x75 || c === 0x55) && pos + 4 <= lineEnd && text.slice(pos, pos + 4).toLowerCase() === "url(") {
      let i = pos + 4;
      while (i < lineEnd && isSpace(text.at(i))) i += 1;
      const first = i < lineEnd ? text.at(i) : 0;
      if (first !== QUOTE && first !== APOS) {
        this.mode = Mode.Url;
        return this.continueUrl(text, i, end);
      }
    }
    return pos;
  }

  private continueUrl(text: TextWindow, pos: number, end: number): number {
    for (let i = pos; i < end; i++) {
      if (text.at(i) === CLOSE_PAREN) {
        this.closed();
        return i + 1;
      }
    }
    return end;
  }

  override endLine(): void {
    if (this.mode === Mode.BlockComment) this.escape = false;
    else this.closed();
  }

  copy(): StyleLexer {
    const out = new StyleLexer(this.lineComments);
    out.mode = this.mode;
    out.escape = this.escape;
    return out;
  }

  equals(other: PrettyLexer): boolean {
    return other instanceof StyleLexer && other.lineComments === this.lineComments && this.sameBase(other);
  }
}
