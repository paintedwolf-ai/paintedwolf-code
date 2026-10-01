import { forceParsing, language, syntaxTreeAvailable } from "@codemirror/language";
import type { Extension } from "@codemirror/state";
import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";

/** Parsing shares the frame budget with viewport updates. */
const VIEWPORT_BUDGET_MS = 8;
const IDLE_SLICE_MS = 10;
/** A timeout prevents background parsing from starving. */
const IDLE_TIMEOUT_MS = 500;
/** Background slices stay out of frames while the user is scrolling or typing. */
const INPUT_QUIET_MS = 150;

type IdleHandle = { cancel(): void };

function whenIdle(run: () => void): IdleHandle {
  if (typeof requestIdleCallback === "function") {
    const handle = requestIdleCallback(() => run(), { timeout: IDLE_TIMEOUT_MS });
    return { cancel: () => cancelIdleCallback(handle) };
  }
  const handle = setTimeout(run, 100);
  return { cancel: () => clearTimeout(handle) };
}

/** Visible lines parse before paint; remaining text parses in idle slices. */
export function highlightBeforePaint(): Extension {
  return ViewPlugin.fromClass(class {
    private queued = false;
    private idle: IdleHandle | undefined;
    private destroyed = false;

    private lastInput = -Infinity;
    private readonly onScroll = () => { this.lastInput = performance.now(); };

    constructor(private readonly view: EditorView) {
      view.scrollDOM.addEventListener("scroll", this.onScroll, { passive: true });
      this.settleViewport();
      this.scheduleRest();
    }

    update(update: ViewUpdate): void {
      if (update.docChanged) this.lastInput = performance.now();
      if (update.viewportChanged || update.docChanged) this.settleViewport();
      if (update.docChanged) this.scheduleRest();
    }

    destroy(): void {
      this.destroyed = true;
      this.view.scrollDOM.removeEventListener("scroll", this.onScroll);
      this.idle?.cancel();
      this.idle = undefined;
    }

    private hasLanguage(): boolean {
      return this.view.state.facet(language) !== null;
    }

    private settleViewport(): void {
      if (this.queued) return;
      this.queued = true;
      queueMicrotask(() => {
        this.queued = false;
        if (this.destroyed || !this.hasLanguage()) return;
        const upto = this.view.viewport.to;
        if (!syntaxTreeAvailable(this.view.state, upto)) forceParsing(this.view, upto, VIEWPORT_BUDGET_MS);
      });
    }

    private scheduleRest(): void {
      if (this.idle || this.destroyed) return;
      this.idle = whenIdle(() => {
        this.idle = undefined;
        if (this.destroyed || !this.hasLanguage()) return;
        const length = this.view.state.doc.length;
        if (syntaxTreeAvailable(this.view.state, length)) return;
        // Recent input reserves the frame budget for interaction.
        if (performance.now() - this.lastInput >= INPUT_QUIET_MS) forceParsing(this.view, length, IDLE_SLICE_MS);
        this.scheduleRest();
      });
    }
  });
}
