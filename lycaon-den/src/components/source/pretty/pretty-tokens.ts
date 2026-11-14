import type { StreamParser } from "@codemirror/language";
import type { Text } from "@codemirror/state";
import type { Tree } from "@lezer/common";
import { highlightTree, tagHighlighter, tags } from "@lezer/highlight";
import type { PrettyLexer } from "./pretty-lexers.ts";
import { StreamLineTokens } from "./pretty-stream-tokens.ts";
import type { TextWindow } from "./pretty-text-window.ts";

const PROTECTED = "p";

// Strings, comments and regular expressions are never broken; every mode
// that has them tags them, including the stream and word-list modes.
const PROTECTED_CLASSES = tagHighlighter([
  { tag: [tags.string, tags.comment, tags.regexp, tags.character, tags.special(tags.string)], class: PROTECTED },
]);

const WINDOW = 4096;

/** Protected text, read from the editor's syntax tree. */
export interface PrettyTokenSource {
  /** Where the protected token covering `pos` ends, or -1 when `pos` is code. */
  protectedEnd(pos: number): number;
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
  private readonly streamTokens: StreamLineTokens | null;

  constructor(
    private readonly tree: Tree,
    private readonly doc?: Text,
    streamParser?: StreamParser<unknown> | null,
    tabSize = 4,
    indentUnit = 2,
  ) {
    this.streamTokens = streamParser ? new StreamLineTokens(streamParser, tabSize, indentUnit) : null;
  }

  get parsedTo(): number {
    return this.tree.length;
  }

  private load(pos: number): void {
    if (this.streamTokens && this.doc) {
      const line = this.doc.lineAt(pos);
      if (this.streamTokens.usesStreamAt(this.doc, line.number)) {
        const win = this.streamTokens.loadWindow(this.doc, pos);
        this.from = win.from;
        this.to = Math.min(this.tree.length, win.to);
        this.starts = win.starts;
        this.ends = win.ends;
        return;
      }
    }
    this.from = pos;
    this.to = Math.min(this.tree.length, pos + WINDOW);
    this.starts = [];
    this.ends = [];
    highlightTree(this.tree, PROTECTED_CLASSES, (from, to) => {
      const last = this.starts.length - 1;
      if (last >= 0 && this.ends[last] === from) this.ends[last] = to;
      else {
        this.starts.push(from);
        this.ends.push(to);
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
    if (i < 0 || this.ends[i]! <= pos) return -1;
    let end = this.ends[i]!;
    while (end === this.to && end < this.tree.length) {
      this.load(end);
      if (this.starts.length === 0 || this.starts[0]! > end) break;
      end = this.ends[0]!;
    }
    return end;
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
