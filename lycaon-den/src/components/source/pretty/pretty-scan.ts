import type { Text } from "@codemirror/state";
import type { PrettyLexer } from "./pretty-lexers.ts";
import { TextWindow } from "./pretty-text-window.ts";

/** Laid-out groups stay flat when their visual line fits this many columns. */
export const PRETTY_TARGET_COLUMNS = 80;
/** Outside data, only source lines wider than this are laid out. */
export const PRETTY_TRIGGER_COLUMNS = 240;
/** Characters between saved scan states. */
export const PRETTY_CHECKPOINT_INTERVAL = 2048;
/** Open groups and containers allowed at once. */
export const PRETTY_MAX_DEPTH = 512;

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
const LESS = 0x3c;
const GREATER = 0x3e;
const SLASH = 0x2f;
const QUOTE = 0x22;
const APOS = 0x27;
const BACKTICK = 0x60;
const BANG = 0x21;
const QUESTION = 0x3f;
const DASH = 0x2d;

/** Words after `}` that continue a statement rather than start one. */
const CONTINUATIONS = new Set([
  "else", "catch", "finally", "while", "from", "as", "satisfies", "of", "in", "instanceof",
]);

const VOID_TAGS = new Set([
  "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr",
]);
const RAW_TAGS = new Set(["pre", "code", "textarea", "script", "style"]);

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

function isTagNameCode(code: number): boolean {
  return isWordCode(code) || code === DASH || code === 0x3a || code === 0x2e;
}

/** A bracket layout: how groups, separators and closers break. */
export interface BracketRule {
  readonly kind: "bracket";
  /** Every non-empty `{}` and `[]` expands, and every line is laid out. */
  readonly data: boolean;
  /** `;` separates statements. */
  readonly statements: boolean;
  /** Which tokens after a `}` start a new visual line. */
  readonly afterBlock: "script" | "style" | null;
  /** Each line is its own record; containers never span lines. */
  readonly records: boolean;
  readonly lexer: () => PrettyLexer;
}

/** Breaks between adjacent tags. */
export interface MarkupRule {
  readonly kind: "markup";
}

export type PrettyRule = BracketRule | MarkupRule;

/** Where a document's rules apply. */
export type PrettyPlan =
  | { readonly kind: "document"; readonly rule: PrettyRule }
  | { readonly kind: "fenced"; readonly fenceRule: (info: string) => PrettyRule | null }
  | { readonly kind: "spans"; readonly spans: readonly PrettySpan[] };

export interface PrettySpan {
  readonly from: number;
  readonly to: number;
  readonly rule: PrettyRule;
}

/** A group opened on the current source line. */
class Frame {
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
class Container {
  constructor(readonly kind: number, readonly depth: number, readonly parent: Container | null) {}
}

function framesEqual(a: Frame | null, b: Frame | null): boolean {
  while (a && b) {
    if (a === b) return true;
    if (a.kind !== b.kind || a.expanded !== b.expanded || a.block !== b.block || a.childIndent !== b.childIndent) return false;
    a = a.parent;
    b = b.parent;
  }
  return a === b;
}

function containersEqual(a: Container | null, b: Container | null): boolean {
  while (a && b) {
    if (a === b) return true;
    if (a.kind !== b.kind) return false;
    a = a.parent;
    b = b.parent;
  }
  return a === b;
}

interface Fence {
  readonly char: number;
  readonly length: number;
  readonly rule: PrettyRule | null;
}

type TagKind = "open" | "close" | "self";

/** A tag being read: its kind and name are known at `<`, its end at `>`. */
interface OpenTag {
  readonly kind: TagKind;
  readonly name: string;
  readonly quote: number;
  readonly slash: boolean;
}

interface MarkupState {
  readonly comment: boolean;
  readonly cdata: boolean;
  readonly rawTag: string | null;
  readonly tag: OpenTag | null;
  readonly previous: TagKind | null;
  readonly previousName: string;
  readonly adjacent: boolean;
  readonly depth: number;
}

const INITIAL_MARKUP: MarkupState = {
  comment: false, cdata: false, rawTag: null, tag: null, previous: null, previousName: "", adjacent: false, depth: 0,
};

function tagsEqual(a: OpenTag | null, b: OpenTag | null): boolean {
  return a === b || (!!a && !!b && a.kind === b.kind && a.name === b.name && a.quote === b.quote && a.slash === b.slash);
}

/** Scan state at a position, with positions stored as distances back from it. */
export interface PrettySnapshot {
  readonly rule: PrettyRule | null;
  readonly lexer: PrettyLexer | null;
  readonly frames: Frame | null;
  readonly containers: Container | null;
  readonly markup: MarkupState;
  readonly fence: Fence | null;
  readonly inLine: boolean;
  readonly laidOut: boolean;
  readonly leadColumns: number;
  readonly hasContent: boolean;
  readonly col: number;
  readonly pendingAhead: number;
  readonly pendingColumns: number;
  readonly visualBack: number;
  readonly visualColumns: number;
}

export function snapshotsEqual(a: PrettySnapshot, b: PrettySnapshot): boolean {
  if (a.rule !== b.rule || a.inLine !== b.inLine || a.laidOut !== b.laidOut || a.leadColumns !== b.leadColumns) return false;
  if (a.hasContent !== b.hasContent || a.col !== b.col || a.pendingAhead !== b.pendingAhead) return false;
  if (a.pendingAhead >= 0 && a.pendingColumns !== b.pendingColumns) return false;
  if (a.visualBack !== b.visualBack || (a.visualBack >= 0 && a.visualColumns !== b.visualColumns)) return false;
  if ((a.lexer === null) !== (b.lexer === null) || (a.lexer && !a.lexer.equals(b.lexer!))) return false;
  if (!framesEqual(a.frames, b.frames) || !containersEqual(a.containers, b.containers)) return false;
  const ma = a.markup;
  const mb = b.markup;
  if (ma.comment !== mb.comment || ma.cdata !== mb.cdata || ma.rawTag !== mb.rawTag || ma.previous !== mb.previous) return false;
  if (!tagsEqual(ma.tag, mb.tag)) return false;
  if (ma.previousName !== mb.previousName || ma.adjacent !== mb.adjacent || ma.depth !== mb.depth) return false;
  const fa = a.fence;
  const fb = b.fence;
  return fa === fb || (!!fa && !!fb && fa.char === fb.char && fa.length === fb.length && fa.rule === fb.rule);
}

/** Receives breaks and the indentation of each synthetic visual line, in document order. */
export interface PrettySink {
  break(pos: number): void;
  indent(from: number, to: number, columns: number): void;
}

/** Remembered answers to "does this `{}` directly hold a `;`", by opening position. */
export interface PrettyBlockFacts {
  get(open: number): boolean | undefined;
  set(open: number, end: number, block: boolean): void;
}

export interface PrettyScanOptions {
  readonly doc: Text;
  readonly plan: PrettyPlan;
  readonly tabSize: number;
  readonly indentUnit: number;
  readonly facts: PrettyBlockFacts;
}

export type PrettyScanFailure = "depth";

/**
 * Streams one document through its layout rules. The scanner keeps all state
 * needed to resume at a position, so a rescan after an edit starts at a saved
 * state before the edit and stops where the new state matches the old one.
 */
export class PrettyScanner {
  readonly text: TextWindow;
  /** Characters examined, including lookahead. */
  visits = 0;
  failure: PrettyScanFailure | null = null;

  private rule: PrettyRule | null = null;
  private lexer: PrettyLexer | null = null;
  private frames: Frame | null = null;
  private containers: Container | null = null;
  private markup: MarkupState = INITIAL_MARKUP;
  private fence: Fence | null = null;
  private inLine = false;
  private laidOut = false;
  private leadColumns = 0;
  private hasContent = false;
  private col = 0;
  private pending = -1;
  private pendingColumns = 0;
  private visualFrom = -1;
  private visualColumns = 0;
  private lineEnd = 0;
  private spanEnd = -1;
  private spanIndex = 0;
  private spanEntered = -1;

  constructor(private readonly options: PrettyScanOptions, private readonly sink: PrettySink) {
    this.text = new TextWindow(options.doc);
    if (options.plan.kind === "document") this.enterRule(options.plan.rule);
  }

  snapshot(pos: number): PrettySnapshot {
    return {
      rule: this.rule,
      lexer: this.lexer?.copy() ?? null,
      frames: this.frames,
      containers: this.containers,
      markup: this.markup,
      fence: this.fence,
      inLine: this.inLine,
      laidOut: this.laidOut,
      leadColumns: this.leadColumns,
      hasContent: this.hasContent,
      col: this.col,
      pendingAhead: this.pending < 0 ? -1 : this.pending - pos,
      pendingColumns: this.pendingColumns,
      visualBack: this.visualFrom < 0 ? -1 : pos - this.visualFrom,
      visualColumns: this.visualColumns,
    };
  }

  restore(snapshot: PrettySnapshot, pos: number): void {
    this.rule = snapshot.rule;
    this.lexer = snapshot.lexer?.copy() ?? null;
    this.frames = snapshot.frames;
    this.containers = snapshot.containers;
    this.markup = snapshot.markup;
    this.fence = snapshot.fence;
    this.inLine = snapshot.inLine;
    this.laidOut = snapshot.laidOut;
    this.leadColumns = snapshot.leadColumns;
    this.hasContent = snapshot.hasContent;
    this.col = snapshot.col;
    this.pending = snapshot.pendingAhead < 0 ? -1 : pos + snapshot.pendingAhead;
    this.pendingColumns = snapshot.pendingColumns;
    this.visualFrom = snapshot.visualBack < 0 ? -1 : pos - snapshot.visualBack;
    this.visualColumns = snapshot.visualColumns;
    if (this.inLine) this.lineEnd = this.options.doc.lineAt(pos).to;
  }

  /** Where the visual line open at the last restore or snapshot began, or -1. */
  get visualStart(): number {
    return this.visualFrom;
  }

  /**
   * Scans from `pos`. Whenever the position reaches `stop`, `onStop` is called
   * with the position and returns the next stop, or -1 to halt. Returns the
   * position reached.
   */
  run(pos: number, stop: number, onStop: (pos: number) => number): number {
    const doc = this.options.doc;
    while (pos <= doc.length) {
      if (pos >= stop) {
        stop = onStop(pos);
        if (stop < 0) return pos;
      }
      if (this.failure) return pos;
      if (!this.inLine) {
        const line = doc.lineAt(pos);
        pos = this.startLine(line.from, line.to);
        if (!this.inLine) {
          if (line.to >= doc.length) return doc.length;
          pos = line.to + 1;
          continue;
        }
      }
      const end = Math.min(this.spanEnd >= 0 ? this.spanEnd : this.lineEnd, stop);
      while (pos < end && !this.failure) pos = this.step(pos, end);
      if (pos >= (this.spanEnd >= 0 ? this.spanEnd : this.lineEnd)) {
        this.endLine();
        const lineTo = this.lineEnd;
        if (lineTo >= doc.length) return doc.length;
        pos = lineTo + 1;
      }
    }
    return pos;
  }

  private enterRule(rule: PrettyRule | null): void {
    this.rule = rule;
    this.lexer = rule?.kind === "bracket" ? rule.lexer() : null;
    this.frames = null;
    this.containers = null;
    this.markup = INITIAL_MARKUP;
  }

  /** Starts the line; returns the first position to scan, leaving `inLine` false when nothing on it is laid out. */
  private startLine(from: number, to: number): number {
    const plan = this.options.plan;
    this.lineEnd = to;
    this.spanEnd = -1;
    let start = from;
    if (plan.kind === "fenced") {
      const head = this.text.slice(from, Math.min(to, from + 200));
      if (this.fence) {
        if (to - from <= 200 && isClosingFence(head, this.fence)) {
          this.fence = null;
          this.enterRule(null);
          return from;
        }
      } else {
        const open = parseOpeningFence(head);
        if (open) {
          const rule = plan.fenceRule(open.info);
          this.fence = { char: open.char, length: open.length, rule };
          this.enterRule(rule);
        }
        return from;
      }
      if (!this.rule) return from;
    } else if (plan.kind === "spans") {
      const spans = plan.spans;
      while (this.spanIndex < spans.length && spans[this.spanIndex]!.to <= from) this.spanIndex += 1;
      const span = spans[this.spanIndex];
      if (!span || span.from > to) return from;
      if (span.from >= from) start = span.from;
      if (this.spanEntered !== this.spanIndex) {
        this.spanEntered = this.spanIndex;
        this.enterRule(span.rule);
      }
      if (span.to < to) this.spanEnd = span.to;
    }
    const rule = this.rule;
    if (!rule) return from;
    this.inLine = true;
    let lead = from;
    let leadColumns = 0;
    for (; lead < to; lead++) {
      const c = this.text.at(lead);
      if (c === SPACE) leadColumns += 1;
      else if (c === TAB) leadColumns += this.options.tabSize;
      else break;
    }
    this.leadColumns = leadColumns;
    this.laidOut = (rule.kind === "bracket" && rule.data) || this.wider(from, to, PRETTY_TRIGGER_COLUMNS);
    this.hasContent = false;
    this.col = 0;
    this.pending = -1;
    this.visualFrom = -1;
    if (rule.kind === "bracket" && rule.records) this.containers = null;
    if (rule.kind === "markup") this.markup = { ...this.markup, previous: null, adjacent: false, depth: 0 };
    return start;
  }

  private wider(from: number, to: number, columns: number): boolean {
    const length = to - from;
    if (length > columns) return true;
    if (length * this.options.tabSize <= columns) return false;
    let width = 0;
    for (let i = from; i < to; i++) {
      width += this.text.at(i) === TAB ? this.options.tabSize : 1;
      if (width > columns) return true;
    }
    return false;
  }

  private endLine(): void {
    const end = this.spanEnd >= 0 ? this.spanEnd : this.lineEnd;
    if (this.visualFrom >= 0 && this.visualColumns > 0 && end > this.visualFrom) {
      this.sink.indent(this.visualFrom, end, this.visualColumns);
    }
    this.visualFrom = -1;
    this.pending = -1;
    this.inLine = false;
    if (this.rule?.kind === "bracket") {
      const open: Frame[] = [];
      for (let f = this.frames; f; f = f.parent) open.push(f);
      for (let i = open.length - 1; i >= 0; i--) {
        const depth = (this.containers?.depth ?? 0) + 1;
        this.containers = new Container(open[i]!.kind, depth, this.containers);
      }
      this.frames = null;
      this.lexer!.endLine();
    }
    if (this.spanEnd >= 0) this.enterRule(null);
  }

  private emitBreak(pos: number, columns: number): void {
    if (pos === this.visualFrom) return;
    if (this.visualFrom >= 0 && this.visualColumns > 0 && pos > this.visualFrom) {
      this.sink.indent(this.visualFrom, pos, this.visualColumns);
    }
    this.sink.break(pos);
    this.visualFrom = pos;
    this.visualColumns = columns;
    this.col = columns;
  }

  private columns(indent: number): number {
    return this.leadColumns + indent * this.options.indentUnit;
  }

  private skipSpaces(pos: number, end: number): number {
    while (pos < end) {
      const c = this.text.at(pos);
      if (c !== SPACE && c !== TAB) break;
      pos += 1;
    }
    this.visits += 1;
    return pos;
  }

  private step(pos: number, end: number): number {
    if (pos === this.pending) {
      this.pending = -1;
      this.emitBreak(pos, this.pendingColumns);
    }
    return this.rule!.kind === "bracket" ? this.stepBracket(this.rule as BracketRule, pos, end) : this.stepMarkup(pos);
  }

  private stepBracket(rule: BracketRule, pos: number, end: number): number {
    const text = this.text;
    const lexer = this.lexer!;
    const skipped = lexer.skip(text, pos, end, this.lineEnd);
    if (skipped > pos) {
      this.visits += skipped - pos;
      this.col += skipped - pos;
      this.hasContent = true;
      return skipped;
    }
    this.visits += 1;
    const c = text.at(pos);
    lexer.noteCode(text, pos);
    if (c === SPACE) {
      this.col += 1;
      return pos + 1;
    }
    if (c === TAB) {
      this.col += this.options.tabSize;
      return pos + 1;
    }
    const lineEnd = this.lineEnd;
    if (isOpener(c)) this.open(rule, c, pos, lineEnd);
    else if (isCloser(c)) this.close(rule, c, pos, lineEnd);
    else if (c === COMMA || (c === SEMICOLON && rule.statements)) this.separate(c, pos, lineEnd);
    else this.col += 1;
    this.hasContent = true;
    return pos + 1;
  }

  private open(rule: BracketRule, c: number, pos: number, lineEnd: number): void {
    const depth = (this.frames?.depth ?? this.containers?.depth ?? 0) + 1;
    if (depth > PRETTY_MAX_DEPTH) {
      this.failure = "depth";
      return;
    }
    const childIndent = (this.frames?.childIndent ?? 0) + 1;
    let expanded = false;
    let block = false;
    if (this.laidOut) {
      const next = this.skipSpaces(pos + 1, lineEnd);
      const nonEmpty = next < lineEnd && this.text.at(next) !== closerOf(c);
      if (nonEmpty) {
        expanded = rule.data && c !== OPEN_PAREN ? true : !this.fits(rule, pos, lineEnd);
        if (expanded && c === OPEN_BRACE && rule.statements) block = this.blockFact(pos, lineEnd);
        if (expanded) {
          this.pending = next;
          this.pendingColumns = this.columns(childIndent);
        }
      }
    }
    this.frames = new Frame(c, expanded, block, childIndent, depth, this.frames);
    this.col += 1;
  }

  private close(rule: BracketRule, c: number, pos: number, lineEnd: number): void {
    const top = this.frames;
    if (top && top.kind === openerOf(c)) {
      if (this.laidOut && top.expanded) this.emitBreak(pos, this.columns(top.childIndent - 1));
      this.frames = top.parent;
    } else if (!top && this.containers && this.containers.kind === openerOf(c)) {
      if (this.laidOut && this.hasContent) this.emitBreak(pos, this.leadColumns);
      this.containers = this.containers.parent;
    } else {
      this.col += 1;
      return;
    }
    this.col += 1;
    if (this.laidOut && c === CLOSE_BRACE && this.breaksAfterBlock(rule, pos, lineEnd)) {
      this.pending = this.skipSpaces(pos + 1, lineEnd);
      this.pendingColumns = this.columns(this.frames?.childIndent ?? 0);
    }
  }

  private separate(c: number, pos: number, lineEnd: number): void {
    this.col += 1;
    if (!this.laidOut) return;
    const group = this.frames;
    const next = this.skipSpaces(pos + 1, lineEnd);
    if (next >= lineEnd || isCloser(this.text.at(next))) return;
    if (!this.separatorBreaks(c, group)) return;
    this.pending = next;
    this.pendingColumns = this.columns(group?.childIndent ?? 0);
  }

  private separatorBreaks(c: number, group: Frame | null): boolean {
    if (group) {
      if (!group.expanded) return false;
      return c !== COMMA || group.kind !== OPEN_BRACE || !group.block;
    }
    return c === SEMICOLON || (this.containers?.kind ?? 0) !== 0;
  }

  /** Whether the `}` at `pos` is followed by a token that starts a new visual line. */
  private breaksAfterBlock(rule: BracketRule, pos: number, lineEnd: number): boolean {
    if (!rule.afterBlock) return false;
    const next = this.skipSpaces(pos + 1, lineEnd);
    if (next >= lineEnd) return false;
    const c = this.text.at(next);
    if (c === QUOTE || c === APOS || c === BACKTICK || c === SLASH) return false;
    if (rule.afterBlock === "style") return c !== CLOSE_BRACE && c !== COMMA && c !== SEMICOLON;
    if (!isWordStart(c)) return false;
    let wordEnd = next + 1;
    while (wordEnd < lineEnd && wordEnd - next < 12 && isWordCode(this.text.at(wordEnd))) wordEnd += 1;
    return !CONTINUATIONS.has(this.text.slice(next, wordEnd));
  }

  private nonEmptyAt(pos: number, lineEnd: number): boolean {
    const next = this.skipSpaces(pos + 1, lineEnd);
    return next < lineEnd && this.text.at(next) !== closerOf(this.text.at(pos));
  }

  /**
   * Whether the group opening at `open` stays flat: its visual line, up to the
   * next place the enclosing layout breaks, fits the target width, and nothing
   * inside it breaks on its own (a data container, or a break after a block).
   */
  private fits(rule: BracketRule, open: number, lineEnd: number): boolean {
    const text = this.text;
    const remaining = PRETTY_TARGET_COLUMNS - this.col;
    if (remaining <= 0) return false;
    const lexer = this.lexer!.copy();
    const stack: number[] = [];
    let width = 0;
    let pos = open;
    for (;;) {
      if (pos >= lineEnd) return false;
      const skipped = lexer.skip(text, pos, Math.min(lineEnd, pos + remaining - width + 1), lineEnd);
      if (skipped > pos) {
        this.visits += skipped - pos;
        width += skipped - pos;
        if (width > remaining) return false;
        pos = skipped;
        continue;
      }
      this.visits += 1;
      const c = text.at(pos);
      lexer.noteCode(text, pos);
      width += c === TAB ? this.options.tabSize : 1;
      if (width > remaining) return false;
      if (isOpener(c)) {
        stack.push(c);
        if (rule.data && c !== OPEN_PAREN && stack.length > 1 && this.nonEmptyAt(pos, lineEnd)) return false;
      } else if (isCloser(c) && stack[stack.length - 1] === openerOf(c)) {
        stack.pop();
        if (stack.length === 0) {
          pos += 1;
          break;
        }
        if (c === CLOSE_BRACE && this.breaksAfterBlock(rule, pos, lineEnd)) return false;
      }
      pos += 1;
    }
    if (text.at(open) === OPEN_BRACE && this.breaksAfterBlock(rule, pos - 1, lineEnd)) return true;
    const parent = this.frames;
    for (;;) {
      if (pos >= lineEnd) return true;
      const skipped = lexer.skip(text, pos, Math.min(lineEnd, pos + remaining - width + 1), lineEnd);
      if (skipped > pos) {
        this.visits += skipped - pos;
        width += skipped - pos;
        if (width > remaining) return false;
        pos = skipped;
        continue;
      }
      this.visits += 1;
      const c = text.at(pos);
      lexer.noteCode(text, pos);
      if (c === COMMA || (c === SEMICOLON && rule.statements)) {
        const next = this.skipSpaces(pos + 1, lineEnd);
        if (next >= lineEnd) return width + 1 <= remaining;
        if (!isCloser(text.at(next)) && this.separatorBreaks(c, parent)) return width + (next - pos) <= remaining;
      } else if (isOpener(c)) {
        if (width + 1 > remaining) return false;
        if (this.nonEmptyAt(pos, lineEnd)) return true;
      } else if (isCloser(c)) {
        const closesParent = parent ? parent.kind === openerOf(c) : this.containers?.kind === openerOf(c);
        if (closesParent) return true;
      }
      width += c === TAB ? this.options.tabSize : 1;
      if (width > remaining) return false;
      pos += 1;
    }
  }

  /** Whether the `{}` opening at `open` directly holds a `;`, from memory or by reading to its end. */
  private blockFact(open: number, lineEnd: number): boolean {
    const known = this.options.facts.get(open);
    if (known !== undefined) return known;
    const text = this.text;
    const lexer = this.lexer!.copy();
    const stack: Array<{ kind: number; open: number; semicolon: boolean }> = [{ kind: OPEN_BRACE, open, semicolon: false }];
    let pos = open + 1;
    while (pos < lineEnd && stack.length > 0) {
      const skipped = lexer.skip(text, pos, lineEnd, lineEnd);
      if (skipped > pos) {
        this.visits += skipped - pos;
        pos = skipped;
        continue;
      }
      this.visits += 1;
      const c = text.at(pos);
      lexer.noteCode(text, pos);
      if (isOpener(c)) {
        stack.push({ kind: c, open: pos, semicolon: false });
      } else if (isCloser(c) && stack[stack.length - 1]!.kind === openerOf(c)) {
        const frame = stack.pop()!;
        if (frame.kind === OPEN_BRACE) this.options.facts.set(frame.open, pos + 1, frame.semicolon);
      } else if (c === SEMICOLON) {
        stack[stack.length - 1]!.semicolon = true;
      }
      pos += 1;
    }
    // A group still open at the line end lays out as a container would.
    for (const frame of stack) if (frame.kind === OPEN_BRACE) this.options.facts.set(frame.open, lineEnd, false);
    return this.options.facts.get(open) ?? false;
  }

  /**
   * Reads markup one character at a time. A break before a tag depends only
   * on the tag's kind and name, read at `<`; whether it closes itself is known
   * at its `>`, so no decision waits on text further on.
   */
  private stepMarkup(pos: number): number {
    const text = this.text;
    const m = this.markup;
    const lineEnd = this.lineEnd;
    this.visits += 1;
    const c = text.at(pos);
    this.col += c === TAB ? this.options.tabSize : 1;
    if (m.comment) {
      if (c === DASH && pos + 2 < lineEnd && text.at(pos + 1) === DASH && text.at(pos + 2) === GREATER) {
        this.markup = { ...m, comment: false, previous: "self", previousName: "!comment", adjacent: true };
        this.col += 2;
        return pos + 3;
      }
      return pos + 1;
    }
    if (m.cdata) {
      if (c === CLOSE_BRACKET && pos + 2 < lineEnd && text.at(pos + 1) === CLOSE_BRACKET && text.at(pos + 2) === GREATER) {
        this.markup = { ...m, cdata: false, previous: null, adjacent: false };
        this.col += 2;
        return pos + 3;
      }
      return pos + 1;
    }
    if (m.tag) {
      const tag = m.tag;
      if (tag.quote !== 0) {
        if (c === tag.quote) this.markup = { ...m, tag: { ...tag, quote: 0 } };
      } else if (c === QUOTE || c === APOS) {
        this.markup = { ...m, tag: { ...tag, quote: c } };
      } else if (c === GREATER) {
        const kind: TagKind = tag.kind === "open" && (tag.slash || VOID_TAGS.has(tag.name)) ? "self" : tag.kind;
        this.markup = {
          ...m,
          tag: null,
          rawTag: kind === "open" && RAW_TAGS.has(tag.name) ? tag.name : null,
          previous: kind,
          previousName: tag.name,
          adjacent: true,
        };
      } else if (c !== SPACE && c !== TAB) {
        const slash = c === SLASH;
        if (slash !== tag.slash) this.markup = { ...m, tag: { ...tag, slash } };
      }
      return pos + 1;
    }
    if (m.rawTag !== null) {
      if (c === LESS && pos + 1 < lineEnd && text.at(pos + 1) === SLASH && this.tagName(pos + 2, lineEnd) === m.rawTag) {
        this.markup = { ...m, rawTag: null, tag: { kind: "close", name: m.rawTag, quote: 0, slash: false } };
      }
      return pos + 1;
    }
    if (c === SPACE || c === TAB) return pos + 1;
    this.hasContent = true;
    if (c !== LESS) {
      if (m.adjacent) this.markup = { ...m, adjacent: false };
      return pos + 1;
    }
    if (pos + 3 < lineEnd && text.at(pos + 1) === BANG && text.at(pos + 2) === DASH && text.at(pos + 3) === DASH) {
      let depth = m.depth;
      if (m.adjacent && m.previous !== null) {
        if (m.previous === "open") depth += 1;
        if (this.laidOut) this.emitBreak(pos, this.columns(Math.max(0, depth)));
      }
      this.markup = { ...m, comment: true, previous: null, adjacent: false, depth };
      this.col += 3;
      return pos + 4;
    }
    if (pos + 8 < lineEnd && text.slice(pos, pos + 9) === "<![CDATA[") {
      this.markup = { ...m, cdata: true, previous: null, adjacent: false };
      this.col += 8;
      return pos + 9;
    }
    const first = pos + 1 < lineEnd ? text.at(pos + 1) : 0;
    let kind: TagKind = "open";
    let name: string;
    if (first === SLASH) {
      kind = "close";
      name = this.tagName(pos + 2, lineEnd);
    } else if (first === BANG || first === QUESTION) {
      kind = "self";
      name = "!decl";
    } else {
      name = this.tagName(pos + 1, lineEnd);
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
      if (breaks && this.laidOut) this.emitBreak(pos, this.columns(Math.max(0, depth)));
    }
    this.markup = { ...m, tag: { kind, name, quote: 0, slash: false }, depth };
    return pos + 1;
  }

  private tagName(from: number, limit: number): string {
    let end = from;
    while (end < limit && isTagNameCode(this.text.at(end))) end += 1;
    return this.text.slice(from, end).toLowerCase();
  }
}

/** A Markdown opening fence and its info string. */
export function parseOpeningFence(lineText: string): { char: number; length: number; info: string } | null {
  const match = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(lineText);
  if (!match) return null;
  const fence = match[1]!;
  const info = match[2] ?? "";
  if (fence[0] === "`" && info.includes("`")) return null;
  return { char: fence.charCodeAt(0), length: fence.length, info: (info.trim().split(/\s+/)[0] ?? "").toLowerCase() };
}

function isClosingFence(lineText: string, fence: Fence): boolean {
  const match = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(lineText);
  return !!match && match[1]!.charCodeAt(0) === fence.char && match[1]!.length >= fence.length;
}
