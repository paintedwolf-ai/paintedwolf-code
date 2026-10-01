import {
  ChangeSet,
  RangeSet,
  RangeSetBuilder,
  RangeValue,
  StateEffect,
  StateField,
  Text,
  Transaction,
  type EditorState,
  type Extension,
} from "@codemirror/state";
import {
  Decoration,
  EditorView,
  ViewPlugin,
  WidgetType,
  type DecorationSet,
  type ViewUpdate,
} from "@codemirror/view";
import { highlightingFor, language } from "@codemirror/language";
import { highlightTree } from "@lezer/highlight";
import { Chunk, diff } from "@codemirror/merge";
import type { SourceAttributedText, SourceComparisonAttribution } from "../../../api/types.ts";
import { attributionLabel, buildAttributedChanges, mapAttribution, runsIn, sameComparisonAttribution } from "../annotations/scope-attribution.ts";
import { editorLineHeightPx } from "../editor/editor-line-height.ts";

import { SOURCE_DIFF_CONFIG } from "./source-text-diff.ts";
import { wordChanges } from "./word-changes.ts";
// Deleted runs longer than this render unhighlighted.
const HIGHLIGHT_DELETIONS_MAX = 3000;

/** How removed lines appear: a mark on the edge, or rows above their replacement. */
export type DeletedLines = "folded" | "inplace";

export type ScopeComparisonInput = {
  attribution?: SourceComparisonAttribution | null;
  /** Text at the start of the range. */
  original: string;
  /** The saved text the host compared against; unsaved typing is measured from it. */
  tip?: string | null;
};

type Comparison = {
  attribution: SourceComparisonAttribution | null;
  original: Text;
  chunks: readonly Chunk[];
  input: ScopeComparisonInput;
};

const setComparison = StateEffect.define<Comparison | null>();

function changesBetween(a: Text, b: Text): ChangeSet {
  const before = a.toString();
  const after = b.toString();
  return ChangeSet.of(
    diff(before, after, SOURCE_DIFF_CONFIG).map((c) => ({
      from: c.fromA,
      to: c.toA,
      insert: after.slice(c.fromB, c.toB),
    })),
    a.length,
  );
}

function textOf(value: string): Text {
  return Text.of(value.split(/\r\n?|\n/));
}

/** Live edits update only the current side of the comparison. */
function followEdit(side: Comparison, tr: Transaction): Pick<Comparison, "original" | "chunks"> {
  return { original: side.original,
    chunks: Chunk.updateB(side.chunks, side.original, tr.newDoc, tr.changes, SOURCE_DIFF_CONFIG) };
}

const comparisonField = StateField.define<Comparison | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setComparison)) return e.value;
    if (!value || !tr.docChanged) return value;
    const { original, chunks } = followEdit(value, tr);
    return { ...value, original, chunks, attribution: value.attribution
      ? { ...value.attribution, after: mapAttribution(value.attribution.after, tr.changes) } : null };
  },
});

const setDeletedLines = StateEffect.define<DeletedLines>();

const deletedLinesField = StateField.define<DeletedLines>({
  // History previews show removals in place by default.
  create: () => "inplace",
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setDeletedLines)) value = e.value;
    return value;
  },
});

class OpenedMark extends RangeValue {}
const openedMark = new OpenedMark();

const setOpened = StateEffect.define<{ at: number; open: boolean }>({
  map: (value, mapping) => ({ at: mapping.mapPos(value.at), open: value.open }),
});

/** Open removals track their replacement positions through edits. */
const openedField = StateField.define<RangeSet<OpenedMark>>({
  create: () => RangeSet.empty,
  update(value, tr) {
    let next = value.map(tr.changes);
    for (const e of tr.effects) {
      if (e.is(setDeletedLines)) next = RangeSet.empty;
      else if (e.is(setOpened)) {
        const { at, open } = e.value;
        next = open
          ? next.update({ add: [openedMark.range(at)] })
          : next.update({ filter: (from) => from !== at });
      }
    }
    return next;
  },
});

const setPeek = StateEffect.define<number | null>();

/** The fold whose preview is open, so its seam can span the line. */
const peekField = StateField.define<number | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setPeek)) return e.value;
    return tr.docChanged ? null : value;
  },
});

function isOpened(set: RangeSet<OpenedMark>, at: number): boolean {
  let found = false;
  set.between(at, at, (from) => {
    if (from === at) {
      found = true;
      return false;
    }
    return undefined;
  });
  return found;
}

export type ScopeDeletionFold = {
  /** Start of the line the mark sits above. */
  at: number;
  /** 1-based line the mark sits above. */
  line: number;
  /** 1-based lines in the start the removal held. */
  fromLine: number;
  toLine: number;
  chunk: Chunk;
};

const NO_FOLDS: readonly ScopeDeletionFold[] = [];

type FoldCache = {
  comparison: Comparison;
  opened: RangeSet<OpenedMark>;
  folds: readonly ScopeDeletionFold[];
};
const foldCache = new WeakMap<EditorState["doc"], FoldCache>();

/** Fold arrays retain identity until their inputs change. */
export function editorScopeDiffFolds(state: EditorState): readonly ScopeDeletionFold[] {
  const comparison = state.field(comparisonField, false);
  if (!comparison || state.field(deletedLinesField, false) !== "folded") return NO_FOLDS;
  const opened = state.field(openedField);
  const held = foldCache.get(state.doc);
  if (held && held.comparison.chunks === comparison.chunks && held.opened === opened
    && held.comparison.attribution?.before === comparison.attribution?.before) {
    return held.folds;
  }
  const folds: ScopeDeletionFold[] = [];
  const { original } = comparison;
  for (const chunk of comparison.chunks) {
    if (chunk.fromA >= chunk.toA || isOpened(opened, chunk.fromB)) continue;
    if (comparison.attribution && runsIn(comparison.attribution.before, chunk.fromA, chunk.toA).length === 0) continue;
    const line = state.doc.lineAt(Math.min(chunk.fromB, state.doc.length));
    folds.push({
      at: line.from,
      line: line.number,
      fromLine: original.lineAt(chunk.fromA).number,
      toLine: original.lineAt(Math.min(chunk.endA, original.length)).number,
      chunk,
    });
  }
  const result = folds.length > 0 ? folds : NO_FOLDS;
  foldCache.set(state.doc, { comparison, opened, folds: result });
  return result;
}

export function renderScopeDeletedLines(
  state: EditorState,
  chunk: Chunk,
): HTMLElement[] {
  const comparison = state.field(comparisonField, false);
  if (!comparison || chunk.fromA >= chunk.toA) return [];
  const text = comparison.original.sliceString(chunk.fromA, chunk.endA);
  const rows: HTMLElement[] = [];
  const makeLine = (): HTMLElement => {
    const row = document.createElement("div");
    row.className = "cm-deletedLine";
    rows.push(row);
    return row.appendChild(document.createElement("del"));
  };
  let line = makeLine();
  const changes = wordChanges(text, chunk.changes.map((change) => ({ from: change.fromA, to: change.toA })));
  let changeI = 0;
  let inside = false;
  const add = (from: number, to: number, cls: string) => {
    for (let at = from; at < to;) {
      if (text.charAt(at) === "\n") {
        if (!line.firstChild) line.appendChild(document.createElement("br"));
        line = makeLine();
        at++;
        continue;
      }
      let nextStop = to;
      const nodeCls = cls + (inside ? " cm-deletedText" : "");
      let flip = false;
      const newline = text.indexOf("\n", at);
      if (newline > -1 && newline < to) nextStop = newline;
      if (changeI < changes.length) {
        const bound = Math.max(0, inside ? changes[changeI]!.to : changes[changeI]!.from);
        if (bound <= nextStop) {
          nextStop = bound;
          if (inside) changeI++;
          flip = true;
        }
      }
      if (nextStop > at) {
        const node = document.createTextNode(text.slice(at, nextStop));
        if (nodeCls.trim()) {
          const span = line.appendChild(document.createElement("span"));
          span.className = nodeCls.trim();
          span.appendChild(node);
        } else {
          line.appendChild(node);
        }
        at = nextStop;
      }
      if (flip) inside = !inside;
    }
  };
  const lang = state.facet(language);
  if (lang && chunk.toA - chunk.fromA <= HIGHLIGHT_DELETIONS_MAX) {
    const tree = lang.parser.parse(text);
    let pos = 0;
    highlightTree(tree, { style: (tags) => highlightingFor(state, tags) }, (from, to, cls) => {
      if (from > pos) add(pos, from, "");
      add(from, to, cls);
      pos = to;
    });
    add(pos, text.length, "");
  } else {
    add(0, text.length, "");
  }
  if (!line.firstChild) line.appendChild(document.createElement("br"));
  if (comparison.attribution) {
    let offset = chunk.fromA;
    for (const row of rows) {
      const length = row.textContent?.length ?? 0;
      const authors = runsIn(comparison.attribution.before, offset, offset + length + 1);
      row.setAttribute("data-tip", attributionLabel(authors));
      if (!authors.some((run) => run.selected)) row.classList.add("cm-den-contextDeletion");
      offset += length + 1;
    }
  }
  return rows;
}

const SELECTING_REMOVED = "cm-den-selecting-removed";

/** Removed lines use native browser selection inside the widget. */
class ScopeDeletionWidget extends WidgetType {
  /** Estimated rows reserve layout space before the widget renders. */
  private readonly height: number;

  constructor(
    readonly chunk: Chunk,
    readonly text: string,
    /** Opened from a fold; the delete bar beside it folds it back. */
    readonly foldBack: boolean,
    private readonly lineHeightPx: number,
    private readonly attribution: readonly SourceAttributedText[] | null,
  ) {
    super();
    this.height = this.text.split("\n").length * lineHeightPx;
  }

  get estimatedHeight(): number {
    return this.height;
  }

  eq(other: ScopeDeletionWidget): boolean {
    return (
      other.chunk.changes === this.chunk.changes &&
      other.text === this.text &&
      other.foldBack === this.foldBack &&
      other.lineHeightPx === this.lineHeightPx &&
      other.attribution === this.attribution
    );
  }

  toDOM(view: EditorView): HTMLElement {
    const dom = document.createElement("div");
    dom.className = "cm-deletedChunk";
    for (const row of renderScopeDeletedLines(view.state, this.chunk)) dom.appendChild(row);
    // Selection stays within removed text during a drag.
    dom.addEventListener("mousedown", () => view.dom.classList.add(SELECTING_REMOVED));
    return dom;
  }
}

export function isScopeDeletionWidget(widget: WidgetType | null | undefined): boolean {
  return widget instanceof ScopeDeletionWidget;
}

/** Only removals opened from a fold expose a fold-back position. */
export function scopeDeletionFoldBack(widget: WidgetType | null | undefined): number | null {
  return widget instanceof ScopeDeletionWidget && widget.foldBack ? widget.chunk.fromB : null;
}

function elementOf(node: Node | null): Element | null {
  if (!node) return null;
  return node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
}

function selectionInRemovedText(view: EditorView): boolean {
  const selection = view.dom.ownerDocument.getSelection();
  if (!selection || selection.isCollapsed) return false;
  return [selection.anchorNode, selection.focusNode].some((node) => {
    const el = elementOf(node);
    return el != null && view.contentDOM.contains(el) && el.closest(".cm-deletedChunk") != null;
  });
}

/** Copy and cut use the native selection without changing removed text. */
function copyRemovedText(event: ClipboardEvent, view: EditorView): boolean {
  if (!selectionInRemovedText(view)) return false;
  const text = view.dom.ownerDocument.getSelection()?.toString() ?? "";
  event.clipboardData?.clearData();
  event.clipboardData?.setData("text/plain", text);
  return true;
}

const removedTextSelection: Extension = [
  ViewPlugin.fromClass(
    class {
      private readonly settle: (event: Event) => void;
      constructor(readonly view: EditorView) {
        this.settle = (event) => {
          const target = elementOf(event.target as Node | null);
          if (event.type === "mousedown" && target?.closest(".cm-deletedChunk")) return;
          view.dom.classList.remove(SELECTING_REMOVED);
        };
        view.dom.addEventListener("mousedown", this.settle, true);
        view.dom.addEventListener("keydown", this.settle, true);
      }
      destroy() {
        this.view.dom.removeEventListener("mousedown", this.settle, true);
        this.view.dom.removeEventListener("keydown", this.settle, true);
        this.view.dom.classList.remove(SELECTING_REMOVED);
      }
    },
  ),
  EditorView.domEventHandlers({ copy: copyRemovedText, cut: copyRemovedText }),
];

const seamLine = Decoration.line({ class: "cm-den-cut" });
const seamLineActive = Decoration.line({ class: "cm-den-cut cm-den-cut--active" });

function buildDeletions(state: EditorState): DecorationSet {
  const comparison = state.field(comparisonField, false);
  if (!comparison) return Decoration.none;
  const mode = state.field(deletedLinesField, false) ?? "inplace";
  const opened = state.field(openedField, false) ?? RangeSet.empty;
  const peek = state.field(peekField, false) ?? null;
  const lineHeightPx = state.facet(editorLineHeightPx);
  const builder = new RangeSetBuilder<Decoration>();
  for (const chunk of comparison.chunks) {
    if (chunk.fromA >= chunk.toA) continue;
    if (comparison.attribution && runsIn(comparison.attribution.before, chunk.fromA, chunk.toA).length === 0) continue;
    const at = Math.min(chunk.fromB, state.doc.length);
    const open = mode === "folded" && isOpened(opened, chunk.fromB);
    if (mode === "inplace" || open) {
      const text = comparison.original.sliceString(chunk.fromA, chunk.endA);
      builder.add(at, at, Decoration.widget({
        block: true,
        side: -1,
        widget: new ScopeDeletionWidget(chunk, text, open, lineHeightPx, comparison.attribution?.before ?? null),
      }));
    } else {
      const line = state.doc.lineAt(at);
      const active = peek === line.from;
      builder.add(line.from, line.from, active ? seamLineActive : seamLine);
    }
  }
  return builder.finish();
}

const deletionDecorations = StateField.define<DecorationSet>({
  create: buildDeletions,
  update(value, tr) {
    const changed =
      tr.state.field(comparisonField, false) !== tr.startState.field(comparisonField, false) ||
      tr.state.field(deletedLinesField, false) !== tr.startState.field(deletedLinesField, false) ||
      tr.state.field(openedField, false) !== tr.startState.field(openedField, false) ||
      tr.state.field(peekField, false) !== tr.startState.field(peekField, false) ||
      tr.state.facet(editorLineHeightPx) !== tr.startState.facet(editorLineHeightPx);
    return changed ? buildDeletions(tr.state) : value;
  },
  provide: (field) => EditorView.decorations.from(field),
});

const changedLine = Decoration.line({ class: "cm-changedLine" });
const changedText = Decoration.mark({ class: "cm-changedText" });
const insertedText = Decoration.mark({ tagName: "ins", class: "cm-insertedLine" });

function buildChanges(view: EditorView): DecorationSet {
  const comparison = view.state.field(comparisonField, false);
  if (!comparison) return Decoration.none;
  if (comparison.attribution) return buildAttributedChanges(view, comparison.attribution.after);
  const doc = view.state.doc;
  const marks = [];
  for (const range of view.visibleRanges) for (const chunk of comparison.chunks) {
    if (chunk.fromB >= range.to) break;
    if (chunk.toB <= range.from || chunk.fromB === chunk.toB) continue;
    const from = Math.max(chunk.fromB, range.from);
    const to = Math.min(chunk.toB, range.to, doc.length);
    if (from >= to) continue;
    marks.push(insertedText.range(from, to));
    const first = doc.lineAt(from);
    const last = doc.lineAt(to - 1);
    for (let number = first.number; number <= last.number; number++) {
      marks.push(changedLine.range(doc.line(number).from));
    }
    const changes = chunk.changes.map((change) => ({
      from: chunk.fromB + change.fromB - first.from, to: chunk.fromB + change.toB - first.from,
    }));
    for (const word of wordChanges(doc.sliceString(first.from, last.to), changes)) {
      const start = Math.max(from, first.from + word.from);
      const end = Math.min(to, first.from + word.to);
      if (start < end) marks.push(changedText.range(start, end));
    }
  }
  return Decoration.set(marks, true);
}

const changeDecorations = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = buildChanges(view);
    }
    update(update: ViewUpdate) {
      if (
        update.viewportChanged ||
        update.state.field(comparisonField, false) !== update.startState.field(comparisonField, false)
      ) {
        this.decorations = buildChanges(update.view);
      }
    }
  },
  { decorations: (plugin) => plugin.decorations },
);

const comparisonAttributes = EditorView.editorAttributes.compute(
  [comparisonField],
  (state): Record<string, string> => (state.field(comparisonField) ? { class: "cm-merge-b" } : {}),
);

const scopeDiffTheme = EditorView.baseTheme({
  ".cm-den-contextText": { backgroundColor: "color-mix(in srgb, var(--den-text-muted) 12%, transparent)" },
  ".cm-den-contextLine": { borderLeft: "2px solid var(--den-text-muted)" },
  ".cm-den-contextDeletion": { backgroundColor: "transparent", opacity: "0.65" },
  ".cm-insertedLine, .cm-deletedLine, .cm-deletedLine del": { textDecoration: "none" },
  ".cm-deletedChunk": { paddingLeft: "6px", cursor: "text", userSelect: "text" },
  [`&.${SELECTING_REMOVED} .cm-line`]: { userSelect: "none" },
  // Removal seams reserve no layout space.
  ".cm-line.cm-den-cut": { position: "relative" },
  ".cm-line.cm-den-cut::before": {
    content: '""',
    position: "absolute",
    left: 0,
    top: "-0.5px",
    width: "36px",
    height: "1px",
    background: "linear-gradient(90deg, var(--den-diff-delete-mark), transparent)",
    pointerEvents: "none",
    transition: "width 160ms ease",
  },
  ".cm-line.cm-den-cut--active::before": {
    width: "100%",
    background:
      "linear-gradient(90deg, var(--den-diff-delete-hue), color-mix(in srgb, var(--den-diff-delete-hue) 35%, transparent))",
  },
  "@media (prefers-reduced-motion: reduce)": {
    ".cm-line.cm-den-cut::before": { transition: "none" },
  },
});

/** Installed on every source editor; paints nothing until a comparison arrives. */
export const scopeDiffExtension: Extension = [
  comparisonField,
  deletedLinesField,
  openedField,
  peekField,
  deletionDecorations,
  changeDecorations,
  comparisonAttributes,
  removedTextSelection,
  scopeDiffTheme,
];

const originalStrings = new WeakMap<Text, string>();

export function editorScopeDiffOriginal(state: EditorState): string | null {
  const original = state.field(comparisonField, false)?.original;
  if (!original) return null;
  let text = originalStrings.get(original);
  if (text === undefined) {
    text = original.toString();
    originalStrings.set(original, text);
  }
  return text;
}

/** Original document identity stays stable until its text changes. */
export function editorScopeDiffOriginalDoc(state: EditorState): Text | null {
  return state.field(comparisonField, false)?.original ?? null;
}

export type ScopeDiffChunks = readonly Chunk[] | null;

/** Chunk identity stays stable between comparison updates. */
export function editorScopeDiffChunks(state: EditorState): ScopeDiffChunks {
  return state.field(comparisonField, false)?.chunks ?? null;
}

function sameInput(a: ScopeComparisonInput, b: ScopeComparisonInput): boolean {
  return (
    sameComparisonAttribution(a.attribution, b.attribution) &&
    a.original === b.original &&
    (a.tip ?? null) === (b.tip ?? null)
  );
}

/** Changes comparison decorations while preserving document content. */
export function applyEditorScopeDiff(
  view: EditorView,
  input: ScopeComparisonInput | null,
): void {
  const held = view.state.field(comparisonField, false);
  if (held === undefined) return;
  if (!input) {
    if (held) view.dispatch({ effects: setComparison.of(null) });
    return;
  }
  if (held && sameInput(held.input, input)) return;
  const doc = view.state.doc;
  const tip = input.tip != null ? textOf(input.tip) : null;
  const original = textOf(input.original);
  // Authorship can change without changing either text endpoint.
  const sameOriginal = held?.original.eq(original) ?? false;
  view.dispatch({
    effects: setComparison.of({
      attribution: input.attribution ? { ...input.attribution, after: tip && !tip.eq(doc) ? mapAttribution(input.attribution.after, changesBetween(tip, doc)) : input.attribution.after } : null,
      original: sameOriginal ? held!.original : original,
      chunks: sameOriginal ? held!.chunks : Chunk.build(original, doc, SOURCE_DIFF_CONFIG),
      input,
    }),
  });
}

export function setScopeDeletedLines(view: EditorView, mode: DeletedLines): void {
  if (view.state.field(deletedLinesField, false) === mode) return;
  view.dispatch({ effects: setDeletedLines.of(mode) });
}

export function openScopeDeletion(view: EditorView, fold: ScopeDeletionFold): void {
  view.dispatch({ effects: [setPeek.of(null), setOpened.of({ at: fold.chunk.fromB, open: true })] });
}

export function foldScopeDeletion(view: EditorView, at: number): void {
  const chunk = view.state.field(comparisonField, false)?.chunks.find(
    (c) => c.fromB === at || Math.min(c.fromB, view.state.doc.length) === at,
  );
  if (!chunk) return;
  view.dispatch({ effects: setOpened.of({ at: chunk.fromB, open: false }) });
}

/** Start of the line whose removal preview is open, or null. */
export function editorScopeDiffPeek(state: EditorState): number | null {
  return state.field(peekField, false) ?? null;
}

export function setScopeDeletionPeek(view: EditorView, at: number | null): void {
  if ((view.state.field(peekField, false) ?? null) === at) return;
  view.dispatch({ effects: setPeek.of(at) });
}
