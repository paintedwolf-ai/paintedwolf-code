import { getIndentUnit, syntaxTree } from "@codemirror/language";
import {
  type EditorState,
  type Range,
  RangeSet,
  RangeSetBuilder,
  RangeValue,
  type Text,
  type Transaction,
} from "@codemirror/state";
import { Decoration, type DecorationSet } from "@codemirror/view";
import { Tree } from "@lezer/common";
import { prettyPlanFor, prettyTargetFor } from "./pretty-recognition.ts";
import { PrettyScanner, type PrettySnapshot, snapshotsEqual } from "./pretty-scan.ts";
import {
  PRETTY_CHECKPOINT_INTERVAL,
  PRETTY_TARGET_COLUMNS,
  PRETTY_TRIGGER_COLUMNS,
  type PrettyBlockFacts,
  type PrettyPlan,
  type PrettySink,
  readsSyntaxTree,
} from "./pretty-scan-state.ts";
import { TreeTokens } from "./pretty-tokens.ts";
import { prettyBreakDecoration, prettyIndentDecoration } from "./pretty-view.ts";

/** Largest document laid out, in characters. */
export const PRETTY_MAX_INPUT = 2 * 1024 * 1024;
/** Most breaks and indentation marks one document may carry. */
export const PRETTY_MAX_DECORATIONS = 1_500_000;
/** Scanner visits allowed per character in one update, beyond a fixed allowance. */
const VISITS_PER_CHARACTER = 8;
const VISITS_ALLOWANCE = 200_000;
/** Text before an edit whose layout can depend on it: one fit check. */
const LOOKBEHIND = PRETTY_TARGET_COLUMNS + 1;
/** Edits this close to a line start may change its indentation. */
const LEAD_REACH = 512;

export type PrettyLimitReason = "size" | "depth" | "decorations" | "work";

export type PrettyOutcome =
  | { readonly kind: "off" }
  | { readonly kind: "unavailable" }
  | { readonly kind: "active" }
  | { readonly kind: "limit"; readonly reason: PrettyLimitReason; readonly length: number };

export interface PrettyConfig {
  readonly enabled: boolean;
  readonly path?: string;
  readonly languageHint?: string;
}

/** Work done by the last update, for bounds. */
export interface PrettyWork {
  readonly visits: number;
  readonly from: number;
  readonly to: number;
  readonly added: number;
}

class Checkpoint extends RangeValue {
  override point = true;
  constructor(readonly snapshot: PrettySnapshot) {
    super();
  }
}

class BlockFact extends RangeValue {
  constructor(readonly block: boolean) {
    super();
  }
  override eq(other: RangeValue): boolean {
    return other instanceof BlockFact && other.block === this.block;
  }
}

const NO_CHECKPOINTS = RangeSet.empty as RangeSet<Checkpoint>;
const NO_FACTS = RangeSet.empty as RangeSet<BlockFact>;
export const NO_WORK: PrettyWork = { visits: 0, from: 0, to: 0, added: 0 };

export const OFF: PrettyOutcome = { kind: "off" };
const UNAVAILABLE: PrettyOutcome = { kind: "unavailable" };
const ACTIVE: PrettyOutcome = { kind: "active" };

export interface PrettyLayoutState {
  readonly config: PrettyConfig;
  readonly plan: PrettyPlan | null;
  readonly outcome: PrettyOutcome;
  readonly decorations: DecorationSet;
  readonly checkpoints: RangeSet<Checkpoint>;
  readonly facts: RangeSet<BlockFact>;
  /** Layout covers the document up to here; rules that read the syntax tree stop at its parsed end. */
  readonly scannedTo: number;
  readonly indentUnit: number;
  readonly tabSize: number;
  readonly work: PrettyWork;
}

/** Facts read from a set and new ones collected during one scan. */
class FactStore implements PrettyBlockFacts {
  readonly added = new Map<number, { end: number; block: boolean }>();
  constructor(private readonly known: RangeSet<BlockFact>) {}

  get(open: number): boolean | undefined {
    const fresh = this.added.get(open);
    if (fresh) return fresh.block;
    let found: boolean | undefined;
    this.known.between(open, open, (from, _to, value) => {
      if (from === open) {
        found = value.block;
        return false;
      }
      return;
    });
    return found;
  }

  set(open: number, end: number, block: boolean): void {
    if (!this.added.has(open)) this.added.set(open, { end, block });
  }

  ranges(): Range<BlockFact>[] {
    return [...this.added.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([open, fact]) => new BlockFact(fact.block).range(open, Math.max(open + 1, fact.end)));
  }
}

function emptyState(config: PrettyConfig, plan: PrettyPlan | null, outcome: PrettyOutcome, state: EditorState, work = NO_WORK): PrettyLayoutState {
  return {
    config,
    plan,
    outcome,
    decorations: Decoration.none,
    checkpoints: NO_CHECKPOINTS,
    facts: NO_FACTS,
    scannedTo: 0,
    indentUnit: getIndentUnit(state),
    tabSize: state.tabSize,
    work,
  };
}

function limited(prev: PrettyLayoutState, reason: PrettyLimitReason, state: EditorState): PrettyLayoutState {
  return emptyState(prev.config, prev.plan, { kind: "limit", reason, length: state.doc.length }, state);
}

function visitLimit(doc: Text): number {
  return doc.length * VISITS_PER_CHARACTER + VISITS_ALLOWANCE;
}

function planReadsTree(plan: PrettyPlan): boolean {
  return plan.kind === "fenced" || (plan.kind === "document" && readsSyntaxTree(plan.rule));
}

/**
 * Where layout may reach: the whole document, or the lines the syntax tree
 * fully covers. Fenced text with no grammar loaded has no tree to wait for.
 */
function layoutEnd(state: EditorState, plan: PrettyPlan): number {
  const doc = state.doc;
  if (!planReadsTree(plan)) return doc.length;
  const tree = syntaxTree(state);
  if (plan.kind === "fenced" && tree === Tree.empty) return doc.length;
  return tree.length >= doc.length ? doc.length : doc.lineAt(tree.length).from;
}

function tokensFor(state: EditorState, plan: PrettyPlan): TreeTokens | null {
  return planReadsTree(plan) ? new TreeTokens(syntaxTree(state)) : null;
}

function collect(added: Range<Decoration>[]): PrettySink {
  return {
    break(pos) {
      added.push(prettyBreakDecoration.range(pos));
    },
    indent(from, to, columns) {
      added.push(prettyIndentDecoration(columns).range(from, to));
    },
  };
}

export function buildLayout(state: EditorState, config: PrettyConfig): PrettyLayoutState {
  const doc = state.doc;
  const target = prettyTargetFor(config.path, config.languageHint);
  if (!target) return emptyState(config, null, UNAVAILABLE, state);
  if (!config.enabled) return emptyState(config, null, OFF, state);
  const plan = prettyPlanFor(doc, target);
  if (doc.length > PRETTY_MAX_INPUT) {
    return emptyState(config, plan, { kind: "limit", reason: "size", length: doc.length }, state);
  }
  const builder = new RangeSetBuilder<Decoration>();
  let added = 0;
  const sink: PrettySink = {
    break(pos) {
      builder.add(pos, pos, prettyBreakDecoration);
      added += 1;
    },
    indent(from, to, columns) {
      builder.add(from, to, prettyIndentDecoration(columns));
      added += 1;
    },
  };
  const facts = new FactStore(NO_FACTS);
  const indentUnit = getIndentUnit(state);
  const end = layoutEnd(state, plan);
  const scanner = new PrettyScanner({ doc, plan, tabSize: state.tabSize, indentUnit, facts, tokens: tokensFor(state, plan) }, sink);
  const checkpoints = new RangeSetBuilder<Checkpoint>();
  const limit = visitLimit(doc);
  let reason: PrettyLimitReason | null = null;
  scanner.run(0, 0, (pos) => {
    if (added > PRETTY_MAX_DECORATIONS) reason = "decorations";
    else if (scanner.visits > limit) reason = "work";
    if (reason) return -1;
    if (plan.kind !== "spans") checkpoints.add(pos, pos, new Checkpoint(scanner.snapshot(pos)));
    return pos + PRETTY_CHECKPOINT_INTERVAL;
  }, end);
  if (scanner.failure === "depth") reason = "depth";
  else if (!reason && added > PRETTY_MAX_DECORATIONS) reason = "decorations";
  else if (!reason && scanner.visits > limit) reason = "work";
  const work = { visits: scanner.visits, from: 0, to: end, added };
  if (reason) return emptyState(config, plan, { kind: "limit", reason, length: doc.length }, state, work);
  return {
    config,
    plan,
    outcome: ACTIVE,
    decorations: builder.finish(),
    checkpoints: checkpoints.finish(),
    facts: RangeSet.of(facts.ranges()),
    scannedTo: end,
    indentUnit,
    tabSize: state.tabSize,
    work,
  };
}

// Characters whose insertion or deletion can change brackets, protected
// text or statements; edits without them leave every block fact standing.
const STRUCTURAL = /[;{}[\]()"'`/*\\<>\n]/;
const SEMICOLON_ONLY = /^[^{}[\]()"'`/*\\<>\n]*$/;

interface ChangeSummary {
  readonly from: number;
  readonly to: number;
  readonly quiet: boolean;
  readonly semicolonsOnly: boolean;
}

function summarize(tr: Transaction): ChangeSummary {
  let from = Infinity;
  let to = -1;
  let quiet = true;
  let semicolonsOnly = true;
  tr.changes.iterChanges((fromA, toA, fromB, toB, inserted) => {
    from = Math.min(from, fromB);
    to = Math.max(to, toB);
    const removed = tr.startState.doc.sliceString(fromA, toA);
    const text = inserted.toString();
    if (STRUCTURAL.test(removed) || STRUCTURAL.test(text)) quiet = false;
    // Only an added `;` can turn a group into a block.
    if (removed.includes(";") || !SEMICOLON_ONLY.test(removed) || !SEMICOLON_ONLY.test(text)) semicolonsOnly = false;
  });
  return { from, to, quiet, semicolonsOnly };
}

function wider(doc: Text, from: number, to: number, tabSize: number): boolean {
  if (to - from > PRETTY_TRIGGER_COLUMNS) return true;
  if ((to - from) * tabSize <= PRETTY_TRIGGER_COLUMNS) return false;
  let width = 0;
  const text = doc.sliceString(from, to);
  for (let i = 0; i < text.length; i++) {
    width += text.charCodeAt(i) === 9 ? tabSize : 1;
    if (width > PRETTY_TRIGGER_COLUMNS) return true;
  }
  return false;
}

function checkpointAtOrBefore(set: RangeSet<Checkpoint>, pos: number): { pos: number; snapshot: PrettySnapshot } | null {
  for (let span = PRETTY_CHECKPOINT_INTERVAL * 2; ; span *= 4) {
    const from = Math.max(0, pos - span);
    let found: { pos: number; snapshot: PrettySnapshot } | null = null;
    set.between(from, pos, (at, _to, value) => {
      found = { pos: at, snapshot: value.snapshot };
    });
    if (found || from === 0) return found;
  }
}

function checkpointAt(set: RangeSet<Checkpoint>, pos: number): PrettySnapshot | null {
  let found: PrettySnapshot | null = null;
  set.between(pos, pos, (at, _to, value) => {
    if (at === pos) {
      found = value.snapshot;
      return false;
    }
    return;
  });
  return found;
}

function firstCheckpointFrom(set: RangeSet<Checkpoint>, pos: number): number {
  const cursor = set.iter(pos);
  return cursor.value ? cursor.from : -1;
}

interface Rescan {
  readonly state: EditorState;
  readonly decorations: DecorationSet;
  readonly checkpoints: RangeSet<Checkpoint>;
  readonly facts: RangeSet<BlockFact>;
  /** Where to start: the last saved state at or before here. */
  readonly restart: number;
  /** Past here, a saved state that matches the new one ends the scan; null scans to the end. */
  readonly compareFrom: number | null;
}

/**
 * Rescans from a saved state before `restart` until a saved state past
 * `compareFrom` matches, or to the end of what may be laid out, replacing
 * the breaks, marks and saved states in between.
 */
function rescan(prev: PrettyLayoutState, job: Rescan): PrettyLayoutState {
  const { state } = job;
  const doc = state.doc;
  const plan = prev.plan!;
  let { checkpoints } = job;
  let start = checkpointAtOrBefore(checkpoints, job.restart);
  // A break waiting past the restart point was placed by text that may have changed.
  while (start && start.snapshot.pendingAhead >= 0 && start.pos + start.snapshot.pendingAhead >= job.restart && start.pos > 0) {
    start = checkpointAtOrBefore(checkpoints, start.pos - 1);
  }
  const added: Range<Decoration>[] = [];
  const store = new FactStore(job.facts);
  const scanner = new PrettyScanner(
    { doc, plan, tabSize: prev.tabSize, indentUnit: prev.indentUnit, facts: store, tokens: tokensFor(state, plan) },
    collect(added),
  );
  const from = start?.pos ?? 0;
  if (start) scanner.restore(start.snapshot, start.pos);
  const visualStart = scanner.visualStart;
  const compareFrom = job.compareFrom ?? Infinity;
  const layoutTo = layoutEnd(state, plan);
  const fresh: Range<Checkpoint>[] = [];
  let converged = doc.length + 1;
  // Past the change, stops land on saved states so the new state can be compared with the old.
  const nextStop = (pos: number): number => {
    const cadence = pos + PRETTY_CHECKPOINT_INTERVAL;
    if (cadence < compareFrom) return cadence;
    const old = firstCheckpointFrom(checkpoints, Math.max(pos + 1, compareFrom));
    return old >= 0 ? Math.min(old, cadence) : cadence;
  };
  const limit = visitLimit(doc);
  let reason: PrettyLimitReason | null = null;
  const reached = scanner.run(from, start ? nextStop(from) : 0, (pos) => {
    if (scanner.visits > limit) {
      reason = "work";
      return -1;
    }
    const snapshot = scanner.snapshot(pos);
    if (pos >= compareFrom) {
      const old = checkpointAt(checkpoints, pos);
      if (old && snapshotsEqual(old, snapshot)) {
        converged = pos;
        return -1;
      }
    }
    fresh.push(new Checkpoint(snapshot).range(pos));
    return nextStop(pos);
  }, layoutTo);
  if (scanner.failure === "depth") reason = "depth";
  if (reason) return limited(prev, reason, state);

  const end = converged;
  const scannedTo = end <= doc.length ? Math.max(prev.scannedTo, reached) : Math.min(reached, layoutTo);
  const decorations = job.decorations.update({
    add: added,
    filterFrom: visualStart >= 0 ? Math.min(visualStart, from) : from,
    filterTo: Math.min(end, doc.length),
    // The visual line open where the scans meet keeps its old mark; every
    // other mark and break in the rescanned range was emitted again.
    filter: (rangeFrom, rangeTo) => {
      const mark = rangeTo > rangeFrom;
      if (mark) return rangeTo >= end || rangeTo < from;
      return rangeFrom < from || rangeFrom >= end;
    },
  });
  const dropFrom = start ? from + 1 : 0;
  const dropTo = Math.min(end - 1, doc.length);
  checkpoints = checkpoints.update(dropTo >= dropFrom
    ? { add: fresh, filterFrom: dropFrom, filterTo: dropTo, filter: () => false }
    : { add: fresh });
  if (decorations.size > PRETTY_MAX_DECORATIONS) return limited(prev, "decorations", state);
  return {
    ...prev,
    outcome: ACTIVE,
    decorations,
    checkpoints,
    facts: store.added.size > 0 ? job.facts.update({ add: store.ranges() }) : job.facts,
    scannedTo: Math.min(scannedTo, doc.length),
    work: { visits: scanner.visits, from, to: Math.min(end, reached, doc.length), added: added.length },
  };
}

/** Rescans from a saved state before an edit to the first saved state after it that still matches. */
export function updateLayout(prev: PrettyLayoutState, tr: Transaction): PrettyLayoutState {
  const state = tr.state;
  const doc = state.doc;
  const plan = prev.plan!;
  if (plan.kind === "spans") return buildLayout(state, prev.config);
  if (prev.outcome.kind === "limit") {
    return doc.length < prev.outcome.length ? buildLayout(state, prev.config) : prev;
  }
  if (doc.length > PRETTY_MAX_INPUT) return limited(prev, "size", state);
  const change = summarize(tr);
  let facts = prev.facts.map(tr.changes);
  let restart = Math.max(0, change.from - LOOKBEHIND);
  if (!change.quiet) {
    // A group whose statement facts the change may alter decides again from its opening.
    facts = facts.update({
      filterFrom: change.from,
      filterTo: change.to,
      filter: (from, to, value) => {
        // A deleted closer collapses its group's range onto the change, so touching counts.
        const affected = from <= change.to && to >= change.from;
        if (!affected || (change.semicolonsOnly && value.block)) return true;
        if (from < change.from) restart = Math.min(restart, from);
        return false;
      },
    });
  }
  const line = doc.lineAt(change.from);
  const oldLine = tr.startState.doc.lineAt(tr.changes.invertedDesc.mapPos(change.from, -1));
  const flips = wider(doc, line.from, line.to, state.tabSize) !== wider(tr.startState.doc, oldLine.from, oldLine.to, state.tabSize);
  if (flips || change.from - line.from <= LEAD_REACH) restart = Math.min(restart, line.from);
  return rescan({ ...prev, scannedTo: Math.min(tr.changes.mapPos(prev.scannedTo), doc.length) }, {
    state,
    decorations: prev.decorations.map(tr.changes),
    checkpoints: prev.checkpoints.map(tr.changes),
    facts,
    restart,
    compareFrom: change.to + LOOKBEHIND,
  });
}

/** Lays out text the parser reached since the last update; earlier layout stands. */
export function extendLayout(prev: PrettyLayoutState, state: EditorState): PrettyLayoutState {
  if (!prev.plan || prev.outcome.kind !== "active" || layoutEnd(state, prev.plan) <= prev.scannedTo) return prev;
  return rescan(prev, {
    state,
    decorations: prev.decorations,
    checkpoints: prev.checkpoints,
    facts: prev.facts,
    restart: prev.scannedTo,
    compareFrom: null,
  });
}

/** Whether layout waits on the parser for text it has not reached. */
export function awaitsParser(layout: PrettyLayoutState, state: EditorState): boolean {
  return !!layout.plan && layout.outcome.kind === "active" && planReadsTree(layout.plan) && layout.scannedTo < state.doc.length;
}
