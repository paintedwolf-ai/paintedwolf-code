/** Whole-cell pointer entry and keyboard access for the line facts surface. */
import { StateEffect, StateField, type EditorState } from "@codemirror/state";
import { EditorView, GutterMarker, ViewPlugin, type ViewUpdate } from "@codemirror/view";
import { agentMarkLines, lineFacts, type LineFactsInput } from "./line-facts.ts";
import { agentWholeFile } from "../../../files/documents/document-agent-presence.ts";
import { gutterControl, type LineGutterControl } from "./line-gutter-controls.ts";
import { isScrollActive } from "../../../platform/scrolling/scroll-activity.ts";

export type LineFactsHandlers = {
  show: (line: number, cell: HTMLElement, via: "pointer" | "focus" | "enter") => void;
  leave: () => void;
  dismiss: () => void;
  refresh: () => void;
};
const setHandlers = StateEffect.define<LineFactsHandlers>();
const handlersField = StateField.define<LineFactsHandlers | null>({
  create: () => null,
  update(value, tr) {
    for (const effect of tr.effects) if (effect.is(setHandlers)) return effect.value;
    return value;
  },
});
export function setLineFactsHandlers(view: EditorView, handlers: LineFactsHandlers): void {
  view.dispatch({ effects: setHandlers.of(handlers) });
}
const CELL = ".files-line-gutter .cm-gutterElement";
const HANDLE = ".files-line-gutter__facts";
function cellFor(target: EventTarget | null): HTMLElement | null {
  return target instanceof Element ? target.closest<HTMLElement>(CELL) : null;
}
function show(view: EditorView, cell: HTMLElement | null, via: "pointer" | "focus" | "enter"): boolean {
  const button = cell?.querySelector<HTMLElement>(HANDLE);
  if (!cell || !button) return false;
  view.state.field(handlersField, false)?.show(Number(button.dataset.line), cell, via);
  return true;
}

export class LineFactsMarker extends GutterMarker implements LineGutterControl {
  readonly press = "hold";
  constructor(readonly line: number, readonly count: number, readonly tint: string, readonly hasRead = false) {
    super();
    this.elementClass = hasRead
      ? "files-line-gutter__cell--facts files-line-gutter__cell--read"
      : "files-line-gutter__cell--facts";
  }
  eq(other: LineFactsMarker): boolean {
    return this.line === other.line && this.count === other.count && this.tint === other.tint && this.hasRead === other.hasRead;
  }
  toDOM(): HTMLElement {
    const button = gutterControl(document.createElement("button"), this);
    button.type = "button";
    button.className = this.hasRead
      ? "files-line-gutter__facts files-line-gutter__facts--read"
      : "files-line-gutter__facts";
    button.dataset.line = String(this.line);
    button.dataset.testid = "files-line-gutter-facts";
    if (this.hasRead) button.dataset.read = "true";
    button.style.backgroundImage = this.tint;
    button.setAttribute("aria-label", `Line ${this.line}: ${this.count} ${this.count === 1 ? "fact" : "facts"}`);
    button.setAttribute("aria-haspopup", "dialog");
    button.setAttribute("aria-expanded", "false");
    return button;
  }
  activate(view: EditorView, button: HTMLElement): void {
    show(view, cellFor(button), "enter");
  }
  // Right arrow opens the card too; Up and Down stay in the gutter's shared roving group.
  claimKey(view: EditorView, button: HTMLElement, event: KeyboardEvent): boolean {
    if (event.key !== "ArrowRight" && event.key !== "Enter" && event.key !== " ") return false;
    show(view, cellFor(button), "enter");
    return true;
  }
}

export function lineFactsTint(input: LineFactsInput, line: number): string {
  // A whole-file read tints the file, so ranges add no gradient over it.
  if (agentWholeFile(input.marks) !== null) return "none";
  const reads = input.marks.filter(painted => painted.mark.kind === "read" && painted.leftAt === null
    && line >= agentMarkLines(input.doc, painted).first && line <= agentMarkLines(input.doc, painted).last);
  if (!reads.length) return "none";
  // Overlapping reads paint one uniform tint, preferring a fresh read.
  const painted = reads.find(p => !p.mark.stale) ?? reads[0]!;
  const color = `color-mix(in srgb, ${painted.color?.caret ?? "var(--den-text-muted)"} 16%, transparent)`;
  return `linear-gradient(${color}, ${color})`;
}

export function lineFactsGutter(input: (state: EditorState) => LineFactsInput) {
  return [handlersField, ViewPlugin.fromClass(class {
    private readonly dismiss = () => this.handlers()?.dismiss();
    private readonly scroll = () => { this.dismiss(); };
    private readonly pointerDown = (event: PointerEvent) => {
      if (!(event.target instanceof Element) || (!cellFor(event.target) && !event.target.closest(".files-line-facts"))) this.dismiss();
    };
    private readonly keyDown = (event: KeyboardEvent) => { if (event.key === "Escape" && !this.view.dom.ownerDocument.querySelector(".files-line-facts")) this.dismiss(); };
    // Rows passing under a resting pointer are not being pointed at.
    private readonly scrolling = () => isScrollActive(this.view.scrollDOM);
    private readonly over = (event: MouseEvent) => {
      if (this.scrolling()) return;
      const removal = (target: EventTarget | null) => target instanceof Element && target.closest(".files-line-gutter__cut, .files-line-gutter__foldback");
      if (removal(event.target)) { this.dismiss(); return; }
      const cell = cellFor(event.target);
      if (cell === cellFor(event.relatedTarget) && !removal(event.relatedTarget)) return;
      if (!show(this.view, cell, "pointer")) this.handlers()?.leave();
    };
    private readonly out = (event: MouseEvent) => {
      if (this.scrolling()) return;
      if (cellFor(event.target) !== cellFor(event.relatedTarget)) this.handlers()?.leave();
    };
    private readonly focus = (event: FocusEvent) => {
      if (event.target instanceof Element && event.target.closest(".files-line-gutter__cut, .files-line-gutter__foldback")) this.dismiss();
      else show(this.view, cellFor(event.target), "focus");
    };
    private readonly blur = (event: FocusEvent) => {
      if (!(event.relatedTarget instanceof Element) || (!event.relatedTarget.closest(".files-line-facts") && !cellFor(event.relatedTarget))) this.handlers()?.dismiss();
    };
    constructor(readonly view: EditorView) {
      view.scrollDOM.addEventListener("scroll", this.scroll, { passive: true });
      view.dom.ownerDocument.addEventListener("pointerdown", this.pointerDown, true);
      view.dom.ownerDocument.addEventListener("keydown", this.keyDown);
      view.dom.addEventListener("mouseover", this.over);
      view.dom.addEventListener("mouseout", this.out);
      view.dom.addEventListener("focusin", this.focus);
      view.dom.addEventListener("focusout", this.blur);
    }
    private handlers() { return this.view.state.field(handlersField, false); }
    update(update: ViewUpdate) {
      const before = input(update.startState), after = input(update.state);
      const changed = before.doc !== after.doc || before.marks !== after.marks
        || before.attribution !== after.attribution || before.findings !== after.findings;
      if (changed) this.handlers()?.refresh();
    }
    destroy() {
      this.handlers()?.dismiss();
      this.view.scrollDOM.removeEventListener("scroll", this.scroll);
      this.view.dom.ownerDocument.removeEventListener("pointerdown", this.pointerDown, true);
      this.view.dom.ownerDocument.removeEventListener("keydown", this.keyDown);
      this.view.dom.removeEventListener("mouseover", this.over);
      this.view.dom.removeEventListener("mouseout", this.out);
      this.view.dom.removeEventListener("focusin", this.focus);
      this.view.dom.removeEventListener("focusout", this.blur);
    }
  })];
}

export function lineFactsMarker(input: LineFactsInput, line: number): LineFactsMarker | null {
  const count = lineFacts(line, input).count;
  if (!count) return null;
  const whole = agentWholeFile(input.marks) !== null;
  const tint = lineFactsTint(input, line);
  const hasRead = whole || tint !== "none";
  return new LineFactsMarker(line, count, tint, hasRead);
}
