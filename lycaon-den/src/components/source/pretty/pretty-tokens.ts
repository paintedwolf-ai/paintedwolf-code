import type { Tree } from "@lezer/common";
import { highlightTree, tagHighlighter, tags } from "@lezer/highlight";
import type { PrettyLexer } from "./pretty-lexers.ts";
import type { TextWindow } from "./pretty-text-window.ts";

const PROTECTED = "p";
const KEYWORD = "k";

// Strings, comments and regular expressions are never broken; every mode
// that has them tags them, including the stream and word-list modes.
const TOKEN_CLASSES = tagHighlighter([
  { tag: [tags.string, tags.comment, tags.regexp, tags.character, tags.special(tags.string)], class: PROTECTED },
  { tag: tags.keyword, class: KEYWORD },
]);

const WINDOW = 4096;

/** Protected text and keywords, read from the editor's syntax tree. */
export interface PrettyTokenSource {
  /** Where the protected token covering `pos` ends, or -1 when `pos` is code. */
  protectedEnd(pos: number): number;
  /** Where a keyword starting at `pos` ends, or -1. */
  keywordEnd(pos: number): number;
}

/**
 * Tokens of one syntax tree, highlighted a window at a time. Only text the
 * tree covers is known; the scanner never reads past it.
 */
export class TreeTokens implements PrettyTokenSource {
  private from = 0;
  private to = -1;
  private starts: number[] = [];
  private ends: number[] = [];
  private kinds: string[] = [];

  constructor(private readonly tree: Tree) {}

  get parsedTo(): number {
    return this.tree.length;
  }

  private load(pos: number): void {
    this.from = pos;
    this.to = Math.min(this.tree.length, pos + WINDOW);
    this.starts = [];
    this.ends = [];
    this.kinds = [];
    highlightTree(this.tree, TOKEN_CLASSES, (from, to, classes) => {
      const kind = classes.includes(PROTECTED) ? PROTECTED : classes.includes(KEYWORD) ? KEYWORD : "";
      if (!kind) return;
      const last = this.starts.length - 1;
      if (last >= 0 && this.kinds[last] === kind && this.ends[last] === from) this.ends[last] = to;
      else {
        this.starts.push(from);
        this.ends.push(to);
        this.kinds.push(kind);
      }
    }, this.from, this.to);
  }

  /** Index of the last span starting at or before `pos`, loading the window around it. */
  private find(pos: number): number {
    if (pos < this.from || pos >= this.to) this.load(pos);
    let lo = 0;
    let hi = this.starts.length - 1;
    let found = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (this.starts[mid]! <= pos) {
        found = mid;
        lo = mid + 1;
      } else {
        hi = mid - 1;
      }
    }
    return found;
  }

  protectedEnd(pos: number): number {
    if (pos >= this.tree.length) return -1;
    const i = this.find(pos);
    if (i < 0 || this.kinds[i] !== PROTECTED || this.ends[i]! <= pos) return -1;
    return this.ends[i]!;
  }

  keywordEnd(pos: number): number {
    if (pos >= this.tree.length) return -1;
    const i = this.find(pos);
    if (i < 0 || this.kinds[i] !== KEYWORD || this.starts[i] !== pos) return -1;
    return this.ends[i]!;
  }
}

/** A lexer that takes protected text from the syntax tree; it carries no state of its own. */
export class TreeLexer implements PrettyLexer {
  skip(text: TextWindow, pos: number, end: number): number {
    const protectedEnd = text.tokens?.protectedEnd(pos) ?? -1;
    return protectedEnd > pos ? Math.min(protectedEnd, end) : pos;
  }

  noteCode(): void {}

  inProtected(): boolean {
    return false;
  }

  endLine(): void {}

  copy(): TreeLexer {
    return this;
  }

  equals(other: PrettyLexer): boolean {
    return other instanceof TreeLexer;
  }
}
