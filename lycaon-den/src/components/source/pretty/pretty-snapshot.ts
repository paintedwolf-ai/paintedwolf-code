import { type Container, containersEqual, type Frame, framesEqual } from "./pretty-bracket.ts";
import { type ChainState, chainStatesEqual, INITIAL_CHAIN_STATE } from "./pretty-chain.ts";
import { type ClauseState, clauseStatesEqual, INITIAL_CLAUSE_STATE, SQL_GROUPS } from "./pretty-clause.ts";
import { JSX_SCRIPT_RULE } from "./pretty-jsx.ts";
import type { PrettyLexer } from "./pretty-lexers.ts";
import { INITIAL_MARKUP, type MarkupState, markupEqual } from "./pretty-markup.ts";
import { type SexpFrame, sexpFramesEqual } from "./pretty-sexp.ts";
import type { Fence, PrettyRule, ScanState } from "./pretty-scan-state.ts";

/** Scan state at a position, with positions stored as distances back from it. */
export interface PrettySnapshot {
  readonly rule: PrettyRule | null;
  readonly lexer: PrettyLexer | null;
  readonly frames: Frame | null;
  readonly containers: Container | null;
  readonly sexpFrames: SexpFrame | null;
  readonly chainState: ChainState;
  readonly clauseState: ClauseState;
  readonly markup: MarkupState;
  readonly fence: Fence | null;
  readonly inLine: boolean;
  readonly protectedRun: boolean;
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
  if (a.rule !== b.rule || a.inLine !== b.inLine || a.protectedRun !== b.protectedRun || a.laidOut !== b.laidOut || a.leadColumns !== b.leadColumns) return false;
  if (a.hasContent !== b.hasContent || a.col !== b.col || a.pendingAhead !== b.pendingAhead) return false;
  if (a.pendingAhead >= 0 && a.pendingColumns !== b.pendingColumns) return false;
  if (a.visualBack !== b.visualBack || (a.visualBack >= 0 && a.visualColumns !== b.visualColumns)) return false;
  if ((a.lexer === null) !== (b.lexer === null) || (a.lexer && !a.lexer.equals(b.lexer!))) return false;
  if (!framesEqual(a.frames, b.frames) || !containersEqual(a.containers, b.containers)) return false;
  if (!sexpFramesEqual(a.sexpFrames, b.sexpFrames) || !chainStatesEqual(a.chainState, b.chainState)) return false;
  if (!clauseStatesEqual(a.clauseState, b.clauseState) || !markupEqual(a.markup, b.markup)) return false;
  const fa = a.fence;
  const fb = b.fence;
  return (
    fa === fb ||
    (!!fa &&
      !!fb &&
      fa.char === fb.char &&
      fa.length === fb.length &&
      fa.rule === fb.rule &&
      (fa.quoteDepth ?? 0) === (fb.quoteDepth ?? 0) &&
      !!fa.unlabelled === !!fb.unlabelled)
  );
}

export function takeSnapshot(s: ScanState, pos: number): PrettySnapshot {
  return {
    rule: s.rule,
    lexer: s.lexer?.copy() ?? null,
    frames: s.frames,
    containers: s.containers,
    sexpFrames: s.sexpFrames,
    chainState: s.chainState,
    clauseState: s.clauseState,
    markup: s.markup,
    fence: s.fence,
    inLine: s.inLine,
    protectedRun: s.protectedRun,
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

export function restoreSnapshot(s: ScanState, snapshot: PrettySnapshot, pos: number, lineEnd: number): void {
  s.rule = snapshot.rule;
  s.lexer = snapshot.lexer?.copy() ?? null;
  s.frames = snapshot.frames;
  s.containers = snapshot.containers;
  s.sexpFrames = snapshot.sexpFrames;
  s.chainState = snapshot.chainState;
  s.clauseState = snapshot.clauseState;
  s.markup = snapshot.markup;
  s.fence = snapshot.fence;
  s.inLine = snapshot.inLine;
  s.protectedRun = snapshot.protectedRun;
  s.laidOut = snapshot.laidOut;
  s.leadColumns = snapshot.leadColumns;
  s.hasContent = snapshot.hasContent;
  s.col = snapshot.col;
  s.pending = snapshot.pendingAhead < 0 ? -1 : pos + snapshot.pendingAhead;
  s.pendingColumns = snapshot.pendingColumns;
  s.visualFrom = snapshot.visualBack < 0 ? -1 : pos - snapshot.visualBack;
  s.visualColumns = snapshot.visualColumns;
  if (s.inLine) s.lineEnd = lineEnd;
}

export function enterScanRule(s: ScanState, rule: PrettyRule | null): void {
  s.rule = rule;
  s.lexer =
    rule?.kind === "bracket"
      ? rule.lexer()
      : rule?.kind === "clause"
        ? SQL_GROUPS.lexer()
        : rule?.kind === "jsx"
          ? JSX_SCRIPT_RULE.lexer()
          : null;
  s.frames = null;
  s.containers = null;
  s.sexpFrames = null;
  s.chainState = INITIAL_CHAIN_STATE;
  s.clauseState = INITIAL_CLAUSE_STATE;
  s.markup = INITIAL_MARKUP;
}
