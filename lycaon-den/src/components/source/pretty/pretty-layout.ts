import { getIndentUnit } from "@codemirror/language";
import {
  type Annotation,
  type EditorState,
  type Extension,
  type Range,
  RangeSet,
  RangeSetBuilder,
  RangeValue,
  StateEffect,
  StateField,
  type Text,
  type Transaction,
} from "@codemirror/state";
import { Decoration, type DecorationSet, EditorView } from "@codemirror/view";
import { keepPositionInView } from "../editor/editor-settled-scroll.ts";
import { prettyPlanFor, prettyTargetFor } from "./pretty-recognition.ts";
import {
  PRETTY_CHECKPOINT_INTERVAL,
  PRETTY_TARGET_COLUMNS,
  PRETTY_TRIGGER_COLUMNS,
  type PrettyBlockFacts,
  type PrettyPlan,
  PrettyScanner,
  type PrettySink,
  type PrettySnapshot,
  snapshotsEqual,
} from "./pretty-scan.ts";
import {
  prettyBreakDecoration,
  prettyIndentDecoration,
  prettyLayoutActive,
  prettyLayoutDecorations,
  prettyPresentation,
} from "./pretty-view.ts";

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
const NO_WORK: PrettyWork = { visits: 0, from: 0, to: 0, added: 0 };

interface PrettyFieldState {
  readonly config: PrettyConfig;
  readonly plan: PrettyPlan | null;
  readonly outcome: PrettyOutcome;
  readonly decorations: DecorationSet;
  readonly checkpoints: RangeSet<Checkpoint>;
  readonly facts: RangeSet<BlockFact>;
  readonly indentUnit: number;
  readonly tabSize: number;
  readonly work: PrettyWork;
}

const OFF: PrettyOutcome = { kind: "off" };
const UNAVAILABLE: PrettyOutcome = { kind: "unavailable" };
const ACTIVE: PrettyOutcome = { kind: "active" };

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

function emptyState(config: PrettyConfig, plan: PrettyPlan | null, outcome: PrettyOutcome, state: EditorState, work = NO_WORK): PrettyFieldState {
  return {
    config,
    plan,
    outcome,
    decorations: Decoration.none,
    checkpoints: NO_CHECKPOINTS,
    facts: NO_FACTS,
    indentUnit: getIndentUnit(state),
    tabSize: state.tabSize,
    work,
  };
}

function visitLimit(doc: Text): number {
  return doc.length * VISITS_PER_CHARACTER + VISITS_ALLOWANCE;
}

function build(state: EditorState, config: PrettyConfig): PrettyFieldState {
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
  const scanner = new PrettyScanner({ doc, plan, tabSize: state.tabSize, indentUnit, facts }, sink);
  const checkpoints = new RangeSetBuilder<Checkpoint>();
  const limit = visitLimit(doc);
  let reason: PrettyLimitReason | null = null;
  scanner.run(0, 0, (pos) => {
    if (added > PRETTY_MAX_DECORATIONS) reason = "decorations";
    else if (scanner.visits > limit) reason = "work";
    if (reason) return -1;
    if (plan.kind !== "spans") checkpoints.add(pos, pos, new Checkpoint(scanner.snapshot(pos)));
    return pos + PRETTY_CHECKPOINT_INTERVAL;
  });
  if (scanner.failure === "depth") reason = "depth";
  else if (!reason && added > PRETTY_MAX_DECORATIONS) reason = "decorations";
  else if (!reason && scanner.visits > limit) reason = "work";
  const work = { visits: scanner.visits, from: 0, to: doc.length, added };
  if (reason) return emptyState(config, plan, { kind: "limit", reason, length: doc.length }, state, work);
  return {
    config,
    plan,
    outcome: ACTIVE,
    decorations: builder.finish(),
    checkpoints: checkpoints.finish(),
    facts: RangeSet.of(facts.ranges()),
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

/** Rescans from a saved state before the change to the first saved state after it that still matches. */
function update(prev: PrettyFieldState, tr: Transaction): PrettyFieldState {
  const state = tr.state;
  const doc = state.doc;
  const plan = prev.plan!;
  if (plan.kind === "spans") return build(state, prev.config);
  if (prev.outcome.kind === "limit") {
    return doc.length < prev.outcome.length ? build(state, prev.config) : prev;
  }
  if (doc.length > PRETTY_MAX_INPUT) {
    return emptyState(prev.config, plan, { kind: "limit", reason: "size", length: doc.length }, state);
  }
  const change = summarize(tr);
  let decorations = prev.decorations.map(tr.changes);
  let checkpoints = prev.checkpoints.map(tr.changes);
  let facts = prev.facts.map(tr.changes);
  let restart = Math.max(0, change.from - LOOKBEHIND);
  if (!change.quiet) {
    // A group whose statement facts the change may alter decides again from its opening.
    facts = facts.update({
      filterFrom: change.from,
      filterTo: change.to,
      filter: (from, to, value) => {
        const affected = change.from === change.to
          ? from < change.from && to > change.from
          : from < change.to && to > change.from;
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

  let start = checkpointAtOrBefore(checkpoints, restart);
  // A break waiting past the change was placed by text the change altered.
  while (start && start.snapshot.pendingAhead >= 0 && start.pos + start.snapshot.pendingAhead >= change.from && start.pos > 0) {
    start = checkpointAtOrBefore(checkpoints, start.pos - 1);
  }
  const added: Range<Decoration>[] = [];
  const sink: PrettySink = {
    break(pos) {
      added.push(prettyBreakDecoration.range(pos));
    },
    indent(from, to, columns) {
      added.push(prettyIndentDecoration(columns).range(from, to));
    },
  };
  const store = new FactStore(facts);
  const scanner = new PrettyScanner({ doc, plan, tabSize: prev.tabSize, indentUnit: prev.indentUnit, facts: store }, sink);
  const from = start?.pos ?? 0;
  if (start) scanner.restore(start.snapshot, start.pos);
  const visualStart = scanner.visualStart;
  const compareFrom = change.to + LOOKBEHIND;
  const fresh: Range<Checkpoint>[] = [];
  let converged = doc.length + 1;
  const nextStop = (pos: number): number => {
    const cadence = pos + PRETTY_CHECKPOINT_INTERVAL;
    const old = firstCheckpointFrom(checkpoints, Math.max(pos + 1, compareFrom));
    return old >= 0 && old <= cadence + PRETTY_CHECKPOINT_INTERVAL ? Math.min(old, Math.max(cadence, compareFrom)) : cadence;
  };
  const limit = visitLimit(doc);
  let reason: PrettyLimitReason | null = null;
  scanner.run(from, start ? nextStop(from) : 0, (pos) => {
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
  });
  if (scanner.failure === "depth") reason = "depth";
  if (reason) return emptyState(prev.config, plan, { kind: "limit", reason, length: doc.length }, state);

  const end = converged;
  decorations = decorations.update({
    add: added,
    filterFrom: visualStart >= 0 ? Math.min(visualStart, from) : from,
    filterTo: Math.min(end, doc.length),
    filter: (rangeFrom, rangeTo) => {
      const mark = rangeTo > rangeFrom;
      if (rangeFrom >= from && rangeFrom < end) return mark && rangeTo > end;
      if (rangeFrom < from) return !(mark && rangeTo > from && rangeTo <= end);
      return true;
    },
  });
  const dropFrom = start ? from + 1 : 0;
  const dropTo = Math.min(end - 1, doc.length);
  checkpoints = checkpoints.update(dropTo >= dropFrom
    ? { add: fresh, filterFrom: dropFrom, filterTo: dropTo, filter: () => false }
    : { add: fresh });
  if (decorations.size > PRETTY_MAX_DECORATIONS) {
    return emptyState(prev.config, plan, { kind: "limit", reason: "decorations", length: doc.length }, state);
  }
  return {
    ...prev,
    outcome: ACTIVE,
    decorations,
    checkpoints,
    facts: store.added.size > 0 ? facts.update({ add: store.ranges() }) : facts,
    work: { visits: scanner.visits, from, to: Math.min(end, doc.length), added: added.length },
  };
}

const setPrettyConfig = StateEffect.define<PrettyConfig>();

function sameConfig(a: PrettyConfig, b: PrettyConfig): boolean {
  return a.enabled === b.enabled && a.path === b.path && a.languageHint === b.languageHint;
}

export const prettyLayoutField = StateField.define<PrettyFieldState>({
  create: (state) => build(state, { enabled: false }),
  update(prev, tr) {
    let config = prev.config;
    for (const effect of tr.effects) if (effect.is(setPrettyConfig)) config = effect.value;
    const state = tr.state;
    if (!sameConfig(config, prev.config) || getIndentUnit(state) !== prev.indentUnit || state.tabSize !== prev.tabSize) {
      return build(state, config);
    }
    if (!tr.docChanged || prev.outcome.kind === "off" || prev.outcome.kind === "unavailable") return prev;
    return update(prev, tr);
  },
  provide: (field) => [
    EditorView.decorations.from(field, (state) => state.decorations),
    prettyLayoutDecorations.from(field, (state) => state.decorations),
    prettyLayoutActive.from(field, (state) => state.outcome.kind === "active"),
  ],
});

export function prettyOutcomeOf(state: EditorState): PrettyOutcome {
  return state.field(prettyLayoutField, false)?.outcome ?? OFF;
}

export function prettyWorkOf(state: EditorState): PrettyWork {
  return state.field(prettyLayoutField, false)?.work ?? NO_WORK;
}

/** Calls `onChange` whenever the layout outcome changes. */
export function prettyOutcomeListener(onChange: (outcome: PrettyOutcome) => void): Extension {
  return EditorView.updateListener.of((update) => {
    const before = update.startState.field(prettyLayoutField, false)?.outcome;
    const after = update.state.field(prettyLayoutField, false)?.outcome;
    if (after && before !== after) onChange(after);
  });
}

export function createPrettyExtension(initial: PrettyConfig = { enabled: false }): Extension {
  return [prettyPresentation, prettyLayoutField.init((state) => build(state, initial))];
}

/** Applies a configuration; turning layout on or off keeps the selection head in view. */
export function applyPrettyConfig(
  view: EditorView,
  config: PrettyConfig,
  annotations?: readonly Annotation<unknown>[],
): void {
  const current = view.state.field(prettyLayoutField, false);
  if (current && sameConfig(current.config, config)) return;
  const toggled = current !== undefined && current.config.enabled !== config.enabled;
  const head = (state: EditorState) => state.selection.main.head;
  view.dispatch({
    effects: [
      setPrettyConfig.of(config),
      ...(toggled ? [EditorView.scrollIntoView(head(view.state))] : []),
    ],
    ...(annotations ? { annotations: [...annotations] } : {}),
  });
  // The reflow moves the selection head; it stays in view.
  if (toggled) keepPositionInView(view, head);
}

export const PRETTY_LIMIT_MESSAGES: Record<PrettyLimitReason, string> = {
  size: "Too large for Pretty",
  depth: "Nested too deeply for Pretty",
  decorations: "Too many breaks for Pretty",
  work: "Too large for Pretty",
};
