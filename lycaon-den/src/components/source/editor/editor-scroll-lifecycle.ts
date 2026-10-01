/** Shares editor scroll phases and measures their cost, per surface. */
import { EditorView, ViewPlugin } from "@codemirror/view";
import type { Extension } from "@codemirror/state";
import { recordSyncDuration } from "../../../chat/stream/den-main-thread-perf.ts";
import { subscribeScrollActivity } from "../../../platform/scrolling/scroll-activity.ts";

/** Names a surface's scroll cost in a perf capture, so surfaces stay comparable. */
export type EditorSurface = "files" | "diffs" | "reader" | "diff-viewer";

/** A settled scroll rests with rendered lines on both sides, so the next gesture starts covered. */
const scrollSettle = ViewPlugin.fromClass(
  class {
    private readonly stop: () => void;

    constructor(view: EditorView) {
      this.stop = subscribeScrollActivity(view.scrollDOM, (phase) => {
        if (phase === "settle") view.noteScrollSettled();
      });
    }

    destroy(): void {
      this.stop();
    }
  },
);

/** Viewport swaps are a surface's scroll cost; perf capture reports them apart from other measures. */
export function editorScrollLifecycle(surface: EditorSurface): Extension {
  return [
    scrollSettle,
    EditorView.measureTiming.of((ms, viewportChanged) => {
      recordSyncDuration(`${surface}.${viewportChanged ? "viewportSwap" : "measure"}`, ms);
    }),
  ];
}
