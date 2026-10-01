import { lineGutterThemeRules } from "./line-gutter-theme.ts";
/** Fixed-width gutter with leading, line-number, and edge zones. */

import {
  RangeSet,
  RangeSetBuilder,
  StateEffect,
  StateField,
  Facet,
  type EditorState,
  type Extension,
  type Text,
  type Transaction,
} from "@codemirror/state";
import {
  EditorView,
  GutterMarker,
  ViewPlugin,
  gutter,
  type ViewUpdate,
} from "@codemirror/view";
import {
  foldEffect,
  foldable,
  foldedRanges,
  unfoldEffect,
} from "@codemirror/language";
import {
  attributionForLine,
  findingGlyph,
  findingLevelLabel,
  type AttributionMark,
  sameAttributionContributors,
  type FindingLineMark,
} from "./knowing-gutter-model.ts";
import {
  changeHunkAtAfterLine,
  diffHunksFromChunks,
  type DiffLineHunk,
} from "../../../files/review/line-diff.ts";
import {
  editorScopeDiffChunks,
  editorScopeDiffFolds,
  editorScopeDiffOriginalDoc,
  editorScopeDiffPeek,
  foldScopeDeletion,
  isScopeDeletionWidget,
  scopeDeletionFoldBack,
  type ScopeDeletionFold,
  type ScopeDiffChunks,
} from "../diff/scope-diff.ts";

import { agentPaint } from "../../../files/documents/document-agent-presence.ts";
import { lineFactsGutter, lineFactsMarker } from "./line-facts-gutter.ts";
import { gutterControl, lineGutterControls, type LineGutterControl } from "./line-gutter-controls.ts";
import {
  elementUnderPointer,
  isScrollActive,
  subscribeScrollActivity,
} from "../../../platform/scrolling/scroll-activity.ts";
import type { LineFactsInput } from "./line-facts.ts";

export function editorLineFactsInput(state: EditorState): LineFactsInput {
  return { doc: state.doc, marks: agentPaint(state).marks,
    attribution: getLineGutterAttribution(state), findings: getLineGutterFindings(state) };
}

const CELL = "files-line-gutter";

/** Widest line number the cell shows beside a finding glyph. */
const GLYPH_DIGIT_LIMIT = 3;

const RESTORE_CHANGE_LABEL = "Restore these lines";
const FOLD_BACK_LABEL = "Fold these lines";
export type LineGutterFindingHandlers = {
  onActivate: (mark: FindingLineMark) => void;
};

export type LineGutterProvenanceHandlers = {
  onActivate: (mark: AttributionMark, anchor: HTMLElement, line: number) => void;
};

export type LineGutterRestoreHandler = {
  onReject: (hunk: DiffLineHunk) => void;
};

export type LineGutterCutHandlers = {
  /** Pointer hovers preview after a pause; focus previews at once. */
  onShow: (fold: ScopeDeletionFold, anchor: DOMRect, via: "pointer" | "focus") => void;
  onHide: () => void;
  onOpen: (fold: ScopeDeletionFold) => void;
};

type LineGutterConfig = { numbers: boolean };

/** A projected document keeps source labels separate from editor positions. */
export const lineGutterSource = Facet.define<
  (state: EditorState, line: number) => { label: string; kind?: "insert" | "delete"; foldable?: boolean },
  ((state: EditorState, line: number) => { label: string; kind?: "insert" | "delete"; foldable?: boolean }) | undefined
>({ combine: values => values[0] });

const lineGutterConfig = Facet.define<LineGutterConfig, LineGutterConfig>({
  combine: (values) => values[0] ?? { numbers: true },
});

/** Numbers off keeps every mark and drops only the digits. */
export function lineGutterNumbers(on: boolean): Extension {
  return on
    ? lineGutterConfig.of({ numbers: true })
    : [
        lineGutterConfig.of({ numbers: false }),
        EditorView.theme({
          [`.${CELL}`]: { minWidth: "26px", width: "26px" },
        }),
      ];
}

const setFindings = StateEffect.define<readonly FindingLineMark[]>();
const setFindingHandlers =
  StateEffect.define<LineGutterFindingHandlers | null>();
const setAttribution = StateEffect.define<readonly AttributionMark[]>();
const setProvenanceHandlers =
  StateEffect.define<LineGutterProvenanceHandlers | null>();
const setRestoreHandler = StateEffect.define<LineGutterRestoreHandler | null>();
const setRestoreTarget = StateEffect.define<DiffLineHunk | null>();
const setCutHandlers = StateEffect.define<LineGutterCutHandlers | null>();

const EMPTY_FINDINGS: readonly FindingLineMark[] = [];

/** Ranges disappear when edits remove all their text. */
function mapLineRange(
  tr: Transaction,
  startLine: number,
  endLine: number,
): { startLine: number; endLine: number } | null {
  const before = tr.startState.doc;
  if (startLine < 1 || endLine > before.lines || startLine > endLine) return null;
  const from = before.line(startLine).from;
  const to = before.line(endLine).to;
  let removed = false;
  tr.changes.iterChangedRanges((fromA, toA) => {
    if (toA > fromA && fromA <= from && toA >= to) removed = true;
  });
  if (removed) return null;
  const after = tr.newDoc;
  return {
    startLine: after.lineAt(tr.changes.mapPos(from, 1)).number,
    endLine: after.lineAt(tr.changes.mapPos(to, -1)).number,
  };
}

const findingsField = StateField.define<readonly FindingLineMark[]>({
  create: () => EMPTY_FINDINGS,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setFindings)) return e.value;
    if (!tr.docChanged || value.length === 0) return value;
    return value.flatMap((mark) => {
      const range = mapLineRange(tr, mark.line, mark.line);
      return range ? [{ ...mark, line: range.startLine }] : [];
    });
  },
});

const findingHandlersField = StateField.define<LineGutterFindingHandlers | null>(
  {
    create: () => null,
    update(value, tr) {
      for (const e of tr.effects) if (e.is(setFindingHandlers)) return e.value;
      return value;
    },
  },
);

const attributionField = StateField.define<readonly AttributionMark[]>({
  create: () => [],
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setAttribution)) return e.value;
    if (!tr.docChanged || value.length === 0) return value;
    return value.flatMap((mark) => {
      const range = mapLineRange(tr, mark.startLine, mark.endLine);
      return range ? [{ ...mark, ...range }] : [];
    });
  },
});

const provenanceHandlersField =
  StateField.define<LineGutterProvenanceHandlers | null>({
    create: () => null,
    update(value, tr) {
      for (const e of tr.effects) if (e.is(setProvenanceHandlers)) return e.value;
      return value;
    },
  });

const restoreHandlerField = StateField.define<LineGutterRestoreHandler | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setRestoreHandler)) return e.value;
    return value;
  },
});

const cutHandlersField = StateField.define<LineGutterCutHandlers | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setCutHandlers)) return e.value;
    return value;
  },
});

const restoreTargetField = StateField.define<DiffLineHunk | null>({
  create: () => null,
  update(value, tr) {
    if (tr.docChanged) return null;
    for (const e of tr.effects) {
      if (e.is(setRestoreTarget)) return e.value;
      if (e.is(setRestoreHandler)) return null;
    }
    return value;
  },
});

/** Digits and the fold control occupy the cell's first child. */
class NumberMarker extends GutterMarker implements LineGutterControl {
  readonly press = "hold";

  constructor(
    readonly line: number,
    readonly fold: "none" | "open" | "folded",
    readonly show: boolean,
    readonly label = String(line),
    readonly kind?: "insert" | "delete",
  ) {
    super();
    this.elementClass = kind === "insert" ? `${CELL}__cell--add` : kind === "delete" ? `${CELL}__cell--del` : "";
  }

  eq(other: NumberMarker): boolean {
    return (
      this.line === other.line &&
      this.fold === other.fold &&
      this.show === other.show && this.label === other.label && this.kind === other.kind
    );
  }

  toDOM(): HTMLElement {
    if (this.fold === "none") {
      const span = document.createElement("span");
      span.className = `${CELL}__number`;
      span.dataset.line = String(this.line);
      span.setAttribute("aria-hidden", "true");
      span.textContent = this.show ? this.label : "";
      return span;
    }
    // The number doubles as the fold control on block-opening lines.
    const btn = gutterControl(document.createElement("button"), this);
    btn.type = "button";
    btn.className = `${CELL}__number ${CELL}__number--foldable`;
    btn.dataset.line = String(this.line);
    if (this.fold === "folded") btn.classList.add(`${CELL}__number--folded`);
    btn.dataset.testid = "files-line-gutter-fold";
    const folded = this.fold === "folded";
    btn.setAttribute("aria-expanded", folded ? "false" : "true");
    btn.setAttribute(
      "aria-label",
      `${folded ? "Unfold" : "Fold"} the block starting at line ${this.label}`,
    );
    btn.textContent = this.show ? this.label : "";
    return btn;
  }

  activate(view: EditorView): void {
    toggleFoldAtLine(view, this.line);
  }
}

/** Reserves a stable minimum width before source labels arrive. */
class SpacerMarker extends GutterMarker {
  eq(): boolean {
    return true;
  }

  toDOM(): HTMLElement {
    const span = document.createElement("span");
    span.className = `${CELL}__number ${CELL}__number--spacer`;
    span.setAttribute("aria-hidden", "true");
    span.textContent = "88888";
    return span;
  }
}

const spacerMarker = new SpacerMarker();

function toggleFoldAtLine(view: EditorView, lineNumber: number): void {
  const doc = view.state.doc;
  if (lineNumber < 1 || lineNumber > doc.lines) return;
  const line = doc.line(lineNumber);
  const existing = foldedAt(view.state, line.from, line.to);
  if (existing) {
    view.dispatch({ effects: unfoldEffect.of(existing) });
    return;
  }
  const range = foldable(view.state, line.from, line.to);
  if (range) view.dispatch({ effects: foldEffect.of(range) });
}

function foldedAt(
  state: EditorState,
  from: number,
  to: number,
): { from: number; to: number } | null {
  let found: { from: number; to: number } | null = null;
  foldedRanges(state).between(from, to, (a, b) => {
    if (a >= from && a <= to) {
      found = { from: a, to: b };
      return false;
    }
    return undefined;
  });
  return found;
}

/** Severity as a shape in the leading zone, and as a class on the cell. */
class FindingGlyphMarker extends GutterMarker implements LineGutterControl {
  constructor(readonly mark: FindingLineMark) {
    super();
    this.elementClass = `${CELL}__cell--finding ${CELL}__cell--${mark.level}`;
  }

  // The label and activation carry the findings, so a kept button must hold the same ones.
  eq(other: FindingGlyphMarker): boolean {
    return (
      this.mark.line === other.mark.line &&
      this.mark.level === other.mark.level &&
      this.mark.findings === other.mark.findings
    );
  }

  toDOM(): HTMLElement {
    const btn = gutterControl(document.createElement("button"), this);
    btn.type = "button";
    btn.className = `${CELL}__finding`;
    btn.dataset.testid = "files-line-gutter-finding";
    btn.dataset.level = this.mark.level;
    btn.setAttribute("aria-label", findingAriaLabel(this.mark));
    btn.textContent = findingGlyph(this.mark.level);
    return btn;
  }

  activate(view: EditorView): void {
    view.state.field(findingHandlersField, false)?.onActivate(this.mark);
  }
}

/** Moves severity color to the cell when the marker cannot fit. */
class FindingTintMarker extends GutterMarker implements LineGutterControl {
  constructor(readonly mark: FindingLineMark) {
    super();
    this.elementClass =
      `${CELL}__cell--finding ${CELL}__cell--${mark.level} ` +
      `${CELL}__cell--tinted`;
  }

  eq(other: FindingTintMarker): boolean {
    return (
      this.mark.line === other.mark.line &&
      this.mark.level === other.mark.level &&
      this.mark.findings === other.mark.findings
    );
  }

  toDOM(): HTMLElement {
    // Spans the cell so hover, focus, and Enter still reach the finding.
    const btn = gutterControl(document.createElement("button"), this);
    btn.type = "button";
    btn.className = `${CELL}__finding-tint`;
    btn.dataset.testid = "files-line-gutter-finding";
    btn.dataset.level = this.mark.level;
    btn.setAttribute("aria-label", findingAriaLabel(this.mark));
    return btn;
  }

  activate(view: EditorView): void {
    view.state.field(findingHandlersField, false)?.onActivate(this.mark);
  }
}

function findingAriaLabel(mark: FindingLineMark): string {
  const n = mark.findings.length;
  return n > 1
    ? `${findingLevelLabel(mark.level)}: ${n} findings on this line`
    : `${findingLevelLabel(mark.level)}: ${mark.findings[0]?.message ?? "finding"}`;
}

const DIFF_ADD = `${CELL}__cell--add`;
const DIFF_DEL = `${CELL}__cell--del`;
class ChangedLineMarker extends GutterMarker {
  elementClass = DIFF_ADD;
}

class DeletedRowMarker extends GutterMarker {
  elementClass = DIFF_DEL;
}

const changedLineMarker = new ChangedLineMarker();
const deletedRowMarker = new DeletedRowMarker();

/** The deletion bar refolds an expanded removal. */
class FoldBackMarker extends GutterMarker implements LineGutterControl {
  readonly press = "claim";

  constructor(
    readonly at: number,
  ) {
    super();
    this.elementClass = `${DIFF_DEL} ${CELL}__cell--foldback`;
  }

  eq(other: FoldBackMarker): boolean {
    return this.at === other.at;
  }

  toDOM(): HTMLElement {
    const btn = gutterControl(document.createElement("button"), this);
    btn.type = "button";
    // Sized to the edge strip, like the attribution bar.
    btn.className = `${CELL}__foldback`;
    btn.dataset.testid = "files-line-gutter-foldback";
    btn.dataset.tip = FOLD_BACK_LABEL;
    btn.dataset.tipPos = "end";
    btn.setAttribute("aria-label", FOLD_BACK_LABEL);
    return btn;
  }

  activate(view: EditorView): void {
    foldScopeDeletion(view, this.at);
  }
}

/** The diff bar links changed lines to their contributing turns. */
class ProvenanceMarker extends GutterMarker implements LineGutterControl {
  constructor(
    readonly line: number,
    readonly attribution: AttributionMark,
  ) {
    super();
    this.elementClass = `${DIFF_ADD} ${CELL}__cell--linked`;
  }

  eq(other: ProvenanceMarker): boolean {
    const a = this.attribution;
    const b = other.attribution;
    return (
      this.line === other.line &&
      a.startLine === b.startLine &&
      a.endLine === b.endLine &&
      sameAttributionContributors(a.contributors, b.contributors)
    );
  }

  toDOM(): HTMLElement {
    const btn = gutterControl(document.createElement("button"), this);
    btn.type = "button";
    // The edge strip leaves the number and leading zone exposed.
    btn.className = `${CELL}__provenance`;
    btn.dataset.testid = "files-line-gutter-provenance";
    btn.setAttribute(
      "aria-label",
      `Changed by ${this.attribution.contributors.length === 1 ? `agent, turn ${this.attribution.contributors[0]!.turn}` : "multiple agent contributions"}. Open provenance.`,
    );
    return btn;
  }

  activate(view: EditorView, button: HTMLElement): void {
    view.state.field(provenanceHandlersField, false)?.onActivate(this.attribution, button, this.line);
  }
}

/** Restores one hunk to its baseline text. */
class RestoreMarker extends GutterMarker implements LineGutterControl {
  readonly press = "claim";

  constructor(readonly hunk: DiffLineHunk) {
    super();
    this.elementClass = `${CELL}__cell--restorable`;
  }

  eq(other: RestoreMarker): boolean {
    const a = this.hunk;
    const b = other.hunk;
    return (
      a.kind === b.kind &&
      a.afterStart === b.afterStart &&
      a.afterEnd === b.afterEnd &&
      a.beforeStart === b.beforeStart &&
      a.beforeEnd === b.beforeEnd
    );
  }

  toDOM(): HTMLElement {
    const btn = gutterControl(document.createElement("button"), this);
    btn.type = "button";
    btn.className = `${CELL}__restore`;
    btn.dataset.testid = "files-line-gutter-restore";
    btn.dataset.tip = RESTORE_CHANGE_LABEL;
    btn.dataset.tipPos = "end";
    btn.setAttribute("aria-label", RESTORE_CHANGE_LABEL);
    btn.appendChild(rewindGlyph());
    return btn;
  }

  activate(view: EditorView): void {
    view.state.field(restoreHandlerField, false)?.onReject(this.hunk);
  }
}

/** A folded removal: a short mark on the edge at the boundary its lines left. */
class CutMarker extends GutterMarker implements LineGutterControl {
  readonly press = "claim";

  readonly preview = {
    show: (view: EditorView, button: HTMLElement, via: "pointer" | "focus") =>
      view.state.field(cutHandlersField, false)?.onShow(this.fold, button.getBoundingClientRect(), via),
    hide: (view: EditorView) => view.state.field(cutHandlersField, false)?.onHide(),
  };

  constructor(readonly fold: ScopeDeletionFold) {
    super();
  }

  // Preserving the hovered button lets pointerleave close its preview.
  eq(other: CutMarker): boolean {
    return (
      this.fold.at === other.fold.at &&
      this.fold.fromLine === other.fold.fromLine &&
      this.fold.toLine === other.fold.toLine &&
      this.fold.chunk.changes === other.fold.chunk.changes
    );
  }

  toDOM(view: EditorView): HTMLElement {
    const btn = gutterControl(document.createElement("button"), this);
    btn.type = "button";
    btn.className = `${CELL}__cut`;
    btn.dataset.testid = "files-line-gutter-cut";
    const count = this.fold.toLine - this.fold.fromLine + 1;
    btn.setAttribute(
      "aria-label",
      count === 1
        ? "1 line removed. Press Enter to show it in place."
        : `${count} lines removed. Press Enter to show them in place.`,
    );
    btn.dataset.at = String(this.fold.at);
    btn.setAttribute(
      "aria-expanded",
      editorScopeDiffPeek(view.state) === this.fold.at ? "true" : "false",
    );
    return btn;
  }

  activate(view: EditorView): void {
    view.state.field(cutHandlersField, false)?.onOpen(this.fold);
  }
}

const SVG_NS = "http://www.w3.org/2000/svg";

/** Raw-DOM rewind glyph matching host action geometry. */
function rewindGlyph(): SVGElement {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.4");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("class", `${CELL}__restore-glyph`);
  const path = document.createElementNS(SVG_NS, "path");
  path.setAttribute(
    "d",
    "M2.86 8a5.14 5.14 0 1 0 1.83-3.94M2.86 2.06v2.74a0.34 0.34 0 0 0 .34.34h2.74M8 5.49V8l2.06 1.49",
  );
  svg.appendChild(path);
  return svg;
}

type BuiltCells = {
  markers: RangeSet<GutterMarker>;
};

const EMPTY_CELLS: BuiltCells = { markers: RangeSet.empty };

const NO_HUNKS: readonly DiffLineHunk[] = [];

type HunkCache = {
  original: Text;
  doc: Text;
  chunks: ScopeDiffChunks;
  hunks: readonly DiffLineHunk[];
};

const hunksByView = new WeakMap<EditorView, HunkCache>();

/** Hunk caches follow original text, current text, and comparison chunks. */
function changeHunksFor(view: EditorView): readonly DiffLineHunk[] {
  const state = view.state;
  const original = editorScopeDiffOriginalDoc(state);
  if (!original) return NO_HUNKS;
  const doc = state.doc;
  const chunks = editorScopeDiffChunks(state);
  const held = hunksByView.get(view);
  if (held && held.original === original && held.doc === doc && held.chunks === chunks) return held.hunks;
  const hunks = diffHunksFromChunks(original, doc, chunks ?? []).filter(
    (h) => h.kind === "change",
  );
  hunksByView.set(view, { original, doc, chunks, hunks });
  return hunks;
}

export function restoreHunkForFold(
  view: EditorView,
  fold: ScopeDeletionFold,
): DiffLineHunk | null {
  return changeHunkAtAfterLine(changeHunksFor(view), fold.line - 1);
}

export function focusedRestoreHunk(view: EditorView): DiffLineHunk | null {
  const hunks = changeHunksFor(view);
  if (hunks.length === 0) return null;
  const line0 = view.state.doc.lineAt(view.state.selection.main.head).number - 1;
  return changeHunkAtAfterLine(hunks, line0);
}

function restoreTarget(
  view: EditorView,
): { hunk: DiffLineHunk; line: number } | null {
  if (!view.state.field(restoreHandlerField, false)) return null;
  const hunk = view.state.field(restoreTargetField, false);
  if (!hunk) return null;
  const line = restoreAnchorLine(view.state.doc, hunk);
  return line == null ? null : { hunk, line };
}

function sameHunk(a: DiffLineHunk | null, b: DiffLineHunk | null): boolean {
  if (!a || !b) return a === b;
  return (
    a.kind === b.kind &&
    a.afterStart === b.afterStart &&
    a.afterEnd === b.afterEnd &&
    a.beforeStart === b.beforeStart &&
    a.beforeEnd === b.beforeEnd
  );
}

function restoreHunkUnder(
  view: EditorView,
  target: EventTarget | null,
): DiffLineHunk | null {
  const hunks = changeHunksFor(view);
  if (!hunks.length || !(target instanceof Element) || !view.dom.contains(target)) return null;
  const row = target.closest(".cm-line, .cm-deletedChunk");
  if (row?.parentElement === view.contentDOM) {
    return changeHunkAtAfterLine(hunks, view.state.doc.lineAt(view.posAtDOM(row)).number - 1);
  }
  const cell = target.closest(`.${CELL} .cm-gutterElement`);
  const number = cell?.querySelector<HTMLElement>(`.${CELL}__number[data-line]`);
  return number ? changeHunkAtAfterLine(hunks, Number(number.dataset.line) - 1) : null;
}

function restoreAnchorLine(
  doc: { lines: number },
  hunk: DiffLineHunk,
): number | null {
  const line = hunk.afterStart + 1;
  if (line >= 1 && line <= doc.lines) return line;
  return doc.lines > 0 ? doc.lines : null;
}

function buildCells(view: EditorView): BuiltCells {
  const state = view.state;
  const doc = state.doc;
  const digits = String(Math.max(doc.lines, 1)).length;
  const glyphFits = digits <= GLYPH_DIGIT_LIMIT;

  const attribution = state.field(attributionField);
  const findings = state.field(findingsField);
  const chunks = editorScopeDiffChunks(state);

  const restore = restoreTarget(view);
  const restoreLine = restore?.line ?? null;

  // Multiple marks can share a line; ranges remain sorted by position.
  const perLine = new Map<number, GutterMarker[]>();
  const push = (line: number, marker: GutterMarker) => {
    const at = perLine.get(line);
    if (at) at.push(marker);
    else perLine.set(line, [marker]);
  };

  const visibleLines = view.visibleRanges.map(range => ({
    first: doc.lineAt(range.from).number, last: doc.lineAt(range.to).number,
  }));
  const visible = (line: number) => visibleLines.some(range => line >= range.first && line <= range.last);
  if (chunks) {
    for (const range of view.visibleRanges) for (const chunk of chunks) {
      if (chunk.fromB > range.to) break;
      if (chunk.fromB >= chunk.toB || chunk.toB <= range.from) continue;
      const first = doc.lineAt(Math.max(range.from, chunk.fromB)).number;
      const last = doc.lineAt(Math.min(range.to, doc.length, chunk.toB - 1)).number;
      for (let n = first; n <= last; n++) {
        // Folded ranges can share a boundary line.
        if (perLine.has(n)) continue;
        const attr = attributionForLine(attribution, n);
        push(n, attr ? new ProvenanceMarker(n, attr) : changedLineMarker);
      }
    }
  }

  for (const fold of editorScopeDiffFolds(state)) if (visible(fold.line)) push(fold.line, new CutMarker(fold));

  if (restore && visible(restore.line)) push(restore.line, new RestoreMarker(restore.hunk));

  for (const mark of findings) {
    if (mark.line < 1 || mark.line > doc.lines || !visible(mark.line)) continue;
    // The restore chip occupies the leading zone, so a finding uses the wash.
    const crowded = !glyphFits || mark.line === restoreLine;
    push(
      mark.line,
      crowded ? new FindingTintMarker(mark) : new FindingGlyphMarker(mark),
    );
  }

  const facts = editorLineFactsInput(state);
  // Read markers are limited to visible lines.
  for (const range of view.visibleRanges) {
    const first = doc.lineAt(range.from).number, last = doc.lineAt(range.to).number;
    for (let line = first; line <= last; line++) {
      if (perLine.get(line)?.some(marker => marker.elementClass.includes("__cell--facts"))) continue;
      const marker = lineFactsMarker(facts, line);
      if (marker) push(line, marker);
    }
  }

  if (perLine.size === 0) return EMPTY_CELLS;

  const builder = new RangeSetBuilder<GutterMarker>();
  for (const [line, markers] of [...perLine.entries()].sort((a, b) => a[0] - b[0])) {
    const from = doc.line(line).from;
    for (const marker of markers) builder.add(from, from, marker);
  }
  return { markers: builder.finish() };
}

function syncCutExpanded(view: EditorView, peek: number | null): void {
  for (const btn of view.dom.querySelectorAll<HTMLElement>(`.${CELL}__cut`)) {
    btn.setAttribute("aria-expanded", btn.dataset.at === String(peek) ? "true" : "false");
  }
}

const lineGutterPlugin = ViewPlugin.fromClass(
  class {
    built: BuiltCells;
    private chunks: ScopeDiffChunks;
    private attribution: readonly AttributionMark[];
    private findings: readonly FindingLineMark[];
    private restoreLine: number | null;
    private original: Text | null;
    private folds: readonly ScopeDeletionFold[];
    private peek: number | null;
    private readonly onMouseMove: (event: MouseEvent) => void;
    private readonly onMouseLeave: (event: MouseEvent) => void;
    private readonly stopScrollActivity: () => void;

    constructor(readonly view: EditorView) {
      this.chunks = editorScopeDiffChunks(view.state);
      this.attribution = view.state.field(attributionField);
      this.findings = view.state.field(findingsField);
      this.original = editorScopeDiffOriginalDoc(view.state);
      this.folds = editorScopeDiffFolds(view.state);
      this.peek = editorScopeDiffPeek(view.state);
      this.restoreLine = restoreTarget(view)?.line ?? null;
      this.built = buildCells(view);
      const setTarget = (hunk: DiffLineHunk | null) => {
        const held = view.state.field(restoreTargetField, false) ?? null;
        if (sameHunk(held, hunk)) return;
        view.dispatch({ effects: setRestoreTarget.of(hunk) });
      };
      this.onMouseMove = (event) => {
        if (!view.state.field(restoreHandlerField, false)) return;
        // Hunks passing under a resting pointer are not being pointed at.
        if (isScrollActive(view.scrollDOM)) return;
        setTarget(restoreHunkUnder(view, event.target));
      };
      this.onMouseLeave = () => setTarget(null);
      view.dom.addEventListener("mousemove", this.onMouseMove);
      view.dom.addEventListener("mouseleave", this.onMouseLeave);
      this.stopScrollActivity = subscribeScrollActivity(view.scrollDOM, (phase) => {
        if (phase !== "settle" || !view.state.field(restoreHandlerField, false)) return;
        setTarget(restoreHunkUnder(view, elementUnderPointer()));
      });
    }

    update(update: ViewUpdate): void {
      const chunks = editorScopeDiffChunks(update.state);
      const attribution = update.state.field(attributionField);
      const findings = update.state.field(findingsField);
      const original = editorScopeDiffOriginalDoc(update.state);
      const folds = editorScopeDiffFolds(update.state);
      const peek = editorScopeDiffPeek(update.state);
      if (peek !== this.peek) {
        this.peek = peek;
        syncCutExpanded(update.view, peek);
      }
      // Moving within a hunk preserves the chip's landing line.
      const restoreLine = restoreTarget(update.view)?.line ?? null;
      if (
        chunks === this.chunks &&
        attribution === this.attribution &&
        findings === this.findings &&
        restoreLine === this.restoreLine &&
        original === this.original &&
        folds === this.folds &&
        !update.docChanged && !update.viewportChanged &&
        agentPaint(update.startState) === agentPaint(update.state)
      ) {
        return;
      }
      this.chunks = chunks;
      this.attribution = attribution;
      this.findings = findings;
      this.restoreLine = restoreLine;
      this.original = original;
      this.folds = folds;
      this.built = buildCells(update.view);
    }

    destroy(): void {
      this.view.dom.removeEventListener("mousemove", this.onMouseMove);
      this.view.dom.removeEventListener("mouseleave", this.onMouseLeave);
      this.stopScrollActivity();
    }
  },
);

const lineGutter = gutter({
  class: `${CELL} cm-gutter`,
  markers: (view) => view.plugin(lineGutterPlugin)?.built.markers ?? RangeSet.empty,
  lineMarker: (view, block) => {
    const state = view.state;
    const line = state.doc.lineAt(block.from);
    const show = state.facet(lineGutterConfig).numbers;
    const source = state.facet(lineGutterSource)?.(state, line.number);
    return new NumberMarker(line.number, source?.foldable === false ? "none" : foldStateOf(state, line), show, source?.label, source?.kind);
  },
  lineMarkerChange: (update) =>
    update.docChanged || update.startState.facet(lineGutterSource) !== update.state.facet(lineGutterSource) ||
    update.startState.facet(lineGutterConfig) !==
      update.state.facet(lineGutterConfig) ||
    update.startState.field(foldStateProbe, false) !==
      update.state.field(foldStateProbe, false),
  widgetMarker: (_view, widget) => {
    const foldBack = scopeDeletionFoldBack(widget);
    if (foldBack != null) return new FoldBackMarker(foldBack);
    if (!isScopeDeletionWidget(widget)) return null;
    return deletedRowMarker;
  },
  initialSpacer: () => spacerMarker,
});

/** Tracks folds through their stable state field. */
const foldStateProbe = StateField.define<number>({
  create: (state) => foldSignature(state),
  update: (value, tr) => {
    if (!tr.docChanged && !tr.effects.some((e) => e.is(foldEffect) || e.is(unfoldEffect)))
      return value;
    return foldSignature(tr.state);
  },
});

function foldSignature(state: EditorState): number {
  let n = 0;
  foldedRanges(state).between(0, state.doc.length, (from, to) => {
    n = (n * 31 + from * 7 + to) | 0;
    return undefined;
  });
  return n;
}

function foldStateOf(
  state: EditorState,
  line: { from: number; to: number },
): "none" | "open" | "folded" {
  if (foldedAt(state, line.from, line.to)) return "folded";
  return foldable(state, line.from, line.to) ? "open" : "none";
}

const lineGutterTheme = EditorView.baseTheme(lineGutterThemeRules(CELL));

export const lineGutterExtension: Extension = [
  findingsField,
  findingHandlersField,
  attributionField,
  provenanceHandlersField,
  restoreHandlerField,
  restoreTargetField,
  cutHandlersField,
  foldStateProbe,
  lineGutterPlugin,
  lineGutterControls,
  lineGutter,
  lineGutterTheme,
  lineFactsGutter(editorLineFactsInput),
];

/** An empty list clears finding marks. */
export function setLineGutterFindings(
  view: EditorView,
  marks: readonly FindingLineMark[],
): void {
  view.dispatch({ effects: setFindings.of(marks) });
}

export function setLineGutterFindingHandlers(
  view: EditorView,
  handlers: LineGutterFindingHandlers | null,
): void {
  view.dispatch({ effects: setFindingHandlers.of(handlers) });
}

/** The attribution as an effect, for a surface folding it into its own transaction; an empty list clears the marks. */
export function lineGutterAttribution(marks: readonly AttributionMark[]): StateEffect<readonly AttributionMark[]> {
  return setAttribution.of(marks);
}

export function setLineGutterAttribution(
  view: EditorView,
  marks: readonly AttributionMark[],
): void {
  view.dispatch({ effects: lineGutterAttribution(marks) });
}

export function setLineGutterProvenanceHandlers(
  view: EditorView,
  handlers: LineGutterProvenanceHandlers | null,
): void {
  view.dispatch({ effects: setProvenanceHandlers.of(handlers) });
}

export function setLineGutterRestoreHandler(
  view: EditorView,
  handler: LineGutterRestoreHandler | null,
): void {
  view.dispatch({ effects: setRestoreHandler.of(handler) });
}

export function setLineGutterCutHandlers(
  view: EditorView,
  handlers: LineGutterCutHandlers | null,
): void {
  view.dispatch({ effects: setCutHandlers.of(handlers) });
}

/** Finding positions mapped through subsequent edits. */
export function getLineGutterFindings(
  state: EditorState,
): readonly FindingLineMark[] {
  return state.field(findingsField, false) ?? EMPTY_FINDINGS;
}

/** Attribution ranges mapped through subsequent edits. */
export function getLineGutterAttribution(
  state: EditorState,
): readonly AttributionMark[] {
  return state.field(attributionField, false) ?? [];
}
