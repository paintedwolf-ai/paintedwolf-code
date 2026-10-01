import { editorScrollPosition, readEditorScrollPosition } from "../../components/source/editor/editor-scroll-position.ts";
import * as Y from "yjs";
import { createEffect, createRoot } from "solid-js";
import { EditorSelection, StateEffect, StateField, type ChangeDesc, type EditorState, type Extension, type Range, type Text } from "@codemirror/state";
import {
  Decoration, EditorView, RectangleMarker, ViewPlugin, layer,
  type DecorationSet, type LayerMarker,
} from "@codemirror/view";
import type { AgentSessionPresence } from "../../api/types.ts";
import { agentColorStyle, readAgentPalette } from "../../contributions/agent-color-palette.ts";
import { type WindowColor } from "../../contributions/window-color-palette.ts";
import { watchPaletteTheme } from "../../contributions/palette-cache.ts";
import { editorShownAgentActivity, type AgentActivityKinds } from "../../settings/editor/editor-prefs.ts";
import { overviewAgentMarks, setOverviewAgentMarks, type OverviewAgentMark } from "../../components/source/annotations/overview-agent-marks.ts";
import { agentDocumentMarks, type AgentMark } from "../components/agent-presence.ts";
import { agentPresenceFor } from "../components/agent-presence-store.ts";
import { decodeUpdate } from "./document-outbox.ts";
import type { DocumentReplica } from "./document-replica.ts";
import { selectionOutline } from "../tree/selection-outline.ts";

const AGENT_FADE_IN_MS = 700;
export const AGENT_FADE_OUT_MS = 1600;

export type PaintedAgentMark = {
  mark: AgentMark;
  from: number;
  to: number;
  color: WindowColor | undefined;
  bornAt: number;
  /** Fade-out start time; null while reported. */
  leftAt: number | null;
};

type AgentPaint = { marks: readonly PaintedAgentMark[] };

const EMPTY_PAINT: AgentPaint = { marks: [] };

function mapMarks(marks: readonly PaintedAgentMark[], changes: ChangeDesc): PaintedAgentMark[] {
  return marks.map(painted => painted.mark.range ? {
    ...painted,
    from: changes.mapPos(painted.from, painted.mark.kind === "insertion" ? -1 : 1),
    to: changes.mapPos(painted.to, -1),
  } : painted);
}

const setAgentPaint = StateEffect.define<readonly PaintedAgentMark[]>({ map: mapMarks });

export const agentPaintField = StateField.define<AgentPaint>({
  create: () => EMPTY_PAINT,
  update(value, transaction) {
    for (const effect of transaction.effects) if (effect.is(setAgentPaint)) return { marks: effect.value };
    return transaction.docChanged ? { marks: mapMarks(value.marks, transaction.changes) } : value;
  },
});

export function agentPaint(state: EditorState): AgentPaint {
  return state.field(agentPaintField, false) ?? EMPTY_PAINT;
}

function absolutePosition(replica: DocumentReplica, encoded: string): number | null {
  try {
    const position = Y.createAbsolutePositionFromRelativePosition(Y.decodeRelativePosition(decodeUpdate(encoded)), replica.doc);
    return position && position.type === replica.text ? position.index : null;
  } catch {
    // A position may name text the replica has not received yet.
    return null;
  }
}

function linePosition(doc: Text, lineNumber: number, character: number | undefined, end: boolean): number | null {
  if (lineNumber > doc.lines) return character !== undefined ? doc.length : null;
  const line = doc.line(Math.max(1, lineNumber));
  if (character === undefined) return end ? line.to : line.from;
  return Math.min(line.to, line.from + character);
}

/** Anchors apply within their document epoch; other ranges use line coordinates. */
export function resolveAgentMark(replica: DocumentReplica, doc: Text, mark: AgentMark): { from: number; to: number } | null {
  const range = mark.range;
  if (!range) return { from: 0, to: 0 };
  if (range.anchor && range.head && mark.documentId === replica.accepted.id && mark.epoch === replica.accepted.epoch) {
    const anchor = absolutePosition(replica, range.anchor);
    const head = absolutePosition(replica, range.head);
    if (anchor !== null && head !== null) {
      return { from: Math.min(doc.length, anchor, head), to: Math.min(doc.length, Math.max(anchor, head)) };
    }
  }
  const from = linePosition(doc, range.start_line, range.start_character, false);
  const to = linePosition(doc, range.end_line, range.end_character, true);
  if (from === null || to === null) return null;
  return { from: Math.min(from, to), to: Math.max(from, to) };
}

/** Preserve fade timing across updates and retain departing marks until fade-out ends. */
export function nextAgentPaint(
  previous: readonly PaintedAgentMark[],
  reported: ReadonlyArray<{ mark: AgentMark; from: number; to: number; color: WindowColor | undefined }>,
  now: number,
): PaintedAgentMark[] {
  const prior = new Map(previous.map(painted => [painted.mark.key, painted]));
  const next: PaintedAgentMark[] = [];
  const present = new Set<string>();
  for (const item of reported) {
    const held = prior.get(item.mark.key);
    present.add(item.mark.key);
    next.push({ ...item, bornAt: held && held.leftAt === null ? held.bornAt : now, leftAt: null });
  }
  for (const painted of previous) {
    if (present.has(painted.mark.key)) continue;
    const leftAt = painted.leftAt ?? now;
    if (now - leftAt < AGENT_FADE_OUT_MS) next.push({ ...painted, leftAt });
  }
  return next;
}

function fadeClass(painted: PaintedAgentMark, now: number): string {
  if (painted.leftAt !== null) return " cm-agent--leaving";
  return now - painted.bornAt < AGENT_FADE_IN_MS ? " cm-agent--entering" : "";
}

/** Negative delays preserve fade progress on redraw; set them only at animation start. */
function fadeDelay(painted: PaintedAgentMark, now: number): string {
  return `${-(now - (painted.leftAt ?? painted.bornAt))}ms`;
}

function paintStyle(element: HTMLElement, painted: PaintedAgentMark, now: number, animationStarts: boolean): void {
  for (const [property, value] of Object.entries(agentColorStyle(painted.color))) element.style.setProperty(property, value);
  if (animationStarts) element.style.animationDelay = fadeDelay(painted, now);
  element.dataset.agentSession = painted.mark.chat.sessionId;
}

function styleAttribute(painted: PaintedAgentMark, now: number): string {
  const vars = Object.entries(agentColorStyle(painted.color)).map(([property, value]) => `${property}: ${value}`);
  return [...vars, `animation-delay: ${fadeDelay(painted, now)}`].join("; ");
}

class AgentMarker extends RectangleMarker {
  constructor(
    rectangle: RectangleMarker,
    readonly base: string,
    readonly painted: PaintedAgentMark,
    readonly now: number,
    readonly outline?: string,
  ) {
    super(base, rectangle.left, rectangle.top, rectangle.width, rectangle.height);
  }

  private classes(): string {
    const { mark } = this.painted;
    return `${this.base} cm-agent-${mark.kind}${mark.stale ? " cm-agent--stale" : ""}${fadeClass(this.painted, this.now)}`;
  }

  eq(other: RectangleMarker): boolean {
    return other instanceof AgentMarker && super.eq(other) && this.base === other.base && this.outline === other.outline
      && this.painted.mark.key === other.painted.mark.key && this.painted.mark.stale === other.painted.mark.stale
      && this.painted.leftAt === other.painted.leftAt && this.painted.color?.caret === other.painted.color?.caret;
  }

  draw(): HTMLDivElement {
    const element = super.draw();
    element.className = this.classes();
    paintStyle(element, this.painted, this.now, true);
    if (this.outline) {
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("width", "100%");
      svg.setAttribute("height", "100%");
      const path = document.createElementNS(svg.namespaceURI, "path");
      path.setAttribute("d", this.outline);
      svg.append(path);
      element.append(svg);
    }
    return element;
  }

  update(element: HTMLElement, previous: RectangleMarker): boolean {
    // Reuse elements within one item to keep caret motion local.
    if (!(previous instanceof AgentMarker) || previous.painted.mark.key !== this.painted.mark.key
      || previous.base !== this.base || !super.update(element, previous)) return false;
    element.className = this.classes();
    paintStyle(element, this.painted, this.now, fadeClass(previous.painted, previous.now) !== fadeClass(this.painted, this.now));
    if (this.outline) element.querySelector("path")?.setAttribute("d", this.outline);
    return true;
  }
}

/** Keep all ranges of one current operation per chat; older facts remain in the gutter. */
export function currentAgentSteps(marks: readonly PaintedAgentMark[]): PaintedAgentMark[] {
  const chosen = new Map<string, PaintedAgentMark>();
  const rank = (painted: PaintedAgentMark) => {
    const source = painted.mark.source;
    return source.kind === "intent" ? (source.item.state === "awaiting_approval" ? 0 : 1)
      : source.kind === "draft" ? 2 : 3;
  };
  for (const painted of marks) {
    const held = chosen.get(painted.mark.chat.sessionId);
    const source = painted.mark.source, prior = held?.mark.source;
    const live = painted.leftAt === null, heldLive = held?.leftAt === null;
    if (!held || (live && !heldLive) || (live === heldLive && (rank(painted) < rank(held)
      || (source.kind === "read" && prior?.kind === "read" && source.item.sequence > prior.item.sequence)))) {
      chosen.set(painted.mark.chat.sessionId, painted);
    }
  }
  return marks.filter(painted => {
    const selected = chosen.get(painted.mark.chat.sessionId);
    return selected?.mark.source.item === painted.mark.source.item;
  });
}

const SELECTION_KINDS = new Set<AgentMark["kind"]>(["pending", "whole-pending"]);

function selectionMarkers(view: EditorView, painted: PaintedAgentMark, now: number): LayerMarker[] {
  const { doc } = view.state;
  const whole = painted.mark.kind === "whole-pending";
  const from = whole ? 0 : Math.min(painted.from, doc.length);
  // An empty line still shows the selection over its line break.
  const to = whole ? doc.length : Math.min(doc.length, painted.to > from ? painted.to : from + 1);
  if (to <= from) return [];
  const rectangles = RectangleMarker.forRange(view, "cm-agent-fill", EditorSelection.range(from, to));
  if (!rectangles.length) return [];
  const left = Math.min(...rectangles.map(rect => rect.left)), top = Math.min(...rectangles.map(rect => rect.top));
  const right = Math.max(...rectangles.map(rect => rect.left + (rect.width ?? 0)));
  const bottom = Math.max(...rectangles.map(rect => rect.top + rect.height));
  const outline = selectionOutline(rectangles).map(edge =>
    `M${edge.x1 - left} ${edge.y1 - top}L${edge.x2 - left} ${edge.y2 - top}`).join("");
  const markers: LayerMarker[] = rectangles.map(rect => new AgentMarker(rect, "cm-agent-fill", painted, now));
  if (outline) {
    markers.push(new AgentMarker(new RectangleMarker("", left, top, right - left, bottom - top), "cm-agent-outline", painted, now, outline));
  }
  return markers;
}

function caretMarkers(view: EditorView, painted: PaintedAgentMark, now: number): LayerMarker[] {
  const { doc } = view.state;
  if (painted.mark.kind === "insertion") {
    const [cursor] = RectangleMarker.forRange(view, "cm-agent-insertion", EditorSelection.cursor(Math.min(painted.from, doc.length)));
    if (!cursor) return [];
    const content = view.contentDOM;
    const width = Math.max(24, content.offsetLeft + content.clientWidth - cursor.left);
    return [new AgentMarker(new RectangleMarker("", cursor.left, cursor.top, width, cursor.height), "cm-agent-insertion", painted, now)];
  }
  const [cursor] = RectangleMarker.forRange(view, "cm-agent-caret", EditorSelection.cursor(Math.min(painted.to, doc.length)));
  return cursor ? [new AgentMarker(cursor, "cm-agent-caret", painted, now)] : [];
}

function paintOrder(a: PaintedAgentMark, b: PaintedAgentMark): number {
  return a.mark.chat.slot - b.mark.chat.slot || (a.mark.key < b.mark.key ? -1 : a.mark.key > b.mark.key ? 1 : 0);
}

const agentSelectionLayer = layer({
  above: false,
  class: "cm-agent-selection-layer",
  update: update => update.docChanged || update.viewportChanged || update.geometryChanged
    || agentPaint(update.startState) !== agentPaint(update.state),
  markers: view => {
    const now = Date.now();
    return currentAgentSteps(agentPaint(view.state).marks).filter(painted => SELECTION_KINDS.has(painted.mark.kind)).sort(paintOrder)
      .flatMap(painted => selectionMarkers(view, painted, now));
  },
});

const agentCaretLayer = layer({
  above: true,
  class: "cm-agent-caret-layer",
  update: (update, dom) => update.docChanged || update.viewportChanged || update.geometryChanged
    || agentPaint(update.startState) !== agentPaint(update.state) || scrolledSince(update.view, dom),
  markers: view => {
    const now = Date.now();
    const marks = currentAgentSteps(agentPaint(view.state).marks).sort(paintOrder);
    return [
      ...marks.filter(painted => painted.mark.caret || painted.mark.kind === "insertion").flatMap(painted => caretMarkers(view, painted, now)),
      ...marks.filter((painted, index) => marks.findIndex(item => item.mark.source.item === painted.mark.source.item) === index)
        .flatMap(painted => labelMarkers(view, painted, now)),
    ];
  },
});

const LABEL_MAX_WIDTH_PX = 360;
const LABEL_GAP_PX = 12;
/** Minimum room for a label after the line end. */
const LABEL_MIN_ROOM_PX = 180;
/** The scrollbar ruler and its margin at the scroller's trailing edge. */
const LABEL_TRAILING_INSET_PX = 20;

/** Layer labels do not affect line width or scrolling. */
class AgentLabelMarker implements LayerMarker {
  constructor(
    readonly painted: PaintedAgentMark,
    readonly fact: string,
    readonly left: number,
    readonly top: number,
    readonly height: number,
    readonly maxWidth: number,
    /** Pin to the visible edge when nearby lines lack room. */
    readonly pinned: boolean,
    readonly now: number,
  ) {}

  eq(other: LayerMarker): boolean {
    return other instanceof AgentLabelMarker && other.painted.mark.key === this.painted.mark.key
      && other.fact === this.fact && other.painted.mark.chat.title === this.painted.mark.chat.title
      && other.painted.color?.caret === this.painted.color?.caret
      && other.left === this.left && other.top === this.top && other.height === this.height && other.maxWidth === this.maxWidth
      && other.pinned === this.pinned;
  }

  draw(): HTMLElement {
    const dom = labelDom(this.painted, this.fact);
    dom.className = `cm-agent-label cm-agent-label--line${fadeClass(this.painted, this.now)}`;
    dom.style.animationDelay = fadeDelay(this.painted, this.now);
    dom.setAttribute("aria-hidden", "true");
    this.place(dom);
    return dom;
  }

  update(dom: HTMLElement, previous: LayerMarker): boolean {
    if (!(previous instanceof AgentLabelMarker) || previous.painted.mark.key !== this.painted.mark.key
      || previous.fact !== this.fact || previous.painted.mark.chat.title !== this.painted.mark.chat.title) return false;
    for (const [property, value] of Object.entries(agentColorStyle(this.painted.color))) dom.style.setProperty(property, value);
    this.place(dom);
    return true;
  }

  private place(dom: HTMLElement): void {
    dom.style.left = `${this.left}px`;
    dom.style.top = `${this.top}px`;
    dom.style.height = `${this.height}px`;
    dom.style.maxWidth = `${this.maxWidth}px`;
    dom.classList.toggle("cm-agent-label--pinned", this.pinned);
  }
}

const layerScroll = new WeakMap<HTMLElement, number>();

/** Horizontal scroll changes the visible edge the labels keep to. */
function scrolledSince(view: EditorView, dom: HTMLElement): boolean {
  const scrollLeft = readEditorScrollPosition(view).left;
  if (layerScroll.get(dom) === scrollLeft) return false;
  layerScroll.set(dom, scrollLeft);
  return true;
}

function labelMarkers(view: EditorView, painted: PaintedAgentMark, now: number): LayerMarker[] {
  const fact = painted.mark.label ?? (painted.mark.kind === "match"
    ? `search match this turn${painted.mark.stale ? ", changed since" : ""}` : undefined);
  if (!fact || painted.leftAt !== null) return [];
  const { doc } = view.state;
  const first = doc.lineAt(painted.mark.kind === "whole-pending" ? 0 : Math.min(painted.from, doc.length)).number;
  const last = doc.lineAt(Math.min(Math.max(painted.to, painted.from), doc.length)).number;
  // Layer coordinates include horizontal scroll.
  const visibleRight = view.scrollDOM.scrollLeft + view.scrollDOM.clientWidth - LABEL_TRAILING_INSET_PX;
  const visibleLeft = view.scrollDOM.scrollLeft + view.contentDOM.offsetLeft;
  const lineEnd = (line: number) => RectangleMarker.forRange(view, "", EditorSelection.cursor(doc.line(line).to))[0];
  // Prefer the change's first line, then the lines just above and below it.
  const candidates = [first, first - 1, last + 1].filter(line => line >= 1 && line <= doc.lines);
  for (const line of candidates) {
    const end = lineEnd(line);
    if (!end) continue;
    const room = visibleRight - end.left - LABEL_GAP_PX;
    if (room >= LABEL_MIN_ROOM_PX) {
      return [new AgentLabelMarker(painted, fact, end.left + LABEL_GAP_PX, end.top, end.height, Math.min(LABEL_MAX_WIDTH_PX, room), false, now)];
    }
  }
  const end = lineEnd(first);
  if (!end) return [];
  // CSS aligns the pinned label's right edge to visibleRight.
  const maxWidth = Math.max(0, Math.min(LABEL_MAX_WIDTH_PX, visibleRight - visibleLeft));
  return [new AgentLabelMarker(painted, fact, visibleRight, end.top, end.height, maxWidth, true, now)];
}

const agentDecorations = EditorView.decorations.compute([agentPaintField], (state): DecorationSet => {
  const at = Date.now();
  const marks = currentAgentSteps(agentPaint(state).marks);
  const ranges: Range<Decoration>[] = [];
  for (const painted of marks) {
    if (painted.mark.kind !== "match" || painted.to <= painted.from) continue;
    ranges.push(Decoration.mark({
      class: `cm-agent-match${painted.mark.stale ? " cm-agent--stale" : ""}${fadeClass(painted, at)}`,
      attributes: { style: styleAttribute(painted, at), "data-agent-session": painted.mark.chat.sessionId },
    }).range(painted.from, Math.min(painted.to, state.doc.length)));
  }
  return Decoration.set(ranges, true);
});

/** Only reads tint the gutter; pending changes stay in the text. */
export function agentWholeFile(marks: readonly PaintedAgentMark[]): PaintedAgentMark | null {
  let chosen: PaintedAgentMark | null = null;
  for (const painted of marks) {
    if (painted.mark.kind !== "whole") continue;
    const leaving = painted.leftAt !== null, heldLeaving = chosen?.leftAt != null;
    const source = painted.mark.source, prior = chosen?.mark.source;
    const newer = source.kind === "read" && prior?.kind === "read" && painted.mark.chat.sessionId === chosen?.mark.chat.sessionId
      ? source.item.sequence > prior.item.sequence : painted.bornAt > (chosen?.bornAt ?? 0);
    if (!chosen || (heldLeaving && !leaving) || (leaving === heldLeaving && newer)) chosen = painted;
  }
  return chosen;
}

let heldWholeFileAttributes: { key: string; attributes: Record<string, string> } | null = null;

/** Whole-file reads tint the gutter. */
const wholeFileAttributes = EditorView.editorAttributes.compute([agentPaintField], state => {
  const at = Date.now();
  const whole = agentWholeFile(agentPaint(state).marks);
  if (!whole) return {};
  // The tint appears at once and fades only as it leaves.
  const fade = whole.leftAt !== null ? " cm-agent-file--leaving" : "";
  const className = `cm-agent-file${whole.mark.stale ? " cm-agent-file--stale" : ""}${fade}`;
  // The selection color supplies a theme-adjusted tint.
  const color = whole.color?.selection ?? "var(--den-selection)";
  const key = `${whole.mark.key}\0${whole.bornAt}\0${whole.leftAt}\0${className}\0${color}`;
  // Stable attributes preserve the animation start time.
  if (heldWholeFileAttributes?.key !== key) {
    const properties = { "--den-agent-file": color, "--den-agent-file-delay": fadeDelay(whole, at) };
    const style = Object.entries(properties).map(([name, value]) => `${name}: ${value}`).join("; ");
    heldWholeFileAttributes = { key, attributes: { class: className, style } };
  }
  return heldWholeFileAttributes.attributes;
});

function capitalized(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** Put the fact first so truncation preserves it. */
function labelDom(painted: PaintedAgentMark, fact: string): HTMLElement {
  const dom = document.createElement("div");
  dom.className = "cm-agent-label";
  dom.dataset.agentSession = painted.mark.chat.sessionId;
  for (const [property, value] of Object.entries(agentColorStyle(painted.color))) dom.style.setProperty(property, value);
  const lead = document.createElement("span");
  const trail = document.createElement("small");
  lead.textContent = capitalized(fact);
  trail.textContent = ` · ${painted.mark.chat.title}`;
  dom.append(lead, trail);
  return dom;
}

export type AgentPresenceSource = {
  sessions: () => ReadonlyMap<string, AgentSessionPresence>;
  openSessionId: () => string | undefined;
};

export function documentAgentPresence(replica: DocumentReplica, source?: AgentPresenceSource): Extension {
  const sessions = source?.sessions ?? (() => agentPresenceFor(replica.accepted.project_id));
  const openSessionId = source?.openSessionId ?? (() => replica.authorship.session_id || undefined);
  const plugin = ViewPlugin.fromClass(class {
    private alive = true;
    private queued = false;
    private timer: ReturnType<typeof setTimeout> | undefined;
    private inputs: { sessions: ReadonlyMap<string, AgentSessionPresence>; kinds: AgentActivityKinds; open: string | undefined } | null = null;
    private palette: ((slot: number) => WindowColor) | null;
    private readonly stops: (() => void)[] = [];
    constructor(readonly view: EditorView) {
      const root = view.dom.ownerDocument.documentElement;
      this.palette = readAgentPalette(root);
      this.stops.push(replica.subscribe(() => this.schedule()));
      this.stops.push(watchPaletteTheme(root, () => { this.palette = readAgentPalette(root); this.schedule(); }));
      this.stops.push(createRoot(dispose => {
        createEffect(() => {
          this.inputs = { sessions: sessions(), kinds: editorShownAgentActivity(), open: openSessionId() };
          this.schedule();
        });
        return dispose;
      }));
    }
    private schedule(): void {
      if (this.queued || !this.alive) return;
      this.queued = true;
      queueMicrotask(() => {
        this.queued = false;
        if (this.alive) this.paint();
      });
    }
    private paint(): void {
      clearTimeout(this.timer);
      const { state } = this.view;
      const now = Date.now();
      const inputs = this.inputs;
      // The document path is read at paint time so a rename moves its marks.
      const marks = inputs ? agentDocumentMarks(inputs.sessions, inputs.kinds, inputs.open, replica.accepted.root_id, replica.accepted.path) : [];
      const reported = [];
      for (const mark of marks) {
        const position = resolveAgentMark(replica, state.doc, mark);
        if (position) reported.push({ mark, ...position, color: this.palette?.(mark.chat.slot) });
      }
      const previous = agentPaint(state).marks;
      const painted = nextAgentPaint(previous, reported, now);
      if (painted.length === 0 && previous.length === 0) return;
      const overview: OverviewAgentMark[] = painted.filter(item => item.mark.range).map(item => ({
        kind: item.mark.kind === "pending" || item.mark.kind === "insertion" ? "agent-pending" : "agent",
        from: item.from, to: item.to, color: item.color?.caret,
      }));
      this.view.dispatch({ effects: [setAgentPaint.of(painted), setOverviewAgentMarks.of(overview)] });
      const due = painted.flatMap(item => item.leftAt !== null ? [item.leftAt + AGENT_FADE_OUT_MS]
        : now - item.bornAt < AGENT_FADE_IN_MS ? [item.bornAt + AGENT_FADE_IN_MS] : []);
      if (due.length) this.timer = setTimeout(() => this.schedule(), Math.max(16, Math.min(...due) - now));
    }
    destroy(): void {
      this.alive = false;
      clearTimeout(this.timer);
      for (const stop of this.stops) stop();
    }
  });
  return [editorScrollPosition, agentPaintField, overviewAgentMarks, plugin, agentSelectionLayer, agentCaretLayer, agentDecorations,
    wholeFileAttributes];
}
