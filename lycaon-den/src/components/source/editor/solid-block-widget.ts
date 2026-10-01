import type { Extension } from "@codemirror/state";
import { EditorView, ViewPlugin, WidgetType, type ViewUpdate } from "@codemirror/view";
import { getOwner, runWithOwner, type JSX } from "solid-js";
import { render } from "solid-js/web";

/** The reactive scope a view resolves its context in (`getOwner`). */
export type ReactiveScope = ReturnType<typeof getOwner>;

/** The span of the scroller that shows content, past its gutters, in scroller pixels. */
export type BlockFrame = { left: number; width: number };

type Held = { host: HTMLElement; dispose: () => void; live: boolean; touched: number };

/** Stamped on the block itself so the write restyles no editor lines. */
function stampFrame(host: HTMLElement, frame: BlockFrame): void {
  host.style.setProperty("--den-editor-block-left", `${frame.left}px`);
  host.style.setProperty("--den-editor-block-w", `${frame.width}px`);
}

/** Retains mounted block views off-document to avoid remounting during scroll. */
export class SolidBlockViews {
  private readonly held = new Map<string, Held>();
  private clock = 0;
  private framed: BlockFrame | undefined;

  /** Parked views beyond this are disposed, newest kept. */
  constructor(private readonly retain: number) {}

  mount(key: string, scope: ReactiveScope, view: () => JSX.Element): HTMLElement {
    const found = this.held.get(key);
    if (found) {
      found.live = true;
      found.touched = ++this.clock;
      return found.host;
    }
    const host = document.createElement("div");
    host.className = "cm-den-block";
    if (this.framed) stampFrame(host, this.framed);
    const build = (): (() => void) => render(view, host);
    const dispose = (scope ? runWithOwner(scope, build) : build()) ?? (() => {});
    this.held.set(key, { host, dispose, live: true, touched: ++this.clock });
    return host;
  }

  /** Pins every block, mounted or parked, to the frame; true when the frame moved. */
  frame(next: BlockFrame): boolean {
    if (this.framed?.left === next.left && this.framed.width === next.width) return false;
    this.framed = next;
    for (const { host } of this.held.values()) stampFrame(host, next);
    return true;
  }

  /** Marks a view as inactive when scrolled out of viewport. */
  park(key: string): void {
    const found = this.held.get(key);
    if (!found) return;
    found.live = false;
    found.touched = ++this.clock;
    this.trim();
  }

  private forget(key: string): void {
    const found = this.held.get(key);
    if (!found) return;
    found.dispose();
    found.host.remove();
    this.held.delete(key);
  }

  destroy(): void {
    for (const key of [...this.held.keys()]) this.forget(key);
  }

  private trim(): void {
    const parked = [...this.held.entries()].filter(([, value]) => !value.live);
    if (parked.length <= this.retain) return;
    parked.sort((left, right) => left[1].touched - right[1].touched);
    for (const [key] of parked.slice(0, parked.length - this.retain)) this.forget(key);
  }
}

/** Where the scroller shows content: its width past the gutters. Undefined while the editor has no box. */
export function readBlockFrame(view: EditorView): BlockFrame | undefined {
  const width = view.scrollDOM.clientWidth;
  if (width <= 0) return undefined;
  const left = view.contentDOM.offsetLeft - view.scrollDOM.offsetLeft;
  return { left, width: width - left };
}

/** Queues a second measure pass so the editor reads the heights a new frame gave the blocks. */
const reflow = {};

/** Frames the views' blocks to the scroller, not to unwrapped content wider than it. */
export function frameBlockViews(views: SolidBlockViews): Extension {
  return ViewPlugin.fromClass(class {
    constructor(private readonly view: EditorView) {
      this.measure();
    }

    update(update: ViewUpdate): void {
      if (update.geometryChanged) this.measure();
    }

    private measure(): void {
      this.view.requestMeasure({
        key: this,
        read: readBlockFrame,
        write: (frame, view) => {
          if (frame && views.frame(frame)) view.requestMeasure({ key: reflow, read: () => null });
        },
      });
    }
  });
}

/** Block widget hosting a reactive Solid view. */
export class SolidBlockWidget extends WidgetType {
  constructor(
    /** Identity across document rebuilds; an equal key keeps the mounted view. */
    readonly key: string,
    private readonly views: SolidBlockViews,
    private readonly scope: ReactiveScope,
    private readonly view: () => JSX.Element,
    /** Reserves layout before the view renders. */
    private readonly reserve: number,
  ) {
    super();
  }

  eq(other: SolidBlockWidget): boolean { return other.key === this.key; }
  get estimatedHeight(): number { return this.reserve; }
  ignoreEvent(): boolean { return true; }

  toDOM(): HTMLElement { return this.views.mount(this.key, this.scope, this.view); }

  destroy(): void { this.views.park(this.key); }
}
