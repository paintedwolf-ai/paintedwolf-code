import { EDITOR_STANDARD_COMMANDS } from "./editor-standard-commands.ts";
import { editorEditingStyle } from "./editor-editing-style.ts";
import { sourceEditorThemeRules } from "./editor-theme-rules.ts";
import { editorScrollPosition } from "./editor-scroll-position.ts";
import {
  EditorView,
  Decoration,
  crosshairCursor,
  keymap,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  highlightWhitespace,
  highlightTrailingWhitespace,
  drawSelection,
  dropCursor,
  placeholder,
  rectangularSelection,
  scrollPastEnd,
  tooltips,
  ViewPlugin,
  type DecorationSet,
  type ViewUpdate,
} from "@codemirror/view";
import {
  autocompletion,
  closeCompletion,
  closeBrackets,
  closeBracketsKeymap,
  completionKeymap,
} from "@codemirror/autocomplete";
import {
  Compartment,
  EditorState,
  StateEffect,
  StateField,
  type Extension,
  type StateEffectType,
} from "@codemirror/state";
import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
} from "@codemirror/commands";
import { search } from "@codemirror/search";
import {
  activateFindableViewForEditor,
  closeFind,
  openFind,
  setFindReplaceOpen,
} from "../../../find/find-controller.ts";
import { openGotoLine } from "../../../find/goto-line-controller.ts";
import {
  syntaxHighlighting,
  codeFolding,
  defaultHighlightStyle,
  bracketMatching,
  foldKeymap,
  indentOnInput,
  indentUnit,
} from "@codemirror/language";
import { indentFoldService } from "./indent-fold.ts";
import { editorCspNonce } from "./editor-csp-nonce.ts";
import { highlightBeforePaint } from "./codemirror-highlight-ready.ts";
import { lineGutterExtension, lineGutterNumbers } from "../annotations/line-gutter.ts";
import { scopeDiffExtension } from "../diff/scope-diff.ts";
import { denScrollbars } from "./codemirror-scrollbars.ts";
import { editorScrollLifecycle, type EditorSurface } from "./editor-scroll-lifecycle.ts";
import { cursorMargin } from "./cursor-margin.ts";
import { denHighlightStyle } from "./codemirror-highlight.generated.ts";
import { bufferWordSource } from "./buffer-word-completion.ts";
import { anchoredSurfaceViewport } from "../../primitives/AnchoredSurface.tsx";
import { editorLineHeightPx } from "./editor-line-height.ts";
import { indentGuides } from "./indent-guides.ts";
import { occurrenceHighlight } from "./occurrence-highlight.ts";
import { secretSpanExtension } from "../secrets/secret-span-decorations.ts";
import { overviewRuler, overviewSymbolMarks } from "../annotations/overview-ruler.ts";
import {
  ALL_OVERVIEW_TICK_KINDS,
  type OverviewTickKinds,
} from "../annotations/overview-ruler-model.ts";
import {
  indentUnitString,
  tabSizeForIndent,
  type IndentInfo,
} from "./indent-detect.ts";
import { buildEditorCommandBridge } from "./editor-command-bridge.ts";
import { selectionHistoryField } from "./selection-history.ts";
import { shortcutPlatform } from "../../../shortcuts/platform.ts";
import { cssFontFamilyForEditor } from "../../../settings/editor/editor-prefs.ts";
import { REVEAL_FLASH_MS } from "../../../ui/reveal-flash.ts";

const setArrivalFlash = StateEffect.define<{
  line: number;
  endLine?: number;
  gen: number;
} | null>();
const setScopePreview = StateEffect.define<{
  line: number;
  endLine?: number;
} | null>();
const setTargetSymbol = StateEffect.define<{ from: number; to: number } | null>();
const setSymbolPending = StateEffect.define<{ from: number; to: number } | null>();

const TARGET_RANGE_CAP = 2000;

type LineRangeSpec = { line: number; endLine?: number };

function lineRangeDecorations(
  state: EditorState,
  spec: LineRangeSpec,
  className: string,
  attributes?: { [attr: string]: string },
): DecorationSet {
  if (spec.line < 1 || state.doc.lines === 0) return Decoration.none;
  const docLines = state.doc.lines;
  const start = Math.min(spec.line, docLines);
  const end = Math.min(
    Math.max(start, spec.endLine ?? start),
    docLines,
    start + TARGET_RANGE_CAP,
  );
  const marks = [];
  for (let n = start; n <= end; n++) {
    marks.push(
      Decoration.line({ class: className, attributes }).range(
        state.doc.line(n).from,
      ),
    );
  }
  return Decoration.set(marks);
}

function defineLineRangeField<T extends LineRangeSpec>(
  effect: StateEffectType<T | null>,
  className: string,
  attributes?: (value: T) => { [attr: string]: string } | undefined,
): StateField<DecorationSet> {
  return StateField.define<DecorationSet>({
    create() {
      return Decoration.none;
    },
    update(deco, tr) {
      deco = deco.map(tr.changes);
      for (const e of tr.effects) {
        if (!e.is(effect)) continue;
        deco =
          e.value == null
            ? Decoration.none
            : lineRangeDecorations(
                tr.state,
                e.value,
                className,
                attributes?.(e.value),
              );
      }
      return deco;
    },
    provide: (f) => EditorView.decorations.from(f),
  });
}

const arrivalFlashField = defineLineRangeField(
  setArrivalFlash,
  "cm-den-arrival-flash",
  (value) => ({ "data-flash": String(value.gen % 2) }),
);

const scopePreviewField = defineLineRangeField(
  setScopePreview,
  "cm-den-scope-preview",
);

const arrivalFlashPlugin = ViewPlugin.fromClass(
  class {
    private timer: ReturnType<typeof setTimeout> | null = null;

    constructor(readonly view: EditorView) {}

    update(update: ViewUpdate) {
      for (const tr of update.transactions) {
        for (const e of tr.effects) {
          if (!e.is(setArrivalFlash)) continue;
          this.arm(e.value != null);
        }
      }
    }

    private arm(active: boolean) {
      if (this.timer != null) {
        clearTimeout(this.timer);
        this.timer = null;
      }
      if (!active) return;
      const view = this.view;
      this.timer = setTimeout(() => {
        this.timer = null;
        if (!view.dom.isConnected) return;
        view.dispatch({ effects: setArrivalFlash.of(null) });
      }, REVEAL_FLASH_MS);
    }

    destroy() {
      if (this.timer != null) clearTimeout(this.timer);
    }
  },
);

let arrivalFlashGen = 0;

function singleRangeMarkField(
  effect: StateEffectType<{ from: number; to: number } | null>,
  className: string,
): StateField<DecorationSet> {
  const mark = Decoration.mark({ class: className });
  return StateField.define<DecorationSet>({
    create() {
      return Decoration.none;
    },
    update(deco, tr) {
      deco = deco.map(tr.changes);
      for (const e of tr.effects) {
        if (!e.is(effect)) continue;
        if (e.value == null) {
          deco = Decoration.none;
          continue;
        }
        const from = Math.max(0, Math.min(e.value.from, tr.state.doc.length));
        const to = Math.max(from, Math.min(e.value.to, tr.state.doc.length));
        deco = to > from ? Decoration.set([mark.range(from, to)]) : Decoration.none;
      }
      return deco;
    },
    provide: (field) => EditorView.decorations.from(field),
  });
}

const symbolPendingField = singleRangeMarkField(
  setSymbolPending,
  "cm-den-symbol-pending",
);

const targetSymbolField = singleRangeMarkField(
  setTargetSymbol,
  "cm-den-symbol-target",
);

const typographyThemes = new Map<string, Extension>();

function typographyTheme(opts: {
  fontSize: number;
  fontFamily: string;
  lineHeight: number;
}): Extension {
  const key = `${opts.fontSize}\0${opts.fontFamily}\0${opts.lineHeight}`;
  const held = typographyThemes.get(key);
  if (held) return held;
  const family = cssFontFamilyForEditor(opts.fontFamily);
  const theme: Extension = [
    EditorView.theme({
      "&": {
        fontSize: `${opts.fontSize}px`,
        fontFamily: family,
      },
      ".cm-scroller": {
        fontFamily: family,
        lineHeight: String(opts.lineHeight),
      },
    }),
    // Block decorations use the rendered row height.
    editorLineHeightPx.of(opts.fontSize * opts.lineHeight),
  ];
  typographyThemes.set(key, theme);
  return theme;
}

/** Shared static theme extension to avoid duplicating style elements per editor instance. */
const denEditorTheme: Extension = EditorView.theme(sourceEditorThemeRules, { dark: false });

/** Only the perf label differs per surface, so each surface keeps one identity. */
const denCodeMirrorThemes = new Map<EditorSurface, Extension>();
function denCodeMirrorTheme(surface: EditorSurface): Extension {
  const held = denCodeMirrorThemes.get(surface);
  if (held) return held;
  const theme: Extension = [
    editorScrollPosition,
    denScrollbars,
    editorScrollLifecycle(surface),
    denEditorTheme,
  ];
  denCodeMirrorThemes.set(surface, theme);
  return theme;
}

export type EditorDisplayPrefs = {
  wordWrap: boolean;
  lineNumbers: boolean;
  fontSize: number;
  fontFamily: string;
  lineHeight: number;
  indentGuides: boolean;
  whitespace: boolean;
  /** Disabling all tick kinds omits the overview ruler. */
  scrollbarTicks: OverviewTickKinds;
  indent: IndentInfo;
};

export const LONG_LINE_THRESHOLD = 10_000;
/** Bounds the cost of full-document decorations. */
export const LARGE_DOCUMENT_CHAR_THRESHOLD = 2 * 1024 * 1024;
/** Bounds wrapped-height measurement during scrolling. */
export const LARGE_DOCUMENT_LINE_THRESHOLD = 20_000;

export function textNeedsSimplifiedDisplay(doc: string): boolean {
  if (doc.length > LARGE_DOCUMENT_CHAR_THRESHOLD) return true;
  if (doc.length <= LONG_LINE_THRESHOLD) return false;
  let start = 0;
  let lines = 1;
  for (;;) {
    const nl = doc.indexOf("\n", start);
    if (nl < 0) return doc.length - start > LONG_LINE_THRESHOLD;
    if (nl - start > LONG_LINE_THRESHOLD) return true;
    lines += 1;
    if (lines > LARGE_DOCUMENT_LINE_THRESHOLD) return true;
    start = nl + 1;
  }
}

const setSimplifiedDisplay = StateEffect.define<boolean>();
const simplifiedDisplayField = StateField.define<boolean>({
  create: () => false,
  update: (value, transaction) => {
    for (const effect of transaction.effects) if (effect.is(setSimplifiedDisplay)) return effect.value;
    return value;
  },
});

export function sourceUsesSimplifiedDisplay(state: EditorState): boolean {
  return state.field(simplifiedDisplayField, false) === true;
}

const wordWrapCompartment = new Compartment();
const lineNumbersCompartment = new Compartment();
const typographyCompartment = new Compartment();
const indentGuidesCompartment = new Compartment();
const whitespaceCompartment = new Compartment();
const overviewRulerCompartment = new Compartment();
const indentCompartment = new Compartment();
const editorCommandBridgeCompartment = new Compartment();

// Shared extensions allow reconfiguration checks by identity.
const emptyExtension: Extension = [];
const lineNumbersOn = lineGutterNumbers(true);
const lineNumbersOff = lineGutterNumbers(false);
const whitespaceExtension = highlightWhitespace();

const indentExtensionsByShape = new Map<string, Extension>();

function indentExtensions(info: IndentInfo): Extension {
  const key = `${indentUnitString(info)} ${tabSizeForIndent(info)}`;
  const held = indentExtensionsByShape.get(key);
  if (held) return held;
  const ext: Extension = [
    indentUnit.of(indentUnitString(info)),
    EditorState.tabSize.of(tabSizeForIndent(info)),
  ];
  indentExtensionsByShape.set(key, ext);
  return ext;
}
const editableCompartment = new Compartment();

type EditableShape = {
  collaborative?: boolean;
  editable: boolean;
  /** Only Files installs command routing. */
  commandBridge: boolean;
};

function editableExtensions(shape: EditableShape): Extension {
  if (!shape.editable) {
    return [
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      EditorView.contentAttributes.of({ "aria-readonly": "true", tabindex: "0" }),
      drawSelection({ drawCursor: false, cursorBlinkRate: 0 }),
      keymap.of([...(shape.commandBridge ? [] : foldKeymap),
        ...defaultKeymap.filter(binding => !shape.commandBridge || !Object.values(EDITOR_STANDARD_COMMANDS).some(command => command === binding.run))]),
    ];
  }
  return [
    ...(shape.collaborative ? [] : [history()]),
    drawSelection({ drawCursor: true, cursorBlinkRate: 1200 }),
    dropCursor(),
    highlightActiveLine(),
    highlightActiveLineGutter(),
    closeBrackets(),
    indentOnInput(),
    // A short delay suppresses tooltips during typing bursts.
    autocompletion({ activateOnTypingDelay: 200 }),
    // Buffer words share the syntax completion source.
    EditorState.languageData.of(() => [{ autocomplete: bufferWordSource }]),
    placeholder("Empty file"),
    highlightTrailingWhitespace(),
    keymap.of([
      ...closeBracketsKeymap,
      ...completionKeymap,
      ...(shape.collaborative ? [] : historyKeymap),
      ...(shape.commandBridge ? [] : foldKeymap),
      ...defaultKeymap.filter(binding => !shape.commandBridge || !Object.values(EDITOR_STANDARD_COMMANDS).some(command => command === binding.run)),
      indentWithTab,
    ]),
  ];
}

/** Reconfigures editability while preserving document, selection, and scroll. */
export function applySourceEditorEditable(
  view: EditorView,
  shape: EditableShape,
): void {
  if (editableCompartment.get(view.state) === undefined) return;
  if (view.state.readOnly === !shape.editable) return;
  if (!shape.editable) closeCompletion(view);
  view.dispatch({
    effects: editableCompartment.reconfigure(editableExtensions(shape)),
  });
}
export function applyEditorCommandBridge(
  view: EditorView,
  overrides?: Record<string, string> | null,
): void {
  if (editorCommandBridgeCompartment.get(view.state) === undefined) return;
  view.dispatch({
    effects: editorCommandBridgeCompartment.reconfigure(
      buildEditorCommandBridge(shortcutPlatform(), overrides),
    ),
  });
}

const denFolding: Extension = codeFolding({
  // The fold header remains visible.
  preparePlaceholder: (state, range) =>
    state.doc.lineAt(range.to).number - state.doc.lineAt(range.from).number,
  placeholderDOM: (_view, onclick, prepared: number) => {
    const el = document.createElement("span");
    el.className = "cm-foldPlaceholder";
    const label = prepared === 1 ? "1 line" : `${prepared} lines`;
    el.textContent = prepared > 0 ? `⋯ ${label}` : "⋯";
    el.setAttribute(
      "aria-label",
      prepared > 0 ? `Unfold ${label}` : "Unfold",
    );
    el.dataset.tip = "Unfold";
    el.onclick = onclick;
    return el;
  },
});

/** Unchanged extensions skip reconfiguration. */
export function applyEditorDisplayPrefs(
  view: EditorView,
  prefs: EditorDisplayPrefs,
): void {
  const effects = sourceDisplayEffects(view.state, prefs);
  if (effects.length === 0) return;
  view.dispatch({ effects });
  // Typography changes invalidate line measurements.
  view.requestMeasure();
}

/** Paged documents apply display limits in the transaction that introduces text. */
export function sourceDisplayEffects(state: EditorState, prefs: EditorDisplayPrefs, simplified = sourceUsesSimplifiedDisplay(state)): StateEffect<unknown>[] {
  const effects: StateEffect<unknown>[] = displayExtensions(prefs, simplified)
    .filter(([compartment, extension]) => compartment.get(state) !== extension)
    .map(([compartment, extension]) => compartment.reconfigure(extension));
  if (simplified !== sourceUsesSimplifiedDisplay(state)) effects.push(setSimplifiedDisplay.of(simplified));
  return effects;
}

function displayExtensions(prefs: EditorDisplayPrefs, simplified: boolean): Array<[Compartment, Extension]> {
  return [
    [
      wordWrapCompartment,
      prefs.wordWrap && !simplified ? EditorView.lineWrapping : emptyExtension,
    ],
    [
      lineNumbersCompartment,
      prefs.lineNumbers ? lineNumbersOn : lineNumbersOff,
    ],
    [
      typographyCompartment,
      typographyTheme({
        fontSize: prefs.fontSize,
        fontFamily: prefs.fontFamily,
        lineHeight: prefs.lineHeight,
      }),
    ],
    [
      indentGuidesCompartment,
      prefs.indentGuides && !simplified ? indentGuides : emptyExtension,
    ],
    [
      whitespaceCompartment,
      prefs.whitespace && !simplified ? whitespaceExtension : emptyExtension,
    ],
    [
      overviewRulerCompartment,
      simplified ? emptyExtension : overviewRuler(prefs.scrollbarTicks),
    ],
    [indentCompartment, indentExtensions(prefs.indent)],
  ];
}

/** Mutable callbacks preserve editor state across mounts. */
export type SourceEditorHandlers = {
  onDocChange?: (update: ViewUpdate) => void;
  onCursorChange?: (line: number, col: number) => void;
};

export type SourceEditorOptions = {
  /** Which surface this editor is, so its scroll cost is reported under its own name. */
  surface: EditorSurface;
  doc: string;
  language?: Extension | null;
  wordWrap?: boolean;
  lineNumbers?: boolean;
  fontSize?: number;
  fontFamily?: string;
  lineHeight?: number;
  indentGuides?: boolean;
  whitespace?: boolean;
  scrollbarTicks?: OverviewTickKinds;
  indent?: IndentInfo;
  editable?: boolean;
  scrollPastEnd?: boolean;
  handlers?: SourceEditorHandlers;
  extensions?: Extension;
  shortcutOverrides?: Record<string, string> | null;
  /** Files editors route commands through the shared command bridge. */
  commandBridge?: boolean;
  collaborative?: boolean;
};

export function createSourceEditorState(args: SourceEditorOptions): EditorState {
  return EditorState.create({ doc: args.doc, extensions: sourceEditorExtensions(args) });
}

function sourceDisplayPrefs(args: SourceEditorOptions): EditorDisplayPrefs {
  return {
    wordWrap: args.wordWrap ?? false,
    lineNumbers: args.lineNumbers ?? true,
    fontSize: args.fontSize ?? 13,
    fontFamily: args.fontFamily ?? "default",
    lineHeight: args.lineHeight ?? 1.5,
    indentGuides: args.indentGuides ?? true,
    whitespace: args.whitespace ?? false,
    scrollbarTicks: args.scrollbarTicks ?? ALL_OVERVIEW_TICK_KINDS,
    indent: args.indent ?? { style: "spaces", width: 4 },
  };
}

function sourceEditorExtensions(args: SourceEditorOptions): Extension[] {
  const simplified = textNeedsSimplifiedDisplay(args.doc);
  const editable = args.editable === true;
  const commandBridge = args.commandBridge === true;
  const handlers: SourceEditorHandlers = args.handlers ?? {};
  const extensions: Extension[] = [
    editorCspNonce(),
    syntaxHighlighting(denHighlightStyle),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    highlightBeforePaint(),
    denCodeMirrorTheme(args.surface),
    // Tooltips outside editor containment remain unclipped.
    tooltips({
      parent: typeof document === "undefined" ? undefined : document.body,
      tooltipSpace: anchoredSurfaceViewport,
    }),
    simplifiedDisplayField.init(() => simplified),
    arrivalFlashField,
    arrivalFlashPlugin,
    scopePreviewField,
    targetSymbolField,
    symbolPendingField,
    // Search state is separate from command routing.
    search(),
    bracketMatching(),
    highlightSpecialChars(),
    EditorState.allowMultipleSelections.of(true),
    rectangularSelection(),
    crosshairCursor(),
    // Extra scroll space lets the last line reach the viewport top.
    args.scrollPastEnd === false ? [] : scrollPastEnd(),
    cursorMargin(),
    // Line numbers expose ranges folded in read-only buffers.
    denFolding,
    lineGutterExtension,
    // Secret marks do not affect layout.
    secretSpanExtension,
    // Indentation supplies folds when no syntax tree exists.
    ...(args.language ? [] : [indentFoldService]),
  ];

  extensions.push(
    ...(commandBridge ? [editorEditingStyle(), selectionHistoryField,
      editorCommandBridgeCompartment.of(buildEditorCommandBridge(shortcutPlatform(), args.shortcutOverrides))] : []),
    // Editability changes keep the live view and caret.
    editableCompartment.of(
      editableExtensions({
        editable,
        collaborative: args.collaborative,
        commandBridge,
      }),
    ),
    // Read-only buffers also report caret movement.
    EditorView.updateListener.of((update) => {
      if (update.docChanged) {
        handlers.onDocChange?.(update);
      }
      if (update.docChanged || update.selectionSet) {
        const head = update.state.selection.main.head;
        const line = update.state.doc.lineAt(head);
        handlers.onCursorChange?.(line.number, head - line.from + 1);
      }
    }),
  );

  extensions.push(
    ...displayExtensions(sourceDisplayPrefs(args), simplified).map(([compartment, extension]) => compartment.of(extension)),
    scopeDiffExtension,
  );
  if (!simplified) extensions.push(occurrenceHighlight, overviewSymbolMarks);
  if (args.language && !simplified) extensions.push(args.language);
  if (args.extensions) extensions.push(args.extensions);
  return extensions;
}

function clampLineRange(
  docLines: number,
  line: number,
  endLine?: number,
): { start: number; end: number } | null {
  if (line < 1 || docLines === 0) return null;
  const start = Math.min(line, docLines);
  const endRaw =
    endLine != null && Number.isFinite(endLine) && endLine >= 1
      ? Math.min(Math.floor(endLine), docLines)
      : start;
  return { start, end: Math.max(start, endRaw) };
}

function arrivalSelection(
  state: EditorState,
  start: number,
  end: number,
  column?: number,
): { anchor: number; head: number } {
  const startObj = state.doc.line(start);
  if (end > start) {
    return { anchor: startObj.from, head: state.doc.line(end).to };
  }
  if (column != null && Number.isFinite(column) && column >= 1) {
    const col = Math.min(Math.floor(column), startObj.length + 1);
    const pos = Math.min(startObj.from + col - 1, startObj.to);
    return { anchor: pos, head: pos };
  }
  return { anchor: startObj.from, head: startObj.from };
}

export function emphasizeAndScrollToLine(
  view: EditorView,
  line: number | undefined,
  endLine?: number,
  column?: number,
): void {
  const range =
    line == null ? null : clampLineRange(view.state.doc.lines, line, endLine);
  if (!range) {
    view.dispatch({ effects: setArrivalFlash.of(null) });
    return;
  }
  const selection = arrivalSelection(
    view.state,
    range.start,
    range.end,
    column,
  );
  arrivalFlashGen += 1;
  view.dispatch({
    selection,
    effects: [
      setArrivalFlash.of({
        line: range.start,
        endLine: range.end,
        gen: arrivalFlashGen,
      }),
      EditorView.scrollIntoView(selection.anchor, { y: "center" }),
    ],
  });
}

/** Leaves selection and scroll in place. */
export function emphasizeScopePreview(
  view: EditorView,
  line: number,
  endLine?: number,
): void {
  const range = clampLineRange(view.state.doc.lines, line, endLine);
  if (!range) return;
  view.dispatch({
    effects: setScopePreview.of({ line: range.start, endLine: range.end }),
  });
}

export function clearScopePreview(view: EditorView): void {
  view.dispatch({ effects: setScopePreview.of(null) });
}

/** Highlights a symbol without changing selection. */
export function emphasizeSymbolRangeOnly(
  view: EditorView,
  from: number,
  to: number,
): void {
  if (from < 0 || to <= from || from >= view.state.doc.length) return;
  view.dispatch({ effects: setTargetSymbol.of({ from, to }) });
}

export function clearSymbolEmphasis(view: EditorView): void {
  view.dispatch({ effects: setTargetSymbol.of(null) });
}

export function markSymbolPending(
  view: EditorView,
  from: number,
  to: number,
): void {
  if (from < 0 || to <= from || from >= view.state.doc.length) return;
  view.dispatch({ effects: setSymbolPending.of({ from, to }) });
}

export function clearSymbolPending(view: EditorView): void {
  view.dispatch({ effects: setSymbolPending.of(null) });
}

export function openEditorFind(view: EditorView): boolean {
  activateFindableViewForEditor(view);
  openFind();
  return true;
}

export function openEditorReplace(view: EditorView): boolean {
  activateFindableViewForEditor(view);
  openFind();
  setFindReplaceOpen(true);
  return true;
}

export function openEditorGotoLine(view: EditorView): boolean {
  closeFind();
  openGotoLine(view);
  return true;
}
