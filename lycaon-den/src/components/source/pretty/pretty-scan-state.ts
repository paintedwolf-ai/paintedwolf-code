import type { Text } from "@codemirror/state";
import type { Container, Frame } from "./pretty-bracket.ts";
import type { PrettyLexer } from "./pretty-lexers.ts";
import { type ClauseState, INITIAL_CLAUSE } from "./pretty-clause-state.ts";
import type { MarkupState } from "./pretty-markup.ts";
import type { TextWindow } from "./pretty-text-window.ts";
import type { PrettyTokenSource } from "./pretty-tokens.ts";

/** Laid-out groups stay flat when their visual line fits this many columns. */
export const PRETTY_TARGET_COLUMNS = 80;
/** Outside data, only source lines wider than this are laid out. */
export const PRETTY_TRIGGER_COLUMNS = 240;
/** Characters between saved scan states. */
export const PRETTY_CHECKPOINT_INTERVAL = 2048;
/** Open groups and containers allowed at once. */
export const PRETTY_MAX_DEPTH = 512;

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
  /** Protected text comes from the editor's syntax tree, so only parsed text is laid out. */
  readonly parsed: boolean;
  readonly lexer: () => PrettyLexer;
}

/** Breaks between adjacent tags. */
export interface MarkupRule {
  readonly kind: "markup";
}

/** SQL: a break before each clause, clause lists that do not fit, and parenthesised groups. */
export interface ClauseRule {
  readonly kind: "clause";
}

export type PrettyRule = BracketRule | MarkupRule | ClauseRule;

/** Whether a rule reads the editor's syntax tree and so lays out only what it has parsed. */
export function readsSyntaxTree(rule: PrettyRule): boolean {
  return rule.kind === "clause" || (rule.kind === "bracket" && rule.parsed);
}

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
  /** Tokens from the editor's syntax tree, for rules that read them. */
  readonly tokens?: PrettyTokenSource | null;
}

export interface Fence {
  readonly char: number;
  readonly length: number;
  readonly rule: PrettyRule | null;
}

/** Everything a scan carries from one position to the next. */
export class ScanState {
  rule: PrettyRule | null = null;
  lexer: PrettyLexer | null = null;
  frames: Frame | null = null;
  containers: Container | null = null;
  markup: MarkupState;
  clause: ClauseState = INITIAL_CLAUSE;
  fence: Fence | null = null;
  inLine = false;
  /** The current source line is laid out. */
  laidOut = false;
  leadColumns = 0;
  /** Something other than leading whitespace precedes the position on its line. */
  hasContent = false;
  /** Width of the current visual line so far. */
  col = 0;
  /** A break decided before reaching its position, or -1. */
  pending = -1;
  pendingColumns = 0;
  /** Where the current synthetic visual line began, or -1 on a source line's first. */
  visualFrom = -1;
  visualColumns = 0;
  lineEnd = 0;
  /** Characters examined, including lookahead. */
  visits = 0;
  failure: "depth" | null = null;

  constructor(markup: MarkupState) {
    this.markup = markup;
  }
}

/** What a rule's scanning step reads and changes. */
export interface ScanCursor {
  readonly text: TextWindow;
  readonly options: PrettyScanOptions;
  readonly state: ScanState;
  /** Starts a synthetic visual line at `pos`, indented by `columns`. */
  emitBreak(pos: number, columns: number): void;
  /** Indentation of a synthetic line `indent` groups deep within its source line. */
  columns(indent: number): number;
  skipSpaces(pos: number, end: number): number;
}
