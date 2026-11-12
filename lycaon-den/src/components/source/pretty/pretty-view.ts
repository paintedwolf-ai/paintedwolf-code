import {
  EditorSelection,
  type EditorState,
  Facet,
  Prec,
  type SelectionRange,
} from "@codemirror/state";
import {
  Decoration,
  type DecorationSet,
  EditorView,
  layer,
  type Rect,
  RectangleMarker,
  WidgetType,
} from "@codemirror/view";
import { isImeComposition } from "../../../platform/interaction/ime-composition.ts";
import { shortcutPlatform } from "../../../shortcuts/platform.ts";
import { installedEditingStyle } from "../editor/editor-editing-style-state.ts";

/** Whether Pretty layout is active in the view. */
export const prettyLayoutActive = Facet.define<boolean, boolean>({
  combine: (values) => values.some(Boolean),
});

/** The decoration set holding Pretty's breaks and indentation marks. */
export const prettyLayoutDecorations = Facet.define<DecorationSet, DecorationSet>({
  combine: (values) => values[0] ?? Decoration.none,
});

const BREAK_CLASS = "pw-pretty-break";
const LINE_CLASS = "pw-pretty-line";

function isBreakElement(node: Node): boolean {
  return node instanceof HTMLElement && node.classList.contains(BREAK_CLASS);
}

function lastTextRect(node: Node): DOMRect | null {
  if (node.nodeType === Node.TEXT_NODE) {
    const length = node.nodeValue?.length ?? 0;
    if (length === 0) return null;
    const range = document.createRange();
    range.setStart(node, length - 1);
    range.setEnd(node, length);
    const rects = range.getClientRects();
    return rects[rects.length - 1] ?? null;
  }
  if (isBreakElement(node)) return null;
  for (let child = node.lastChild; child; child = child.previousSibling) {
    const rect = lastTextRect(child);
    if (rect && rect.height > 0) return rect;
  }
  return null;
}

// The end of a visual line is the right edge of its last character, found by
// walking back from the break; elements without text are passed over.
function rightEdgeBefore(dom: HTMLElement): Rect | null {
  for (let node = dom.previousSibling; node; node = node.previousSibling) {
    if (isBreakElement(node)) continue;
    const rect = lastTextRect(node);
    if (rect && rect.height > 0) {
      return { left: rect.right, right: rect.right, top: rect.top, bottom: rect.bottom };
    }
  }
  return null;
}

export class PrettyBreakWidget extends WidgetType {
  override toDOM(): HTMLElement {
    const el = document.createElement("br");
    el.className = BREAK_CLASS;
    el.setAttribute("aria-hidden", "true");
    return el;
  }

  override get lineBreaks(): number {
    return 1;
  }

  override coordsAt(dom: HTMLElement): Rect | null {
    return rightEdgeBefore(dom);
  }

  override eq(other: WidgetType): boolean {
    return other instanceof PrettyBreakWidget;
  }
}

export const prettyBreakDecoration = Decoration.widget({
  widget: new PrettyBreakWidget(),
  side: -2,
});

/** Indentation classes cover this many columns; deeper lines use the last one. */
export const PRETTY_MAX_INDENT_COLUMNS = 256;

const indentMarks: Decoration[] = [];

/** An inline-block mark over one visual line, indented by `columns`. */
export function prettyIndentDecoration(columns: number): Decoration {
  const clamped = Math.max(1, Math.min(PRETTY_MAX_INDENT_COLUMNS, Math.round(columns)));
  let mark = indentMarks[clamped];
  if (!mark) {
    mark = Decoration.mark({ class: `${LINE_CLASS} ${LINE_CLASS}-${clamped}` });
    indentMarks[clamped] = mark;
  }
  return mark;
}

// The content security policy refuses style attributes, so each indentation
// width is a class.
function indentRules(): Record<string, Record<string, string>> {
  const rules: Record<string, Record<string, string>> = {
    [`.${LINE_CLASS}`]: { display: "inline-block", verticalAlign: "top" },
    [`.${BREAK_CLASS}`]: { userSelect: "none" },
  };
  for (let columns = 1; columns <= PRETTY_MAX_INDENT_COLUMNS; columns++) {
    rules[`.${LINE_CLASS}-${columns}`] = {
      marginLeft: `${columns}ch`,
      maxWidth: `calc(100% - ${columns}ch)`,
    };
  }
  return rules;
}

const ACTIVE_CLASS = "pw-pretty-active";
const WRAP_CLASS = "pw-pretty-wrap";
const ACTIVE_LINE_CLASS = "pw-pretty-active-line";

const prettyTheme = EditorView.baseTheme({
  ...indentRules(),
  [`.${WRAP_CLASS}`]: {
    whiteSpace_fallback: "pre-wrap",
    whiteSpace: "break-spaces",
    wordBreak: "break-word",
    overflowWrap: "anywhere",
    flexShrink: "1",
  },
});

// The editor's own active-line background covers the whole source line, which
// can be the whole document; Pretty draws the visual line instead.
const prettyActiveLineTheme = EditorView.theme({
  [`&.${ACTIVE_CLASS} .cm-activeLine`]: { backgroundColor: "transparent" },
  [`.${ACTIVE_LINE_CLASS}`]: {
    backgroundColor: "color-mix(in srgb, var(--den-text) 4%, transparent)",
  },
});

const NO_ATTRIBUTES: Record<string, string> = {};

const SEARCH_START = 256;

function previousBreak(set: DecorationSet, lineFrom: number, pos: number, inclusive: boolean): number | null {
  for (let span = SEARCH_START; ; span *= 4) {
    const from = Math.max(lineFrom, pos - span);
    let found: number | null = null;
    set.between(from, pos, (at, to, value) => {
      if (at !== to || !(value.spec.widget instanceof PrettyBreakWidget)) return;
      if (at < pos || (inclusive && at === pos)) found = at;
    });
    if (found !== null || from === lineFrom) return found;
  }
}

function nextBreak(set: DecorationSet, lineTo: number, pos: number, inclusive: boolean): number | null {
  for (let span = SEARCH_START; ; span *= 4) {
    const to = Math.min(lineTo, pos + span);
    let found: number | null = null;
    set.between(pos, to, (at, end, value) => {
      if (at !== end || !(value.spec.widget instanceof PrettyBreakWidget)) return;
      if (at > pos || (inclusive && at === pos)) {
        found = at;
        return false;
      }
      return;
    });
    if (found !== null || to === lineTo) return found;
  }
}

/** The visual line holding `pos`; at a break, `assoc` picks the side. */
export function prettyVisualLineAt(
  state: EditorState,
  pos: number,
  assoc: number,
): { from: number; to: number } {
  const line = state.doc.lineAt(pos);
  if (!state.facet(prettyLayoutActive)) return { from: line.from, to: line.to };
  const set = state.facet(prettyLayoutDecorations);
  const after = assoc > 0;
  const from = previousBreak(set, line.from, pos, after) ?? line.from;
  const to = nextBreak(set, line.to, pos, !after) ?? line.to;
  return { from, to };
}

function boundaryMotion(forward: boolean, extend: boolean) {
  return (view: EditorView): boolean => {
    if (!view.state.facet(prettyLayoutActive)) return false;
    const move = (range: SelectionRange): SelectionRange => {
      const visual = prettyVisualLineAt(view.state, range.head, range.assoc || 1);
      const start = EditorSelection.cursor(range.head, range.assoc);
      let moved = view.moveToLineBoundary(start, forward, true);
      // A wrapped row boundary comes first; past the visual line, or at its end
      // already, the visual line boundary holds.
      if (moved.head === range.head || (forward ? moved.head > visual.to : moved.head < visual.from)) {
        moved = EditorSelection.cursor(forward ? visual.to : visual.from, forward ? -1 : 1);
      }
      return extend ? EditorSelection.range(range.anchor, moved.head) : moved;
    };
    const selection = EditorSelection.create(
      view.state.selection.ranges.map(move),
      view.state.selection.mainIndex,
    );
    view.dispatch({ selection, scrollIntoView: true, userEvent: "select" });
    return true;
  };
}

type LineKey = { forward: boolean; extend: boolean };

// Line keys of every editing style: Home and End, Cmd+Left and Cmd+Right on
// macOS, and Control+A and Control+E on macOS outside Vim, where Control+A
// adjusts a number.
function lineKey(event: KeyboardEvent, state: EditorState): LineKey | null {
  const extend = event.shiftKey;
  if (event.altKey) return null;
  if (!event.ctrlKey && !event.metaKey && (event.key === "Home" || event.key === "End")) {
    return { forward: event.key === "End", extend };
  }
  if (shortcutPlatform() !== "macos") return null;
  if (event.metaKey && !event.ctrlKey && (event.key === "ArrowLeft" || event.key === "ArrowRight")) {
    return { forward: event.key === "ArrowRight", extend };
  }
  const letter = event.key.toLowerCase();
  if (event.ctrlKey && !event.metaKey && (letter === "a" || letter === "e") && state.facet(installedEditingStyle) !== "vim") {
    return { forward: letter === "e", extend };
  }
  return null;
}

// Ahead of the editing styles, which send these keys to the source line ends.
const prettyLineKeys = Prec.highest(EditorView.domEventHandlers({
  keydown(event, view) {
    if (!view.state.facet(prettyLayoutActive) || view.composing || isImeComposition(event)) return false;
    const key = lineKey(event, view.state);
    if (!key || !boundaryMotion(key.forward, key.extend)(view)) return false;
    event.preventDefault();
    return true;
  },
}));

const activeVisualLineLayer = layer({
  above: false,
  class: "pw-pretty-active-layer",
  update: (update) =>
    update.selectionSet ||
    update.docChanged ||
    update.viewportChanged ||
    update.geometryChanged ||
    update.startState.facet(prettyLayoutActive) !== update.state.facet(prettyLayoutActive) ||
    update.startState.facet(prettyLayoutDecorations) !== update.state.facet(prettyLayoutDecorations),
  markers: (view) => {
    if (!view.state.facet(prettyLayoutActive)) return [];
    const head = view.state.selection.main;
    const visual = prettyVisualLineAt(view.state, head.head, head.assoc || 1);
    const pieces = RectangleMarker.forRange(
      view,
      ACTIVE_LINE_CLASS,
      EditorSelection.range(visual.from, visual.to),
    );
    if (pieces.length === 0) return [];
    let top = Infinity;
    let bottom = -Infinity;
    for (const piece of pieces) {
      top = Math.min(top, piece.top);
      bottom = Math.max(bottom, piece.top + (piece.height ?? 0));
    }
    const content = view.contentDOM.getBoundingClientRect();
    const scroller = view.scrollDOM.getBoundingClientRect();
    const left = content.left - scroller.left + view.scrollDOM.scrollLeft;
    return [new RectangleMarker(ACTIVE_LINE_CLASS, left, top, view.contentDOM.clientWidth, bottom - top)];
  },
});

/**
 * Presentation that depends on whether layout is active: soft wrapping, line
 * boundary keys and the active visual line.
 */
export const prettyPresentation = [
  prettyTheme,
  prettyActiveLineTheme,
  EditorView.contentAttributes.compute([prettyLayoutActive], (state) =>
    state.facet(prettyLayoutActive) ? { class: WRAP_CLASS } : NO_ATTRIBUTES,
  ),
  EditorView.editorAttributes.compute([prettyLayoutActive], (state) =>
    state.facet(prettyLayoutActive) ? { class: ACTIVE_CLASS } : NO_ATTRIBUTES,
  ),
  prettyLineKeys,
  activeVisualLineLayer,
];
