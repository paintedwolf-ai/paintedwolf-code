/** Gutter handle, hint, and dismissal rules for the line facts card. */
import { Prec, StateEffect, StateField, type EditorState } from "@codemirror/state";
import { EditorView, GutterMarker, keymap, ViewPlugin, type ViewUpdate } from "@codemirror/view";
import { agentMarkLines, lineFacts, lineFactsHint, lineFactsHintLabel, type LineFactsInput } from "./line-facts.ts";
import { agentWholeFile } from "../../../files/documents/agent-paint-lifetime.ts";
import { gutterControl, type LineGutterControl } from "./line-gutter-controls.ts";

export type LineFactsHandlers = {
  /** Opens the card for a line. `keyboard` moves focus to its first row. */
  open: (line: number, cell: HTMLElement, via: "pointer" | "keyboard") => void;
  dismiss: () => boolean | void;
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
function open(view: EditorView, button: HTMLElement, via: "pointer" | "keyboard"): void {
  const cell = cellFor(button);
  if (!cell) return;
  view.state.field(handlersField, false)?.open(Number(button.dataset.line), cell, via);
}

export class LineFactsMarker extends GutterMarker implements LineGutterControl {
  readonly press = "hold";
  constructor(
    readonly line: number,
    readonly count: number,
    readonly tint: string,
    readonly hasRead: boolean,
    readonly hint: string,
  ) {
    super();
    this.elementClass = hasRead
      ? "files-line-gutter__cell--facts files-line-gutter__cell--read"
      : "files-line-gutter__cell--facts";
  }
  eq(other: LineFactsMarker): boolean {
    return this.line === other.line
      && this.count === other.count
      && this.tint === other.tint
      && this.hasRead === other.hasRead
      && this.hint === other.hint;
  }
  toDOM(): HTMLElement {
    const button = gutterControl(document.createElement("button"), this);
    button.type = "button";
    button.className = this.hasRead
      ? "files-line-gutter__facts files-line-gutter__facts--read"
      : "files-line-gutter__facts";
    button.dataset.line = String(this.line);
    button.dataset.testid = "files-line-gutter-facts";
    button.dataset.tip = this.hint;
    button.dataset.tipPos = "end";
    if (this.hasRead) button.dataset.read = "true";
    button.style.backgroundImage = this.tint;
    button.setAttribute("aria-label", `Line ${this.line}: ${this.count} ${this.count === 1 ? "fact" : "facts"}`);
    button.setAttribute("aria-haspopup", "dialog");
    button.setAttribute("aria-expanded", "false");
    return button;
  }
  activate(view: EditorView, button: HTMLElement): void {
    open(view, button, "pointer");
  }
  // Up and Down stay in the gutter's roving group; Enter, Space, and Right enter the card.
  claimKey(view: EditorView, button: HTMLElement, event: KeyboardEvent): boolean {
    if (event.key !== "ArrowRight" && event.key !== "Enter" && event.key !== " ") return false;
    open(view, button, "keyboard");
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

function isTopmostFactsSurface(doc: Document): boolean {
  const surfaces = doc.querySelectorAll<HTMLElement>('[data-den-anchored-surface]:not([role="tooltip"])');
  return surfaces.length === 0 || Boolean(surfaces.item(surfaces.length - 1)?.classList.contains("files-line-facts"));
}

export function lineFactsGutter(input: (state: EditorState) => LineFactsInput) {
  // High precedence closes an open card before default keymaps collapse a selection.
  const escapeKeymap = Prec.high(keymap.of([{
    key: "Escape",
    run: view => isTopmostFactsSurface(view.dom.ownerDocument) && Boolean(view.state.field(handlersField, false)?.dismiss()),
  }]));
  return [handlersField, escapeKeymap, ViewPlugin.fromClass(class {
    private readonly dismiss = () => this.handlers()?.dismiss();
    private readonly scroll = () => { this.dismiss(); };
    private readonly pointerDown = (event: PointerEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      if (target?.closest(".files-line-facts") || target?.closest(HANDLE)) return;
      this.dismiss();
    };
    private readonly blur = (event: FocusEvent) => {
      const related = event.relatedTarget;
      if (!(related instanceof Node) || (!this.view.dom.contains(related) && !(related instanceof Element && related.closest(".files-line-facts")))) {
        this.dismiss();
      }
    };
    constructor(readonly view: EditorView) {
      view.scrollDOM.addEventListener("scroll", this.scroll, { passive: true });
      view.dom.ownerDocument.addEventListener("pointerdown", this.pointerDown, true);
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
      this.view.dom.removeEventListener("focusout", this.blur);
    }
  })];
}

export function lineFactsMarker(input: LineFactsInput, line: number): LineFactsMarker | null {
  const facts = lineFacts(line, input);
  const hint = lineFactsHint(facts);
  if (!hint) return null;
  const whole = agentWholeFile(input.marks) !== null;
  const tint = lineFactsTint(input, line);
  const hasRead = whole || tint !== "none";
  return new LineFactsMarker(line, facts.count, tint, hasRead, lineFactsHintLabel(hint));
}
