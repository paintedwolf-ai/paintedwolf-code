/** Delegated input for every line gutter control: activation, pointer claims, previews, and one roving tab stop. */
import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";
import { isScrollActive, subscribeScrollActivity } from "../../../platform/scrolling/scroll-activity.ts";

export const GUTTER_CONTROL = "data-line-gutter-control";

const FACTS = "files-line-gutter__facts";

export type LineGutterControl = {
  /** Click, Enter, or Space. */
  activate(view: EditorView, button: HTMLElement): void;
  /** Claims a key before activation and arrow navigation. */
  claimKey?(view: EditorView, button: HTMLElement, event: KeyboardEvent): boolean;
  /** `hold` keeps editor focus and selection; `claim` keeps the press from every ancestor. */
  press?: "hold" | "claim";
  /** Shown while the pointer is over the control or it has focus. */
  preview?: {
    show(view: EditorView, button: HTMLElement, via: "pointer" | "focus"): void;
    hide(view: EditorView): void;
  };
};

const controls = new WeakMap<Element, LineGutterControl>();

/** Registers a marker's button; this plugin handles its input, so the button carries no listeners. */
export function gutterControl<T extends HTMLElement>(button: T, control: LineGutterControl): T {
  button.setAttribute(GUTTER_CONTROL, "");
  button.tabIndex = -1;
  controls.set(button, control);
  return button;
}

function controlButton(target: EventTarget | null): HTMLElement | null {
  return target instanceof Element ? target.closest<HTMLElement>(`[${GUTTER_CONTROL}]`) : null;
}

function gutterControls(view: EditorView): HTMLElement[] {
  return [...view.dom.querySelectorAll<HTMLElement>(`[${GUTTER_CONTROL}]`)];
}

/** An activation that changes a marker replaces its button; focus moves to the replacement. */
function activateFromKeyboard(view: EditorView, button: HTMLElement, run: () => void): void {
  const focused = button.ownerDocument.activeElement === button;
  run();
  if (!focused || button.isConnected) return;
  const { testid, line } = button.dataset;
  const replacement = gutterControls(view).find((other) =>
    other.dataset.testid === testid && other.dataset.line === line);
  if (!replacement) return;
  selectGutterTabStop(view, replacement);
  replacement.focus({ preventScroll: true });
}

const focusedGutterControl = new WeakMap<EditorView, HTMLElement>();
const pendingGutterTabStops = new WeakSet<EditorView>();

/** A viewport can add hundreds of controls in one editor update. */
function scheduleGutterTabStop(view: EditorView): void {
  if (pendingGutterTabStops.has(view)) return;
  pendingGutterTabStops.add(view);
  queueMicrotask(() => {
    if (!pendingGutterTabStops.delete(view)) return;
    selectGutterTabStop(view);
  });
}

function selectGutterTabStop(view: EditorView, selected?: HTMLElement): void {
  const buttons = gutterControls(view);
  const gutters = view.dom.querySelector(".cm-gutters");
  const hidden = buttons.length > 0 ? "false" : "true";
  if (gutters?.getAttribute("aria-hidden") !== hidden) gutters?.setAttribute("aria-hidden", hidden);
  if (!buttons.length) return;
  if (selected) focusedGutterControl.set(view, selected);
  const held = focusedGutterControl.get(view);
  const line = view.state.doc.lineAt(view.state.selection.main.head).number;
  let nearest: HTMLElement | undefined;
  let distance = Number.POSITIVE_INFINITY;
  for (const button of buttons) {
    if (button.dataset.line == null) continue;
    const next = Math.abs(Number(button.dataset.line) - line);
    if (next >= distance) continue;
    nearest = button;
    distance = next;
  }
  const active = held && buttons.includes(held) ? held : nearest ?? buttons[0];
  for (const button of buttons) {
    const tabIndex = button === active ? 0 : -1;
    if (button.tabIndex !== tabIndex) button.tabIndex = tabIndex;
  }
}

/** Arrows move among the visible actions; fact handles keep Up and Down within their column. */
function moveAmongControls(view: EditorView, button: HTMLElement, event: KeyboardEvent): void {
  if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
  const factLine = button.classList.contains(FACTS);
  const vertical = event.key === "ArrowDown" || event.key === "ArrowUp";
  const buttons = gutterControls(view).filter(other => !factLine || !vertical || other.classList.contains(FACTS));
  const at = buttons.indexOf(button);
  if (at < 0) return;
  let next = -1;
  if (event.key === "ArrowDown" || event.key === "ArrowRight") {
    next = factLine ? Math.min(at + 1, buttons.length - 1) : (at + 1) % buttons.length;
  } else if (event.key === "ArrowUp" || event.key === "ArrowLeft") {
    next = factLine ? Math.max(at - 1, 0) : (at - 1 + buttons.length) % buttons.length;
  } else if (event.key === "Home") {
    next = 0;
  } else if (event.key === "End") {
    next = buttons.length - 1;
  }
  if (next < 0) return;
  event.preventDefault();
  event.stopPropagation();
  const target = buttons[next]!;
  selectGutterTabStop(view, target);
  target.focus({ preventScroll: true });
}

/** Controls appear only through gutter syncs, and every sync runs inside a view update. */
export const lineGutterControls = ViewPlugin.fromClass(
  class {
    private readonly listeners: [string, (event: Event) => void, boolean][];
    /** The control whose preview the pointer opened. */
    private pointed: LineGutterControl | null = null;
    private readonly stopScrollActivity: () => void;

    constructor(readonly view: EditorView) {
      const at = (event: Event) => {
        const button = controlButton(event.target);
        const control = button ? controls.get(button) : undefined;
        return button && control ? { button, control } : null;
      };
      // Previews track the control itself, not its children.
      const itself = (event: Event) => {
        const control = event.target instanceof Element ? controls.get(event.target) : undefined;
        return control ? { button: event.target as HTMLElement, control } : null;
      };
      this.listeners = [
        ["pointerdown", (event) => {
          if (at(event)?.control.press !== "claim") return;
          event.preventDefault();
          event.stopPropagation();
        }, false],
        ["mousedown", (event) => {
          if (at(event)?.control.press === "hold") event.preventDefault();
        }, false],
        ["click", (event) => {
          const hit = at(event);
          if (!hit) return;
          event.preventDefault();
          event.stopPropagation();
          hit.control.activate(view, hit.button);
        }, false],
        ["keydown", (event) => {
          const hit = at(event);
          if (!hit || !(event instanceof KeyboardEvent)) return;
          if (hit.control.claimKey?.(view, hit.button, event)) {
            event.preventDefault();
            event.stopPropagation();
            return;
          }
          if (event.key === "Enter" || event.key === " ") {
            event.preventDefault();
            activateFromKeyboard(view, hit.button, () => hit.control.activate(view, hit.button));
            return;
          }
          moveAmongControls(view, hit.button, event);
        }, false],
        ["focus", (event) => {
          const hit = itself(event);
          if (!hit) return;
          selectGutterTabStop(view, hit.button);
          hit.control.preview?.show(view, hit.button, "focus");
        }, true],
        ["blur", (event) => itself(event)?.control.preview?.hide(view), true],
        // Controls passing under a resting pointer are not being pointed at.
        ["mouseenter", (event) => {
          const hit = itself(event);
          if (!hit?.control.preview || isScrollActive(view.scrollDOM)) return;
          this.pointed = hit.control;
          hit.control.preview.show(view, hit.button, "pointer");
        }, true],
        ["mouseleave", (event) => {
          const hit = itself(event);
          if (!hit?.control.preview || hit.control !== this.pointed) return;
          this.pointed = null;
          hit.control.preview.hide(view);
        }, true],
      ];
      for (const [type, listener, capture] of this.listeners) view.dom.addEventListener(type, listener, capture);
      // A preview anchors to its control, which scrolling carries away.
      this.stopScrollActivity = subscribeScrollActivity(view.scrollDOM, (phase) => {
        if (phase !== "start" || !this.pointed) return;
        this.pointed.preview?.hide(view);
        this.pointed = null;
      });
      scheduleGutterTabStop(view);
    }

    update(update: ViewUpdate): void {
      if (update.selectionSet) focusedGutterControl.delete(update.view);
      if (update.transactions.length > 0 || update.viewportChanged || update.heightChanged) {
        scheduleGutterTabStop(update.view);
      }
    }

    destroy(): void {
      for (const [type, listener, capture] of this.listeners) this.view.dom.removeEventListener(type, listener, capture);
      this.stopScrollActivity();
      pendingGutterTabStops.delete(this.view);
      focusedGutterControl.delete(this.view);
    }
  },
);
