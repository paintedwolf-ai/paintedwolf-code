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
   * stopped. Returns `pos` when the character there is code.
   */
  skip(text: TextWindow, pos: number, end: number): number;
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

const enum Mode {
  Code,
  Single,
  Double,
  BlockComment,
  LineComment,
  Template,
  Regex,
  Url,
}

function isSpace(code: number): boolean {
  return code === 0x20 || code === 0x09 || code === 0x0d;
}

function isIdentifierCode(code: number): boolean {
  return (
    (code >= 0x61 && code <= 0x7a) ||
    (code >= 0x41 && code <= 0x5a) ||
    (code >= 0x30 && code <= 0x39) ||
    code === 0x5f ||
    code === DOLLAR ||
    code > 0x7f
  );
}

/** Shared resumable scanning of quoted strings and block and line comments. */
abstract class LexerBase implements PrettyLexer {
  protected mode = Mode.Code;
  protected escape = false;

  abstract skip(text: TextWindow, pos: number, end: number): number;
  abstract copy(): PrettyLexer;

  noteCode(_text: TextWindow, _pos: number): void {}

  inProtected(): boolean {
    return this.mode !== Mode.Code;
  }

  endLine(): void {
    if (this.mode === Mode.LineComment) this.mode = Mode.Code;
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
        // A star ended the previous chunk.
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

  skip(text: TextWindow, pos: number, end: number): number {
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
    if (this.options.comments && c === SLASH && pos + 1 < end) {
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
    if (this.mode !== Mode.BlockComment) this.closed();
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

const REGEX_AFTER = new Set([
  0, 0x28, 0x2c, 0x3d, 0x3a, 0x5b, 0x21, 0x26, 0x7c, 0x3f, 0x7b, 0x7d, 0x3b,
  0x2b, 0x2d, 0x7e, 0x2a, 0x25, 0x3c, 0x3e, 0x5e,
]);

const REGEX_AFTER_WORDS = new Set([
  "return", "case", "throw", "delete", "void", "typeof", "instanceof", "in",
  "of", "yield", "await", "else", "do", "new",
]);

/**
 * JavaScript and TypeScript: strings, comments, regular expressions, and
 * template literals, which are protected whole including their `${}`
 * expressions.
 */
export class ScriptLexer extends LexerBase {
  // Each entry is a template (-1) or an expression inside one, counting its open braces.
  private templates: number[] = [];
  private expressionQuote = 0;
  private regexClass = false;
  private lastCode = 0;
  private lastWord = "";

  skip(text: TextWindow, pos: number, end: number): number {
    switch (this.mode) {
      case Mode.Double: return this.continueQuoted(text, pos, end, QUOTE);
      case Mode.Single: return this.continueQuoted(text, pos, end, APOS);
      case Mode.BlockComment: return this.continueBlockComment(text, pos, end);
      case Mode.LineComment: return end;
      case Mode.Template: return this.continueTemplate(text, pos, end);
      case Mode.Regex: return this.continueRegex(text, pos, end);
    }
    if (pos >= end) return pos;
    const c = text.at(pos);
    if (c === QUOTE || c === APOS) {
      this.mode = c === QUOTE ? Mode.Double : Mode.Single;
      return this.continueQuoted(text, pos + 1, end, c);
    }
    if (c === BACKTICK) {
      this.mode = Mode.Template;
      this.templates = [-1];
      return this.continueTemplate(text, pos + 1, end);
    }
    if (c === SLASH && pos + 1 < end) {
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
    if (c === SLASH && this.regexAllowed()) {
      this.mode = Mode.Regex;
      this.regexClass = false;
      return this.continueRegex(text, pos + 1, end);
    }
    return pos;
  }

  private regexAllowed(): boolean {
    if (REGEX_AFTER.has(this.lastCode)) return true;
    return isIdentifierCode(this.lastCode) && REGEX_AFTER_WORDS.has(this.lastWord);
  }

  private continueRegex(text: TextWindow, pos: number, end: number): number {
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
        while (i < end && isIdentifierCode(text.at(i))) i += 1;
        this.closedWith(SLASH);
        return i;
      }
    }
    return i;
  }

  private continueTemplate(text: TextWindow, pos: number, end: number): number {
    let i = pos;
    while (i < end) {
      const c = text.at(i);
      i += 1;
      const top = this.templates[this.templates.length - 1]!;
      if (this.escape) {
        this.escape = false;
        continue;
      }
      if (c === BACKSLASH) {
        this.escape = true;
        continue;
      }
      if (top === -1) {
        if (c === BACKTICK) {
          this.templates.pop();
          if (this.templates.length === 0) {
            this.closedWith(BACKTICK);
            return i;
          }
        } else if (c === DOLLAR && i < end && text.at(i) === OPEN_BRACE) {
          this.templates.push(0);
          i += 1;
        }
        continue;
      }
      if (this.expressionQuote !== 0) {
        if (c === this.expressionQuote) this.expressionQuote = 0;
        continue;
      }
      if (c === QUOTE || c === APOS) this.expressionQuote = c;
      else if (c === BACKTICK) this.templates.push(-1);
      else if (c === OPEN_BRACE) this.templates[this.templates.length - 1] = top + 1;
      else if (c === CLOSE_BRACE) {
        if (top === 0) this.templates.pop();
        else this.templates[this.templates.length - 1] = top - 1;
      }
    }
    return i;
  }

  private closedWith(code: number): void {
    this.closed();
    this.lastCode = code;
    this.lastWord = "";
  }

  protected override closed(): void {
    const closedString = this.mode === Mode.Single || this.mode === Mode.Double;
    super.closed();
    if (closedString) {
      this.lastCode = QUOTE;
      this.lastWord = "";
    }
  }

  override noteCode(text: TextWindow, pos: number): void {
    const c = text.at(pos);
    if (isSpace(c)) return;
    if (isIdentifierCode(c)) {
      this.lastWord = isIdentifierCode(this.lastCode) && this.lastWord.length < 12 ? this.lastWord + String.fromCharCode(c) : String.fromCharCode(c);
    } else {
      this.lastWord = "";
    }
    this.lastCode = c;
  }

  override endLine(): void {
    // Quoted strings end at the line unless a backslash continues them.
    if ((this.mode === Mode.Single || this.mode === Mode.Double) && !this.escape) this.closed();
    else if (this.mode === Mode.Regex || this.mode === Mode.LineComment) this.closed();
    else if (this.mode === Mode.Single || this.mode === Mode.Double) this.escape = false;
  }

  copy(): ScriptLexer {
    const out = new ScriptLexer();
    out.mode = this.mode;
    out.escape = this.escape;
    out.templates = this.templates.slice();
    out.expressionQuote = this.expressionQuote;
    out.regexClass = this.regexClass;
    out.lastCode = this.lastCode;
    out.lastWord = this.lastWord;
    return out;
  }

  equals(other: PrettyLexer): boolean {
    if (!(other instanceof ScriptLexer) || !this.sameBase(other)) return false;
    if (other.lastCode !== this.lastCode || other.lastWord !== this.lastWord) return false;
    if (other.expressionQuote !== this.expressionQuote || other.regexClass !== this.regexClass) return false;
    if (other.templates.length !== this.templates.length) return false;
    for (let i = 0; i < this.templates.length; i++) if (other.templates[i] !== this.templates[i]) return false;
    return true;
  }
}

/** CSS and its preprocessors: strings, comments, and unquoted `url()`. */
export class StyleLexer extends LexerBase {
  constructor(private readonly lineComments: boolean) {
    super();
  }

  skip(text: TextWindow, pos: number, end: number): number {
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
    if (c === SLASH && pos + 1 < end) {
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
    if ((c === 0x75 || c === 0x55) && pos + 4 <= end && text.slice(pos, pos + 4).toLowerCase() === "url(") {
      let i = pos + 4;
      while (i < end && isSpace(text.at(i))) i += 1;
      const first = i < end ? text.at(i) : 0;
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
    if (this.mode !== Mode.BlockComment) this.closed();
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
