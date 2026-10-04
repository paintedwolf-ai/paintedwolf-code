import { EditorSelection, RangeSet, StateEffect, StateField, type Extension, type Range, type Text } from "@codemirror/state";
import { Decoration, EditorView, ViewPlugin, WidgetType, type DecorationSet, type ViewUpdate } from "@codemirror/view";
import type { SourceReaderMatch } from "../../../api/types.ts";
import { copyTextToClipboard } from "../../../utils/clipboard.ts";
import { createSourceEditorState, applyEditorDisplayPrefs, sourceDisplayEffects, textNeedsSimplifiedDisplay, type EditorDisplayPrefs } from "../editor/codemirror-theme.ts";
import type { EditorSurface } from "../editor/editor-scroll-lifecycle.ts";
import type { DenReaderViewState } from "../../../../shared/app-state-types.ts";
import { acquireEditorViewport, releaseEditorViewport } from "../editor/editor-viewport-pool.ts";
import { editorLineHeightPx } from "../editor/editor-line-height.ts";
import { overviewDiffTicks } from "../annotations/overview-ruler.ts";
import { lineGutterAttribution, lineGutterSource } from "../annotations/line-gutter.ts";
import { setLineFactsHandlers } from "../annotations/line-facts-gutter.ts";
import { ReaderDocument, type ReaderSlot, type ReaderSide, type ReaderBoundary } from "./source-reader-document.ts";
import { readerMarks, readerSecretSpans } from "./source-reader-spans.ts";
import { displayStart, displayEnd } from "./source-reader-window.ts";
import { projectedSecretSpans } from "../secrets/secret-span-decorations.ts";
import { selectedReaderText, type ReaderTextAccess } from "./source-reader-selection.ts";
import { bindReaderViewportInput } from "./source-reader-viewport-input.ts";

const setDocument = StateEffect.define<ReaderDocument>();
const documentField = StateField.define<ReaderDocument>({
  create: () => new ReaderDocument([]),
  update: (value, transaction) => {
    for (const effect of transaction.effects) if (effect.is(setDocument)) return effect.value;
    return value;
  },
});
const setAlignment = StateEffect.define<ReadonlyMap<number, number>>();
class AlignmentSpacer extends WidgetType {
  constructor(readonly height: number) { super(); }
  eq(other: AlignmentSpacer): boolean { return this.height === other.height; }
  get estimatedHeight(): number { return this.height; }
  toDOM(): HTMLElement {
    const space = document.createElement("div");
    space.className = "cm-den-reader-alignment";
    space.style.height = `${this.height}px`;
    space.setAttribute("aria-hidden", "true");
    return space;
  }
  ignoreEvent(): boolean { return true; }
}
function rowAlignment(values: ReadonlyMap<number, number>, doc: Text): { values: ReadonlyMap<number, number>; decorations: DecorationSet } {
  // Text-line padding contaminates CodeMirror's baseline font-height measurement.
  return { values, decorations: Decoration.set([...values].filter(([, height]) => height > 0).map(([from, height]) =>
    Decoration.widget({ widget: new AlignmentSpacer(height), block: true, side: 1 }).range(doc.lineAt(from).to)), true) };
}
const alignmentField = StateField.define<{ values: ReadonlyMap<number, number>; decorations: DecorationSet }>({
  create: state => rowAlignment(new Map(), state.doc),
  update: (value, transaction) => {
    for (const effect of transaction.effects) {
      if (effect.is(setAlignment)) return rowAlignment(effect.value, transaction.state.doc);
      if (!effect.is(setDocument)) continue;
      const previous = transaction.startState.field(documentField);
      const entries = new Map(effect.value.entries.map(entry => [entry.slot.index, entry]));
      const mapped = new Map<number, number>();
      for (const [from, height] of value.values) {
        const old = previous.at(from);
        const next = old && entries.get(old.slot.index);
        if (next && next.slot === old?.slot && height > 0) mapped.set(transaction.state.doc.lineAt(next.from).from, height);
      }
      return rowAlignment(mapped, transaction.state.doc);
    }
    return value;
  },
  provide: field => EditorView.decorations.from(field, value => value.decorations),
});
/** A find hit names the comparison it landed in; the wire match names only the row. */
export type ReaderMatch = { section: string; match: SourceReaderMatch };
const setMatch = StateEffect.define<ReaderMatch | undefined>();
const matchField = StateField.define<ReaderMatch | undefined>({
  create: () => undefined,
  update: (value, transaction) => {
    for (const effect of transaction.effects) if (effect.is(setMatch)) return effect.value;
    return value;
  },
});

class RangeWidget extends WidgetType {
  constructor(readonly slot: ReaderSlot, readonly load: (slot: ReaderSlot, offset?: number) => void, readonly lineHeight: number) { super(); }
  eq(other: RangeWidget): boolean { return this.slot === other.slot && this.lineHeight === other.lineHeight; }
  get estimatedHeight(): number { return this.slot.pending ? Math.max(0, this.slot.lines ?? 1) * this.lineHeight : this.lineHeight + 4; }
  toDOM(): HTMLElement {
    if (this.slot.pending) {
      const space = document.createElement("div");
      space.className = "cm-den-reader-unloaded";
      space.style.height = `${this.estimatedHeight}px`;
      space.setAttribute("aria-label", "Loading source range");
      return space;
    }
    const button = document.createElement("button");
    button.type = "button";
    button.className = "cm-foldPlaceholder cm-den-reader-gap";
    button.dataset.readerGap = String(this.slot.index);
    button.textContent = "Show lines";
    button.setAttribute("aria-label", button.textContent);
    button.addEventListener("click", () => this.load(this.slot));
    // Block widget spacing is row padding so the height map reflects the full extent.
    const row = document.createElement("div");
    row.className = "cm-den-reader-gap-row";
    row.appendChild(button);
    return row;
  }
  ignoreEvent(): boolean { return true; }
}

/** Non-text ranges (headers, folds, pending placeholders) replaced by block widgets. */
function chromeRanges(document: ReaderDocument): RangeSet<Decoration> {
  const marks: Range<Decoration>[] = [];
  for (const entry of document.entries) {
    if (entry.slot.header || entry.slot.kind === "gap" || entry.slot.pending) {
      marks.push(Decoration.replace({}).range(entry.from, entry.to));
    }
  }
  return RangeSet.of(marks, true);
}

/** State-level block decorations covering folded runs and unread row placeholders. */
function blockDecorations(document: ReaderDocument, load: (slot: ReaderSlot, offset?: number) => void, lineHeight: number, extra: Range<Decoration>[] = []): DecorationSet {
  const marks: Range<Decoration>[] = [...extra];
  for (const entry of document.entries) {
    const { slot, from, to } = entry;
    // A section header is the surface's block, not the reader's.
    if (slot.header) continue;
    if (slot.kind === "gap" || slot.pending) {
      marks.push(Decoration.replace({ widget: new RangeWidget(slot, load, lineHeight), block: true }).range(from, to));
    }
  }
  return Decoration.set(marks, true);
}

/** Inline decorations (change colors, mark spans, search matches) for visible ranges. */
function inlineDecorations(view: EditorView, found: ReaderMatch | undefined, changes: boolean): DecorationSet {
  const document = view.state.field(documentField);
  const marks: Range<Decoration>[] = [];
  const lines = new Set<number>();
  const matchEntry = found && document.rowAt(found.section, found.match.row);
  const matchFrom = matchEntry && found ? matchEntry.from + found.match.from : -1;
  const matchTo = matchEntry && found ? matchEntry.from + found.match.to : -1;
  for (const { from: visibleFrom, to: visibleTo } of view.visibleRanges) {
    for (const entry of document.entriesBetween(visibleFrom, visibleTo)) {
      const { row, slot, from, to } = entry;
      if (slot.header || slot.kind === "gap" || slot.pending || !row) continue;
      // Cached line start from document construction.
      const lineStart = entry.lineFrom;
      if (!lines.has(lineStart)) {
        lines.add(lineStart);
        const changeClass = changes ? row.kind === "insert" ? "cm-changedLine cm-den-reader-add" : row.kind === "delete" ? "cm-den-reader-delete" : "" : "";
        const reloadingClass = slot.reloading ? "cm-den-reader-reloading" : "";
        const lineClass = changeClass && reloadingClass ? `${changeClass} ${reloadingClass}` : (changeClass || reloadingClass);
        marks.push(Decoration.line({ class: lineClass, attributes: { "data-source-row": String(row.index) } }).range(lineStart));
      }
      const localMatch = matchFrom < to && matchTo > from ? { from: Math.max(0, matchFrom - from), to: Math.min(to, matchTo) - from } : undefined;
      for (const mark of readerMarks(changes ? row : { ...row, changed: [] }, localMatch)) {
        marks.push(Decoration.mark({ class: mark.class, attributes: mark.title ? { "data-tip": mark.title } : undefined }).range(from + mark.from, from + mark.to));
      }
    }
  }
  return Decoration.set(marks, true);
}

export type ReaderEditorOptions = {
  parent: HTMLElement;
  /** Names this reader's scroll cost in a perf capture. */
  surface: EditorSurface;
  prefs: EditorDisplayPrefs;
  /** The comparison behind one section; a file reader has only the main one. */
  access: (section: string) => ReaderTextAccess | undefined;
  marks?: (document: ReaderDocument) => Range<Decoration>[];
  scrollPastEnd?: boolean;
  commandBridge?: boolean;
  side?: ReaderSide;
  /** Extensions of the host's own, such as framing the block chrome it draws. */
  extensions?: Extension;
  load: (slot: ReaderSlot, offset?: number) => void;
  error: (cause: unknown) => void;
  /** Present when the host sizes itself to the content height. */
  heightChanged?: (editor: ReaderEditor, contentHeight: number) => void;
  scroll?: (editor: ReaderEditor) => void;
  viewportInput?: (editor: ReaderEditor) => void;
  selectionChanged?: (editor: ReaderEditor) => void;
  fullSide: () => ReaderSide;
  changes: () => boolean;
  facts: (editor: ReaderEditor, line: number, anchor: HTMLElement, enter: boolean) => void;
  hideFacts: () => void;
};

export type ReaderSelection = { ranges: { anchor: ReaderBoundary; head: ReaderBoundary }[]; main: number };

/** Read-only editor over a paged source document; the host loads rows as they near the viewport. */
export class ReaderEditor {
  readonly view: EditorView;
  private destroyed = false;
  private viewportRevision = 0;
  /** Offset at native input; unchanged old notifications do not consume its delivery. */
  private pendingNativeScroll: number | undefined;
  private readonly stopViewportInput: () => void;
  private restoredViewport?: { anchor: NonNullable<DenReaderViewState["viewport"]>; revision: number; top?: number };
  private selectAll = false;
  private sourceSelection: { anchor: ReaderBoundary; head: ReaderBoundary }[] = [];
  private displayPrefs: EditorDisplayPrefs;
  private readonly options: ReaderEditorOptions;

  constructor(options: ReaderEditorOptions) {
    this.options = options;
    this.displayPrefs = options.prefs;
    const blocks = StateField.define<DecorationSet>({
      create: () => Decoration.none,
      update: (value, transaction) => (transaction.effects.some(effect => effect.is(setDocument)) || transaction.startState.facet(editorLineHeightPx) !== transaction.state.facet(editorLineHeightPx))
        ? blockDecorations(transaction.state.field(documentField), options.load, transaction.state.facet(editorLineHeightPx), options.marks?.(transaction.state.field(documentField))) : value,
      provide: field => EditorView.decorations.from(field),
    });
    // Rows are drawn for the screen, not for everything the page retains.
    const rows = ViewPlugin.fromClass(class {
      decorations: DecorationSet;
      constructor(view: EditorView) { this.decorations = inlineDecorations(view, view.state.field(matchField), options.changes()); }
      update(update: ViewUpdate): void {
        // Rows can change without the text changing: a secret screened after
        // the read, a contributor attributed, a kind revised. The document
        // effect is what says so, not `docChanged`.
        const replaced = update.transactions.some(transaction =>
          transaction.effects.some(effect => effect.is(setDocument)));
        if (replaced || update.docChanged || update.viewportChanged
          || update.startState.field(matchField) !== update.state.field(matchField)) {
          this.decorations = inlineDecorations(update.view, update.state.field(matchField), options.changes());
        }
      }
    }, { decorations: value => value.decorations });
    // Atomic ranges prevent cursor selection inside non-text chrome.
    const atomicChrome = EditorView.atomicRanges.of(view => chromeRanges(view.state.field(documentField)));
    this.view = acquireEditorViewport(createSourceEditorState({
      doc: "", surface: options.surface, ...options.prefs, editable: false, scrollPastEnd: options.scrollPastEnd, commandBridge: options.commandBridge,
      extensions: [documentField, matchField, alignmentField, blocks, rows, atomicChrome,
        projectedSecretSpans.compute([documentField], state => {
          const spans: ReturnType<typeof readerSecretSpans> = [];
          for (const entry of state.field(documentField).entries) {
            if (!entry.row) continue;
            const secretSpans = readerSecretSpans(entry.row);
            if (!secretSpans.length) continue;
            for (const span of secretSpans) {
              spans.push({ ...span, from: entry.from + span.from, to: entry.from + span.to });
            }
          }
          return spans;
        }),
        overviewDiffTicks.compute([documentField], state => {
          if (!options.changes()) return [];
          const ticks: { kind: "add" | "del"; fromLine: number; toLine: number }[] = [];
          for (const entry of state.field(documentField).entries) {
            const kind = entry.row?.kind;
            if (kind === "insert" || kind === "delete") {
              ticks.push({ kind: kind === "insert" ? "add" : "del", fromLine: entry.line, toLine: entry.line });
            }
          }
          return ticks;
        }),
        lineGutterSource.of((state, number) => {
          const line = state.doc.line(number);
          const entry = state.field(documentField).at(line.from);
          const row = entry && line.from < entry.to ? entry.row : undefined;
          return { label: row ? String((options.side === "before" ? row.before_line : row.after_line || row.before_line) || "") : "",
            kind: options.changes() && (row?.kind === "insert" || row?.kind === "delete") ? row.kind : undefined,
            foldable: false };
        }),
        EditorView.editorAttributes.of({ class: "cm-merge-b cm-den-reader" }),
        EditorView.contentAttributes.of({ "data-den-find-remote": "" }),
        EditorView.updateListener.of(update => {
          if (update.docChanged || update.geometryChanged) this.scheduleViewport("content");
          else if (update.viewportChanged) this.scheduleViewport("viewport");
          if (update.selectionSet && !update.transactions.some(transaction => transaction.effects.some(effect => effect.is(setDocument)))) {
            this.selectAll = false;
            this.sourceSelection = update.state.selection.ranges.flatMap((range, index) => {
              const previous = update.startState.selection.ranges[index], saved = this.sourceSelection[index];
              const forward = range.anchor <= range.head;
              const anchor = saved && previous?.anchor === range.anchor ? saved.anchor : this.document.boundary(range.anchor, forward ? 1 : -1);
              const head = range.empty ? anchor : saved && previous?.head === range.head ? saved.head : this.document.boundary(range.head, forward ? -1 : 1);
              return anchor && head ? [{ anchor, head }] : [];
            });
            options.selectionChanged?.(this);
          }
        }),
        EditorView.domEventHandlers({
          copy: event => this.copy(event),
          keydown: event => {
            if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "a") {
              event.preventDefault();
              this.view.dispatch({ selection: { anchor: 0, head: this.view.state.doc.length } });
              this.selectAll = true;
              return true;
            }
            return false;
          },
          scroll: () => {
            if (this.view.scrollDOM.scrollTop !== this.pendingNativeScroll) this.pendingNativeScroll = undefined;
            options.scroll?.(this); return false;
          },
        }),
        options.extensions ?? [],
      ],
    }), options.parent);
    this.stopViewportInput = bindReaderViewportInput(this.view.dom, pending => {
      this.pendingNativeScroll = pending ? this.view.scrollDOM.scrollTop : undefined;
      this.interruptViewportRestoration();
      options.viewportInput?.(this);
    }, () => { this.pendingNativeScroll = undefined; });
    setLineFactsHandlers(this.view, {
      show: (line, anchor, via) => options.facts(this, line, anchor, via === "enter"),
      leave: options.hideFacts, dismiss: options.hideFacts, refresh: options.hideFacts,
    });
  }

  get document(): ReaderDocument { return this.view.state.field(documentField); }
  selection(): ReaderSelection { return { ranges: structuredClone(this.sourceSelection), main: this.view.state.selection.mainIndex }; }
  /** Restoring a selection moves the caret; the document it lands in is unchanged. */
  restoreSelection(selection: ReaderSelection): void {
    this.sourceSelection = structuredClone(selection.ranges);
    const ranges = this.selectionRanges(this.document);
    if (!ranges.length) return;
    this.view.dispatch({ selection: EditorSelection.create(ranges, Math.min(ranges.length - 1, selection.main)) });
  }

  /** Maps the held source boundaries onto a document's positions. */
  private selectionRanges(document: ReaderDocument) {
    return this.sourceSelection.map(range => {
      if (range.anchor.row === range.head.row && range.anchor.offset === range.head.offset) return EditorSelection.cursor(document.position(range.anchor, 1));
      const forward = range.anchor.row < range.head.row || range.anchor.row === range.head.row && range.anchor.offset <= range.head.offset;
      return EditorSelection.range(document.position(range.anchor, forward ? 1 : -1), document.position(range.head, forward ? -1 : 1));
    });
  }
  prefs(value: EditorDisplayPrefs): void { this.displayPrefs = value; applyEditorDisplayPrefs(this.view, value); }

  setRows(rows: readonly ReaderSlot[], reset = false, mainIndex = this.view.state.selection.mainIndex): void {
    const next = new ReaderDocument(rows, this.options.side);
    const previous = this.document.text;
    let from = 0, end = previous.length, nextEnd = next.text.length;
    if (!reset) {
      const prevEntries = this.document.entries;
      const nextEntries = next.entries;
      let startEntry = 0;
      while (startEntry < prevEntries.length && startEntry < nextEntries.length) {
        const p = prevEntries[startEntry]!, n = nextEntries[startEntry]!;
        if (p.slot !== n.slot || p.from !== n.from || p.to !== n.to) break;
        startEntry++;
      }
      let endPrev = prevEntries.length;
      let endNext = nextEntries.length;
      while (endPrev > startEntry && endNext > startEntry) {
        const p = prevEntries[endPrev - 1]!, n = nextEntries[endNext - 1]!;
        if (p.slot !== n.slot || (previous.length - p.from) !== (next.text.length - n.from) || (previous.length - p.to) !== (next.text.length - n.to)) break;
        endPrev--;
        endNext--;
      }
      // A removed tail also takes the separator before it; an appended one brings its own.
      from = startEntry === nextEntries.length && startEntry < prevEntries.length
        ? startEntry > 0 ? prevEntries[startEntry - 1]!.to : 0
        : startEntry < prevEntries.length ? prevEntries[startEntry]!.from : previous.length;
      end = endPrev < prevEntries.length ? prevEntries[endPrev]!.from : previous.length;
      nextEnd = endNext < nextEntries.length ? nextEntries[endNext]!.from : next.text.length;
    }
    if (reset) this.sourceSelection = [];
    const ranges = this.selectionRanges(next);
    const attributed = new Map<number, NonNullable<ReaderSlot["contributors"]>>();
    for (const entry of next.entries) {
      const contributors = entry.row?.contributors?.filter(author => author.session_id);
      if (contributors?.length) attributed.set(entry.line, contributors);
    }
    // One transaction carries text, decorations, and display effects, so the editor measures once.
    this.view.dispatch({ changes: { from, to: end, insert: next.text.slice(from, nextEnd) },
      effects: [setDocument.of(next),
        lineGutterAttribution([...attributed].map(([line, authors]) => ({ startLine: line, endLine: line,
          contributors: authors.map(author => ({ sessionId: author.session_id ?? "", toolCallId: author.tool_call_id ?? "", turn: author.turn, ts: "" })),
        }))),
        ...sourceDisplayEffects(this.view.state, this.displayPrefs, textNeedsSimplifiedDisplay(next.text)), ...(reset ? [setMatch.of(undefined)] : [])],
      ...(ranges.length ? { selection: EditorSelection.create(ranges, Math.min(ranges.length - 1, mainIndex)) } : {}),
    });
    if (reset) { this.selectAll = false; this.view.scrollDOM.scrollTop = 0; }
    this.scheduleViewport("content");
  }

  reveal(section: string, row: number, match?: SourceReaderMatch): boolean {
    const entry = this.document.rowAt(section, row);
    if (!entry) return false;
    const lineEnd = this.view.state.doc.lineAt(entry.from).to;
    if (match && entry.from + match.to > lineEnd) return false;
    const from = Math.min(lineEnd, entry.from + Math.max(0, match?.from ?? 0));
    const to = Math.min(lineEnd, entry.from + Math.max(0, match?.to ?? match?.from ?? 0));
    this.viewportRevision++;
    this.view.dispatch({ selection: { anchor: from, head: to }, effects: [setMatch.of(match && { section, match }), EditorView.scrollIntoView(from, { y: "center" })] });
    return true;
  }

  viewportAnchor(): DenReaderViewState["viewport"] {
    const restored = this.restoredViewport;
    if (restored?.revision === this.viewportRevision && (restored.top === undefined || restored.top === this.view.scrollDOM.scrollTop)) return restored.anchor;
    this.restoredViewport = undefined;
    const top = this.view.scrollDOM.getBoundingClientRect().top - this.view.documentTop;
    const block = this.view.lineBlockAtHeight(top);
    const entry = this.document.at(block.from);
    if (!entry) return undefined;
    const start = displayStart(entry.slot), count = displayEnd(entry.slot) - start;
    const relative = top - block.top;
    const viewport = this.view.scrollDOM;
    const maximum = viewport.scrollHeight - viewport.clientHeight;
    const affinity = viewport.clientHeight > 0 && maximum > 0 && Math.abs(maximum - viewport.scrollTop) <= 1 ? { atEnd: true } : {};
    if (!entry.slot.pending) return { rank: start, fraction: relative / Math.max(1, block.height), ...affinity };
    const rowHeight = Math.max(1, block.height) / Math.max(1, count);
    const rank = Math.min(Math.max(0, count - 1), Math.max(0, Math.floor(relative / rowHeight)));
    return { rank: start + rank, fraction: (relative - rank * rowHeight) / rowHeight, ...affinity };
  }

  get viewportInputPending(): boolean { return this.pendingNativeScroll !== undefined; }

  interruptViewportRestoration(): void {
    this.viewportRevision++;
    this.restoredViewport = undefined;
  }

  restoreViewport(anchor: NonNullable<DenReaderViewState["viewport"]>): void {
    if (this.viewportInputPending) return;
    const revision = this.viewportRevision;
    const restored: NonNullable<ReaderEditor["restoredViewport"]> = { anchor, revision };
    this.restoredViewport = restored;
    this.view.requestMeasure({ key: this.restoreViewport,
      read: () => {
        // Page materialization changes the extent; an end anchor follows the measured boundary.
        if (anchor.atEnd) return Math.max(0, this.view.scrollDOM.scrollHeight - this.view.scrollDOM.clientHeight);
        const entry = this.document.entries.find(value => displayStart(value.slot) <= anchor.rank && displayEnd(value.slot) > anchor.rank);
        if (!entry) return undefined;
        const block = this.view.lineBlockAt(entry.from);
        const rows = entry.slot.pending ? Math.max(1, displayEnd(entry.slot) - displayStart(entry.slot)) : 1;
        const rowOffset = entry.slot.pending ? anchor.rank - displayStart(entry.slot) : 0;
        return this.view.documentTop - this.view.scrollDOM.getBoundingClientRect().top + this.view.scrollDOM.scrollTop
          + block.top + block.height * (rowOffset + anchor.fraction) / rows;
      },
      write: top => {
        if (this.destroyed || this.restoredViewport !== restored) return;
        if (top === undefined || revision !== this.viewportRevision) { this.restoredViewport = undefined; return; }
        this.view.scrollDOM.scrollTop = top;
        // Keep the logical position across the browser's rounded pixel assignment.
        restored.top = this.view.scrollDOM.scrollTop;
      },
    });
  }

  get alignment(): ReadonlyMap<number, number> { return this.view.state.field(alignmentField).values; }
  align(alignment: ReadonlyMap<number, number>): void {
    const held = this.alignment;
    if (alignment.size === held.size && [...alignment].every(([position, height]) => Math.abs((held.get(position) ?? 0) - height) < 0.5)) return;
    this.view.dispatch({ effects: setAlignment.of(alignment) });
  }

  clearMatch(): void { this.view.dispatch({ effects: setMatch.of(undefined) }); }
  /** Releasing twice would hand one viewport to two readers. */
  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.stopViewportInput();
    releaseEditorViewport(this.view);
  }

  /**
   * Reports content height and scans for unread ranges near the viewport.
   * Joins the active editor measure pass to avoid a redundant layout pass.
   */
  private scheduleViewport(reason: "viewport" | "content"): void {
    if (this.destroyed) return;
    if (reason === "viewport" && !this.document.entries.some(entry => entry.slot.pending)) return;
    // Requests with identical keys coalesce within the same measure cycle.
    this.view.requestMeasure({ key: this,
        read: () => {
          if (this.destroyed || this.view.scrollDOM.clientHeight <= 0) return undefined;
          const top = this.view.scrollDOM.getBoundingClientRect().top - this.view.documentTop;
          const bottom = top + this.view.scrollDOM.clientHeight;
          // Adjacent replacements share line blocks but retain separate widget heights.
          const blocks = this.view.viewportLineBlocks.flatMap(line => typeof line.type === "number" ? [line] : line.type);
          const ranges = blocks.flatMap(block => {
            const widget = block.widget;
            if (!(widget instanceof RangeWidget) || !widget.slot.pending) return [];
            if (block.bottom < top - 100 || block.top > bottom + 100) return [];
            const slot = widget.slot;
            const fraction = Math.max(0, Math.min(1, (top - block.top) / Math.max(1, block.height)));
            const start = displayStart(slot), end = displayEnd(slot);
            const offset = Math.min(end - 1, start + Math.floor(fraction * (end - start) / 100) * 100);
            return [{ slot, offset }];
          });
          const height = this.options.heightChanged ? this.view.lineBlockAt(this.view.state.doc.length).bottom : 0;
          return { ranges, height, scrollTop: this.view.scrollDOM.scrollTop };
        },
        write: measured => queueMicrotask(() => {
          if (this.destroyed || !measured) return;
          this.options.heightChanged?.(this, measured.height);
          // A write in the same measure cycle, such as a restored viewport, moved the ranges this read found.
          if (this.view.scrollDOM.scrollTop !== measured.scrollTop) { this.scheduleViewport("viewport"); return; }
          for (const { slot, offset } of measured.ranges) this.options.load(slot, offset);
        }),
      });
  }

  private copy(event: ClipboardEvent): boolean {
    const selection = this.view.state.selection;
    const selectedSource = this.sourceSelection.some(range => range.anchor.row !== range.head.row || range.anchor.offset !== range.head.offset);
    if (selection.main.empty && !selectedSource && !this.selectAll) return false;
    event.preventDefault();
    const sections = this.document.sections;
    if (this.selectAll) {
      // Selecting one comparison copies its source; selecting a page of them
      // copies the page, because no single comparison holds all of it.
      const whole = sections.length === 1 ? this.options.access(sections[0]!) : undefined;
      if (whole) void whole.content(this.options.side ?? this.options.fullSide()).then(copyTextToClipboard).catch(this.options.error);
      else void copyTextToClipboard(this.view.state.doc.toString()).catch(this.options.error);
      return true;
    }
    const ranges = selection.ranges.map((range, index) => {
      const saved = this.sourceSelection[index];
      const forward = saved && (saved.anchor.row < saved.head.row || saved.anchor.row === saved.head.row && saved.anchor.offset <= saved.head.offset);
      const start = saved ? forward ? saved.anchor : saved.head : this.document.boundary(range.from, 1);
      const end = saved ? forward ? saved.head : saved.anchor : this.document.boundary(range.to, -1);
      if (!start || !end) return Promise.resolve("");
      // A selection inside one comparison reads that source, including rows the
      // editor never loaded; one crossing comparisons copies what the page shows.
      const access = start.section === end.section ? this.options.access(start.section) : undefined;
      if (!access) return Promise.resolve(this.view.state.sliceDoc(range.from, range.to));
      return selectedReaderText(access, start, end);
    });
    void Promise.all(ranges).then(parts => copyTextToClipboard(parts.join("\n"))).catch(this.options.error);
    return true;
  }
}
