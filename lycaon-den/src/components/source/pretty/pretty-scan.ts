import { closeBracketLine, type Container, containersEqual, type Frame, framesEqual, stepBracket } from "./pretty-bracket.ts";
import type { PrettyLexer } from "./pretty-lexers.ts";
import { INITIAL_MARKUP, type MarkupState, markupEqual, startMarkupLine, stepMarkup } from "./pretty-markup.ts";
import {
  type Fence,
  PRETTY_TRIGGER_COLUMNS,
  type PrettyRule,
  type PrettyScanOptions,
  type PrettySink,
  type ScanCursor,
  ScanState,
} from "./pretty-scan-state.ts";
import { TextWindow } from "./pretty-text-window.ts";

const SPACE = 0x20;
const TAB = 0x09;

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
  if (!markupEqual(a.markup, b.markup)) return false;
  const fa = a.fence;
  const fb = b.fence;
  return fa === fb || (!!fa && !!fb && fa.char === fb.char && fa.length === fb.length && fa.rule === fb.rule);
}

/**
 * Streams one document through its layout rules. The scanner keeps all state
 * needed to resume at a position, so a rescan after an edit starts at a saved
 * state before the edit and stops where the new state matches the old one.
 */
export class PrettyScanner implements ScanCursor {
  readonly text: TextWindow;
  readonly state = new ScanState(INITIAL_MARKUP);
  private spanEnd = -1;
  private spanIndex = 0;
  private spanEntered = -1;

  constructor(readonly options: PrettyScanOptions, private readonly sink: PrettySink) {
    this.text = new TextWindow(options.doc);
    if (options.plan.kind === "document") this.enterRule(options.plan.rule);
  }

  get visits(): number {
    return this.state.visits;
  }

  get failure(): "depth" | null {
    return this.state.failure;
  }

  /** Where the visual line open at the last restore began, or -1. */
  get visualStart(): number {
    return this.state.visualFrom;
  }

  snapshot(pos: number): PrettySnapshot {
    const s = this.state;
    return {
      rule: s.rule,
      lexer: s.lexer?.copy() ?? null,
      frames: s.frames,
      containers: s.containers,
      markup: s.markup,
      fence: s.fence,
      inLine: s.inLine,
      laidOut: s.laidOut,
      leadColumns: s.leadColumns,
      hasContent: s.hasContent,
      col: s.col,
      pendingAhead: s.pending < 0 ? -1 : s.pending - pos,
      pendingColumns: s.pendingColumns,
      visualBack: s.visualFrom < 0 ? -1 : pos - s.visualFrom,
      visualColumns: s.visualColumns,
    };
  }

  restore(snapshot: PrettySnapshot, pos: number): void {
    const s = this.state;
    s.rule = snapshot.rule;
    s.lexer = snapshot.lexer?.copy() ?? null;
    s.frames = snapshot.frames;
    s.containers = snapshot.containers;
    s.markup = snapshot.markup;
    s.fence = snapshot.fence;
    s.inLine = snapshot.inLine;
    s.laidOut = snapshot.laidOut;
    s.leadColumns = snapshot.leadColumns;
    s.hasContent = snapshot.hasContent;
    s.col = snapshot.col;
    s.pending = snapshot.pendingAhead < 0 ? -1 : pos + snapshot.pendingAhead;
    s.pendingColumns = snapshot.pendingColumns;
    s.visualFrom = snapshot.visualBack < 0 ? -1 : pos - snapshot.visualBack;
    s.visualColumns = snapshot.visualColumns;
    if (s.inLine) s.lineEnd = this.options.doc.lineAt(pos).to;
  }

  /**
   * Scans from `pos`. Whenever the position reaches `stop`, `onStop` is called
   * with the position and returns the next stop, or -1 to halt. Returns the
   * position reached.
   */
  run(pos: number, stop: number, onStop: (pos: number) => number): number {
    const doc = this.options.doc;
    const s = this.state;
    while (pos <= doc.length) {
      if (pos >= stop) {
        stop = onStop(pos);
        if (stop < 0) return pos;
      }
      if (s.failure) return pos;
      if (!s.inLine) {
        const line = doc.lineAt(pos);
        pos = this.startLine(line.from, line.to);
        if (!s.inLine) {
          if (line.to >= doc.length) return doc.length;
          pos = line.to + 1;
          continue;
        }
      }
      const lineEnd = this.spanEnd >= 0 ? this.spanEnd : s.lineEnd;
      const end = Math.min(lineEnd, stop);
      while (pos < end && !s.failure) pos = this.step(pos, end);
      if (pos >= lineEnd) {
        this.endLine();
        if (s.lineEnd >= doc.length) return doc.length;
        pos = s.lineEnd + 1;
      }
    }
    return pos;
  }

  emitBreak(pos: number, columns: number): void {
    const s = this.state;
    if (pos === s.visualFrom) return;
    if (s.visualFrom >= 0 && s.visualColumns > 0 && pos > s.visualFrom) this.sink.indent(s.visualFrom, pos, s.visualColumns);
    this.sink.break(pos);
    s.visualFrom = pos;
    s.visualColumns = columns;
    s.col = columns;
  }

  columns(indent: number): number {
    return this.state.leadColumns + indent * this.options.indentUnit;
  }

  skipSpaces(pos: number, end: number): number {
    while (pos < end) {
      const c = this.text.at(pos);
      if (c !== SPACE && c !== TAB) break;
      pos += 1;
    }
    this.state.visits += 1;
    return pos;
  }

  private step(pos: number, end: number): number {
    const s = this.state;
    if (pos === s.pending) {
      s.pending = -1;
      this.emitBreak(pos, s.pendingColumns);
    }
    const rule = s.rule!;
    return rule.kind === "bracket" ? stepBracket(this, rule, pos, end) : stepMarkup(this, pos);
  }

  private enterRule(rule: PrettyRule | null): void {
    const s = this.state;
    s.rule = rule;
    s.lexer = rule?.kind === "bracket" ? rule.lexer() : null;
    s.frames = null;
    s.containers = null;
    s.markup = INITIAL_MARKUP;
  }

  /** Starts the line; returns the first position to scan, leaving `inLine` false when nothing on it is laid out. */
  private startLine(from: number, to: number): number {
    const plan = this.options.plan;
    const s = this.state;
    s.lineEnd = to;
    this.spanEnd = -1;
    let start = from;
    if (plan.kind === "fenced") {
      const head = this.text.slice(from, Math.min(to, from + 200));
      if (s.fence) {
        if (to - from <= 200 && isClosingFence(head, s.fence)) {
          s.fence = null;
          this.enterRule(null);
          return from;
        }
      } else {
        const fence = parseOpeningFence(head);
        if (fence) {
          const rule = plan.fenceRule(fence.info);
          s.fence = { char: fence.char, length: fence.length, rule };
          this.enterRule(rule);
        }
        return from;
      }
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
    const rule = s.rule;
    if (!rule) return from;
    s.inLine = true;
    let leadColumns = 0;
    for (let pos = from; pos < to; pos++) {
      const c = this.text.at(pos);
      if (c === SPACE) leadColumns += 1;
      else if (c === TAB) leadColumns += this.options.tabSize;
      else break;
    }
    s.leadColumns = leadColumns;
    s.laidOut = (rule.kind === "bracket" && rule.data) || this.wider(from, to);
    s.hasContent = false;
    s.col = 0;
    s.pending = -1;
    s.visualFrom = -1;
    if (rule.kind === "bracket" && rule.records) s.containers = null;
    if (rule.kind === "markup") s.markup = startMarkupLine(s.markup);
    return start;
  }

  private wider(from: number, to: number): boolean {
    const length = to - from;
    const tabSize = this.options.tabSize;
    if (length > PRETTY_TRIGGER_COLUMNS) return true;
    if (length * tabSize <= PRETTY_TRIGGER_COLUMNS) return false;
    let width = 0;
    for (let pos = from; pos < to; pos++) {
      width += this.text.at(pos) === TAB ? tabSize : 1;
      if (width > PRETTY_TRIGGER_COLUMNS) return true;
    }
    return false;
  }

  private endLine(): void {
    const s = this.state;
    const end = this.spanEnd >= 0 ? this.spanEnd : s.lineEnd;
    if (s.visualFrom >= 0 && s.visualColumns > 0 && end > s.visualFrom) this.sink.indent(s.visualFrom, end, s.visualColumns);
    s.visualFrom = -1;
    s.pending = -1;
    s.inLine = false;
    if (s.rule?.kind === "bracket") closeBracketLine(s);
    if (this.spanEnd >= 0) this.enterRule(null);
  }
}

/** A Markdown opening fence and its info string. */
function parseOpeningFence(lineText: string): { char: number; length: number; info: string } | null {
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
