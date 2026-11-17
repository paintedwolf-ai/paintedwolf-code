import { closeBracketLine, stepBracket } from "./pretty-bracket.ts";
import { closeChainLine, startChainLine, stepChain } from "./pretty-chain.ts";
import { INITIAL_CLAUSE_STATE, stepClause } from "./pretty-clause.ts";
import { isClosingFence, parseOpeningFence, unlabelledFenceSpan } from "./pretty-fenced.ts";
import { stepJsx } from "./pretty-jsx.ts";
import { INITIAL_MARKUP, startMarkupLine, stepMarkup } from "./pretty-markup.ts";
import { closeSexpLine, stepSexp } from "./pretty-sexp.ts";
import {
  type PrettyRule,
  type PrettyScanOptions,
  type PrettySink,
  type ScanCursor,
  ScanState,
} from "./pretty-scan-state.ts";
import { enterScanRule, type PrettySnapshot, restoreSnapshot, takeSnapshot } from "./pretty-snapshot.ts";
import { TextWindow } from "./pretty-text-window.ts";
import { exceedsLineWidth } from "./pretty-width.ts";


const SPACE = 0x20;
const TAB = 0x09;

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
    this.text = new TextWindow(options.doc, options.tokens ?? null);
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
    return takeSnapshot(this.state, pos);
  }

  restore(snapshot: PrettySnapshot, pos: number): void {
    const lineEnd = snapshot.inLine ? this.options.doc.lineAt(pos).to : 0;
    restoreSnapshot(this.state, snapshot, pos, lineEnd);
  }

  /**
   * Scans from `pos` up to the line starting at `endAt`. Whenever the position
   * reaches `stop`, `onStop` is called with the position and returns the next
   * stop, or -1 to halt. Returns the position reached.
   */
  run(pos: number, stop: number, onStop: (pos: number) => number, endAt = this.options.doc.length): number {
    const doc = this.options.doc;
    const s = this.state;
    while (pos <= doc.length) {
      if (pos >= stop) {
        stop = onStop(pos);
        if (stop < 0) return pos;
      }
      if (s.failure) return pos;
      if (!s.inLine && pos >= endAt && endAt < doc.length) return pos;
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
        if (this.spanEnd >= 0 && this.spanEnd < s.lineEnd) {
          this.spanIndex += 1;
          this.endLine();
          pos = lineEnd;
          continue;
        }
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
    if (s.visualFrom >= 0 && s.visualColumns > 0 && pos > s.visualFrom) {
      this.sink.indent(s.visualFrom, pos, s.visualColumns);
    }
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
    switch (rule.kind) {
      case "bracket": return stepBracket(this, rule, pos, end);
      case "markup": return stepMarkup(this, pos, end);
      case "clause": return stepClause(this, pos, end);
      case "sexp": return stepSexp(this, rule.data, pos, end);
      case "chain": return stepChain(this, pos, end);
      case "jsx": return stepJsx(this, pos, end);
    }
  }

  private enterRule(rule: PrettyRule | null): void {
    enterScanRule(this.state, rule);
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
          this.spanEnd = -1;
          this.enterRule(null);
          return from;
        }
        if (s.fence.unlabelled && !s.rule) {
          const detected = unlabelledFenceSpan(this.text, from, to, s.fence);
          if (detected) {
            this.enterRule(detected.rule);
            this.spanEnd = detected.spanEnd;
          }
        }
      } else {
        const fence = parseOpeningFence(head);
        if (fence) {
          const rule = plan.fenceRule(fence.info);
          s.fence = {
            char: fence.char,
            length: fence.length,
            rule,
            quoteDepth: fence.quoteDepth,
            unlabelled: fence.info === "",
          };
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
    s.laidOut = (rule.kind === "bracket" && rule.data) ||
      (rule.kind === "sexp" && rule.data) ||
      exceedsLineWidth(this.text, from, to, this.options.tabSize, rule, this.options.lineWidth);
    s.hasContent = false;
    s.col = 0;
    s.pending = -1;
    s.visualFrom = -1;
    if (rule.kind === "bracket" && rule.records) s.containers = null;
    if (rule.kind === "markup") s.markup = startMarkupLine(s.markup);
    if (rule.kind === "chain") s.chainState = startChainLine(s, this.text.slice(from, Math.min(to, from + 200)));
    if (rule.kind === "clause") s.clauseState = INITIAL_CLAUSE_STATE;
    return start;
  }

  private endLine(): void {
    const s = this.state;
    const end = this.spanEnd >= 0 ? this.spanEnd : s.lineEnd;
    if (s.visualFrom >= 0 && s.visualColumns > 0 && end > s.visualFrom) {
      this.sink.indent(s.visualFrom, end, s.visualColumns);
    }
    s.visualFrom = -1;
    s.pending = -1;
    s.inLine = false;
    if (
      s.rule?.kind === "bracket" ||
      s.rule?.kind === "clause" ||
      s.rule?.kind === "jsx" ||
      (s.rule?.kind === "markup" && s.markup.embedded)
    ) {
      closeBracketLine(s);
    }
    if (s.rule?.kind === "sexp") closeSexpLine(s);
    if (s.rule?.kind === "chain") closeChainLine(s);
    if (this.spanEnd >= 0 && s.lineEnd >= this.spanEnd) {
      this.enterRule(null);
      this.spanEnd = -1;
    }
  }
}
